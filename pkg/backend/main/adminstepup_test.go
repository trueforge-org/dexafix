package mainbackend

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// adminCookieOf returns the proof that the second step was done for the Admin pages.
func adminCookieOf(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == adminCookie && c.MaxAge > 0 {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", adminCookie, resp.Cookies())
	return nil
}

// hasAdminCookie reports whether the response sets the Admin proof.
func hasAdminCookie(resp *http.Response) bool {
	for _, c := range resp.Cookies() {
		if c.Name == adminCookie && c.MaxAge > 0 {
			return true
		}
	}
	return false
}

// sessionOfClient returns the session cookie a client made with signup holds.
func sessionOfClient(t *testing.T, c *http.Client) *http.Cookie {
	t.Helper()
	ct, ok := c.Transport.(cookieTransport)
	if !ok {
		t.Fatal("client holds no session")
	}
	return ct.cookie
}

// adminProof seals an Admin proof for user uid, given in the session, with the given purpose and
// lifetime.
func adminProof(t *testing.T, srv *Server, purpose string, uid int64, session *http.Cookie, ttl time.Duration) *http.Cookie {
	t.Helper()
	sealed, _, err := srv.sealValue(purpose, adminStepUp{UID: uid, S: adminSessionBinding(session.Value)}, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: adminCookie, Value: sealed}
}

func TestAdminStepUp(t *testing.T) {
	ts, root, _ := newAuthServer(t)
	srv := serverOf(ts)
	signup(t, ts, root, "root") // user 1, administrator
	secret, codes := tfEnable(t, srv, 1)

	// the second step of the sign-in also proves the Admin pages
	fresh := noRedirectClient()
	mfa := tfMFACookie(t, tfPasswordLogin(t, fresh, ts.URL, "root@example.com"))
	r, _ := tfSecondStep(t, fresh, ts.URL, mfa, tfCode(t, srv, 1))
	if r.StatusCode != 303 || !tfHasSession(r) {
		t.Fatalf("second step: %d, session %v", r.StatusCode, tfHasSession(r))
	}
	session := sessionCookieOf(t, r)
	proof := adminCookieOf(t, r)
	if r, page := tfRequest(t, fresh, "GET", ts.URL+"/admin", nil, session, proof); r.StatusCode != 200 || !strings.Contains(page, "Network and retention") {
		t.Fatalf("admin page right after the second step: %d", r.StatusCode)
	}

	// a session alone, without the proof, is sent to the code page
	only := noRedirectClient()
	if r, _ := tfRequest(t, only, "GET", ts.URL+"/admin", nil, session); r.StatusCode != 303 || r.Header.Get("Location") != "/admin/verify" {
		t.Fatalf("admin page with the session only: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r, _ := tfRequest(t, only, "POST", ts.URL+"/admin/signup", url.Values{}, session); r.StatusCode != 303 || r.Header.Get("Location") != "/admin/verify" {
		t.Fatalf("admin action with the session only: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r, page := tfRequest(t, only, "GET", ts.URL+"/admin/verify", nil, session); r.StatusCode != 200 || !strings.Contains(page, `action="/admin/verify"`) {
		t.Fatalf("code page: %d", r.StatusCode)
	}

	// a wrong code is refused and sets no proof
	r, _ = tfRequest(t, only, "POST", ts.URL+"/admin/verify", url.Values{"code": {tfWrong(t, secret)}}, session)
	if r.StatusCode != 401 || hasAdminCookie(r) {
		t.Fatalf("wrong code: %d, proof set %v", r.StatusCode, hasAdminCookie(r))
	}

	// a recovery code is accepted and sets the proof
	r, _ = tfRequest(t, only, "POST", ts.URL+"/admin/verify", url.Values{"code": {codes[0]}}, session)
	if r.StatusCode != 303 || r.Header.Get("Location") != "/admin" {
		t.Fatalf("right code: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	proof = adminCookieOf(t, r)
	if r, _ := tfRequest(t, only, "GET", ts.URL+"/admin", nil, session, proof); r.StatusCode != 200 {
		t.Fatalf("admin page after the code: %d", r.StatusCode)
	}
	if r, _ := tfRequest(t, only, "GET", ts.URL+"/admin/verify", nil, session, proof); r.StatusCode != 303 || r.Header.Get("Location") != "/admin" {
		t.Fatalf("code page while verified: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestAdminWithoutTwoFactorIsNotAsked(t *testing.T) {
	ts, root, _ := newAuthServer(t)
	signup(t, ts, root, "root") // user 1, administrator, no two-step verification
	if r, _ := tfRequest(t, root, "GET", ts.URL+"/admin", nil); r.StatusCode != 200 {
		t.Fatalf("admin page without two-step verification: %d", r.StatusCode)
	}
	if r, _ := tfRequest(t, root, "GET", ts.URL+"/admin/verify", nil); r.StatusCode != 303 || r.Header.Get("Location") != "/admin" {
		t.Fatalf("code page without two-step verification: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestAdminProofIsNotPortable(t *testing.T) {
	ts, root, _ := newAuthServer(t)
	srv := serverOf(ts)
	signup(t, ts, root, "root") // user 1, administrator
	bob := noRedirectClient()
	signup(t, ts, bob, "bob") // user 2
	if r := post(t, root, ts.URL+"/admin/users/2/role", url.Values{"role": {"admin"}}); r.StatusCode != 303 {
		t.Fatalf("promote bob: %d", r.StatusCode)
	}
	tfEnable(t, srv, 1)
	tfEnable(t, srv, 2)
	rootSession := sessionOfClient(t, root)
	bobSession := sessionOfClient(t, bob)

	page := func(session, proof *http.Cookie) int {
		t.Helper()
		r, _ := tfRequest(t, noRedirectClient(), "GET", ts.URL+"/admin", nil, session, proof)
		return r.StatusCode
	}

	// control: the root's own proof, with the root's own session
	if code := page(rootSession, adminProof(t, srv, sealAdminStep, 1, rootSession, time.Minute)); code != 200 {
		t.Fatalf("control: %d, want 200", code)
	}

	// a proof made for root does not open the Admin pages for bob
	if r, _ := tfRequest(t, noRedirectClient(), "GET", ts.URL+"/admin", nil, bobSession, adminProof(t, srv, sealAdminStep, 1, rootSession, time.Minute)); r.StatusCode != 303 || r.Header.Get("Location") != "/admin/verify" {
		t.Errorf("root's proof for bob: %d %s", r.StatusCode, r.Header.Get("Location"))
	}

	// a proof sealed for another purpose is not accepted
	if code := page(rootSession, adminProof(t, srv, "mfa", 1, rootSession, time.Minute)); code != 303 {
		t.Errorf("proof sealed for another purpose: %d, want 303", code)
	}

	// an expired proof is not accepted
	if code := page(rootSession, adminProof(t, srv, sealAdminStep, 1, rootSession, -time.Second)); code != 303 {
		t.Errorf("expired proof: %d, want 303", code)
	}
}

func TestAdminVerifyIsForAdministratorsOnly(t *testing.T) {
	ts, root, _ := newAuthServer(t)
	signup(t, ts, root, "root") // user 1, administrator
	bob := noRedirectClient()
	signup(t, ts, bob, "bob") // user 2, not an administrator
	session := sessionOfClient(t, bob)

	if r, _ := tfRequest(t, bob, "GET", ts.URL+"/admin/verify", nil, session); r.StatusCode != 404 {
		t.Errorf("code page for a user: %d, want 404", r.StatusCode)
	}
	if r, _ := tfRequest(t, bob, "POST", ts.URL+"/admin/verify", url.Values{"code": {"123456"}}, session); r.StatusCode != 404 {
		t.Errorf("code post for a user: %d, want 404", r.StatusCode)
	}
}

func TestAdminStepUpDoesNotBlockAccessTokens(t *testing.T) {
	ts, root, _ := newAuthServer(t)
	srv := serverOf(ts)
	signup(t, ts, root, "root") // user 1, administrator
	tfEnable(t, srv, 1)
	token := createPAT(t, ts, root, 1, url.Values{"name": {"script"}, "expires": {"never"}})

	if code, _ := apiCall(t, noRedirectClient(), "GET", ts.URL+"/api/v1/audit", "", bearer(token)); code != 200 {
		t.Fatalf("admin API with an access token and two-step verification: %d, want 200", code)
	}
}

// The Admin pages ask an administrator with two-step verification for a code again. The proof is a
// cookie for /admin only, so the JSON API cannot see it: a session cookie must not stand in for it
// there, or a stolen cookie would reach everything the Admin pages protect.
func TestTheAdminAPIRefusesTheSessionOfAnAdministratorWithTwoStepVerification(t *testing.T) {
	ts, root, _ := newAuthServer(t)
	srv := serverOf(ts)
	signup(t, ts, root, "root") // user 1, administrator
	session := sessionOfClient(t, root)
	cookie := http.Header{"Cookie": {session.Name + "=" + session.Value}}
	calls := map[string]func() int{
		"list users": func() int {
			c, _ := apiCall(t, noRedirectClient(), "GET", ts.URL+"/api/v1/users", "", cookie)
			return c
		},
		"audit log": func() int {
			c, _ := apiCall(t, noRedirectClient(), "GET", ts.URL+"/api/v1/audit", "", cookie)
			return c
		},
		"add a user": func() int {
			c, _ := apiCall(t, noRedirectClient(), "POST", ts.URL+"/api/v1/users", `{"username":"dave","email":"dave@example.com"}`, cookie)
			return c
		},
	}
	// Without two-step verification the session is as good here as on the Admin pages.
	for name, call := range calls {
		if name != "add a user" {
			if code := call(); code != 200 {
				t.Errorf("%s without two-step verification: %d, want 200", name, code)
			}
		}
	}
	tfEnable(t, srv, 1)
	for name, call := range calls {
		if code := call(); code != http.StatusForbidden {
			t.Errorf("%s with the session of an administrator with two-step verification: %d, want 403", name, code)
		}
	}
	if users, err := srv.store.ListUsers(context.Background()); err != nil || len(users) != 1 {
		t.Errorf("a user was added: %d %v", len(users), err)
	}
	// Their own data stays reachable with the session, and an access token still works.
	if code, _ := apiCall(t, noRedirectClient(), "GET", ts.URL+"/api/v1/users/1/settings", "", cookie); code != 200 {
		t.Errorf("own settings with the session: %d, want 200", code)
	}
	token := createPAT(t, ts, root, 1, url.Values{"name": {"script"}, "expires": {"never"}})
	if code, _ := apiCall(t, noRedirectClient(), "GET", ts.URL+"/api/v1/users", "", bearer(token)); code != 200 {
		t.Errorf("access token: %d, want 200", code)
	}
}

func TestTheAdminAPIAnswersWhenTheSecondStepCannotBeLookedUp(t *testing.T) {
	ts, root, st := newAuthServer(t)
	signup(t, ts, root, "root")
	session := sessionOfClient(t, root)
	breakTable(t, st, "user_totp")
	cookie := http.Header{"Cookie": {session.Name + "=" + session.Value}}
	if code, _ := apiCall(t, noRedirectClient(), "GET", ts.URL+"/api/v1/users", "", cookie); code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", code)
	}
}

// The proof belongs to the session it was given in. A copy of the cookie is of no use with another
// session of the same administrator, and none once the session is over.
func TestTheAdminProofBelongsToTheSessionItWasGivenIn(t *testing.T) {
	ts, root, _ := newAuthServer(t)
	srv := serverOf(ts)
	signup(t, ts, root, "root") // user 1, administrator
	_, recovery := tfEnable(t, srv, 1)

	// Two sign-ins of the same administrator, each with a second step of its own (a code works once,
	// so the second one uses a recovery code).
	signIn := func(code string) (*http.Cookie, *http.Cookie) {
		t.Helper()
		c := noRedirectClient()
		mfa := tfMFACookie(t, tfPasswordLogin(t, c, ts.URL, "root@example.com"))
		r, _ := tfSecondStep(t, c, ts.URL, mfa, code)
		if r.StatusCode != 303 {
			t.Fatalf("second step: %d", r.StatusCode)
		}
		return sessionCookieOf(t, r), adminCookieOf(t, r)
	}
	sessionA, proofA := signIn(tfCode(t, srv, 1))
	sessionB, _ := signIn(recovery[0])
	page := func(session, proof *http.Cookie) int {
		t.Helper()
		r, _ := tfRequest(t, noRedirectClient(), "GET", ts.URL+"/admin", nil, session, proof)
		return r.StatusCode
	}
	if code := page(sessionA, proofA); code != 200 {
		t.Fatalf("the proof with its own session: %d", code)
	}
	if code := page(sessionB, proofA); code != 303 {
		t.Errorf("the proof of one session with another session: %d, want 303", code)
	}
	if code := page(nil, proofA); code != 303 && code != 401 {
		t.Errorf("the proof without any session: %d", code)
	}
	// Signing out ends the session and with it the proof: a copy of both cookies stays useless.
	if r, _ := tfRequest(t, noRedirectClient(), "POST", ts.URL+"/logout", url.Values{}, sessionA, proofA); r.StatusCode != 303 {
		t.Fatalf("sign out: %d", r.StatusCode)
	}
	if code := page(sessionA, proofA); code == 200 {
		t.Errorf("the admin pages opened with a session that was signed out: %d", code)
	}
}
