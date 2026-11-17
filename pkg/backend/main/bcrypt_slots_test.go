package mainbackend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/shared/apiclient"
)

// fillSlots takes every password check slot and frees them again when the test ends.
func fillSlots(t *testing.T, b *Backend) {
	t.Helper()
	old := bcryptWait
	bcryptWait = 20 * time.Millisecond
	t.Cleanup(func() { bcryptWait = old })
	for range cap(b.bcryptSlots) {
		b.bcryptSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(b.bcryptSlots) {
			<-b.bcryptSlots
		}
	})
}

func TestPasswordChecksOfTheWebWaitForASlotAndThenGiveUp(t *testing.T) {
	t.Run("sign-in says the server is busy and counts no failure", func(t *testing.T) {
		g := newRig(t)
		g.signUp("alice")
		fillSlots(t, g.b)
		rep := g.do("POST", "/auth/password", map[string]string{"Email": "alice@example.com", "Password": testPassword})
		var out apiclient.SignInOutcome
		rep.decode(t, &out)
		if out.Status != http.StatusServiceUnavailable || !strings.Contains(out.Error, "busy") || out.Redirect != "" {
			t.Fatalf("sign-in with every slot busy: %+v", out)
		}
		if n, _ := g.b.limiter.st.CountFailures(context.Background(), loginKey("alice@example.com"), time.Now().Add(-time.Hour)); n != 0 {
			t.Fatalf("a busy server counted %d failed attempts against the account", n)
		}
	})
	t.Run("confirming the password is answered as throttled", func(t *testing.T) {
		ts, c, _ := newAuthServer(t)
		signup(t, ts, c, "alice")
		srv := serverOf(ts)
		u, err := srv.store.GetUser(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		fillSlots(t, srv.Backend)
		ok, status, msg, err := srv.checkReauth(reauthRequest(url.Values{"current": {testPassword}}, ""), u, false)
		if err != nil || ok || status != http.StatusTooManyRequests || !strings.Contains(msg, "busy") {
			t.Fatalf("ok=%v status=%d msg=%q err=%v, want refused with 429", ok, status, msg, err)
		}
	})
	t.Run("a visitor who left does not wait for a slot", func(t *testing.T) {
		g := newRig(t)
		fillSlots(t, g.b)
		old := bcryptWait
		bcryptWait = time.Hour // the visitor must be what ends the wait
		t.Cleanup(func() { bcryptWait = old })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if release, ok := g.b.bcryptSlot(ctx); ok || release != nil {
			t.Fatal("a slot was given to a visitor who had gone")
		}
	})
	t.Run("a free slot is taken and given back", func(t *testing.T) {
		g := newRig(t)
		release, ok := g.b.bcryptSlot(httptest.NewRequest("GET", "/", nil).Context())
		if !ok || len(g.b.bcryptSlots) != 1 {
			t.Fatalf("ok=%v, slots in use %d, want 1", ok, len(g.b.bcryptSlots))
		}
		release()
		if len(g.b.bcryptSlots) != 0 {
			t.Fatalf("slots in use after release: %d", len(g.b.bcryptSlots))
		}
	})
}

// Hashing a new password costs what checking one does, so sign-up, a password change, an account
// added by an administrator and a reset all wait for a slot too, and say the server is busy.
func TestHashingAPasswordWaitsForASlotToo(t *testing.T) {
	t.Run("sign-up is refused as busy and no account is made", func(t *testing.T) {
		g := newRig(t)
		fillSlots(t, g.b)
		rep := g.do("POST", "/auth/signup", map[string]string{"Username": "alice", "Email": "alice@example.com", "Password": testPassword, "Confirm": testPassword})
		var out apiclient.SignInOutcome
		rep.decode(t, &out)
		if out.Status != http.StatusServiceUnavailable || !strings.Contains(out.Error, "busy") || out.Redirect != "" {
			t.Fatalf("sign-up with every slot busy: %+v", out)
		}
		if _, err := g.st.GetUserByEmail(context.Background(), "alice@example.com"); err == nil {
			t.Fatal("an account was made without a password check slot")
		}
	})
	t.Run("every sign-up attempt counts against the address, not only the ones that find a used address", func(t *testing.T) {
		g := newRig(t)
		before, _ := g.b.limiter.st.CountFailures(context.Background(), ipKey(&http.Request{RemoteAddr: "192.0.2.10:1"}), time.Now().Add(-time.Hour))
		g.signUp("alice")
		after, _ := g.b.limiter.st.CountFailures(context.Background(), ipKey(&http.Request{RemoteAddr: "192.0.2.10:1"}), time.Now().Add(-time.Hour))
		if after != before+1 {
			t.Fatalf("a sign-up that worked counted %d times", after-before)
		}
	})
	t.Run("storing a new password", func(t *testing.T) {
		g := newRig(t)
		uid, _ := g.signUp("alice")
		fillSlots(t, g.b)
		if err := g.b.setPassword(context.Background(), uid, "another password 12"); !errors.Is(err, errBusy) {
			t.Fatalf("setPassword with every slot busy: %v", err)
		}
		if _, ok, err := g.b.verifyPassword(context.Background(), "alice@example.com", testPassword); err != nil || !ok {
			t.Fatalf("the old password stopped working: %v %v", ok, err)
		}
		rec := httptest.NewRecorder()
		internalError(rec, errBusy)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("busy is %d", rec.Code)
		}
	})
	t.Run("the public API making an account", func(t *testing.T) {
		g := newJobRig(t)
		fillSlots(t, g.b)
		if code, body := g.callAs(g.rootToken(), "POST", "/users", `{"username":"dave","email":"dave@example.com","password":"a long enough password"}`); code != http.StatusServiceUnavailable {
			t.Fatalf("status %d %s, want 503", code, body)
		}
	})
	t.Run("an administrator adding an account", func(t *testing.T) {
		g := newRig(t)
		_, session := g.signUp("root") // the first account is the administrator
		fillSlots(t, g.b)
		rep := g.do("POST", "/admin/users", map[string]string{"username": "dave", "email": "dave@example.com", "password": "a long enough password"}, session)
		if rep.Code != http.StatusServiceUnavailable {
			t.Fatalf("status %d %s, want 503", rep.Code, rep.body())
		}
	})
}
