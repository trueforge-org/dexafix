package mainbackend

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
)

func TestButtonImportSinceIsTheStartOfYesterdayInTheUsersZone(t *testing.T) {
	now := time.Date(2026, 3, 10, 1, 30, 0, 0, time.UTC) // still the 9th in Los Angeles
	for zone, want := range map[string]time.Time{
		"UTC":                 time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		"America/Los_Angeles": time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC), // 8 March 00:00 PST
		"not/a-zone":          time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
	} {
		if got := buttonImportSince(now, zone); !got.Equal(want) {
			t.Errorf("%s: since %v, want %v", zone, got, want)
		}
	}
}

// The button asks for two days, Sync all for everything, and a day re-import for that day.
func TestImportQueuesTheWindowTheRequestAsksFor(t *testing.T) {
	impatient(t)
	for name, tc := range map[string]struct {
		do        func(r *jobRig)
		wantSince bool
		wantDay   string
	}{
		"the button":   {func(r *jobRig) { _, _ = r.api.Import(r.alice, 2, "withings", "") }, true, ""},
		"sync all":     {func(r *jobRig) { _, _ = r.api.ImportAll(r.alice, 2, "withings") }, false, ""},
		"a single day": {func(r *jobRig) { _, _ = r.api.Import(r.alice, 2, "withings", "2026-03-01") }, false, "2026-03-01"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newJobRig(t)
			r.connectWithings()
			r.workerIdle(kindWithingsImport)
			tc.do(r)
			jobs, err := r.st.ListJobs(context.Background(), models.JobFilter{UserID: 2, Kinds: []string{kindWithingsImport}})
			if err != nil || len(jobs) != 1 {
				t.Fatalf("jobs %v, err %v", jobs, err)
			}
			job, err := r.st.GetJob(context.Background(), jobs[0].PublicID) // the list leaves payloads out
			if err != nil {
				t.Fatal(err)
			}
			var p importPayload
			if err := json.Unmarshal([]byte(job.Payload), &p); err != nil {
				t.Fatal(err)
			}
			if p.Day != tc.wantDay || p.Since.IsZero() == tc.wantSince {
				t.Fatalf("payload %+v, want since set: %v, day %q", p, tc.wantSince, tc.wantDay)
			}
			if tc.wantSince && time.Since(p.Since) > 72*time.Hour {
				t.Fatalf("since %v is more than about two days back", p.Since)
			}
		})
	}
}

// The daily run goes to the connected users an administrator allowed and who switched it on, and it
// asks for the last two days only.
func TestDailyWithingsImportIsScheduledOnlyForUsersWhoOptedIn(t *testing.T) {
	r := newJobRig(t)
	ctx := context.Background()
	r.connectWithings()
	if err := r.st.PutIntegration(ctx, 1, "withings", []byte(`{}`)); err != nil { // the administrator, too
		t.Fatal(err)
	}
	run := func() []models.Job {
		r.b.runScheduler(ctx)
		jobs, err := r.st.ListJobs(ctx, models.JobFilter{Kinds: []string{kindWithingsImport}})
		if err != nil {
			t.Fatal(err)
		}
		return jobs
	}
	if jobs := run(); len(jobs) != 0 {
		t.Fatalf("%d jobs before anybody opted in", len(jobs))
	}
	// Allowed, but not switched on.
	if err := r.st.SetUserWithingsAutoAllowed(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if jobs := run(); len(jobs) != 0 {
		t.Fatalf("%d jobs for a user who has not switched it on", len(jobs))
	}
	// Switched on for alice; the administrator never allowed it for themselves.
	if err := r.st.SetUserWithingsAuto(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	jobs := run()
	if len(jobs) != 1 || jobs[0].UserID != 2 || jobs[0].Origin != models.JobOriginSchedule {
		t.Fatalf("jobs %+v", jobs)
	}
	job, err := r.st.GetJob(ctx, jobs[0].PublicID)
	if err != nil {
		t.Fatal(err)
	}
	var p importPayload
	if err := json.Unmarshal([]byte(job.Payload), &p); err != nil {
		t.Fatal(err)
	}
	if age := time.Since(p.Since); age < 47*time.Hour || age > 50*time.Hour {
		t.Fatalf("the run looks %v back, want about two days", age)
	}
	// Taking the permission away stops it.
	if err := r.st.SetUserWithingsAutoAllowed(ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	if err := r.st.PutJobSchedule(ctx, models.JobSchedule{UserID: 2, Kind: kindWithingsImport}); err != nil { // due again
		t.Fatal(err)
	}
	if got := run(); len(got) != 1 {
		t.Fatalf("a forbidden user was scheduled again: %d jobs", len(got))
	}
}
