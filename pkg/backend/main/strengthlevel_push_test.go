package mainbackend

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
)

// New scale data queues a seven-day overwriting send to Strength Level, but only for a user who may
// use Strength Level, whom an administrator allowed the push and who switched it on.
func TestNewScaleDataQueuesTheStrengthLevelPushOnlyThroughEveryGate(t *testing.T) {
	ctx := context.Background()
	w := 80.0
	add := func(r *jobRig) {
		if err := r.api.CreateScaleReading(r.alice, 2, models.ScaleReading{MeasuredAt: time.Now().UTC().Add(-time.Hour), WeightKg: &w}); err != nil {
			t.Fatal(err)
		}
	}
	queued := func(r *jobRig) []models.Job {
		jobs, err := r.st.ListJobs(ctx, models.JobFilter{UserID: 2, Kinds: []string{kindStrengthLevelSync}})
		if err != nil {
			t.Fatal(err)
		}
		return jobs
	}
	for name, tc := range map[string]struct {
		slAuto, allowed, on, login bool
		want                       bool
	}{
		"everything on":                 {true, true, true, true, true},
		"Strength Level not allowed":    {false, true, true, true, false},
		"push not allowed":              {true, false, true, true, false},
		"the user did not switch it on": {true, true, false, true, false},
		"no saved login":                {true, true, true, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := newJobRig(t)
			if tc.slAuto {
				r.allowStrengthLevel(false)
			}
			if tc.login {
				r.saveStrengthLevelLogin()
			}
			if tc.allowed {
				if err := r.st.SetUserStrengthLevelPushAllowed(ctx, 2, true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.on {
				if err := r.st.SetUserStrengthLevelPush(ctx, 2, true); err != nil && tc.allowed {
					t.Fatal(err)
				}
			}
			add(r)
			add(r) // a second change merges into the queued job
			jobs := queued(r)
			if !tc.want {
				if len(jobs) != 0 {
					t.Fatalf("%d jobs queued, want none", len(jobs))
				}
				return
			}
			if len(jobs) != 1 || jobs[0].Origin != models.JobOriginAuto || jobs[0].Interactive {
				t.Fatalf("jobs %+v, want one automatic job", jobs)
			}
			job, err := r.st.GetJob(ctx, jobs[0].PublicID)
			if err != nil {
				t.Fatal(err)
			}
			var p slSyncPayload
			if err := json.Unmarshal([]byte(job.Payload), &p); err != nil {
				t.Fatal(err)
			}
			if p.Days != 7 || !p.Update {
				t.Fatalf("payload %+v, want 7 days overwriting", p)
			}
			// Switched off after it was queued: the job refuses to run.
			if err := r.st.SetUserStrengthLevelPushAllowed(ctx, 2, false); err != nil {
				t.Fatal(err)
			}
			if _, err := leaseStrengthLevelSync(ctx, r.b, job); err == nil {
				t.Fatal("a job queued before the push was forbidden still runs")
			}
		})
	}
}

// A Withings import queues the push only when it changed something.
func TestWithingsImportQueuesTheStrengthLevelPushOnlyWhenReadingsChanged(t *testing.T) {
	ctx := context.Background()
	r := newJobRig(t)
	r.allowStrengthLevel(false)
	r.saveStrengthLevelLogin()
	if err := r.st.SetUserStrengthLevelPushAllowed(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetUserStrengthLevelPush(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	apply := func(result string) {
		since, _ := json.Marshal(importPayload{Since: time.Now().Add(-48 * time.Hour)})
		if _, err := applyWithingsImport(ctx, r.b, models.Job{UserID: 2, Kind: kindWithingsImport, Payload: string(since)}, json.RawMessage(result)); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		jobs, _ := r.st.ListJobs(ctx, models.JobFilter{UserID: 2, Kinds: []string{kindStrengthLevelSync}})
		return len(jobs)
	}
	apply(`{"readings":[]}`)
	if count() != 0 {
		t.Fatal("an import that found nothing queued a send")
	}
	apply(`{"readings":[{"source":"withings","external_id":"g1","measured_at":"` + at + `","weight_kg":80}],"seen":["g1"]}`)
	if count() != 1 {
		t.Fatalf("%d sends after new data, want 1", count())
	}
}

// An administrator allows the two automatic syncs per user; the users' own switches stay off, and
// forbidding Strength Level sync also takes the push permission away.
func TestAdminAllowsTheAutomaticSyncsPerUser(t *testing.T) {
	ctx := context.Background()
	r := newJobRig(t)
	allowed := func() models.User {
		u, err := r.st.GetUser(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	if res, err := r.api.AdminSetStrengthLevelPush(r.root, 2, true); err != nil || res.Message != "Enable Strength Level sync for alice first." {
		t.Fatalf("push before Strength Level sync: %+v %v", res, err)
	}
	if allowed().StrengthLevelPushAllowed {
		t.Fatal("the push was allowed for a user without Strength Level sync")
	}
	for _, f := range []func() error{
		func() error { _, err := r.api.AdminSetStrengthLevel(r.root, 2, true); return err },
		func() error { _, err := r.api.AdminSetStrengthLevelPush(r.root, 2, true); return err },
		func() error { _, err := r.api.AdminSetWithingsAuto(r.root, 2, true); return err },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	u := allowed()
	if !u.StrengthLevelPushAllowed || !u.WithingsAutoAllowed || u.StrengthLevelPush || u.WithingsAuto {
		t.Fatalf("permissions %+v: both must be allowed and neither switched on", u)
	}
	page, err := r.api.AdminPage(r.root, "")
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, x := range page.Users {
		if x.ID == 2 {
			seen = x.WithingsAuto && x.StrengthLevelPush
		}
	}
	if !seen {
		t.Fatal("the admin page does not show the permissions")
	}
	if err := r.st.SetUserStrengthLevelPush(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.api.AdminSetStrengthLevel(r.root, 2, false); err != nil {
		t.Fatal(err)
	}
	if u = allowed(); u.StrengthLevelPushAllowed || u.StrengthLevelPush || !u.WithingsAutoAllowed {
		t.Fatalf("after forbidding Strength Level: %+v", u)
	}
	if _, err := r.api.AdminSetWithingsAuto(r.alice, 2, true); err == nil {
		t.Fatal("a user changed their own permission")
	}
}
