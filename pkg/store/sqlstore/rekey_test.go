package sqlstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/store"
)

func keyOf(fill byte) []byte { return bytes.Repeat([]byte{fill}, KeySize) }

func cipherWith(t *testing.T, cur byte, prev ...byte) *Cipher {
	t.Helper()
	var p [][]byte
	for _, f := range prev {
		p = append(p, keyOf(f))
	}
	c, err := NewCipher(keyOf(cur), p...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSealFormatAndKeyIDs(t *testing.T) {
	c := cipherWith(t, 1, 2)
	v := c.seal("v", "ctx")
	if !strings.HasPrefix(v, encV2+c.current.id+":") {
		t.Fatalf("format: %q", v)
	}
	// An old key is still read by its id; a configuration without it says what is missing.
	oldValue := cipherWith(t, 2).seal("legacy", "ctx")
	if got, e, err := c.open(oldValue, "ctx"); err != nil || got != "legacy" || e != c.previous[0] {
		t.Fatalf("previous key: %q %v", got, err)
	}
	if _, _, err := cipherWith(t, 3).open(oldValue, "ctx"); err == nil || !strings.Contains(err.Error(), "PREVIOUS") {
		t.Fatalf("unknown kid: %v", err)
	}
	if _, _, err := c.open(v, "other context"); err == nil {
		t.Fatal("context ignored")
	}
}

func TestV1ValuesStayReadable(t *testing.T) {
	cur := cipherWith(t, 1)
	// Build a v1 value the way the old code did: AES under the master key itself.
	nonce := bytes.Repeat([]byte{7}, cur.current.v1.NonceSize())
	sealed := cur.current.v1.Seal(nonce, nonce, []byte("old"), []byte("ctx"))
	v1 := encV1 + base64.RawStdEncoding.EncodeToString(sealed)
	for name, c := range map[string]*Cipher{"current": cur, "previous": cipherWith(t, 5, 1)} {
		if got, _, err := c.open(v1, "ctx"); err != nil || got != "old" {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
}

func TestKeyCheck(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.db")
	s := openFile(t, path, WithCipher(cipherWith(t, 1)))
	if v := rawColumn(t, s, `SELECT value FROM app_settings WHERE name = 'crypto.check'`); len(v) != 64 || strings.HasPrefix(v, encPrefix) {
		t.Fatalf("check value: %q", v)
	}
	fillSensitive(t, s)
	s.Close()

	if _, err := Open(ctx, DriverSQLite, "file:"+path, WithCipher(cipherWith(t, 2))); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong key: %v", err)
	}
	right, err := Open(ctx, DriverSQLite, "file:"+path, WithCipher(cipherWith(t, 1)))
	if err != nil {
		t.Fatalf("right key: %v", err)
	}
	defer right.Close()
	// The check value is not a ciphertext: encryption leaves it alone.
	if v := rawColumn(t, right, `SELECT value FROM app_settings WHERE name = 'crypto.check'`); strings.HasPrefix(v, encPrefix) {
		t.Fatalf("check value encrypted: %q", v)
	}
}

func TestKeyCheckForDatabaseWithoutCheckValue(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.db")
	s := openFile(t, path, WithCipher(cipherWith(t, 1)))
	fillSensitive(t, s)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM app_settings WHERE name = 'crypto.check'`); err != nil { // as before the check existed
		t.Fatal(err)
	}
	s.Close()

	if _, err := Open(ctx, DriverSQLite, "file:"+path, WithCipher(cipherWith(t, 2))); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong key: %v", err)
	}
	right := openFile(t, path, WithCipher(cipherWith(t, 1)))
	if v := rawColumn(t, right, `SELECT COUNT(*) FROM app_settings WHERE name = 'crypto.check'`); v != "1" {
		t.Fatalf("check not written: %q", v)
	}
}

func TestHasEncryptedValues(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.db")
	dsn := "file:" + path
	if has, err := HasEncryptedValues(ctx, DriverSQLite, dsn); err != nil || has {
		t.Fatalf("new file: %v %v", has, err)
	}
	plain := openFile(t, path)
	fillSensitive(t, plain)
	plain.Close()
	if has, err := HasEncryptedValues(ctx, DriverSQLite, dsn); err != nil || has {
		t.Fatalf("plaintext database: %v %v", has, err)
	}
	enc := openFile(t, path, WithCipher(cipherWith(t, 1)))
	enc.Close()
	if has, err := HasEncryptedValues(ctx, DriverSQLite, dsn); err != nil || !has {
		t.Fatalf("encrypted database: %v %v", has, err)
	}
}

func TestRekey(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.db")
	s := openFile(t, path, WithCipher(cipherWith(t, 1)))
	u := fillSensitive(t, s)
	oldLookup := rawColumn(t, s, `SELECT email_lookup FROM users LIMIT 1`)
	oldID := s.cipher.current.id
	s.Close()

	// New key current, old key previous: everything still works.
	s = openFile(t, path, WithCipher(cipherWith(t, 2, 1)))
	checkReadable(t, s, u.ID)

	dry, err := s.Rekey(ctx, true)
	if err != nil || dry.Emails != 1 || dry.Integrations != 1 || dry.Providers != 1 || dry.Settings == 0 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if v := rawColumn(t, s, rawEmail); !strings.Contains(v, oldID) {
		t.Fatalf("dry run changed data: %q", v)
	}

	done, err := s.Rekey(ctx, false)
	if err != nil || done.Changed() != dry.Changed() {
		t.Fatalf("rekey: %+v %v", done, err)
	}
	s.Close()

	// A store that only knows the new key reads everything, including the e-mail lookup.
	only := openFile(t, path, WithCipher(cipherWith(t, 2)))
	checkReadable(t, only, u.ID)
	for _, q := range []string{rawIntegration, rawSetting, rawProvider, rawEmail} {
		if v := rawColumn(t, only, q); !strings.HasPrefix(v, encV2+only.cipher.current.id+":") {
			t.Errorf("%s not under the new key: %q", q, v)
		}
	}
	if got, err := only.GetUserByEmail(ctx, "alice@example.com"); err != nil || got.ID != u.ID {
		t.Fatalf("lookup after rekey: %+v %v", got, err)
	}
	if rawColumn(t, only, `SELECT email_lookup FROM users LIMIT 1`) == oldLookup {
		t.Error("lookup not recomputed")
	}
	// Idempotent: a second run has nothing to do.
	again, err := only.Rekey(ctx, false)
	if err != nil || again.Changed() != 0 || again.Current == 0 {
		t.Fatalf("second run: %+v %v", again, err)
	}
	// The old key no longer matches the database.
	if _, err := Open(ctx, DriverSQLite, "file:"+path, WithCipher(cipherWith(t, 1))); err == nil {
		t.Fatal("old key accepted after rekey")
	}
}

func TestRekeyFailureLeavesDatabaseUntouched(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.db")
	s := openFile(t, path, WithCipher(cipherWith(t, 1)))
	fillSensitive(t, s)
	s.Close()

	s = openFile(t, path, WithCipher(cipherWith(t, 2, 1)))
	// A provider secret no key can open: fails after integrations and settings were handled.
	if _, err := s.db.ExecContext(ctx, `UPDATE auth_providers SET client_secret = 'enc:v2:deadbeef:AAAA'`); err != nil {
		t.Fatal(err)
	}
	before := rawColumn(t, s, rawIntegration)
	if _, err := s.Rekey(ctx, false); err == nil || !strings.Contains(err.Error(), "sign-in provider g") {
		t.Fatalf("rekey: %v", err)
	}
	if rawColumn(t, s, rawIntegration) != before {
		t.Error("partial rekey was kept")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Rekey(cancelled, false); err == nil {
		t.Error("cancelled context succeeded")
	}
}

// A row that was written after it was read is not put back as it was: for a refresh token that works
// once, that would lose it.
func TestRekeyRefusesToOverwriteARowThatChangedAfterItWasRead(t *testing.T) {
	ctx := context.Background()
	s := openFile(t, filepath.Join(t.TempDir(), "a.db"), WithCipher(cipherWith(t, 1)))
	u := fillSensitive(t, s)
	stale := rawColumn(t, s, rawIntegration)
	if err := s.PutIntegration(ctx, u.ID, "withings", []byte(`{"refresh_token":"newer"}`)); err != nil { // somebody wrote meanwhile
		t.Fatal(err)
	}
	err := exactlyOne(ctx, s.db.NewUpdate().Model((*integrationRow)(nil)).Set("data = ?", "x").
		Where("user_id = ?", u.ID).Where("provider = ?", "withings").Where("data = ?", stale), "an integration")
	if err == nil || !strings.Contains(err.Error(), "changed while the keys were being replaced") {
		t.Fatalf("a stale update went through: %v", err)
	}
	got, err := s.GetIntegration(ctx, u.ID, "withings")
	if err != nil || !strings.Contains(string(got), "newer") {
		t.Fatalf("the newer value was overwritten: %q %v", got, err)
	}
	if err := exactlyOne(ctx, s.db.NewUpdate().Model((*settingRow)(nil)).Set("value = ?", "x").Where("name = ?", "no-such-setting"), "setting"); err == nil {
		t.Error("an update of no row passed")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := exactlyOne(cancelled, s.db.NewUpdate().Model((*settingRow)(nil)).Set("value = ?", "x").Where("name = ?", "a"), "setting"); err == nil {
		t.Error("a cancelled context passed")
	}
}

// A job history longer than one page is resealed completely.
func TestRekeyReadsALongJobHistoryInPages(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.db")
	s := openFile(t, path, WithCipher(cipherWith(t, 1)))
	u, err := s.CreateUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	n := rekeyPage*2 + 37
	for i := 0; i < n; i++ {
		if _, _, err := s.EnqueueJob(ctx, models.NewJob{UserID: u.ID, Kind: "demo.import", Origin: models.JobOriginUser, DedupeKey: fmt.Sprint("k", i),
			Payload: "secret", MaxAttempts: 3, Timeout: time.Minute}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s = openFile(t, path, WithCipher(cipherWith(t, 2, 1)))
	done, err := s.Rekey(ctx, false)
	if err != nil || done.Jobs != n {
		t.Fatalf("rekeyed %d of %d jobs: %v", done.Jobs, n, err)
	}
	again, err := s.Rekey(ctx, false)
	if err != nil || again.Jobs != 0 {
		t.Fatalf("second run: %+v %v", again, err)
	}
}

// On PostgreSQL the tables are locked against writers while the keys are replaced.
func TestRekeyLocksTheTablesAgainstWritersOnPostgreSQL(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if s.driver != DriverPostgres {
		if err := lockForRekey(ctx, bun.Tx{}, DriverSQLite, "users"); err != nil {
			t.Fatalf("SQLite needs no lock: %v", err)
		}
		t.Skip("PostgreSQL only")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockForRekey(ctx, tx, s.driver, "users", "app_settings"); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := s.PutSetting(short, "locked", "x"); err == nil {
		t.Fatal("a write went through while the tables were locked")
	}
	if _, err := s.GetUser(ctx, 1); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reads must go on: %v", err)
	}
}
