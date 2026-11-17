package cmd

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/trueforge-org/dexafix/pkg/shared/edge"
	"github.com/trueforge-org/dexafix/pkg/workers/builtin"
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

// A slow reader must not hold a connection for as long as it likes: every listener has a write
// timeout (downloads extend their own).
func TestEveryServerHasAWriteTimeout(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.WriteTimeout != edge.WriteTimeout || srv.ReadTimeout == 0 || srv.ReadHeaderTimeout == 0 {
		t.Fatalf("timeouts: read %v, header %v, write %v", srv.ReadTimeout, srv.ReadHeaderTimeout, srv.WriteTimeout)
	}
}

// Before secrets were per provider the flag was a plain path (and the variable had another name). Both
// still work, so an upgrade does not stop the workers; the secret then opens every provider, which is
// said in the log.
func TestALegacyWorkerSecretFileStillWorksAndIsWarnedAbout(t *testing.T) {
	logs := captureLog(t)
	path := writeSecretFile(t, strings.Repeat("w", 40)+"\n")
	groups, all, err := loadWorkerSecretGroups([]string{path})
	if err != nil || len(groups) != 1 || len(all) != 1 {
		t.Fatalf("%+v %v %v", groups, all, err)
	}
	if len(groups[0].Providers) != len(builtin.Names()) {
		t.Errorf("the legacy secret opens %v, want every provider", groups[0].Providers)
	}
	if !strings.Contains(logs.String(), "opens every provider") {
		t.Errorf("no warning: %s", logs)
	}
	t.Setenv("DEXAFIX_WORKER_SECRET_FILES", "")
	t.Setenv("DEXAFIX_WORKER_SECRET_FILE", path)
	c := &cobra.Command{}
	addWorkerSecretFilesFlag(c)
	if got, _ := c.Flags().GetStringSlice("worker-secret-file"); len(got) != 1 || got[0] != path {
		t.Errorf("the old variable gives %v", got)
	}
}

func TestTheWorkerSecretVariableHoldsSeveralEntries(t *testing.T) {
	t.Setenv("DEXAFIX_WORKER_SECRET_FILES", "withings=/a;strengthlevel=/b")
	c := &cobra.Command{}
	addWorkerSecretFilesFlag(c)
	if got, _ := c.Flags().GetStringSlice("worker-secret-file"); len(got) != 2 || got[0] != "withings=/a" || got[1] != "strengthlevel=/b" {
		t.Errorf("entries %v", got)
	}
}

func TestSecretsInitReportsWhatItCannotRead(t *testing.T) {
	o, d := testInit(t)
	o.workerDirs = nil
	o.legacyWorkerDir = d["worker"]
	if err := os.MkdirAll(filepath.Join(d["api"], fileWorkerSecret), 0o700); err != nil { // a folder where the secret file belongs
		t.Fatal(err)
	}
	if err := o.run(); err == nil {
		t.Error("a worker secret that cannot be read was accepted")
	}
	root := t.TempDir()
	if _, _, err := run(t, "secrets", "init", "--host-dir", filepath.Join(root, "h"), "--api-dir", filepath.Join(root, "a"), "--worker-dir", "nosuch="+filepath.Join(root, "w")); err == nil {
		t.Error("a worker dir of an unknown provider was accepted")
	}
}
