package mainbackend

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
)

// discoveryServer answers the OpenID discovery document, counting how often it is asked.
func discoveryServer(t *testing.T, delay time.Duration, fail *atomic.Bool) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(delay)
		if fail != nil && fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"id_token_signing_alg_values_supported":["RS256"]}`,
			srv.URL, srv.URL+"/auth", srv.URL+"/token", srv.URL+"/keys")
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestDiscoveryRunsOncePerIssuerWhoeverAsks(t *testing.T) {
	g := newRig(t)
	srv, hits := discoveryServer(t, 150*time.Millisecond, nil)
	p := models.AuthProvider{Slug: "idp", Issuer: srv.URL}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.b.oidcProvider(context.Background(), p); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Fatalf("the provider was asked %d times at once, want 1", n)
	}
	if _, err := g.b.oidcProvider(context.Background(), p); err != nil || hits.Load() != 1 {
		t.Fatalf("a cached provider was fetched again: %v, %d requests", err, hits.Load())
	}
}

func TestAFailedDiscoveryIsRememberedForAMinute(t *testing.T) {
	g := newRig(t)
	var down atomic.Bool
	down.Store(true)
	srv, hits := discoveryServer(t, 0, &down)
	p := models.AuthProvider{Slug: "idp", Issuer: srv.URL}
	for i := 0; i < 5; i++ {
		if _, err := g.b.oidcProvider(context.Background(), p); err == nil {
			t.Fatal("a provider that is down was discovered")
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("a provider that is down was asked %d times, want 1", n)
	}
	// An administrator who changes the provider wants it tried again at once.
	down.Store(false)
	g.b.forgetOIDCProvider(srv.URL)
	if _, err := g.b.oidcProvider(context.Background(), p); err != nil {
		t.Fatalf("after the provider was forgotten: %v", err)
	}
	// And a failure that is older than the minute is tried again by itself.
	g.b.forgetOIDCProvider(srv.URL)
	g.b.oidcMu.Lock()
	delete(g.b.oidcCache, srv.URL)
	g.b.oidcFailed[srv.URL] = oidcFailure{err: fmt.Errorf("old"), until: time.Now().Add(-time.Second)}
	g.b.oidcMu.Unlock()
	if _, err := g.b.oidcProvider(context.Background(), p); err != nil {
		t.Fatalf("after the failure expired: %v", err)
	}
}

func TestADiscoveryThatGivesUpBecauseTheVisitorLeftIsNotRemembered(t *testing.T) {
	g := newRig(t)
	srv, hits := discoveryServer(t, 300*time.Millisecond, nil)
	p := models.AuthProvider{Slug: "idp", Issuer: srv.URL}
	gone, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := g.b.oidcProvider(gone, p); err == nil {
		t.Fatal("a discovery that was cut off succeeded")
	}
	if _, err := g.b.oidcProvider(context.Background(), p); err != nil {
		t.Fatalf("the next visitor was refused: %v", err)
	}
	if hits.Load() < 2 {
		t.Errorf("the cut-off was remembered as a failure of the provider (%d requests)", hits.Load())
	}
	// A visitor who leaves while another fetch runs stops waiting without ending that fetch.
	g2 := newRig(t)
	srv2, _ := discoveryServer(t, 300*time.Millisecond, nil)
	p2 := models.AuthProvider{Slug: "idp2", Issuer: srv2.URL}
	started := make(chan struct{})
	go func() { close(started); g2.b.oidcProvider(context.Background(), p2) }()
	<-started
	time.Sleep(50 * time.Millisecond)
	waiter, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	if _, err := g2.b.oidcProvider(waiter, p2); err == nil {
		t.Error("a waiter that left got an answer")
	}
}

func TestAProviderAnswerIsCutOffAtAMegabyte(t *testing.T) {
	g := newRig(t)
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"issuer":"x","pad":"`)
		io.WriteString(w, strings.Repeat("a", 2<<20))
		io.WriteString(w, `"}`)
	}))
	defer big.Close()
	if _, err := g.b.oidcProvider(context.Background(), models.AuthProvider{Slug: "big", Issuer: big.URL}); err == nil {
		t.Fatal("an answer of two megabytes was read")
	}
	// What is exactly at the limit still reads; the body is not cut when it ends there.
	for name, size := range map[string]int{"exactly the limit": maxProviderResponse, "one over": maxProviderResponse + 1, "small": 10} {
		body := &limitedBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("a", size))), left: maxProviderResponse}
		got, err := io.ReadAll(body)
		switch {
		case size > maxProviderResponse && err == nil:
			t.Errorf("%s: read %d bytes without an error", name, len(got))
		case size <= maxProviderResponse && (err != nil || len(got) != size):
			t.Errorf("%s: read %d bytes: %v", name, len(got), err)
		}
	}
}
