package withings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A worker without an injected client talks through one whose dialer refuses private addresses.
func TestTheDefaultClientIsTheGuardedOne(t *testing.T) {
	w := New(Config{}, nil)
	tr, ok := w.http.Transport.(*http.Transport)
	if !ok || tr == http.DefaultTransport || w.http.CheckRedirect == nil {
		t.Fatalf("default client: %T, redirect check %v", w.http.Transport, w.http.CheckRedirect != nil)
	}
}

func TestAnImportOfMoreThanTheBackendTakesStops(t *testing.T) {
	// Each page carries 1000 groups and says there is more: the collection stops at the cap, with a
	// message that says what to do, instead of growing until the pages run out.
	page := make([]measureGroup, 1000)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := json.Marshal(measBody{Groups: page, More: 1, Offset: calls * 1000})
		json.NewEncoder(rw).Encode(map[string]any{"status": 0, "body": json.RawMessage(body)})
	}))
	defer srv.Close()
	w := New(Config{APIBase: srv.URL}, srv.Client())
	if _, err := w.getMeas(context.Background(), "token", time.Time{}, time.Time{}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("getMeas: %v after %d pages", err, calls)
	}
	if calls > maxMeasureGroups/1000+2 {
		t.Errorf("%d pages were fetched", calls)
	}
}
