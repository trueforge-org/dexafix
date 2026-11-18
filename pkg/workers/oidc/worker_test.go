package oidc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
	"github.com/trueforge-org/dexafix/pkg/workers/runner"
)

// fakeIdP is a provider on this machine: discovery, keys, a token endpoint and a userinfo endpoint.
type fakeIdP struct {
	*httptest.Server
	issuerSuffix string // added to the issuer the document states
	keys         string
	tokenStatus  int
	tokenBody    string
	tokenForm    url.Values
	tokenAuth    string
	userinfo     string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	f := &fakeIdP{keys: `{"keys":[{"kid":"1","kty":"RSA"}]}`, tokenStatus: 200,
		tokenBody: `{"access_token":"at","token_type":"Bearer","id_token":"the.id.token"}`, userinfo: `{"id":12345678901234567890}`}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": f.URL + f.issuerSuffix, "authorization_endpoint": f.URL + "/auth",
			"token_endpoint": f.URL + "/token", "userinfo_endpoint": f.URL + "/userinfo", "jwks_uri": f.URL + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(f.keys)) })
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.tokenForm, f.tokenAuth = r.PostForm, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.tokenStatus)
		w.Write([]byte(f.tokenBody))
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(f.userinfo))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func TestDiscoveryReturnsTheEndpointsAndTheKeys(t *testing.T) {
	f := newFakeIdP(t)
	res, err := New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: f.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Issuer != f.URL || res.AuthURL != f.URL+"/auth" || res.TokenURL != f.URL+"/token" || res.UserinfoURL != f.URL+"/userinfo" || string(res.JWKS) != f.keys {
		t.Fatalf("result %+v", res)
	}
	f.issuerSuffix = "/" // a trailing slash is the one difference that is accepted
	if res, err = New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: f.URL}); err != nil || res.Issuer != f.URL+"/" {
		t.Fatalf("trailing slash: %+v %v", res, err)
	}
}

func TestDiscoveryFailsWithoutLeakingWhatTheProviderSaid(t *testing.T) {
	f := newFakeIdP(t)
	for name, tc := range map[string]struct {
		set       func()
		issuer    string
		permanent bool
		want      string
	}{
		"another issuer":     {func() { f.issuerSuffix = "/other" }, f.URL, true, "another issuer"},
		"keys not a key set": {func() { f.keys = `["secret-internal-page"]` }, f.URL, true, "not a key set"},
		"keys not JSON":      {func() { f.keys = `secret-internal-page` }, f.URL, true, "not a key set"},
		"issuer not allowed": {func() {}, "http://idp.example.com", true, "not allowed"},
		"no discovery there": {func() {}, f.URL + "/missing", true, "HTTP 404"},
		"address blocked":    {func() {}, "https://169.254.169.254", true, workerapi.BlockedMessage},
	} {
		t.Run(name, func(t *testing.T) {
			f.issuerSuffix, f.keys = "", `{"keys":[]}`
			tc.set()
			_, err := New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: tc.issuer})
			if err == nil || runner.IsPermanent(err) != tc.permanent || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-internal-page") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestDiscoveryOfAProviderThatIsDownIsTemporary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 503) }))
	defer srv.Close()
	_, err := New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: srv.URL})
	if ok, _ := runner.IsRetryable(err); !ok {
		t.Fatalf("a 503 should be retried: %v", err)
	}
	if _, err = New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("a provider that is not there was reached")
	} else if ok, _ := runner.IsRetryable(err); !ok {
		t.Fatalf("a refused connection should be retried: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = New(nil).discover(ctx, workerapi.DiscoverPayload{Issuer: srv.URL}); err != context.Canceled {
		t.Fatalf("a cancelled job should say so: %v", err)
	}
}

func TestDiscoveryRefusesWhatIsTooLargeOrHasNoEndpoints(t *testing.T) {
	f := newFakeIdP(t)
	f.keys = `{"keys":[],"pad":"` + strings.Repeat("a", maxResponse) + `"}`
	if _, err := New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: f.URL}); !runner.IsPermanent(err) || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("a megabyte of keys: %v", err)
	}
	bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"issuer":"http://127.0.0.1"}`)) }))
	defer bare.Close()
	if _, err := New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: bare.URL}); !runner.IsPermanent(err) {
		t.Fatalf("a document without endpoints: %v", err)
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`not json`)) }))
	defer junk.Close()
	if _, err := New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: junk.URL}); !runner.IsPermanent(err) || !strings.Contains(err.Error(), "not a discovery document") {
		t.Fatalf("junk: %v", err)
	}
}

func exchangePayload(f *fakeIdP) workerapi.ExchangePayload {
	return workerapi.ExchangePayload{TokenURL: f.URL + "/token", ClientID: "cid", ClientSecret: "csecret",
		RedirectURL: "https://dexa.example/auth/idp/callback", Code: "the-code", Verifier: "the-verifier"}
}

func TestTheCodeExchangeReturnsTheIDToken(t *testing.T) {
	f := newFakeIdP(t)
	res, err := New(nil).exchange(context.Background(), exchangePayload(f))
	if err != nil || res.IDToken != "the.id.token" || res.Userinfo != nil {
		t.Fatalf("result %+v %v", res, err)
	}
	if f.tokenForm.Get("code") != "the-code" || f.tokenForm.Get("code_verifier") != "the-verifier" ||
		f.tokenForm.Get("redirect_uri") != "https://dexa.example/auth/idp/callback" || f.tokenForm.Get("grant_type") != "authorization_code" {
		t.Fatalf("token request %v", f.tokenForm)
	}
	if !strings.HasPrefix(f.tokenAuth, "Basic ") {
		t.Fatalf("the client credentials went as %q", f.tokenAuth)
	}
	p := exchangePayload(f)
	p.SecretInParams = true // Apple
	if _, err = New(nil).exchange(context.Background(), p); err != nil || f.tokenForm.Get("client_secret") != "csecret" || f.tokenAuth != "" {
		t.Fatalf("in params: %v %v auth=%q", err, f.tokenForm, f.tokenAuth)
	}
}

func TestTheCodeExchangeOfAPlainOAuthProviderReturnsTheUserinfo(t *testing.T) {
	f := newFakeIdP(t)
	f.tokenBody = `{"access_token":"at","token_type":"Bearer"}`
	p := exchangePayload(f)
	p.UserinfoURL = f.URL + "/userinfo"
	res, err := New(nil).exchange(context.Background(), p)
	if err != nil || res.IDToken != "" || string(res.Userinfo) != f.userinfo { // the number is not turned into a float
		t.Fatalf("result %+v %v", res, err)
	}
	f.userinfo = "not json"
	if _, err = New(nil).exchange(context.Background(), p); !runner.IsPermanent(err) || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("junk userinfo: %v", err)
	}
	p.UserinfoURL = f.URL + "/nothing"
	if _, err = New(nil).exchange(context.Background(), p); !runner.IsPermanent(err) || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("missing userinfo: %v", err)
	}
	f.tokenBody = `{"access_token":"other","token_type":"Bearer"}`
	p.UserinfoURL = f.URL + "/userinfo"
	if _, err = New(nil).exchange(context.Background(), p); !runner.IsPermanent(err) || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("refused userinfo: %v", err)
	}
}

func TestTheCodeExchangeFailsWithoutLeakingTheAnswer(t *testing.T) {
	f := newFakeIdP(t)
	for name, tc := range map[string]struct {
		status    int
		body      string
		permanent bool
		want      string
	}{
		"the provider refuses the code": {400, `{"error":"invalid_grant","error_description":"secret-internal-page"}`, true, "HTTP 400"},
		"the provider is broken":        {502, `secret-internal-page`, false, "could not be reached"},
		"no id_token in the answer":     {200, `{"access_token":"at","token_type":"Bearer"}`, true, "no id_token"},
	} {
		t.Run(name, func(t *testing.T) {
			f.tokenStatus, f.tokenBody = tc.status, tc.body
			_, err := New(nil).exchange(context.Background(), exchangePayload(f))
			retry, _ := runner.IsRetryable(err)
			if err == nil || runner.IsPermanent(err) != tc.permanent || retry == tc.permanent || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-internal-page") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(nil).exchange(ctx, exchangePayload(f)); err != context.Canceled {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestTheCodeExchangeRefusesAnIncompleteOrBlockedJob(t *testing.T) {
	f := newFakeIdP(t)
	for name, mod := range map[string]func(*workerapi.ExchangePayload){
		"no code":                            func(p *workerapi.ExchangePayload) { p.Code = "" },
		"no client":                          func(p *workerapi.ExchangePayload) { p.ClientID = "" },
		"a token url that is not allowed":    func(p *workerapi.ExchangePayload) { p.TokenURL = "http://idp.example.com/token" },
		"a userinfo url that is not allowed": func(p *workerapi.ExchangePayload) { p.UserinfoURL = "ftp://x/y" },
	} {
		t.Run(name, func(t *testing.T) {
			p := exchangePayload(f)
			mod(&p)
			if _, err := New(nil).exchange(context.Background(), p); !runner.IsPermanent(err) || !strings.Contains(err.Error(), "incomplete") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	p := exchangePayload(f)
	p.TokenURL = "https://169.254.169.254/token"
	if _, err := New(nil).exchange(context.Background(), p); !runner.IsPermanent(err) || !strings.Contains(err.Error(), workerapi.BlockedMessage) {
		t.Fatalf("blocked: %v", err)
	}
}

func TestRedirectsStayWithinTheRulesForProviders(t *testing.T) {
	var target string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) }))
	defer srv.Close()
	c := providerClient(false)
	for name, tc := range map[string]struct{ to, want string }{
		"to plain http elsewhere": {"http://idp.example.com/x", "not allowed"},
		"in a loop":               {srv.URL, "too many redirects"},
	} {
		t.Run(name, func(t *testing.T) {
			target = tc.to
			resp, err := c.Get(srv.URL)
			if err == nil {
				resp.Body.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestTheProxyOfTheEnvironmentIsUsedOnlyWhenPrivateAddressesAreAllowed(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.internal:3128")
	req, _ := http.NewRequest("GET", "https://idp.example.com/", nil)
	for allow, wantProxy := range map[bool]bool{false: false, true: true} {
		tr := providerClient(allow).Transport.(limitedTransport).base.(*http.Transport)
		u, err := tr.Proxy(req)
		if err != nil || (u != nil) != wantProxy {
			t.Fatalf("allow=%v: proxy %v err %v", allow, u, err)
		}
	}
}

func TestTheHandlersDecodeTheirJobs(t *testing.T) {
	f := newFakeIdP(t)
	h := New(nil).Handlers()
	job := func(v any) *runner.Job {
		b, _ := json.Marshal(v)
		return &runner.Job{Job: workerapi.Job{Payload: b}}
	}
	res, err := h[KindDiscover](context.Background(), job(workerapi.DiscoverPayload{Issuer: f.URL}))
	if err != nil || res.(workerapi.DiscoverResult).AuthURL != f.URL+"/auth" {
		t.Fatalf("discover: %v %v", res, err)
	}
	res, err = h[KindExchange](context.Background(), job(exchangePayload(f)))
	if err != nil || res.(workerapi.ExchangeResult).IDToken == "" {
		t.Fatalf("exchange: %v %v", res, err)
	}
	for _, kind := range []string{KindDiscover, KindExchange} {
		if _, err := h[kind](context.Background(), &runner.Job{Job: workerapi.Job{Payload: []byte("{")}}); !runner.IsPermanent(err) {
			t.Fatalf("%s with an unreadable job: %v", kind, err)
		}
	}
	for kind, p := range map[string]any{KindDiscover: workerapi.DiscoverPayload{Issuer: "x"}, KindExchange: workerapi.ExchangePayload{}} {
		if res, err := h[kind](context.Background(), job(p)); err == nil || res != nil {
			t.Fatalf("%s with nothing to do: %v %v", kind, res, err)
		}
	}
}

func TestAClientPassedToNewIsUsedForEveryJob(t *testing.T) {
	c := &http.Client{}
	w := New(c)
	if w.client(true) != c || w.client(false) != c {
		t.Fatal("the given client was not used")
	}
}

func TestAnAnswerOfExactlyTheLimitIsAccepted(t *testing.T) {
	f := newFakeIdP(t)
	const head, tail = `{"keys":[],"pad":"`, `"}`
	f.keys = head + strings.Repeat("a", maxResponse-len(head)-len(tail)) + tail
	if len(f.keys) != maxResponse {
		t.Fatalf("test setup: %d bytes", len(f.keys))
	}
	if res, err := New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: f.URL}); err != nil || len(res.JWKS) != maxResponse {
		t.Fatalf("an answer that is exactly the limit: %v", err)
	}
}

func TestDiscoveryFailsCleanlyWhenTheKeysCannotBeFetchedOrAnswerIsCutShort(t *testing.T) {
	var keys http.HandlerFunc
	var userinfo string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/keys" {
			keys(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/auth",
			"token_endpoint": srv.URL + "/token", "userinfo_endpoint": userinfo, "jwks_uri": srv.URL + "/keys"})
	}))
	defer srv.Close()
	for name, tc := range map[string]struct {
		keys      http.HandlerFunc
		permanent bool
		want      string
	}{
		"the key set is not there": {func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, true, "signing keys: the provider answered HTTP 404"},
		"the answer is cut short": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			w.Write([]byte("abc")) // the connection ends before the promised length
		}, false, "signing keys: the provider could not be reached"},
	} {
		t.Run(name, func(t *testing.T) {
			keys = tc.keys
			_, err := New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: srv.URL})
			retry, _ := runner.IsRetryable(err)
			if err == nil || runner.IsPermanent(err) != tc.permanent || retry == tc.permanent || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// A userinfo endpoint a provider may not have is left out of the result, not an error.
	keys = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"keys":[]}`)) }
	userinfo = "ftp://idp.example.com/userinfo"
	if res, err := New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: srv.URL}); err != nil || res.UserinfoURL != "" {
		t.Fatalf("an unusable userinfo endpoint: %+v %v", res, err)
	}
}

func TestTheFailuresOfAProviderReadAsSentences(t *testing.T) {
	if got := (statusError{code: 503}).Error(); got != "HTTP 503" {
		t.Fatalf("status error %q", got)
	}
	if got := (badAnswer{msg: "the answer is too large"}).Error(); got != "the answer is too large" {
		t.Fatalf("bad answer %q", got)
	}
}

func TestADiscoveryDocumentWithoutEndpointsIsRefused(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL}) // the right issuer, nothing else
	}))
	defer srv.Close()
	_, err := New(nil).discover(context.Background(), workerapi.DiscoverPayload{Issuer: srv.URL})
	if !runner.IsPermanent(err) || !strings.Contains(err.Error(), "lacks an endpoint") {
		t.Fatalf("err = %v", err)
	}
}
