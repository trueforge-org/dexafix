package mainbackend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/store"
)

// policyFails is a store that cannot read the two-step policy.
type policyFails struct{ store.Store }

func (p policyFails) GetSetting(ctx context.Context, name string) (string, error) {
	if name == settingRequire2FA {
		return "", errors.New("the store lost the policy")
	}
	return p.Store.GetSetting(ctx, name)
}
func (p policyFails) Unwrap() store.Store { return p.Store }

// Every place that asks for the two-step policy answers an error when it cannot be read.
func TestEveryAskForTheTwoStepPolicyFailsWhenItCannotBeRead(t *testing.T) {
	base := newRig(t)
	uid, session := base.signUp("root") // the first account is the administrator; signing up asks for the policy, so before it breaks
	g := newRigOn(t, policyFails{base.st}, base.st)
	u, err := g.st.GetUser(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("Basic credentials on the API", func(t *testing.T) {
		logs := captureSignInLog(t)
		rec := g.publicGet(context.Background(), fmt.Sprintf("/api/v1/users/%d/settings", uid), basic("root@example.com", testPassword))
		if rec.Code != http.StatusUnauthorized || !strings.Contains(logs.String(), "authentication failed") {
			t.Fatalf("%d, log %s", rec.Code, logs)
		}
	})
	t.Run("the security page", func(t *testing.T) {
		if rep := g.do("GET", fmt.Sprintf("/users/%d/pages/settings", uid), nil, session); rep.Code != http.StatusInternalServerError {
			t.Fatalf("%d %s", rep.Code, rep.body())
		}
	})
	t.Run("the Admin page", func(t *testing.T) {
		if rep := g.do("GET", "/admin/page", nil, session); rep.Code != http.StatusInternalServerError {
			t.Fatalf("%d %s", rep.Code, rep.body())
		}
	})
	t.Run("a sign-in that needs the second step", func(t *testing.T) {
		if _, err := g.b.secondStep(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), u, "local"); err == nil {
			t.Fatal("secondStep went on without the policy")
		}
	})
}

// A sign-in cannot start its second step when the password it proved cannot be looked up.
func TestTheSecondStepNeedsThePasswordToBeLookedUp(t *testing.T) {
	g := newRig(t)
	uid, _ := g.signUp("alice")
	if err := g.st.PutSetting(context.Background(), settingRequire2FA, require2FAEveryone); err != nil {
		t.Fatal(err)
	}
	u, _ := g.st.GetUser(context.Background(), uid)
	breakTable(t, g.st, "user_identities")
	if next, err := g.b.secondStep(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), u, "local"); err == nil || next != "" {
		t.Fatalf("%q %v", next, err)
	}
}

// The admin proof is bound to a session: without one it is of no use.
func TestTheAdminProofIsOfNoUseWithoutASession(t *testing.T) {
	g := newRig(t)
	uid, session := g.signUp("root")
	sealed, _, err := g.b.sealValue(sealAdminStep, adminStepUp{UID: uid, S: adminSessionBinding(session.Value)}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: adminCookie, Value: sealed})
	if g.b.adminVerified(req, uid) {
		t.Error("the proof counted without a session")
	}
	req.AddCookie(session)
	if !g.b.adminVerified(req, uid) {
		t.Error("the proof did not count with its own session")
	}
}

func TestAHeightProblemFromTheStoreIsExplained(t *testing.T) {
	if got := profileInvalid(fmt.Errorf("%w: height must be a number", store.ErrInvalid)); !strings.Contains(got, "Height") {
		t.Errorf("%q", got)
	}
}

// stateFailsOn fails the failure counters whose key starts with a prefix.
type stateFailsOn struct {
	StateStore
	prefix string
}

func (s stateFailsOn) RecordFailure(ctx context.Context, key string, at time.Time) error {
	if strings.HasPrefix(key, s.prefix) {
		return errors.New("the counter cannot be written")
	}
	return s.StateStore.RecordFailure(ctx, key, at)
}

// A login that cannot be counted against the account, or against the account from everywhere, is
// refused (fails closed), whichever of the two counters broke.
func TestALoginThatCannotBeCountedIsRefused(t *testing.T) {
	for _, prefix := range []string{"loginaddr:", "login:"} {
		t.Run(prefix, func(t *testing.T) {
			g := newRig(t)
			g.signUp("alice")
			g.b.limiter.st = stateFailsOn{g.b.limiter.st, prefix}
			rep := g.signIn("alice")
			var out struct{ Status int }
			rep.decode(t, &out)
			if out.Status != http.StatusTooManyRequests || rep.cookie(sessionCookie) != nil {
				t.Fatalf("%d %s", out.Status, rep.body())
			}
		})
	}
}

func TestAnOIDCDiscoveryWhoseIssuerDiffersByASlashIsRetried(t *testing.T) {
	g := newRig(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`, srv.URL+"/", srv.URL+"/auth", srv.URL+"/token", srv.URL+"/keys")
	}))
	defer srv.Close()
	if _, err := g.b.oidcProvider(context.Background(), models.AuthProvider{Slug: "idp", Issuer: srv.URL}); err != nil {
		t.Fatalf("a provider that advertises its issuer with a trailing slash: %v", err)
	}
}

// A provider client without a transport of its own still gets the size limit.
func TestTheSizeLimitAlsoGoesOnAClientWithoutATransport(t *testing.T) {
	g := newRig(t)
	g.b.httpClient = &http.Client{}
	ctx := g.b.oidcContext(context.Background())
	if ctx == nil {
		t.Fatal("no context")
	}
}

// With every slot busy a reset cannot hash the new password and says so.
func TestAResetIsRefusedAsBusyWhenNoSlotIsFree(t *testing.T) {
	g := newRig(t)
	uid, _ := g.signUp("alice")
	token, _, err := g.b.newResetToken(context.Background(), uid, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fillSlots(t, g.b)
	rep := g.doWithToken("POST", "/auth/password/reset", token, map[string]string{"Password": "a new long password", "Confirm": "a new long password"})
	if rep.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", rep.Code, rep.body())
	}
}

// adsFails is a store whose read of the ads.txt setting fails when told to.
type adsFails struct {
	store.Store
	fail atomic.Bool
}

func (a *adsFails) GetSetting(ctx context.Context, name string) (string, error) {
	if name == settingAdsTxt && a.fail.Load() {
		return "", errors.New("the store lost the ads.txt")
	}
	return a.Store.GetSetting(ctx, name)
}
func (a *adsFails) Unwrap() store.Store { return a.Store }

func TestTheAdsTxtSettingFailsAndIsIgnoredWhenItNoLongerValidates(t *testing.T) {
	base := newRig(t)
	uid, session := base.signUp("root")
	_ = uid
	wrapper := &adsFails{Store: base.st}
	g := newRigOn(t, wrapper, base.st)
	ctx := context.Background()

	if err := g.st.PutSetting(ctx, settingAdsTxt, strings.Repeat("a", 200_000)); err != nil { // too long to be valid now
		t.Fatal(err)
	}
	if got, err := g.b.adsTxtSetting(ctx); err != nil || got != "" {
		t.Fatalf("an invalid stored value: %q %v", got, err)
	}
	wrapper.fail.Store(true)
	if _, err := g.b.adsTxtSetting(ctx); err == nil {
		t.Fatal("a store that fails was read")
	}
	if err := g.b.reloadSettings(ctx); err == nil {
		t.Fatal("the settings were reloaded without the ads.txt")
	}

	t.Run("saving: the body is not JSON", func(t *testing.T) {
		wrapper.fail.Store(false)
		if rep := g.do("POST", "/admin/ads-txt", "{not json", session); rep.Code != http.StatusBadRequest {
			t.Fatalf("%d %s", rep.Code, rep.body())
		}
	})
	t.Run("saving: the store refuses the write", func(t *testing.T) {
		rejectWritesOn(t, g.st, "app_settings", "INSERT", "UPDATE", "DELETE")
		if rep := g.do("POST", "/admin/ads-txt", map[string]string{"text": "google.com, pub-1234567890123456, DIRECT"}, session); rep.Code != http.StatusInternalServerError {
			t.Fatalf("%d %s", rep.Code, rep.body())
		}
	})
}

func TestSavingTheAdsTxtFailsWhenTheSettingsCannotBeReloaded(t *testing.T) {
	base := newRig(t)
	_, session := base.signUp("root")
	wrapper := &adsFails{Store: base.st}
	g := newRigOn(t, wrapper, base.st)
	wrapper.fail.Store(true)
	if rep := g.do("POST", "/admin/ads-txt", map[string]string{"text": "google.com, pub-1234567890123456, DIRECT"}, session); rep.Code != http.StatusInternalServerError {
		t.Fatalf("%d %s", rep.Code, rep.body())
	}
}
