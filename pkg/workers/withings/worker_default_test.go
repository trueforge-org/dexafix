package withings

import (
	"net/http"
	"testing"
)

// A worker without an injected client talks through one whose dialer refuses private addresses.
func TestTheDefaultClientIsTheGuardedOne(t *testing.T) {
	w := New(Config{}, nil)
	tr, ok := w.http.Transport.(*http.Transport)
	if !ok || tr == http.DefaultTransport || w.http.CheckRedirect == nil {
		t.Fatalf("default client: %T, redirect check %v", w.http.Transport, w.http.CheckRedirect != nil)
	}
}
