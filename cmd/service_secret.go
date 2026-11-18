package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	mainbackend "github.com/trueforge-org/dexafix/pkg/backend/main"
	"github.com/trueforge-org/dexafix/pkg/workers/builtin"
)

// The web (frontend) and api (backend) processes prove to each other with a shared secret in a
// file. The first line is the secret the web process sends; the backend accepts every line, so a
// secret is rotated by adding a new first line, restarting both, and removing the old line later.

// minServiceSecretLen is the shortest accepted secret, in characters. `secrets service-secret`
// makes 64 hex characters (256 bits).
const minServiceSecretLen = 32

func addServiceSecretFlag(c *cobra.Command) {
	c.Flags().String("service-secret-file", envOr("DEXAFIX_SERVICE_SECRET_FILE", ""),
		"file with the secret the web and api processes share, one per line (the first is the one sent); create one with `dexafix secrets service-secret`")
}

// secretFile describes a file of shared secrets, for the messages that name it.
type secretFile struct {
	what   string // "service secret"
	flag   string // "--service-secret-file"
	env    string // "DEXAFIX_SERVICE_SECRET_FILE"
	create string // "dexafix secrets service-secret"
}

var (
	serviceSecretFile = secretFile{"service secret", "--service-secret-file", "DEXAFIX_SERVICE_SECRET_FILE", "dexafix secrets service-secret"}
	workerSecretFile  = secretFile{"worker secret", "--worker-secret-file", "DEXAFIX_WORKER_SECRET_FILE", "dexafix secrets worker-secret"}
)

// loadServiceSecrets reads the secrets in a file. Blank lines and lines starting with # are skipped.
func loadServiceSecrets(path string) ([]string, error) { return serviceSecretFile.load(path) }

// loadWorkerSecrets reads the secrets the workers and the api process share, in the same format.
func loadWorkerSecrets(path string) ([]string, error) { return workerSecretFile.load(path) }

func (k secretFile) load(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("%s (%s) is required: create one with `%s`", k.flag, k.env, k.create)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", k.what, err)
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 && !mountedSecret(path) {
		log.Warn().Str("file", path).Str("mode", fmt.Sprintf("%04o", fi.Mode().Perm())).
			Msg("the " + k.what + " file can be read by other users; restrict it (chmod 600) unless a secret store mounts it for you")
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) < minServiceSecretLen {
			return nil, fmt.Errorf("%s: a %s must be at least %d characters; create one with `%s`", path, k.what, minServiceSecretLen, k.create)
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no secret", path)
	}
	return out, nil
}

func newServiceSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails: since Go 1.24 a broken random source ends the program
	return hex.EncodeToString(b)
}

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Create secrets for a deployment",
}

var serviceSecretCmd = &cobra.Command{
	Use:   "service-secret",
	Short: "Print a new secret for the web and api processes to share",
	Long: "Prints a new random secret. Save it in a file readable only by the two processes and give the path " +
		"to both with --service-secret-file:\n\n  dexafix secrets service-secret > service.secret && chmod 600 service.secret",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(cmd.OutOrStdout(), newServiceSecret())
		return nil
	},
}

var workerSecretCmd = &cobra.Command{
	Use:   "worker-secret",
	Short: "Print a new secret for the api process and its workers to share",
	Long: "Prints a new random secret. Save it in a file readable only by the api process and the workers and give the path " +
		"to both with --worker-secret-file. It must differ from the service secret: the two open different doors.\n\n" +
		"  dexafix secrets worker-secret > worker.secret && chmod 600 worker.secret",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintln(cmd.OutOrStdout(), newServiceSecret())
		return nil
	},
}

func addWorkerSecretFlag(c *cobra.Command, help string) {
	c.Flags().String("worker-secret-file", envOr("DEXAFIX_WORKER_SECRET_FILE", ""), help)
}

// addWorkerSecretFilesFlag is the api process's side: one secret file per provider, so that the
// secret of one provider's workers opens the jobs of that provider only.
func addWorkerSecretFilesFlag(c *cobra.Command) {
	var def []string
	if v := os.Getenv("DEXAFIX_WORKER_SECRET_FILES"); v != "" {
		def = strings.Split(v, ";")
	} else if v := os.Getenv("DEXAFIX_WORKER_SECRET_FILE"); v != "" {
		def = []string{v} // the name before secrets were per provider: still read, as a legacy file
	}
	c.Flags().StringSlice("worker-secret-file", def,
		"PROVIDERS=FILE: the file with the secret of the workers of those providers, one per line (the first is the one they send); "+
			"repeat it for each provider (withings=/run/secrets/withings,strengthlevel=...), or list providers with + (a+b=FILE) only if they must share a worker; "+
			"create one with `dexafix secrets worker-secret`. Every secret must differ from the others and from the service secret")
}

// loadWorkerSecretGroups reads the PROVIDERS=FILE entries of --worker-secret-file. A secret opens
// the providers named in its entry and nothing else, and no secret may serve two entries.
func loadWorkerSecretGroups(entries []string) ([]mainbackend.WorkerSecrets, []string, error) {
	known := map[string]bool{}
	for _, n := range builtin.Names() {
		known[n] = true
	}
	var groups []mainbackend.WorkerSecrets
	var all []string
	seen := map[string]bool{}
	for _, e := range entries {
		names, path, ok := strings.Cut(e, "=")
		if !ok && e != "" && !strings.ContainsAny(e, "=+") {
			// Before secrets were per provider the flag was a plain path, and the secret in it opened
			// every provider. That still works, so an upgrade does not stop the workers, but it is the
			// configuration the per-provider secrets exist to replace.
			log.Warn().Str("file", e).Msg("--worker-secret-file without PROVIDERS= opens every provider's jobs to one secret: " +
				"give each provider its own file (withings=FILE --worker-secret-file strengthlevel=FILE), see docs/admin/07-workers.md")
			names, path, ok = strings.Join(builtin.Names(), "+"), e, true
		}
		if !ok || names == "" || path == "" {
			return nil, nil, fmt.Errorf("--worker-secret-file %q: write PROVIDERS=FILE, such as withings=/run/secrets/worker_withings (a worker secret opens only the providers named)", e)
		}
		var providers []string
		for _, n := range strings.Split(names, "+") {
			if n = strings.TrimSpace(n); !known[n] {
				return nil, nil, fmt.Errorf("--worker-secret-file %q: unknown worker provider %q (known: %s)", e, n, strings.Join(builtin.Names(), ", "))
			}
			providers = append(providers, n)
		}
		secrets, err := loadWorkerSecrets(path)
		if err != nil {
			return nil, nil, err
		}
		for _, sec := range secrets {
			if seen[sec] {
				return nil, nil, fmt.Errorf("--worker-secret-file %q: this secret is already used for other providers; each provider's workers need a secret of their own", e)
			}
			seen[sec] = true
		}
		groups = append(groups, mainbackend.WorkerSecrets{Providers: providers, Secrets: secrets})
		all = append(all, secrets...)
	}
	return groups, all, nil
}

func init() {
	secretsCmd.AddCommand(serviceSecretCmd, workerSecretCmd)
	RootCmd.AddCommand(secretsCmd)
}

// mountedSecret reports whether path is in one of the places where a container platform mounts
// secrets (Docker, Compose, Kubernetes). Those files are managed by the platform, which commonly makes
// them readable inside the container only, so the permission warning would just be noise.
func mountedSecret(path string) bool {
	p := filepath.Clean(path)
	return strings.HasPrefix(p, "/run/secrets/") || strings.HasPrefix(p, "/var/run/secrets/")
}
