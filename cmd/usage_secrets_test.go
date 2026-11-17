package cmd

import (
	"strings"
	"testing"
)

// A password in an environment variable must not follow a typo into the usage text, which lands in
// container and CI logs.
func TestUsageTextNeverPrintsASecretFromTheEnvironment(t *testing.T) {
	t.Setenv("DEXAFIX_DSN", "postgres://u:secretpw@h/d")
	t.Setenv("DEXAFIX_ADMIN_DSN", "postgres://a:secretpw@h/d")
	t.Setenv("DEXAFIX_FROM_DSN", "postgres://f:secretpw@h/d")
	t.Setenv("DEXAFIX_TO_DSN", "postgres://t:secretpw@h/d")
	t.Setenv("DEXAFIX_VALKEY_URL", "redis://:secretpw@h:6379/0")
	for _, args := range [][]string{
		{"serve", "--nope"}, {"serve", "--help"}, {"db", "migrate", "--nope"}, {"db", "copy", "--nope"},
		{"db", "provision", "--nope"}, {"serve", "api", "--nope"}, {"serve", "api", "--help"},
	} {
		out, errText, _ := run(t, args...)
		if strings.Contains(out+errText, "secretpw") {
			t.Errorf("%v printed the password:\n%s%s", args, out, errText)
		}
	}
}

// The environment still supplies the value when the flag was not typed, and the flag wins when it was.
func TestSecretFlagsStillReadTheEnvironment(t *testing.T) {
	t.Setenv("DEXAFIX_DSN", "postgres://u:secretpw@h/d")
	c := dbMigrateCmd
	if err := c.Flags().Set("dsn", defaultSQLiteDSN); err != nil {
		t.Fatal(err)
	}
	c.Flags().Lookup("dsn").Changed = false
	if _, dsn, err := dbFlags(c); err != nil || dsn != "postgres://u:secretpw@h/d" {
		t.Fatalf("dsn %q %v", dsn, err)
	}
	if err := c.Flags().Set("dsn", "file:other.db"); err != nil {
		t.Fatal(err)
	}
	if _, dsn, err := dbFlags(c); err != nil || dsn != "file:other.db" {
		t.Fatalf("typed dsn %q %v", dsn, err)
	}
	c.Flags().Lookup("dsn").Changed = false
	_ = c.Flags().Set("dsn", defaultSQLiteDSN)
	c.Flags().Lookup("dsn").Changed = false
}
