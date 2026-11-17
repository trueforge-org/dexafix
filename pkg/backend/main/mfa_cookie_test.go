package mainbackend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trueforge-org/dexafix/pkg/shared/apiclient"
)

// The second-step cookie proves a password that was right a moment ago. Changing the password
// ends that: a cookie from before the change opens neither the code page nor the enrolment.
func TestASecondStepCookieDoesNotOutliveAPasswordChange(t *testing.T) {
	ctx := context.Background()

	t.Run("the code page", func(t *testing.T) {
		g := newRig(t)
		uid, mfa := g.withTwoFactor("alice")
		good := tfCode(t, g.srv, uid)
		if out := stepOf(t, g.do("GET", "/pages/login/2fa", nil, mfa)); out.Mode != "verify" {
			t.Fatalf("the code page before the change: %+v", out)
		}
		if err := g.b.setPassword(ctx, uid, "a brand new password 123"); err != nil {
			t.Fatal(err)
		}
		wantRedirect(t, g.do("POST", "/auth/2fa", map[string]string{"Code": good}, mfa), "/", "cookie from before the password change")
		wantRedirect(t, g.do("GET", "/pages/login/2fa", nil, mfa), "/", "the code page after the change")
	})

	t.Run("the enrolment, which shows the pending secret", func(t *testing.T) {
		g := newRig(t)
		uid, mfa := g.needingSetup("alice")
		if out := stepOf(t, g.do("GET", "/pages/login/2fa/setup", nil, mfa)); out.Secret == "" {
			t.Fatalf("the setup page before the change shows no secret: %+v", out)
		}
		if err := g.b.setPassword(ctx, uid, "a brand new password 123"); err != nil {
			t.Fatal(err)
		}
		rep := g.do("GET", "/pages/login/2fa/setup", nil, mfa)
		if out := stepOf(t, rep); out.Secret != "" {
			t.Fatalf("a cookie from before the password change shows the pending secret: %+v", out)
		}
		wantRedirect(t, rep, "/", "the setup page after the change")
		if rep := g.do("POST", "/auth/2fa/setup", map[string]string{"Code": "123456"}, mfa); rep.cookie(sessionCookie) != nil {
			t.Fatal("a cookie from before the password change finished the enrolment")
		}
	})

	t.Run("an account that loses its password altogether", func(t *testing.T) {
		g := newRig(t)
		uid, mfa := g.withTwoFactor("alice")
		good := tfCode(t, g.srv, uid)
		if err := g.st.DeleteIdentity(ctx, uid, "local"); err != nil {
			t.Fatal(err)
		}
		wantRedirect(t, g.do("POST", "/auth/2fa", map[string]string{"Code": good}, mfa), "/", "cookie after the password was removed")
	})

	t.Run("a lookup that fails refuses the cookie", func(t *testing.T) {
		g := newRig(t)
		uid, mfa := g.withTwoFactor("alice")
		good := tfCode(t, g.srv, uid)
		breakTable(t, g.st, "user_identities")
		wantRedirect(t, g.do("POST", "/auth/2fa", map[string]string{"Code": good}, mfa), "/", "cookie while the password cannot be looked up")
	})

	t.Run("the same password still works after another sign-in", func(t *testing.T) {
		g := newRig(t)
		uid, _ := g.withTwoFactor("alice")
		newer := g.mfaCookieOf("alice") // a second sign-in does not end the first cookie
		rep := g.do("POST", "/auth/2fa", map[string]string{"Code": tfCode(t, g.srv, uid)}, newer)
		if rep.cookie(sessionCookie) == nil {
			t.Fatalf("a fresh cookie was refused: %s", rep.body())
		}
	})
}

func stepOf(t *testing.T, rep reply) apiclient.SecondStep {
	t.Helper()
	var out apiclient.SecondStep
	rep.decode(t, &out)
	return out
}

// A policy that cannot be read is an error. Treating it as "off" would let a database that fails
// for a moment turn the requirement off for everyone who signs in then.
func TestAPolicyThatCannotBeReadIsAnErrorNotOff(t *testing.T) {
	g := newRig(t)
	uid, session := g.signUp("alice")
	u, err := g.st.GetUser(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.st.PutSetting(context.Background(), settingRequire2FA, require2FAEveryone); err != nil {
		t.Fatal(err)
	}
	breakTable(t, g.st, "app_settings")

	ctx := context.Background()
	if p, err := g.b.twoFactorPolicy(ctx); err == nil {
		t.Errorf("policy %q read from a table that is gone", p)
	}
	if req, err := g.b.twoFactorRequired(ctx, u); err == nil || req {
		t.Errorf("required %v, %v", req, err)
	}
	rec := httptest.NewRecorder()
	if next, err := g.b.secondStep(rec, httptest.NewRequest("POST", "/auth/password", nil), u, "local"); err == nil || next != "" {
		t.Errorf("the sign-in went on without knowing the policy: %q %v", next, err)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a cookie was set")
	}
	if rep := g.do("POST", twoFactorPath(uid, "disable"), map[string]string{"Current": testPassword, "Code": "123456"}, session); rep.Code != http.StatusInternalServerError {
		t.Errorf("disable while the policy cannot be read: %d %s", rep.Code, rep.body())
	}
}
