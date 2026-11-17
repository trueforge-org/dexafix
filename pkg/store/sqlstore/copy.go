package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"
)

// copyBatch is the number of rows inserted per statement.
const copyBatch = 500

// CopyStats reports how many rows Copy transferred per table.
type CopyStats struct {
	Users        int
	DexaScans    int
	ScaleReads   int
	Integrations int
	Identities   int
	AccessTokens int
	Settings     int
	Providers    int
	AuditEvents  int
	Schedules    int // job schedules (paused ones stay paused)
	Nonces       int // single-use values that have not expired yet
}

// nonceRow is a spent single-use value; the target must refuse it too, or a second-step cookie or
// state that was used on the source could be replayed there until it expires.
type nonceRow struct {
	bun.BaseModel `bun:"table:used_nonces"`

	Nonce     string    `bun:"nonce,pk"`
	ExpiresAt time.Time `bun:"expires_at"`
}

// Copy transfers every row from src to dst, preserving ids. It works between any pair of backends.
// Both databases must be at the same schema version and dst must be empty. The copy is a single
// transaction on dst, so a failure leaves it untouched, and the source is read in one transaction
// too (a snapshot on PostgreSQL), so rows written meanwhile cannot make it skip or repeat rows. Use a
// stopped source anyway: what is written after the snapshot is not in the copy.
//
// Not copied, because they mean nothing on another database or are rebuilt: sessions (people sign in
// again), password reset links, failed-attempt counters, worker jobs (queued ones are made again by
// the schedules) and worker instances.
func Copy(ctx context.Context, src, dst *Store) (CopyStats, error) {
	var stats CopyStats

	sv, err := src.SchemaVersion(ctx)
	if err != nil {
		return stats, fmt.Errorf("source: %w", err)
	}
	dv, err := dst.SchemaVersion(ctx)
	if err != nil {
		return stats, fmt.Errorf("target: %w", err)
	}
	if sv != dv {
		return stats, fmt.Errorf("schema versions differ (source %d, target %d): run `dexafix db migrate up` on both", sv, dv)
	}
	// The body domain goes along when both stores have it; a store opened WithoutBody has none.
	withBody := src.BodyStore != nil && dst.BodyStore != nil
	tables := []any{(*scheduleRow)(nil), (*userRow)(nil), (*integrationRow)(nil), (*identityRow)(nil), (*totpRow)(nil), (*recoveryRow)(nil), (*passkeyRow)(nil), (*tokenRow)(nil), (*settingRow)(nil), (*providerRow)(nil), (*auditRow)(nil)}
	if withBody {
		tables = append(tables, (*dexaRow)(nil), (*scaleRow)(nil), (*bodyStateRow)(nil))
	}
	for _, m := range tables {
		q := dst.db.NewSelect().Model(m)
		if _, ok := m.(*settingRow); ok {
			q = q.Where("name <> ?", checkSetting) // written by Open when the target has a key
		}
		n, err := q.Count(ctx)
		if err != nil {
			return stats, fmt.Errorf("target: %w", err)
		}
		if n > 0 {
			return stats, errors.New("target database is not empty")
		}
	}

	var readOpts *sql.TxOptions // SQLite: one read transaction already is a snapshot
	if src.driver == DriverPostgres {
		readOpts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	srcTx, err := src.db.BeginTx(ctx, readOpts)
	if err != nil {
		return stats, fmt.Errorf("source: %w", err)
	}
	defer srcTx.Rollback() // only reads
	err = dst.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var err error
		if stats.Users, err = copyTable[userRow](ctx, srcTx, tx, "id", nil, false); err != nil {
			return fmt.Errorf("users: %w", err)
		}
		if stats.Schedules, err = copyTable[scheduleRow](ctx, srcTx, tx, "user_id, kind", nil, false); err != nil {
			return fmt.Errorf("worker_schedules: %w", err)
		}
		// Only the values that still matter; a value the target already holds stays.
		now := time.Now().UTC()
		if stats.Nonces, err = copyTable[nonceRow](ctx, srcTx, tx, "nonce", func(q *bun.SelectQuery) *bun.SelectQuery { return q.Where("expires_at >= ?", now) }, true); err != nil {
			return fmt.Errorf("used_nonces: %w", err)
		}
		if withBody {
			// Like the main key check: the source's check goes with its ciphertext.
			if _, err := tx.NewDelete().Model((*bodyMetaRow)(nil)).Where("name = ?", checkBody).Exec(ctx); err != nil {
				return fmt.Errorf("body_meta: %w", err)
			}
			if stats.DexaScans, err = copyTable[dexaRow](ctx, srcTx, tx, "id", nil, false); err != nil {
				return fmt.Errorf("dexa_scans: %w", err)
			}
			if stats.ScaleReads, err = copyTable[scaleRow](ctx, srcTx, tx, "id", nil, false); err != nil {
				return fmt.Errorf("scale_readings: %w", err)
			}
			if _, err = copyTable[bodyStateRow](ctx, srcTx, tx, "user_id", nil, false); err != nil {
				return fmt.Errorf("body_user_state: %w", err)
			}
			if _, err = copyTable[bodyMetaRow](ctx, srcTx, tx, "name", nil, false); err != nil {
				return fmt.Errorf("body_meta: %w", err)
			}
		}
		if stats.Integrations, err = copyTable[integrationRow](ctx, srcTx, tx, "user_id, provider", nil, false); err != nil {
			return fmt.Errorf("user_integrations: %w", err)
		}
		// Sessions are not copied: they are short-lived, and users simply sign in again.
		if stats.Identities, err = copyTable[identityRow](ctx, srcTx, tx, "provider, subject", nil, false); err != nil {
			return fmt.Errorf("user_identities: %w", err)
		}
		if _, err = copyTable[passkeyRow](ctx, srcTx, tx, "credential_id", nil, false); err != nil {
			return fmt.Errorf("passkeys: %w", err)
		}
		if _, err = copyTable[totpRow](ctx, srcTx, tx, "user_id", nil, false); err != nil {
			return fmt.Errorf("user_totp: %w", err)
		}
		if _, err = copyTable[recoveryRow](ctx, srcTx, tx, "user_id, code_hash", nil, false); err != nil {
			return fmt.Errorf("user_recovery_codes: %w", err)
		}
		if stats.AccessTokens, err = copyTable[tokenRow](ctx, srcTx, tx, "id", nil, false); err != nil {
			return fmt.Errorf("access_tokens: %w", err)
		}
		// The source's key check goes with its ciphertext, so the target's own is replaced.
		if _, err := tx.NewDelete().Model((*settingRow)(nil)).Where("name = ?", checkSetting).Exec(ctx); err != nil {
			return fmt.Errorf("app_settings: %w", err)
		}
		if stats.Settings, err = copyTable[settingRow](ctx, srcTx, tx, "name", nil, false); err != nil {
			return fmt.Errorf("app_settings: %w", err)
		}
		if stats.Providers, err = copyTable[providerRow](ctx, srcTx, tx, "slug", nil, false); err != nil {
			return fmt.Errorf("auth_providers: %w", err)
		}
		if stats.AuditEvents, err = copyTable[auditRow](ctx, srcTx, tx, "id", nil, false); err != nil {
			return fmt.Errorf("audit_log: %w", err)
		}
		if dst.driver == DriverPostgres {
			// Explicit ids do not advance the serial sequences; align them so new inserts do not collide.
			seqTables := []string{"access_tokens", "audit_log"} // users have random ids, no sequence
			if withBody {
				seqTables = append(seqTables, "dexa_scans", "scale_readings")
			}
			for _, table := range seqTables {
				q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%[1]s', 'id'), COALESCE(MAX(id), 1), MAX(id) IS NOT NULL) FROM %[1]s`, table)
				if _, err := tx.ExecContext(ctx, q); err != nil {
					return fmt.Errorf("reset sequence %s: %w", table, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return CopyStats{}, err
	}
	return stats, nil
}

// copyTable pages through one table of src ordered by orderBy, optionally filtered, and inserts the
// rows into tx. With ignoreConflicts a row the target already has is left alone.
func copyTable[T any](ctx context.Context, src bun.IDB, tx bun.Tx, orderBy string, filter func(*bun.SelectQuery) *bun.SelectQuery, ignoreConflicts bool) (int, error) {
	total := 0
	for offset := int64(0); ; offset += copyBatch {
		var rows []T
		q := src.NewSelect().Model(&rows)
		if filter != nil {
			q = filter(q)
		}
		if err := q.OrderExpr(orderBy).Limit(copyBatch).Offset(offset).Scan(ctx); err != nil {
			return total, err
		}
		if len(rows) == 0 {
			return total, nil
		}
		ins := tx.NewInsert().Model(&rows).Returning("NULL")
		if ignoreConflicts {
			ins = ins.On("CONFLICT DO NOTHING")
		}
		if _, err := ins.Exec(ctx); err != nil {
			return total, err
		}
		total += len(rows)
		if len(rows) < copyBatch {
			return total, nil
		}
	}
}
