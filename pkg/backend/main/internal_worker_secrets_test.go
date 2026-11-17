package mainbackend

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
	"github.com/trueforge-org/dexafix/pkg/store"
)

const (
	demoWorkerSecret   = "demo-worker-secret-0123456789abcdef"
	scriptWorkerSecret = "script-worker-secret-0123456789abcde"
)

// scopedFixture gives each provider its own worker secret.
func scopedFixture(t *testing.T) *scriptedFixture {
	t.Helper()
	f := newScriptedFixture(t)
	err := f.b.SetWorkerSecrets(
		WorkerSecrets{Providers: []string{"demo"}, Secrets: []string{demoWorkerSecret, "older-demo-worker-secret-0123456789"}},
		WorkerSecrets{Providers: []string{"script"}, Secrets: []string{scriptWorkerSecret}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *scriptedFixture) asWorker(secret string) *workerapi.Client {
	c, err := workerapi.New(f.srv.URL, nil, secret)
	if err != nil {
		panic(err)
	}
	return c
}

func TestAWorkerSecretOpensTheJobsOfItsProviderOnly(t *testing.T) {
	f := scopedFixture(t)
	ctx := context.Background()
	job := f.queue(t, demoKind, models.JobOriginUser)

	// The worker of the other provider names the kind and gets what it would for a kind that does not exist.
	other := f.asWorker(scriptWorkerSecret)
	if l, err := other.Lease(ctx, workerapi.LeaseRequest{Worker: "bad", Kinds: []string{demoKind}}); err == nil || l != nil {
		t.Fatalf("a secret of another provider leased a job: %v %v", l, err)
	}
	if got := f.job(t, job.PublicID); got.State != models.JobQueued || got.Attempts != 0 {
		t.Errorf("the job was touched: %s, %d attempts", got.State, got.Attempts)
	}
	// Naming both kinds leases nothing of the other provider either: only its own kinds count.
	f.queue(t, scriptKind, models.JobOriginUser)
	l, err := other.Lease(ctx, workerapi.LeaseRequest{Worker: "bad", Kinds: []string{demoKind, scriptKind}})
	if err != nil || l == nil || l.Job.Kind != scriptKind {
		t.Fatalf("lease = %+v, %v: want the script job", l, err)
	}
	// It cannot register for the other provider's kinds either.
	if code, _ := f.raw("POST", "/register", map[string]string{workerapi.HeaderWorker: scriptWorkerSecret}, `{"id":"w9","kinds":["`+demoKind+`"]}`); code != http.StatusOK {
		t.Fatalf("register: %d", code)
	}
	workers, err := f.st.ListWorkers(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range workers {
		if w.ID == "w9" && len(w.Kinds) != 0 {
			t.Errorf("the worker registered for %v", w.Kinds)
		}
	}

	// The right secret, and a rotated one, lease it.
	for _, secret := range []string{demoWorkerSecret, "older-demo-worker-secret-0123456789"} {
		l, err := f.asWorker(secret).Lease(ctx, workerapi.LeaseRequest{Worker: "w1", Kinds: []string{demoKind}})
		if err != nil || l == nil || l.Job.ID != job.PublicID {
			t.Fatalf("lease with %q: %+v, %v", secret[:5], l, err)
		}
		if err := f.asWorker(secret).Release(ctx, l.Job.ID, l.Token); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEveryLeaseThatCarriesAPayloadLeavesAnAuditEntry(t *testing.T) {
	f := scopedFixture(t)
	f.queue(t, demoKind, models.JobOriginUser)
	if l, err := f.asWorker(demoWorkerSecret).Lease(context.Background(), workerapi.LeaseRequest{Worker: "demo-1", Kinds: []string{demoKind}}); err != nil || l == nil {
		t.Fatalf("lease: %v %v", l, err)
	}
	events, err := f.st.ListAuditEvents(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Action == auditWorkerLease {
			if e.TargetID == nil || *e.TargetID != f.user.ID || e.Detail != demoKind+" worker=demo-1" {
				t.Errorf("audit entry %+v", e)
			}
			if strings.Contains(e.Detail, "s3cret") {
				t.Error("the audit entry holds the payload")
			}
			return
		}
	}
	t.Error("no audit entry for the lease")
}

func TestKnownKindsAreFilteredByProviderAndListedOnce(t *testing.T) {
	f := scopedFixture(t)
	ctx := context.WithValue(context.Background(), workerProvidersKey{}, map[string]bool{"demo": true})
	got := f.b.knownKinds(ctx, []string{demoKind, demoKind, scriptKind, "nosuch.kind", demoKind})
	if len(got) != 1 || got[0] != demoKind {
		t.Errorf("knownKinds = %v", got)
	}
	if got := f.b.knownKinds(context.Background(), []string{demoKind}); len(got) != 0 {
		t.Errorf("a request without a secret named kinds: %v", got)
	}
}

func TestWorkerSecretsAreRefusedWhenTheyWouldOpenTooMuch(t *testing.T) {
	f := newScriptedFixture(t)
	for name, groups := range map[string][]WorkerSecrets{
		"an unknown provider":       {{Providers: []string{"nosuch"}, Secrets: []string{"a"}}},
		"no provider":               {{Secrets: []string{"a"}}},
		"no secret":                 {{Providers: []string{"demo"}, Secrets: []string{" "}}},
		"one secret for two groups": {{Providers: []string{"demo"}, Secrets: []string{"a"}}, {Providers: []string{"script"}, Secrets: []string{"a"}}},
	} {
		if err := f.b.SetWorkerSecrets(groups...); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
