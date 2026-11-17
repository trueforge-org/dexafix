package mainbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/store"
)

// A Withings result is applied to the days the job asked for and no others, so a worker cannot
// overwrite the readings of any other day.
func TestAWithingsResultOutsideTheJobsWindowIsRefused(t *testing.T) {
	f := newWorkerFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	reading := func(at time.Time) string {
		return fmt.Sprintf(`{"readings":[{"source":"withings","external_id":"g1","measured_at":%q,"weight_kg":80}]}`, at.Format(time.RFC3339))
	}
	apply := func(payload importPayload, result string) error {
		p, _ := json.Marshal(payload)
		_, err := applyWithingsImport(ctx, f.b, models.Job{UserID: f.user.ID, Kind: kindWithingsImport, Payload: string(p)}, json.RawMessage(result))
		return err
	}
	since := now.Add(-14 * 24 * time.Hour)
	day := now.Add(-5 * 24 * time.Hour).Format(dateLayout)
	dayStart, _ := time.Parse(dateLayout, day)

	for name, tc := range map[string]struct {
		payload importPayload
		at      time.Time
		ok      bool
	}{
		"bulk, since the start":               {importPayload{Since: since}, since.Add(time.Hour), true},
		"bulk, a day before the start":        {importPayload{Since: since}, since.Add(-23 * time.Hour), true},
		"bulk, long before the start":         {importPayload{Since: since}, since.Add(-30 * 24 * time.Hour), false},
		"a full import reaches far back":      {importPayload{}, time.Date(2015, 6, 1, 8, 0, 0, 0, time.UTC), true},
		"nothing before 1900":                 {importPayload{}, time.Date(1850, 1, 1, 0, 0, 0, 0, time.UTC), false},
		"nothing in year 1":                   {importPayload{}, time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), false},
		"nothing in year 9999":                {importPayload{}, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), false},
		"nothing a week ahead":                {importPayload{}, now.Add(7 * 24 * time.Hour), false},
		"a day import takes its day":          {importPayload{Day: day}, dayStart.Add(10 * time.Hour), true},
		"a day import allows the zone's edge": {importPayload{Day: day}, dayStart.Add(-2 * time.Hour), true},
		"a day import refuses another day":    {importPayload{Day: day}, dayStart.Add(-10 * 24 * time.Hour), false},
		"a day import refuses a later day":    {importPayload{Day: day}, dayStart.Add(5 * 24 * time.Hour), false},
	} {
		err := apply(tc.payload, reading(tc.at))
		if tc.ok && err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
		if !tc.ok && err != errBadResult {
			t.Errorf("%s: accepted or failed differently: %v", name, err)
		}
	}
	if err := apply(importPayload{Day: "not-a-day"}, `{"readings":[]}`); err != errBadResult {
		t.Errorf("a bad day: %v", err)
	}
	// An unknown time zone falls back to UTC instead of failing the job.
	if userLocation("Mars/Olympus") != time.UTC || userLocation("Europe/Amsterdam").String() != "Europe/Amsterdam" {
		t.Error("userLocation")
	}
}

// What a worker hands over as a token pair is stored as the fields of the token format only.
func TestWithingsTokensAreStoredWithTheKnownFieldsOnly(t *testing.T) {
	f := newWorkerFixture(t)
	ctx := context.Background()
	job := models.Job{UserID: f.user.ID, Kind: kindWithingsImport}
	in := `{"access_token":"a","refresh_token":"r","expires_at":"2026-01-02T03:04:05Z","withings_userid":"42","evil":"x","app":{"client_secret":"y"}}`
	if err := f.b.storeWithingsTokens(ctx, job, json.RawMessage(in), true); err != nil {
		t.Fatal(err)
	}
	raw, err := f.st.GetIntegration(ctx, f.user.ID, providerWithings)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("%q: %v", got, err)
	}
	if len(fields) != 4 || fields["access_token"] != "a" || fields["refresh_token"] != "r" || fields["withings_userid"] != "42" ||
		strings.Contains(got, "evil") || strings.Contains(got, "client_secret") {
		t.Errorf("stored %s", got)
	}
	// A job that only refreshes does not bring back a connection that was removed.
	if err := f.st.DeleteIntegration(ctx, f.user.ID, providerWithings); err != nil && err != store.ErrNotFound {
		t.Fatal(err)
	}
	if err := f.b.storeWithingsTokens(ctx, job, json.RawMessage(in), false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.GetIntegration(ctx, f.user.ID, providerWithings); err != store.ErrNotFound {
		t.Errorf("the removed connection came back: %v", err)
	}
}
