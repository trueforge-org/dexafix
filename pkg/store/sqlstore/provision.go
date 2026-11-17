package sqlstore

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/rs/zerolog/log"

	"github.com/trueforge-org/dexafix/pkg/shared/must"
)

// The names Provision creates. They are fixed: the connection strings that deploy init writes and the
// body service's search_path refer to them.
const (
	ProvisionAppRole    = "dexafix"       // the api's role; owns what it creates in the database, not the database
	ProvisionBodyRole   = "dexafix_body"  // the body service's role; owns the schema "body" only
	ProvisionOwnerRole  = "dexafix_owner" // owns the database and cannot log in
	ProvisionDatabase   = "dexafix"
	ProvisionBodySchema = "body"
)

// minProvisionPassword is the shortest password Provision recommends for a role (a shorter one is accepted, with a warning). `secrets init` makes 64
// hexadecimal characters.
const minProvisionPassword = 16

// provisionRunLockKey is the advisory lock that keeps two runs of Provision against one server apart (the
// roles and the database are shared state, and a rerun while another is half done could fail or
// undo it). It is taken in the database of the administrator's connection. The tests' own lock
// (provisionLockKey in provision_test.go) is another number: they hold it while they call Provision.
const provisionRunLockKey = 7274709

// ProvisionOptions are what Provision needs. AdminDSN is a superuser's connection string (to any
// database, usually "postgres"); the two passwords are the ones the application roles get.
type ProvisionOptions struct {
	AdminDSN     string
	AppPassword  string
	BodyPassword string
}

// Provision makes the PostgreSQL roles, the database and the body schema the recommended deployment
// runs on, so that no service runs as a superuser and a leak of one cannot read the other's data or
// damage the database:
//
//	dexafix_owner  owns the database; cannot log in. Nobody who can log in may drop it, take its
//	               connect right from the others or change its settings.
//	dexafix        works in the schema "public" and has no rights in the schema "body"
//	dexafix_body   owns the schema "body" and can use nothing else
//
// It can be run again and again: what exists is kept (a database that an older version let the api's
// role own goes to dexafix_owner), the roles lose any extra attributes and memberships they have, and
// the passwords are set again, so a changed password file takes effect at the next run. It never
// drops anything. Two runs at the same moment wait for each other.
func Provision(ctx context.Context, o ProvisionOptions) error {
	if o.AdminDSN == "" || o.AppPassword == "" || o.BodyPassword == "" {
		return errors.New("provision: an admin connection string and both passwords are required")
	}
	if len(o.AppPassword) < minProvisionPassword || len(o.BodyPassword) < minProvisionPassword {
		// Not refused: a deployment that was set up with shorter ones must keep starting. It is said.
		log.Warn().Int("minimum", minProvisionPassword).Msg("provision: a role password is shorter than recommended; `dexafix secrets init` makes long ones")
	}
	if o.AppPassword == o.BodyPassword {
		return errors.New("provision: the two roles must have different passwords")
	}

	cfg, err := parsePG(o.AdminDSN)
	if err != nil {
		return err
	}
	admin := openPool(cfg)
	defer admin.Close()

	// One run at a time: the lock belongs to a connection of its own and ends with it. Taking it is also
	// the first use of the connection, so it says whether the administrator can connect at all.
	lock, err := admin.Conn(ctx)
	if err != nil {
		return fmt.Errorf("provision: connect as the administrator: %w", err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, provisionRunLockKey); err != nil {
		return fmt.Errorf("provision: connect as the administrator, or wait for another run: %w", err)
	}

	if err := ensureRole(ctx, admin, ProvisionOwnerRole, ""); err != nil {
		return err
	}
	for _, r := range []struct{ role, password string }{
		{ProvisionAppRole, o.AppPassword},
		{ProvisionBodyRole, o.BodyPassword},
	} {
		if err := ensureRole(ctx, admin, r.role, r.password); err != nil {
			return err
		}
	}

	var exists bool
	if err := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, ProvisionDatabase).Scan(&exists); err != nil {
		return fmt.Errorf("provision: look for the database: %w", err)
	}
	database, owner := quoteIdent(ProvisionDatabase), quoteIdent(ProvisionOwnerRole)
	if !exists {
		// CREATE DATABASE cannot run in a transaction and takes no placeholders; the names are constants.
		if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, database, owner)); err != nil {
			return fmt.Errorf("provision: create the database: %w", err)
		}
	}
	dbCfg := cfg.Copy()
	dbCfg.Database = ProvisionDatabase
	db := openPool(dbCfg)
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("provision: connect to the database: %w", err)
	}
	defer tx.Rollback()
	if err := execAll(ctx, tx, []string{
		// What belongs to the database as a whole. They run here, in the transaction on the database
		// itself (any connection to it may), so one step and one failure path cover them all.
		// The owner is set on every run: a database made by an earlier version belonged to the api's role,
		// which could then lock the other role out, change the database's settings or drop it.
		fmt.Sprintf(`ALTER DATABASE %s OWNER TO %s`, database, owner),
		// Connecting is a right of PUBLIC by default, so any role could connect, and the api's role as
		// owner could take it away from the others. It is granted to the two roles only.
		fmt.Sprintf(`REVOKE ALL ON DATABASE %s FROM PUBLIC`, database),
		fmt.Sprintf(`GRANT CONNECT, TEMPORARY ON DATABASE %s TO %s, %s`, database, quoteIdent(ProvisionAppRole), quoteIdent(ProvisionBodyRole)),
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s AUTHORIZATION %s`, quoteIdent(ProvisionBodySchema), quoteIdent(ProvisionBodyRole)),
		fmt.Sprintf(`ALTER SCHEMA %s OWNER TO %s`, quoteIdent(ProvisionBodySchema), quoteIdent(ProvisionBodyRole)),
		fmt.Sprintf(`ALTER ROLE %s SET search_path = %s`, quoteIdent(ProvisionBodyRole), quoteIdent(ProvisionBodySchema)),
		// Nobody but the owner of the database may use the schema "public": without this every role may
		// look at its tables' names and create objects there. The grant gives the api's role the use of
		// it (the owner of the database is a role that cannot log in).
		`REVOKE ALL ON SCHEMA public FROM PUBLIC`,
		fmt.Sprintf(`GRANT ALL ON SCHEMA public TO %s`, quoteIdent(ProvisionAppRole)),
		fmt.Sprintf(`REVOKE ALL ON SCHEMA %s FROM PUBLIC`, quoteIdent(ProvisionBodySchema)),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// stmtRunner is what runs a statement: a pool or a transaction.
type stmtRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// execAll runs the statements one after the other and stops at the first the server refuses.
func execAll(ctx context.Context, db stmtRunner, stmts []string) error {
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("provision: %s: %w", stmt, err)
		}
	}
	return nil
}

// roleAttributes are set on every role Provision handles, whatever an earlier version or an image
// gave it: none of them is a superuser, may make databases or roles, replicate or bypass row security.
const roleAttributes = `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`

// ensureRole creates a role or, when it exists, brings it back to what Provision wants: the
// attributes above, no memberships, and (for a role that logs in) the password again. An empty
// password makes a role that cannot log in. The statement is built by the server with format(%I,
// %L), so no name or password is ever quoted by hand, and a password goes over as a SCRAM-SHA-256
// verifier, so it is not in the statement that log_statement=ddl or pg_stat_statements records.
func ensureRole(ctx context.Context, db *sql.DB, role, password string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return fmt.Errorf("provision: look for role %s: %w", role, err)
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	login, verifier := "NOLOGIN", ""
	if password != "" {
		login, verifier = "LOGIN", scramVerifier(password)
	}
	// One statement for both kinds of role: with a verifier it has a PASSWORD clause, without one it has
	// none (format ignores an argument no placeholder takes).
	var stmt string
	if err := db.QueryRowContext(ctx, `SELECT format($1::text || ' ROLE %I ' || $2::text || ' ' || $3::text || CASE WHEN $5::text = '' THEN '' ELSE ' PASSWORD %L' END, $4::text, $5::text)`,
		verb, login, roleAttributes, role, verifier).Scan(&stmt); err != nil {
		return fmt.Errorf("provision: prepare role %s: %w", role, err)
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		// The statement can hold the password, so it is not part of the message (the server's error is not).
		return fmt.Errorf("provision: %s role %s: %w", verb, role, err)
	}
	return dropMemberships(ctx, db, role)
}

// dropMemberships takes the role out of every role it is a member of (pg_read_all_data, a
// superuser's role): a role that Provision handles is only what it is here. One block, run by the
// server, so no list of names travels back and forth.
func dropMemberships(ctx context.Context, db *sql.DB, role string) error {
	name := "'" + strings.ReplaceAll(role, "'", "''") + "'" // a constant of this file, quoted all the same
	return execAll(ctx, db, []string{`DO $provision$ DECLARE g name; BEGIN
 FOR g IN SELECT r.rolname FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.roleid JOIN pg_roles u ON u.oid = m.member WHERE u.rolname = ` + name + `
 LOOP EXECUTE format('REVOKE %I FROM %I', g, ` + name + `); END LOOP; END $provision$`})
}

// scramIterations is PostgreSQL's default work factor for SCRAM-SHA-256 (scram_iterations).
const scramIterations = 4096

// scramVerifier makes the PostgreSQL SCRAM-SHA-256 verifier of a password: the server stores it as
// it is, so the password itself is never in a statement. A password with characters beyond ASCII
// is sent as it is instead and the server does the normalisation (SASLprep) that the verifier
// would need.
func scramVerifier(password string) string {
	for _, r := range password {
		if r < 0x20 || r > 0x7e {
			return password
		}
	}
	salt := make([]byte, 16)
	_, _ = rand.Read(salt) // never fails: since Go 1.24 a broken random source ends the program
	// Cannot fail: the length asked for is one SHA-256 block's worth, and the iterations are positive.
	salted := must.Value(pbkdf2.Key(sha256.New, password, salt, scramIterations, sha256.Size))
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac(salted, "Client Key"))
	enc := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", scramIterations, enc(salt), enc(stored[:]), enc(mac(salted, "Server Key")))
}

// openPG opens a connection pool from a PostgreSQL connection string (URL or key/value form),
// optionally changing the parsed settings first (another database, another user).
func openPG(dsn string, change func(*pgx.ConnConfig)) (*sql.DB, error) {
	cfg, err := parsePG(dsn)
	if err != nil {
		return nil, err
	}
	if change != nil {
		change(cfg)
	}
	return openPool(cfg), nil
}

// parsePG parses a connection string without ever echoing it: it may hold a password.
func parsePG(dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("provision: the connection string cannot be parsed")
	}
	return cfg, nil
}

// openPool opens a small pool for a parsed configuration. Nothing connects until the first use.
func openPool(cfg *pgx.ConnConfig) *sql.DB {
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(2)
	return db
}

// quoteIdent quotes a name that comes from a constant of this file, never from a request.
func quoteIdent(s string) string { return pgx.Identifier{s}.Sanitize() }
