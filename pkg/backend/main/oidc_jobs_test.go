package mainbackend

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/shared/apiclient"
	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
	"github.com/trueforge-org/dexafix/pkg/store"
)

// jobFaults is a store whose calls fail when a test says so, for the steps around the jobs of the
// sign-in providers and the mail: a name is the method.
type jobFaults struct {
	store.Store
	failing atomic.Value // map[string]bool, replaced as a whole
}

func (f *jobFaults) Unwrap() store.Store { return f.Store }

func (f *jobFaults) failOn(calls ...string) {
	m := map[string]bool{}
	for _, c := range calls {
		m[c] = true
	}
	f.failing.Store(m)
}

func (f *jobFaults) fails(call string) bool {
	m, _ := f.failing.Load().(map[string]bool)
	return m[call]
}

func (f *jobFaults) ClearJobResult(ctx context.Context, id string) error {
	if f.fails("ClearJobResult") {
		return errFlaky
	}
	return f.Store.ClearJobResult(ctx, id)
}
func (f *jobFaults) ListJobs(ctx context.Context, fl models.JobFilter) ([]models.Job, error) {
	if f.fails("ListJobs") {
		return nil, errFlaky
	}
	return f.Store.ListJobs(ctx, fl)
}
func (f *jobFaults) ListWorkers(ctx context.Context, since time.Time) ([]models.WorkerInstance, error) {
	if f.fails("ListWorkers") {
		return nil, errFlaky
	}
	return f.Store.ListWorkers(ctx, since)
}
func (f *jobFaults) ListAuthProviders(ctx context.Context) ([]models.AuthProvider, error) {
	if f.fails("ListAuthProviders") {
		return nil, errFlaky
	}
	return f.Store.ListAuthProviders(ctx)
}
func (f *jobFaults) GetAuthProviderMetadata(ctx context.Context, slug string) (models.AuthProviderMetadata, error) {
	if f.fails("GetAuthProviderMetadata") {
		return models.AuthProviderMetadata{}, errFlaky
	}
	return f.Store.GetAuthProviderMetadata(ctx, slug)
}
func (f *jobFaults) GetAuthProvider(ctx context.Context, slug string) (models.AuthProvider, error) {
	if f.fails("GetAuthProvider") {
		return models.AuthProvider{}, errFlaky
	}
	return f.Store.GetAuthProvider(ctx, slug)
}
func (f *jobFaults) PutAuthProviderMetadata(ctx context.Context, m models.AuthProviderMetadata) error {
	if f.fails("PutAuthProviderMetadata") {
		return errFlaky
	}
	return f.Store.PutAuthProviderMetadata(ctx, m)
}
func (f *jobFaults) GetIdentity(ctx context.Context, provider, subject string) (models.Identity, error) {
	if f.fails("GetIdentity") {
		return models.Identity{}, errFlaky
	}
	return f.Store.GetIdentity(ctx, provider, subject)
}
func (f *jobFaults) GetSetting(ctx context.Context, name string) (string, error) {
	if f.fails("GetSetting") {
		return "", errFlaky
	}
	return f.Store.GetSetting(ctx, name)
}

// faultyRig is a rig (without workers) over a store that can be made to fail, with an OpenID
// provider saved and its metadata stored, as a first sign-in would have left them.
func faultyRig(t *testing.T) (*rig, *jobFaults, *fakeIdP, models.AuthProvider) {
	t.Helper()
	base := newRig(t)
	faults := &jobFaults{Store: base.st}
	g := newRigOn(t, faults, base.st)
	idp := newFakeIdP(t)
	p := models.AuthProvider{Slug: "idp", Label: "IdP", Kind: models.ProviderKindOIDC, Issuer: idp.URL, ClientID: "client-1", ClientSecret: "s3cret", Scopes: "openid"}
	g.addProvider(p)
	if err := g.st.PutAuthProviderMetadata(context.Background(), idpMetadata(t, idp, "idp")); err != nil {
		t.Fatal(err)
	}
	return g, faults, idp, p
}

// idpMetadata is what a worker would have found at the fake provider.
func idpMetadata(t *testing.T, idp *fakeIdP, slug string) models.AuthProviderMetadata {
	t.Helper()
	resp, err := http.Get(idp.URL + "/jwks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	keys, _ := io.ReadAll(resp.Body)
	return models.AuthProviderMetadata{Slug: slug, Issuer: idp.URL, AuthURL: idp.URL + "/authorize", TokenURL: idp.URL + "/token", JWKS: string(keys), FetchedAt: time.Now()}
}

func TestTheFailureMessagesTellWhatCanBeDone(t *testing.T) {
	p := models.AuthProvider{Label: "IdP"}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"no worker":     {errNoWorker, "not available right now"},
		"paused":        {errKindPaused, "not available right now"},
		"too many":      {errTooManyJobs, "busy"},
		"rotated keys":  {errStaleKeys, "changed its signing keys"},
		"anything else": {errors.New("boom"), "Could not sign in with IdP."},
	} {
		if got := providerFailureMessage(p, tc.err); !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q, want it to say %q", name, got, tc.want)
		}
	}
	if got := outboundFailure(errNoWorker, "Could not send the test e-mail", settingLabelMail); !strings.Contains(got, "no worker") {
		t.Errorf("no worker: %q", got)
	}
	if got := outboundFailure(errKindPaused, "Could not send the test e-mail", settingLabelMail); !strings.Contains(got, "paused") {
		t.Errorf("paused: %q", got)
	}
}

// ---- stored keys ----

func rewriteHeader(t *testing.T, token string, header map[string]string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	h, _ := json.Marshal(header)
	parts[0] = base64.RawURLEncoding.EncodeToString(h)
	return strings.Join(parts, ".")
}

func TestStoredKeysVerifyATokenAndKnowWhichKeyItNames(t *testing.T) {
	idp := newFakeIdP(t)
	m := idpMetadata(t, idp, "idp")
	keys, err := parseKeySet(m.JWKS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseKeySet("not json"); err == nil {
		t.Fatal("a key set that is not JSON was read")
	}
	good := idp.token(t, "client-1", "sub-1", "")
	if kid, err := keyID(good); err != nil || kid != "k1" {
		t.Fatalf("key id %q %v", kid, err)
	}
	if _, err := keyID("nonsense"); err == nil {
		t.Fatal("a malformed token has a key id")
	}
	unknown := rewriteHeader(t, good, map[string]string{"alg": "RS256", "kid": "k2"})
	noKid := rewriteHeader(t, good, map[string]string{"alg": "RS256"})
	for name, tc := range map[string]struct {
		token string
		knows bool
	}{"the known key": {good, true}, "another key": {unknown, false}, "no key id": {noKid, true}, "not a token": {"nonsense", true}} {
		if got := keys.knows(tc.token); got != tc.knows {
			t.Errorf("%s: knows = %v", name, got)
		}
	}
	if payload, err := keys.VerifySignature(context.Background(), good); err != nil || !strings.Contains(string(payload), "sub-1") {
		t.Fatalf("a good signature: %s %v", payload, err)
	}
	if _, err := keys.VerifySignature(context.Background(), "nonsense"); err == nil {
		t.Fatal("a malformed token verified")
	}
	if _, err := keys.VerifySignature(context.Background(), unknown); err == nil {
		t.Fatal("a token of another key verified")
	}
	parts := strings.Split(good, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"someone-else"}`))
	if _, err := keys.VerifySignature(context.Background(), strings.Join(parts, ".")); err == nil {
		t.Fatal("a changed token verified")
	}
}

func TestVerifyingATokenAgainstTheStoredMetadata(t *testing.T) {
	idp := newFakeIdP(t)
	m := idpMetadata(t, idp, "idp")
	p := models.AuthProvider{Slug: "idp", ClientID: "client-1"}
	ctx := context.Background()
	if idt, stale, err := verifyIDToken(ctx, p, m, idp.token(t, "client-1", "sub-1", "n")); err != nil || stale || idt.Subject != "sub-1" || idt.Nonce != "n" {
		t.Fatalf("a good token: %+v %v %v", idt, stale, err)
	}
	if _, stale, err := verifyIDToken(ctx, p, m, idp.token(t, "client-2", "sub-1", "")); err == nil || stale {
		t.Fatalf("a token for another client: stale=%v %v", stale, err)
	}
	idp.kid = "k2"
	if _, stale, err := verifyIDToken(ctx, p, m, idp.token(t, "client-1", "sub-1", "")); err == nil || !stale {
		t.Fatalf("a token of an unknown key: stale=%v %v", stale, err)
	}
	m.JWKS = "not json"
	if _, stale, err := verifyIDToken(ctx, p, m, idp.token(t, "client-1", "sub-1", "")); err == nil || stale || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("unreadable keys: stale=%v %v", stale, err)
	}
}

// ---- leases and results of the oidc kinds ----

func TestTheLeaseOfADiscoveryNamesTheIssuerAndTheSetting(t *testing.T) {
	g := newRig(t)
	if _, err := leaseOIDCDiscover(context.Background(), g.b, models.Job{Payload: "{"}); err == nil {
		t.Fatal("an unreadable job was leased")
	}
	l := *g.b.cfg()
	l.allowPrivateProviders = true
	g.b.swapSettings(&l)
	got, err := leaseOIDCDiscover(context.Background(), g.b, models.Job{Payload: `{"issuer":"https://idp.example.com"}`})
	if p := got.(workerapi.DiscoverPayload); err != nil || p.Issuer != "https://idp.example.com" || !p.AllowPrivate {
		t.Fatalf("lease %+v %v", got, err)
	}
}

func TestAWorkersDiscoveryResultIsCheckedAgainBeforeItIsKept(t *testing.T) {
	g, faults, idp, p := faultyRig(t)
	ctx := context.Background()
	good := func() workerapi.DiscoverResult {
		m := idpMetadata(t, idp, "idp")
		return workerapi.DiscoverResult{Issuer: m.Issuer, AuthURL: m.AuthURL, TokenURL: m.TokenURL, JWKS: json.RawMessage(m.JWKS)}
	}
	apply := func(job models.Job, res any) (string, error) {
		raw, _ := json.Marshal(res)
		return applyOIDCDiscover(ctx, g.b, job, raw)
	}
	withSlug := models.Job{Payload: `{"slug":"idp","issuer":"` + p.Issuer + `"}`}
	for name, tc := range map[string]struct {
		job models.Job
		res any
	}{
		"an unreadable job":        {models.Job{Payload: "{"}, good()},
		"an unreadable result":     {withSlug, "not an object"},
		"another issuer":           {withSlug, func() workerapi.DiscoverResult { r := good(); r.Issuer = "https://elsewhere.example.com"; return r }()},
		"an authorisation address": {withSlug, func() workerapi.DiscoverResult { r := good(); r.AuthURL = "ftp://idp.example.com/auth"; return r }()},
		"a token address":          {withSlug, func() workerapi.DiscoverResult { r := good(); r.TokenURL = ""; return r }()},
		"a userinfo address":       {withSlug, func() workerapi.DiscoverResult { r := good(); r.UserinfoURL = "ftp://x/y"; return r }()},
		"a key set that is not":    {withSlug, func() workerapi.DiscoverResult { r := good(); r.JWKS = json.RawMessage(`"keys"`); return r }()},
	} {
		if _, err := apply(tc.job, tc.res); !errors.Is(err, errBadResult) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without a slug nothing is kept (an administrator tries an issuer before saving it).
	if err := g.st.DeleteAuthProviderMetadata(ctx, "idp"); err != nil {
		t.Fatal(err)
	}
	if kept, err := apply(models.Job{Payload: `{"issuer":"` + p.Issuer + `"}`}, good()); err != nil || !strings.Contains(kept, p.Issuer) {
		t.Fatalf("a try: %q %v", kept, err)
	}
	if _, err := g.st.GetAuthProviderMetadata(ctx, "idp"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a try was stored: %v", err)
	}
	// A provider that was removed or changed meanwhile gets nothing.
	if _, err := apply(models.Job{Payload: `{"slug":"gone","issuer":"` + p.Issuer + `"}`}, good()); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(models.Job{Payload: `{"slug":"idp","issuer":"https://old.example.com"}`}, workerapi.DiscoverResult{Issuer: "https://old.example.com/", AuthURL: good().AuthURL, TokenURL: good().TokenURL, JWKS: good().JWKS}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.GetAuthProviderMetadata(ctx, "idp"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the result for another issuer was stored: %v", err)
	}
	if _, err := apply(withSlug, good()); err != nil {
		t.Fatal(err)
	}
	if m, err := g.st.GetAuthProviderMetadata(ctx, "idp"); err != nil || m.TokenURL != p.Issuer+"/token" {
		t.Fatalf("stored %+v %v", m, err)
	}
	// The store failing is not the worker's fault: the worker reports again.
	for _, call := range []string{"GetAuthProvider", "PutAuthProviderMetadata"} {
		faults.failOn(call)
		if _, err := apply(withSlug, good()); !errors.Is(err, errFlaky) {
			t.Errorf("%s failing: %v", call, err)
		}
	}
}

func TestTheLeaseOfACodeExchangeCarriesTheSecretsOfTheProvider(t *testing.T) {
	g, faults, idp, _ := faultyRig(t)
	ctx := context.Background()
	job := models.Job{Payload: `{"slug":"idp","code":"c","verifier":"v","redirect":"https://dexa.example.com/cb","nonce":"n"}`}
	lease := func(j models.Job) (workerapi.ExchangePayload, error) {
		got, err := leaseOIDCExchange(ctx, g.b, j)
		p, _ := got.(workerapi.ExchangePayload)
		return p, err
	}
	p, err := lease(job)
	if err != nil || p.TokenURL != idp.URL+"/token" || p.ClientSecret != "s3cret" || p.Code != "c" || p.Verifier != "v" || p.RedirectURL != "https://dexa.example.com/cb" || p.SecretInParams || p.UserinfoURL != "" {
		t.Fatalf("lease %+v %v", p, err)
	}
	if strings.Contains(job.Payload, "s3cret") {
		t.Fatal("the secret is in the job")
	}
	for name, j := range map[string]models.Job{"an unreadable job": {Payload: "{"}, "a provider that was removed": {Payload: `{"slug":"gone"}`}} {
		if _, err := lease(j); !isPermanent(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Plain OAuth 2.0 has endpoints of its own and no discovery.
	g.addProvider(models.AuthProvider{Slug: "plain", Label: "Plain", Kind: models.ProviderKindOAuth2, TokenURL: "https://plain.example.com/token", UserinfoURL: "https://plain.example.com/me", ClientID: "c", ClientSecret: "ps"})
	if p, err := lease(models.Job{Payload: `{"slug":"plain","code":"c"}`}); err != nil || p.TokenURL != "https://plain.example.com/token" || p.UserinfoURL != "https://plain.example.com/me" || p.ClientSecret != "ps" {
		t.Fatalf("oauth2 lease %+v %v", p, err)
	}
	// Apple's secret is a JWT signed here, so the worker never holds the private key.
	key := pemOf(t, newECKey(t))
	g.addProvider(models.AuthProvider{Slug: "apple", Label: "Apple", Kind: models.ProviderKindApple, Issuer: appleIssuer, ClientID: "svc", ClientSecret: key, AppleTeamID: "TEAM", AppleKeyID: "KEY"})
	if err := g.st.PutAuthProviderMetadata(ctx, idpMetadata(t, idp, "apple")); err != nil {
		t.Fatal(err)
	}
	if p, err := lease(models.Job{Payload: `{"slug":"apple","code":"c"}`}); err != nil || !p.SecretInParams || strings.Count(p.ClientSecret, ".") != 2 || strings.Contains(p.ClientSecret, "PRIVATE KEY") {
		t.Fatalf("apple lease %+v %v", p, err)
	}
	g.addProvider(models.AuthProvider{Slug: "apple2", Label: "Apple", Kind: models.ProviderKindApple, Issuer: appleIssuer, ClientID: "svc", ClientSecret: "not a key", AppleTeamID: "TEAM", AppleKeyID: "KEY"})
	if err := g.st.PutAuthProviderMetadata(ctx, idpMetadata(t, idp, "apple2")); err != nil {
		t.Fatal(err)
	}
	if _, err := lease(models.Job{Payload: `{"slug":"apple2","code":"c"}`}); !isPermanent(err) {
		t.Fatalf("a key that cannot sign: %v", err)
	}
	// A provider whose endpoints no worker has fetched yet.
	g.addProvider(models.AuthProvider{Slug: "fresh", Label: "Fresh", Kind: models.ProviderKindOIDC, Issuer: idp.URL, ClientID: "c", ClientSecret: "s"})
	if _, err := lease(models.Job{Payload: `{"slug":"fresh","code":"c"}`}); !isPermanent(err) {
		t.Fatalf("no endpoints: %v", err)
	}
	for _, call := range []string{"GetAuthProvider", "GetAuthProviderMetadata"} {
		faults.failOn(call)
		if _, err := lease(job); !errors.Is(err, errFlaky) || isPermanent(err) {
			t.Errorf("%s failing: %v", call, err)
		}
	}
}

func isPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

func TestAWorkersCodeExchangeIsVerifiedBeforeItIsBelieved(t *testing.T) {
	g, faults, idp, p := faultyRig(t)
	ctx := context.Background()
	job := models.Job{Payload: `{"slug":"idp","code":"c","nonce":"n-1"}`}
	apply := func(j models.Job, res any) (string, error) {
		raw, _ := json.Marshal(res)
		if s, ok := res.(string); ok {
			raw = []byte(s)
		}
		return applyOIDCExchange(ctx, g.b, j, raw)
	}
	token := func(aud, nonce string) workerapi.ExchangeResult {
		return workerapi.ExchangeResult{IDToken: idp.token(t, aud, "sub-1", nonce)}
	}
	kept, err := apply(job, token("client-1", "n-1"))
	var id oidcIdentity
	if err != nil || json.Unmarshal([]byte(kept), &id) != nil || id.Subject != "sub-1" || !strings.Contains(string(id.Claims), "sub-1@example.com") {
		t.Fatalf("a good exchange: %q %v", kept, err)
	}
	for name, tc := range map[string]struct {
		job models.Job
		res any
	}{
		"an unreadable job":          {models.Job{Payload: "{"}, token("client-1", "n-1")},
		"an unreadable result":       {job, "not an object"},
		"a provider that is gone":    {models.Job{Payload: `{"slug":"gone"}`}, token("client-1", "n-1")},
		"a token that is garbage":    {job, workerapi.ExchangeResult{IDToken: "nonsense"}},
		"a token for another client": {job, token("client-2", "n-1")},
		"another nonce":              {job, token("client-1", "n-2")},
	} {
		if _, err := apply(tc.job, tc.res); !errors.Is(err, errBadResult) {
			t.Errorf("%s: %v", name, err)
		}
	}
	idp.kid = "k2"
	if _, err := apply(job, token("client-1", "n-1")); !errors.Is(err, errStaleKeys) {
		t.Errorf("a key the stored set lacks: %v", err)
	}
	idp.kid = ""
	// A provider whose endpoints and keys were never stored cannot have a token checked.
	g.addProvider(models.AuthProvider{Slug: "fresh", Label: "Fresh", Kind: models.ProviderKindOIDC, Issuer: p.Issuer, ClientID: "client-1", ClientSecret: "s"})
	if _, err := apply(models.Job{Payload: `{"slug":"fresh","nonce":"n-1"}`}, token("client-1", "n-1")); !errors.Is(err, errBadResult) {
		t.Errorf("no metadata: %v", err)
	}
	// Plain OAuth 2.0: the account is what the userinfo says, and it needs a subject.
	g.addProvider(models.AuthProvider{Slug: "plain", Label: "Plain", Kind: models.ProviderKindOAuth2, TokenURL: "https://plain.example.com/token", UserinfoURL: "https://plain.example.com/me", ClientID: "c", ClientSecret: "s", SubjectClaim: "id"})
	plain := models.Job{Payload: `{"slug":"plain"}`}
	if kept, err := apply(plain, workerapi.ExchangeResult{Userinfo: json.RawMessage(`{"id":12345678901234567890,"name":"Pat"}`)}); err != nil || !strings.Contains(kept, `"subject":"12345678901234567890"`) {
		t.Errorf("a plain OAuth account: %q %v", kept, err)
	}
	for name, ui := range map[string]string{"userinfo that is not an object": `["x"]`, "userinfo without the subject": `{"name":"Pat"}`} {
		if _, err := apply(plain, workerapi.ExchangeResult{Userinfo: json.RawMessage(ui)}); !errors.Is(err, errBadResult) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, call := range []string{"GetAuthProvider", "GetAuthProviderMetadata"} {
		faults.failOn(call)
		if _, err := apply(job, token("client-1", "n-1")); !errors.Is(err, errFlaky) {
			t.Errorf("%s failing: %v", call, err)
		}
	}
}

func newECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// ---- waiting for a worker ----

func TestAPageThatWaitsForAWorkerGivesUpAndCancelsTheJob(t *testing.T) {
	g, _, idp, _ := faultyRig(t)
	ctx := context.Background()
	// A worker is listed (so the job is queued) but nobody leases anything.
	if err := g.st.TouchWorker(ctx, "idle", []string{kindOIDCExchange}, "v", time.Now()); err != nil {
		t.Fatal(err)
	}
	old := oidcWait
	oidcWait = 150 * time.Millisecond
	t.Cleanup(func() { oidcWait = old })
	spec := enqueueSpec{Kind: kindOIDCExchange, Origin: models.JobOriginUser, Dedupe: "oidc.exchange:x",
		Payload: oidcExchangeJob{Slug: "idp", Code: "c", Verifier: "v", Redirect: "https://dexa.example.com/cb"}}
	if _, err := g.b.runOIDCJob(ctx, spec); err == nil || !strings.Contains(err.Error(), "did not answer in time") {
		t.Fatalf("a worker that never comes: %v", err)
	}
	if jobs, _ := g.st.ListJobs(ctx, models.JobFilter{Kinds: []string{kindOIDCExchange}, States: []string{models.JobCanceled}}); len(jobs) != 1 {
		t.Fatalf("%d canceled jobs", len(jobs))
	}
	// A visitor who leaves ends the wait.
	oidcWait = time.Minute
	gone, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	spec.Dedupe = "oidc.exchange:y"
	if _, err := g.b.runOIDCJob(gone, spec); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a visitor who left: %v", err)
	}
	// Nobody is running the kind at all.
	if _, err := g.b.runOIDCJob(ctx, enqueueSpec{Kind: kindOIDCDiscover, Origin: models.JobOriginAdmin, Payload: oidcDiscoverJob{Issuer: idp.URL}}); !errors.Is(err, errNoWorker) {
		t.Fatalf("no worker: %v", err)
	}
}

// ---- keys that change, and the sign-in that notices ----

func TestASignInWithATokenOfAnUnknownKeyAsksForTheKeysAgain(t *testing.T) {
	g := newProviderRig(t)
	idp := newFakeIdP(t)
	g.addProvider(models.AuthProvider{Slug: "idp", Label: "IdP", Kind: models.ProviderKindOIDC, Issuer: idp.URL, ClientID: "client-1", ClientSecret: "s", Scopes: "openid"})
	state, cookie, rep := g.beginProvider("idp", map[string]any{})
	var out apiclient.SignInOutcome
	rep.decode(t, &out)
	auth, err := url.Parse(out.Redirect)
	if err != nil || state == "" {
		t.Fatalf("start: %v %s", err, rep.body())
	}
	idp.mu.Lock()
	idp.nonce = auth.Query().Get("nonce")
	idp.mu.Unlock()
	idp.kid = "rotated"
	got := g.callback("idp", state, cookie, nil)
	if got.Redirect != "" || !strings.Contains(got.Error, "changed its signing keys") {
		t.Fatalf("callback %+v", got)
	}
	// A worker fetches the keys again (the start already fetched them once).
	waitFor(t, "a second discovery", func() bool {
		jobs, _ := g.st.ListJobs(context.Background(), models.JobFilter{Kinds: []string{kindOIDCDiscover}, States: []string{models.JobSucceeded}})
		return len(jobs) == 2
	})
}

func TestTheKeysAreRefreshedWhenTheyAreOldOrMissingAndNotMoreOftenThanOnceAMinute(t *testing.T) {
	g, faults, idp, p := faultyRig(t)
	ctx := context.Background()
	if err := g.st.TouchWorker(ctx, "idle", []string{kindOIDCDiscover}, "v", time.Now()); err != nil {
		t.Fatal(err)
	}
	queued := func() int {
		jobs, _ := g.st.ListJobs(ctx, models.JobFilter{Kinds: []string{kindOIDCDiscover}, States: []string{models.JobQueued}})
		return len(jobs)
	}
	g.addProvider(models.AuthProvider{Slug: "plain", Label: "Plain", Kind: models.ProviderKindOAuth2, TokenURL: "https://p.example.com/t", UserinfoURL: "https://p.example.com/u", ClientID: "c", ClientSecret: "s"})
	g.b.refreshProviderKeys(ctx, models.AuthProvider{Slug: "plain", Kind: models.ProviderKindOAuth2})
	if queued() != 0 {
		t.Fatal("keys were fetched for a provider that has none")
	}
	g.b.refreshProviderKeys(ctx, p)
	g.b.refreshProviderKeys(ctx, p) // within the minute: no second job
	if queued() != 1 {
		t.Fatalf("%d refreshes queued", queued())
	}
	// The maintenance loop: fresh keys are left alone, a provider without keys and old keys are fetched again.
	g.b.keyRefresh.Delete("idp")
	g.b.refreshStaleProviderKeys(ctx)
	if queued() != 1 {
		t.Fatalf("fresh keys were fetched again: %d jobs", queued())
	}
	old := idpMetadata(t, idp, "idp")
	old.FetchedAt = time.Now().Add(-2 * providerKeyMaxAge)
	if err := g.st.PutAuthProviderMetadata(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := g.st.CancelJobs(ctx, 0, []string{kindOIDCDiscover}, time.Now()); err != nil {
		t.Fatal(err)
	}
	g.b.refreshStaleProviderKeys(ctx)
	if queued() != 1 {
		t.Fatalf("old keys: %d jobs", queued())
	}
	// A store that fails is logged and skipped, never a reason to stop.
	faults.failOn("GetAuthProviderMetadata")
	g.b.keyRefresh.Delete("idp")
	g.b.refreshStaleProviderKeys(ctx)
	faults.failOn("ListAuthProviders")
	g.b.refreshStaleProviderKeys(ctx)
	// A job that cannot be queued is logged too; a paused kind is the administrator's choice and is not.
	faults.failOn("ListJobs")
	g.b.keyRefresh.Delete("idp")
	g.b.refreshProviderKeys(ctx, p)
	faults.failOn()
	l := *g.b.cfg()
	ks := l.workers.Kinds[kindOIDCDiscover]
	ks.Enabled = false
	l.workers.Kinds = map[string]kindSettings{kindOIDCDiscover: ks}
	g.b.swapSettings(&l)
	g.b.keyRefresh.Delete("idp")
	g.b.refreshProviderKeys(ctx, p)
}

func TestABearerTokenWhoseKeysCannotBeReadIsRefused(t *testing.T) {
	g, faults, idp, _ := faultyRig(t)
	uid, _ := g.signUp("alice")
	if err := g.st.PutIdentity(context.Background(), models.Identity{UserID: uid, Provider: "idp", Subject: "sub-1"}); err != nil {
		t.Fatal(err)
	}
	faults.failOn("GetAuthProviderMetadata")
	hdr := map[string]string{"Authorization": "Bearer " + idp.token(t, "client-1", "sub-1", "")}
	// The store failing is not a reason to let a token in.
	if rec := g.publicGet(context.Background(), "/api/v1/users/"+itoa(uid)+"/settings", hdr); rec.Code != http.StatusUnauthorized {
		t.Fatalf("%d", rec.Code)
	}
}

func TestABearerTokenWhoseAccountCannotBeLookedUpIsRefused(t *testing.T) {
	g, faults, idp, _ := faultyRig(t)
	uid, _ := g.signUp("alice")
	faults.failOn("GetIdentity")
	hdr := map[string]string{"Authorization": "Bearer " + idp.token(t, "client-1", "sub-1", "")}
	if rec := g.publicGet(context.Background(), "/api/v1/users/"+itoa(uid)+"/settings", hdr); rec.Code != http.StatusUnauthorized {
		t.Fatalf("%d", rec.Code)
	}
}

func TestABearerTokenOfAnUnknownKeyIsRefusedAndTheKeysAreAskedFor(t *testing.T) {
	g, _, idp, _ := faultyRig(t)
	uid, _ := g.signUp("alice")
	if err := g.st.PutIdentity(context.Background(), models.Identity{UserID: uid, Provider: "idp", Subject: "sub-1"}); err != nil {
		t.Fatal(err)
	}
	idp.kid = "rotated"
	hdr := map[string]string{"Authorization": "Bearer " + idp.token(t, "client-1", "sub-1", "")}
	if rec := g.publicGet(context.Background(), "/api/v1/users/"+itoa(uid)+"/settings", hdr); rec.Code != http.StatusUnauthorized {
		t.Fatalf("%d", rec.Code)
	}
	if jobs, _ := g.st.ListJobs(context.Background(), models.JobFilter{Kinds: []string{kindOIDCDiscover}, States: []string{models.JobQueued}}); len(jobs) != 1 {
		t.Fatalf("%d refreshes queued", len(jobs))
	}
}

func TestSavingAProviderFailsCleanlyWhenWhatWasLearnedAboutItCannotBeStored(t *testing.T) {
	for name, form := range map[string]func(idp *fakeIdP) url.Values{
		"an OpenID provider": func(idp *fakeIdP) url.Values {
			return url.Values{"kind": {"oidc"}, "slug": {"idp"}, "label": {"IdP"}, "issuer": {idp.URL}, "client_id": {"c"}, "client_secret": {"s"}}
		},
		"a plain OAuth provider": func(*fakeIdP) url.Values {
			return url.Values{"kind": {"oauth2"}, "slug": {"plain"}, "label": {"Plain"}, "auth_url": {"https://p.example.com/a"},
				"token_url": {"https://p.example.com/t"}, "userinfo_url": {"https://p.example.com/u"}, "client_id": {"c"}, "client_secret": {"s"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newAdminEnv(t)
			idp := newFakeIdP(t)
			breakTable(t, e.st, "auth_provider_metadata")
			if code, out := e.do(t, "POST", "/admin/providers", form(idp)); code == 303 {
				t.Fatalf("saved although the metadata could not be stored: %d %q", code, out)
			}
		})
	}
}

func TestAnIdentityThatCannotBeClearedStillSignsTheVisitorIn(t *testing.T) {
	base := newRig(t)
	faults := &jobFaults{Store: base.st}
	g := newRigOn(t, faults, base.st)
	g.startWorkers()
	idp := newOAuthFake(t)
	g.addProvider(idp.provider())
	uid, _ := g.signUp("alice")
	if err := g.st.PutIdentity(context.Background(), models.Identity{UserID: uid, Provider: "idp", Subject: "subject-1"}); err != nil {
		t.Fatal(err)
	}
	faults.failOn("ClearJobResult")
	state, cookie, _ := g.beginProvider("idp", map[string]any{})
	if out := g.callback("idp", state, cookie, nil); out.Redirect == "" {
		t.Fatalf("callback %+v", out)
	}
}
