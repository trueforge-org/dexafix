package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// provisionDSN is the administrator's connection string; the tests need a real PostgreSQL server, and
// they create the roles "dexafix" and "dexafix_body" and the database "dexafix" in it.
func provisionDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DEXAFIX_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DEXAFIX_TEST_POSTGRES_DSN is not set")
	}
	if err := checkProvisionAdmin(dsn); err != nil {
		t.Fatal(err)
	}
	lockProvisioning(t, dsn)
	return dsn
}

// provisionLockKey is the advisory lock that every test taking the shared roles and the database
// "dexafix" holds while it runs. Packages are tested in parallel, and one of these tests makes the
// database refuse connections for a moment; cmd's TestDBProvisionSetsUpTheRolesTheDatabaseAndTheSchema
// takes the same lock with the same number.
const provisionLockKey = 7274707

// lockProvisioning waits for the lock and holds it until the test is over.
func lockProvisioning(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", provisionLockKey); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) }) // closing the connection releases the lock
}

// checkProvisionAdmin refuses an administrator that is one of the roles Provision makes. Provision
// sets their passwords, so the next connection of that administrator, and with it every other test
// of the package that uses the same DSN, would fail with "password authentication failed".
func checkProvisionAdmin(dsn string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return err
	}
	if cfg.User == ProvisionAppRole || cfg.User == ProvisionBodyRole {
		return fmt.Errorf("DEXAFIX_TEST_POSTGRES_DSN logs in as %q, a role that Provision creates and gives a new password: "+
			"use another administrator, for example postgres://postgres:x@localhost:5432/postgres", cfg.User)
	}
	return nil
}

func TestCheckProvisionAdmin(t *testing.T) {
	for dsn, wantErr := range map[string]bool{
		"postgres://postgres:x@localhost:5432/postgres?sslmode=disable":          false,
		"postgres://admin:x@localhost:5432/dexafix_test?sslmode=disable":         false,
		"postgres://dexafix:dexafix@localhost:5432/dexafix_test?sslmode=disable": true,
		"postgres://dexafix_body:x@localhost:5432/dexafix_test?sslmode=disable":  true,
	} {
		if err := checkProvisionAdmin(dsn); (err != nil) != wantErr {
			t.Errorf("checkProvisionAdmin(%q) = %v, want error: %v", dsn, err, wantErr)
		}
	}
}

// openAs connects as another role to the database "dexafix".
func openAs(t *testing.T, adminDSN, user, password string) *sql.DB {
	t.Helper()
	db, err := openPG(adminDSN, func(c *pgx.ConnConfig) {
		c.User, c.Password, c.Database = user, password, ProvisionDatabase
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestProvisionIsolatesTheRoles(t *testing.T) {
	ctx := context.Background()
	dsn := provisionDSN(t)
	o := ProvisionOptions{AdminDSN: dsn, AppPassword: "app-pw-with-'quote-and-\\backslash", BodyPassword: "body-pw-1-0123456789"}

	// Twice: the second run keeps what exists.
	for i := 0; i < 2; i++ {
		if err := Provision(ctx, o); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}

	app := openAs(t, dsn, ProvisionAppRole, o.AppPassword)
	body := openAs(t, dsn, ProvisionBodyRole, o.BodyPassword)

	if _, err := app.ExecContext(ctx, `CREATE TABLE public.prov_app (id int)`); err != nil {
		t.Fatalf("the api's role must work in public: %v", err)
	}
	t.Cleanup(func() { app.ExecContext(ctx, `DROP TABLE IF EXISTS public.prov_app`) })

	if _, err := body.ExecContext(ctx, `CREATE TABLE prov_body (id int)`); err != nil { // search_path = body
		t.Fatalf("the body role must work in its schema: %v", err)
	}
	var schema string
	if err := body.QueryRowContext(ctx, `SELECT table_schema FROM information_schema.tables WHERE table_name = 'prov_body'`).Scan(&schema); err != nil || schema != ProvisionBodySchema {
		t.Fatalf("search_path of the body role: schema %q, err %v", schema, err)
	}
	t.Cleanup(func() { body.ExecContext(ctx, `DROP TABLE IF EXISTS body.prov_body`) })

	for name, c := range map[string]struct {
		db  *sql.DB
		sql string
	}{
		"api creates in body":    {app, `CREATE TABLE body.prov_x (id int)`},
		"api reads body":         {app, `SELECT * FROM body.prov_body`},
		"body creates in public": {body, `CREATE TABLE public.prov_x (id int)`},
		"body reads public":      {body, `SELECT * FROM public.prov_app`},
	} {
		if _, err := c.db.ExecContext(ctx, c.sql); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: want permission denied, got %v", name, err)
		}
	}
}

func TestProvisionAppliesAChangedPassword(t *testing.T) {
	ctx := context.Background()
	dsn := provisionDSN(t)
	o := ProvisionOptions{AdminDSN: dsn, AppPassword: "first-app-pw-0123456789", BodyPassword: "first-body-pw-0123456789"}
	if err := Provision(ctx, o); err != nil {
		t.Fatal(err)
	}
	o.AppPassword = "second-app-pw-0123456789"
	if err := Provision(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := openAs(t, dsn, ProvisionAppRole, "second-app-pw-0123456789").PingContext(ctx); err != nil {
		t.Fatalf("the new password must work: %v", err)
	}
	if err := openAs(t, dsn, ProvisionAppRole, "first-app-pw-0123456789").PingContext(ctx); err == nil {
		t.Fatal("the old password must not work any more")
	}
}

func TestProvisionRefusesBadInput(t *testing.T) { // needs no server
	ctx := context.Background()
	for name, o := range map[string]ProvisionOptions{
		"no dsn":         {AppPassword: "app-password-0123456789", BodyPassword: "body-password-0123456789"},
		"no password":    {AdminDSN: "postgres://x@h/postgres", AppPassword: "app-password-0123456789"},
		"same passwords": {AdminDSN: "postgres://x@h/postgres", AppPassword: "same-password-0123456789", BodyPassword: "same-password-0123456789"},
	} {
		if err := Provision(ctx, o); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	err := Provision(ctx, ProvisionOptions{AdminDSN: "postgres://:not a url\x7f", AppPassword: "app-password-0123456789", BodyPassword: "body-password-0123456789"})
	if err == nil || strings.Contains(err.Error(), "not a url") {
		t.Errorf("a bad connection string must be refused without echoing it: %v", err)
	}
}

// The database belongs to a role that cannot log in: the api's role, which a compromised api would
// hand to an attacker, cannot lock the body role out, change the database or drop it.
func TestProvisionLeavesTheApiRoleNoPowerOverTheDatabase(t *testing.T) {
	ctx := context.Background()
	dsn := provisionDSN(t)
	o := ProvisionOptions{AdminDSN: dsn, AppPassword: "app-pw-0123456789abcdef", BodyPassword: "body-pw-0123456789abcdef"}
	// A database that an earlier version made: owned by the api's role, open to PUBLIC.
	admin, err := openPG(dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err := Provision(ctx, o); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`ALTER DATABASE dexafix OWNER TO dexafix`,
		`GRANT CONNECT ON DATABASE dexafix TO PUBLIC`,
		`ALTER ROLE dexafix SUPERUSER CREATEDB CREATEROLE BYPASSRLS`,
		`GRANT pg_read_all_data TO dexafix_body`,
	} {
		if _, err := admin.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := Provision(ctx, o); err != nil { // the rerun puts everything right
		t.Fatal(err)
	}

	var owner string
	if err := admin.QueryRowContext(ctx, `SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = 'dexafix'`).Scan(&owner); err != nil || owner != ProvisionOwnerRole {
		t.Fatalf("the database belongs to %q (%v), want %s", owner, err, ProvisionOwnerRole)
	}
	var login bool
	if err := admin.QueryRowContext(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, ProvisionOwnerRole).Scan(&login); err != nil || login {
		t.Fatalf("the owner can log in: %v %v", login, err)
	}
	for _, role := range []string{ProvisionAppRole, ProvisionBodyRole} {
		var super, createdb, createrole, repl, bypass, canlogin bool
		if err := admin.QueryRowContext(ctx, `SELECT rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls, rolcanlogin FROM pg_roles WHERE rolname = $1`, role).
			Scan(&super, &createdb, &createrole, &repl, &bypass, &canlogin); err != nil {
			t.Fatal(err)
		}
		if super || createdb || createrole || repl || bypass || !canlogin {
			t.Errorf("%s: superuser %v createdb %v createrole %v replication %v bypassrls %v login %v", role, super, createdb, createrole, repl, bypass, canlogin)
		}
		var n int
		if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_auth_members m JOIN pg_roles u ON u.oid = m.member WHERE u.rolname = $1`, role).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s is a member of %d roles (%v)", role, n, err)
		}
	}
	var publicCanConnect bool
	if err := admin.QueryRowContext(ctx, `SELECT has_database_privilege('public_probe_role_that_does_not_exist_hopefully', 'dexafix', 'CONNECT')`).Scan(&publicCanConnect); err == nil && publicCanConnect {
		t.Error("a role that has no grant can connect")
	}

	// What the api's role could do before, and what a compromised api would do with it.
	app := openAs(t, dsn, ProvisionAppRole, o.AppPassword)
	body := openAs(t, dsn, ProvisionBodyRole, o.BodyPassword)
	for name, stmt := range map[string]string{
		"make the database read-only": `ALTER DATABASE dexafix SET default_transaction_read_only = on`,
		"rename the database":         `ALTER DATABASE dexafix RENAME TO gone`,
		"become the owner":            `SET ROLE dexafix_owner`,
		"give itself rights":          `ALTER ROLE dexafix SUPERUSER`,
	} {
		if _, err := app.ExecContext(ctx, stmt); err == nil {
			t.Errorf("the api's role could %s", name)
		}
	}
	// A REVOKE by somebody who never granted the right is not an error in PostgreSQL, it does nothing:
	// what counts is that the body role can still connect afterwards (below).
	for _, stmt := range []string{`REVOKE CONNECT ON DATABASE dexafix FROM dexafix_body`, `REVOKE ALL ON DATABASE dexafix FROM PUBLIC`} {
		app.ExecContext(ctx, stmt)
	}
	// Both roles still work.
	for name, db := range map[string]*sql.DB{"api": app, "body": body} {
		if err := db.PingContext(ctx); err != nil {
			t.Errorf("the %s role cannot connect: %v", name, err)
		}
	}
	if _, err := app.ExecContext(ctx, `CREATE TABLE public.prov_owner_check (id int)`); err != nil {
		t.Fatalf("the api's role must still work in public: %v", err)
	}
	t.Cleanup(func() { app.ExecContext(ctx, `DROP TABLE IF EXISTS public.prov_owner_check`) })
	// A role that nobody granted anything cannot connect at all.
	if _, err := admin.ExecContext(ctx, `DROP ROLE IF EXISTS dexafix_stranger`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE ROLE dexafix_stranger LOGIN PASSWORD 'stranger-pw-0123456789'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.ExecContext(ctx, `DROP ROLE IF EXISTS dexafix_stranger`) })
	if err := openAs(t, dsn, "dexafix_stranger", "stranger-pw-0123456789").PingContext(ctx); err == nil {
		t.Error("a role with no grant connected to the database")
	}
}

// The password is sent as a SCRAM verifier, so it is not in the statement the server logs; the roles
// still sign in with it, whatever characters it has.
func TestProvisionSendsAVerifierAndTheRolesSignInWithThePassword(t *testing.T) {
	ctx := context.Background()
	dsn := provisionDSN(t)
	for name, pw := range map[string]string{
		"ascii with quotes": "pw-with-'quotes'-and-\\-slashes-012345",
		"hexadecimal":       strings.Repeat("0123456789abcdef", 4),
		"beyond ascii":      "pässword-met-ë-en-€-0123456789",
	} {
		o := ProvisionOptions{AdminDSN: dsn, AppPassword: pw, BodyPassword: "other-" + pw}
		if err := Provision(ctx, o); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := openAs(t, dsn, ProvisionAppRole, pw).PingContext(ctx); err != nil {
			t.Errorf("%s: the api's role cannot sign in: %v", name, err)
		}
		if err := openAs(t, dsn, ProvisionBodyRole, "other-"+pw).PingContext(ctx); err != nil {
			t.Errorf("%s: the body role cannot sign in: %v", name, err)
		}
		admin, err := openPG(dsn, nil)
		if err != nil {
			t.Fatal(err)
		}
		var stored string
		err = admin.QueryRowContext(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname = $1`, ProvisionAppRole).Scan(&stored)
		admin.Close()
		if err != nil || !strings.HasPrefix(stored, "SCRAM-SHA-256$") {
			t.Errorf("%s: stored password %.20q (%v)", name, stored, err)
		}
	}
}

func TestAScramVerifierHasPostgresFormat(t *testing.T) {
	v := scramVerifier("a long enough password")
	parts := strings.Split(v, "$")
	if len(parts) != 3 || parts[0] != "SCRAM-SHA-256" || !strings.HasPrefix(parts[1], "4096:") || strings.Count(parts[2], ":") != 1 {
		t.Fatalf("verifier %q", v)
	}
	if strings.Contains(v, "a long enough password") {
		t.Error("the password is in the verifier")
	}
	w := scramVerifier("a long enough password")
	if v == w {
		t.Error("two verifiers of one password are equal: the salt is not random")
	}
	if raw := scramVerifier("pässword with ü"); raw != "pässword with ü" {
		t.Errorf("a password beyond ASCII goes as it is: %q", raw)
	}
}

// Two runs at the same time wait for each other and both succeed.
func TestTwoProvisionRunsAtOnceBothSucceed(t *testing.T) {
	ctx := context.Background()
	dsn := provisionDSN(t)
	o := ProvisionOptions{AdminDSN: dsn, AppPassword: "app-pw-0123456789abcdef", BodyPassword: "body-pw-0123456789abcdef"}
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { errs <- Provision(ctx, o) }()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Errorf("run %d: %v", i, err)
		}
	}
}

// A short password is accepted (a deployment set up with one must keep starting), with a warning.
func TestAShortRolePasswordIsAcceptedWithAWarning(t *testing.T) {
	var logs bytes.Buffer
	old := log.Logger
	log.Logger = zerolog.New(&logs)
	t.Cleanup(func() { log.Logger = old })
	err := Provision(context.Background(), ProvisionOptions{AdminDSN: "postgres://:not a url\x7f", AppPassword: "short-one", BodyPassword: "short-two"})
	if err == nil || !strings.Contains(logs.String(), "shorter than recommended") {
		t.Fatalf("error %v, log %s", err, logs.String())
	}
}
