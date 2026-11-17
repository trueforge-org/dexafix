package mainbackend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
)

// A job that was canceled while it ran does not apply its result or take new credentials: a removed
// login or connection would come back with them.
func TestAResultOrCredentialsAfterACancelAreNotApplied(t *testing.T) {
	f := newScriptedFixture(t)
	ctx := context.Background()
	var applied, saved int
	f.sc.set(func(s *scripted) {
		s.apply = func(context.Context, *Backend, models.Job, json.RawMessage) (string, error) {
			applied++
			return "{}", nil
		}
		s.cred = func(context.Context, *Backend, models.Job, json.RawMessage) error { saved++; return nil }
	})
	j := f.queue(t, scriptKind, models.JobOriginUser)
	l := f.leaseKind(t, scriptKind)
	if _, err := f.st.CancelJob(ctx, j.PublicID, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.client.SaveCredentials(ctx, l.Job.ID, l.Token, json.RawMessage(`{"token":"new"}`)); !errors.Is(err, workerapi.ErrLeaseLost) {
		t.Fatalf("credentials after the cancel: %v", err)
	}
	if err := f.client.Complete(ctx, l.Job.ID, l.Token, workerapi.Completion{Outcome: models.OutcomeOK, Result: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if applied != 0 || saved != 0 {
		t.Fatalf("applied %d results and %d credentials after the cancel", applied, saved)
	}
	if got := f.job(t, j.PublicID); got.State != models.JobCanceled {
		t.Fatalf("the job ended as %s", got.State)
	}
}

// Two reports of one attempt at the same moment: the result is applied once.
func TestTwoReportsOfOneAttemptAreAppliedOnce(t *testing.T) {
	f := newScriptedFixture(t)
	ctx := context.Background()
	var mu sync.Mutex
	applied := 0
	started, release := make(chan struct{}), make(chan struct{})
	f.sc.set(func(s *scripted) {
		s.apply = func(context.Context, *Backend, models.Job, json.RawMessage) (string, error) {
			mu.Lock()
			applied++
			first := applied == 1
			mu.Unlock()
			if first {
				close(started)
				<-release
			}
			return "{}", nil
		}
	})
	f.queue(t, scriptKind, models.JobOriginUser)
	l := f.leaseKind(t, scriptKind)
	done := make(chan error, 1)
	go func() {
		done <- f.client.Complete(ctx, l.Job.ID, l.Token, workerapi.Completion{Outcome: models.OutcomeOK, Result: json.RawMessage(`{}`)})
	}()
	<-started
	if err := f.client.Complete(ctx, l.Job.ID, l.Token, workerapi.Completion{Outcome: models.OutcomeOK, Result: json.RawMessage(`{}`)}); !errors.Is(err, workerapi.ErrLeaseLost) {
		t.Errorf("the second report: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the first report: %v", err)
	}
	if applied != 1 {
		t.Fatalf("the result was applied %d times", applied)
	}
}

func TestAPersonCannotQueueWithoutEnd(t *testing.T) {
	f := newScriptedFixture(t)
	for i := 0; i < maxQueuedJobsPerKind; i++ {
		if _, _, err := f.b.enqueueJob(context.Background(), enqueueSpec{UserID: f.user.ID, Kind: scriptKind, Origin: models.JobOriginUser, Dedupe: "k" + string(rune('a'+i))}); err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
	}
	_, _, err := f.b.enqueueJob(context.Background(), enqueueSpec{UserID: f.user.ID, Kind: scriptKind, Origin: models.JobOriginUser, Dedupe: "one-more"})
	if !errors.Is(err, errTooManyJobs) {
		t.Fatalf("one more: %v", err)
	}
	if msg, known := queueFailure("Script", err); !known || !strings.Contains(msg, "waiting") {
		t.Errorf("message %q %v", msg, known)
	}
	// The scheduler is not held to it, and other people are not affected.
	if _, _, err := f.b.enqueueJob(context.Background(), enqueueSpec{UserID: f.user.ID, Kind: scriptKind, Origin: models.JobOriginSchedule, Dedupe: "scheduled"}); err != nil {
		t.Errorf("a scheduled job: %v", err)
	}
	if _, _, err := f.b.enqueueJob(context.Background(), enqueueSpec{UserID: f.admin.ID, Kind: scriptKind, Origin: models.JobOriginUser}); err != nil {
		t.Errorf("another person's job: %v", err)
	}
	// The count of waiting jobs cannot be read: the request is refused rather than let through.
	f.flaky.listJobs.Store(true)
	if _, _, err := f.b.enqueueJob(context.Background(), enqueueSpec{UserID: f.admin.ID, Kind: scriptKind, Origin: models.JobOriginUser, Dedupe: "x"}); err == nil {
		t.Error("a job was queued without knowing how many wait")
	}
}

// A worker row is kept under the providers its secret opens, so a worker of one provider cannot
// overwrite the row of another's by choosing its id.
func TestWorkerRowsAreKeptPerProvider(t *testing.T) {
	f := scopedFixture(t)
	ctx := context.Background()
	for secret, kind := range map[string]string{demoWorkerSecret: demoKind, scriptWorkerSecret: scriptKind} {
		if code, _ := f.raw("POST", "/register", map[string]string{workerapi.HeaderWorker: secret}, `{"id":"same-name","kinds":["`+kind+`"]}`); code != http.StatusOK {
			t.Fatalf("register: %d", code)
		}
	}
	ws, err := f.st.ListWorkers(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string][]string{}
	for _, w := range ws {
		ids[w.ID] = w.Kinds
	}
	if len(ids["demo:same-name"]) != 1 || len(ids["script:same-name"]) != 1 || len(ids) != 2 {
		t.Fatalf("worker rows %v", ids)
	}
}

func TestTooManyWorkersAreRefusedAtRegistration(t *testing.T) {
	f := scopedFixture(t)
	for i := 0; i < 300; i++ {
		code, _ := f.raw("POST", "/register", map[string]string{workerapi.HeaderWorker: demoWorkerSecret}, `{"id":"w`+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))+`","kinds":["`+demoKind+`"]}`)
		if code != http.StatusOK && code >= 500 {
			t.Fatalf("server error %d", code)
		}
	}
	ws, err := f.st.ListWorkers(context.Background(), time.Now().Add(-time.Hour))
	if err != nil || len(ws) > 256 {
		t.Fatalf("%d worker rows: %v", len(ws), err)
	}
}

// One worker secret may not ask for jobs faster than the lease budget allows.
func TestLeasesAreRateLimitedPerSecret(t *testing.T) {
	f := scopedFixture(t)
	body := `{"worker":"w","kinds":["` + demoKind + `"],"wait_seconds":0}`
	limited := 0
	for i := 0; i < leaseBurst+60; i++ {
		code, _ := f.raw("POST", "/lease", map[string]string{workerapi.HeaderWorker: demoWorkerSecret}, body)
		if code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("a worker asked for jobs without limit")
	}
	// Another secret has a budget of its own.
	if code, _ := f.raw("POST", "/lease", map[string]string{workerapi.HeaderWorker: scriptWorkerSecret}, `{"worker":"w","kinds":["`+scriptKind+`"]}`); code == http.StatusTooManyRequests {
		t.Error("the budget of one secret was used up by another")
	}
}

// What a job keeps of its result is a summary: one beyond a megabyte is not kept, the job fails.
func TestAResultTooLargeToKeepFailsTheJob(t *testing.T) {
	f := newScriptedFixture(t)
	ctx := context.Background()
	f.sc.set(func(s *scripted) {
		s.apply = func(context.Context, *Backend, models.Job, json.RawMessage) (string, error) {
			return `"` + strings.Repeat("x", workerMaxStoredResult) + `"`, nil
		}
	})
	j := f.queue(t, scriptKind, models.JobOriginUser)
	l := f.leaseKind(t, scriptKind)
	if err := f.client.Complete(ctx, l.Job.ID, l.Token, workerapi.Completion{Outcome: models.OutcomeOK, Result: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	got := f.job(t, j.PublicID)
	if got.State != models.JobFailed || got.Result != "" || !strings.Contains(got.Error, "too large") {
		t.Fatalf("%+v", got)
	}
}
