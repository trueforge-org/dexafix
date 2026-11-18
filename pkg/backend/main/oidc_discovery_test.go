package mainbackend

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/store"
)

// The backend never fetches a provider's configuration or keys itself: a worker does (oidc.discover),
// and the backend keeps what it found.

// countingIdP is the fake provider with a count of the requests it got.
func countingIdP(t *testing.T) (*fakeIdP, *atomic.Int64) {
	t.Helper()
	idp := newFakeIdP(t)
	var hits atomic.Int64
	inner := idp.Config.Handler
	idp.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/") {
			hits.Add(1)
		}
		inner.ServeHTTP(w, r)
	})
	return idp, &hits
}

func TestTheFirstSignInStartHasAWorkerFetchTheProviderAndTheResultIsKept(t *testing.T) {
	g := newProviderRig(t)
	idp, hits := countingIdP(t)
	p := models.AuthProvider{Slug: "idp", Label: "IdP", Kind: models.ProviderKindOIDC, Issuer: idp.URL, ClientID: "client-1", ClientSecret: "s", Scopes: "openid"}
	g.addProvider(p)
	if _, err := g.st.GetAuthProviderMetadata(context.Background(), "idp"); err != store.ErrNotFound {
		t.Fatalf("metadata before any sign-in: %v", err)
	}
	for i := 0; i < 3; i++ {
		if state, _, rep := g.beginProvider("idp", map[string]any{}); state == "" {
			t.Fatalf("start %d: %s", i, rep.body())
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("the provider was asked %d times, want once and then the stored answer", n)
	}
	m, err := g.st.GetAuthProviderMetadata(context.Background(), "idp")
	if err != nil || m.AuthURL != idp.URL+"/authorize" || m.TokenURL != idp.URL+"/token" || !strings.Contains(m.JWKS, `"kid":"k1"`) || time.Since(m.FetchedAt) > time.Minute {
		t.Fatalf("stored metadata %+v err=%v", m, err)
	}
}

func TestAProviderThatIsDownIsAskedAgainByTheNextVisitor(t *testing.T) {
	g := newProviderRig(t)
	var down atomic.Bool
	idp := newFakeIdP(t)
	inner := idp.Config.Handler
	idp.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	})
	g.addProvider(models.AuthProvider{Slug: "idp", Label: "IdP", Kind: models.ProviderKindOIDC, Issuer: idp.URL, ClientID: "client-1", ClientSecret: "s"})
	down.Store(true)
	if state, _, rep := g.beginProvider("idp", map[string]any{}); state != "" || !strings.Contains(rep.body(), `"status":502`) {
		t.Fatalf("a provider that is down: %d %s", rep.Code, rep.body())
	}
	down.Store(false)
	if state, _, rep := g.beginProvider("idp", map[string]any{}); state == "" {
		t.Fatalf("a provider that is back: %s", rep.body())
	}
}

func TestSigningInNeedsAWorkerAndSaysSoWithoutConnectingOut(t *testing.T) {
	g := newRig(t)
	idp, hits := countingIdP(t)
	g.addProvider(models.AuthProvider{Slug: "idp", Label: "IdP", Kind: models.ProviderKindOIDC, Issuer: idp.URL, ClientID: "client-1", ClientSecret: "s"})
	if state, _, rep := g.beginProvider("idp", map[string]any{}); state != "" || !strings.Contains(rep.body(), `"status":502`) || !strings.Contains(rep.body(), "not available") {
		t.Fatalf("no worker: %d %s", rep.Code, rep.body())
	}
	if hits.Load() != 0 {
		t.Fatal("the backend asked the provider itself")
	}
}

func TestAnAdministratorWhoSavesAProviderGetsItsConfigurationStoredWithIt(t *testing.T) {
	idp, hits := countingIdP(t)
	ts, admin, st := newAuthServer(t)
	signup(t, ts, admin, "root")
	addProvider(t, ts, admin, idp, false)
	if hits.Load() != 1 {
		t.Fatalf("discovery ran %d times", hits.Load())
	}
	if m, err := st.GetAuthProviderMetadata(context.Background(), "idp"); err != nil || m.Issuer != idp.URL {
		t.Fatalf("metadata %+v err=%v", m, err)
	}
	// Removing the provider removes what was learned about it.
	r := post(t, admin, ts.URL+"/admin/providers/idp/delete", nil)
	r.Body.Close()
	if _, err := st.GetAuthProviderMetadata(context.Background(), "idp"); err != store.ErrNotFound {
		t.Fatalf("metadata after removal: %v", err)
	}
}

func TestATokenSignedWithAnUnknownKeyQueuesOneRefreshAndTheNextRequestWorks(t *testing.T) {
	g := newProviderRig(t)
	idp, hits := countingIdP(t)
	g.addProvider(models.AuthProvider{Slug: "idp", Label: "IdP", Kind: models.ProviderKindOIDC, Issuer: idp.URL, ClientID: "client-1", ClientSecret: "s"})
	uid, _ := g.signUp("alice")
	if err := g.st.PutIdentity(context.Background(), models.Identity{UserID: uid, Provider: "idp", Subject: "sub-1"}); err != nil {
		t.Fatal(err)
	}
	// Nothing is stored yet: the token is refused now, and a worker fetches the keys.
	path := fmt.Sprintf("/api/v1/users/%d/settings", uid)
	hdr := map[string]string{"Authorization": "Bearer " + idp.token(t, "client-1", "sub-1", "")}
	if rec := g.publicGet(context.Background(), path, hdr); rec.Code != http.StatusUnauthorized {
		t.Fatalf("before the keys are known: %d", rec.Code)
	}
	waitFor(t, "a worker fetching the keys", func() bool { _, err := g.st.GetAuthProviderMetadata(context.Background(), "idp"); return err == nil })
	if rec := g.publicGet(context.Background(), path, hdr); rec.Code != http.StatusOK {
		t.Fatalf("with the keys stored: %d", rec.Code)
	}
	if hits.Load() != 1 {
		t.Fatalf("discovery ran %d times", hits.Load())
	}
}
