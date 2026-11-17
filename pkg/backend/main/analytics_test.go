package mainbackend

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/trueforge-org/dexafix/pkg/shared/apiclient"
	"github.com/trueforge-org/dexafix/pkg/shared/edge"
	"github.com/trueforge-org/dexafix/pkg/store"
)

// offSite finds an element that fetches from another site: a src attribute with an address, or an
// inline import. Links (href) do not count.
var offSite = regexp.MustCompile(`(?i)\bsrc\s*=\s*["']?(https?:)?//|@import|<link[^>]+stylesheet[^>]+href\s*=\s*["']?(https?:)?//`)

const (
	testScarf = "123e4567-e89b-12d3-a456-426614174000"
	testGA    = "G-ABC123XYZ9"
	testAds   = "ca-pub-1234567890123456"
)

func analyticsOf(scarf, ga, ads string) apiclient.Analytics {
	return apiclient.Analytics{ScarfPixelID: scarf, GoogleAnalyticsID: ga, AdSensePublisher: ads}
}

// mustGetResp fetches a page and keeps the response, for its headers.
func mustGetResp(t *testing.T, c *http.Client, u string) *http.Response {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func analyticsForm(scarf, ga, ads string) url.Values {
	return url.Values{"scarf_pixel_id": {scarf}, "google_analytics_id": {ga}, "adsense_publisher": {ads}}
}

func TestValidateAnalytics(t *testing.T) {
	for _, c := range []struct {
		scarf, ga, ads string
		bad            bool
	}{
		{"", "", "", false},
		{testScarf, testGA, testAds, false},
		{"  " + testScarf + "  ", "", "", false}, // trimmed
		{"not-a-uuid", "", "", true},
		{testScarf + "x", "", "", true},
		{"", "UA-123-1", "", true}, // the old Universal Analytics form is gone
		{"", "g-abc123xyz9", "", true},
		{"", `G-ABC"><script>`, "", true},
		{"", "", "pub-1234567890123456", true},
		{"", "", "ca-pub-123", true},
		{"", "", `ca-pub-1234567890123456"; script-src *`, true}, // nothing may reach the policy or the page
	} {
		_, msg := validateAnalytics(analyticsOf(c.scarf, c.ga, c.ads))
		if (msg != "") != c.bad {
			t.Errorf("validateAnalytics(%q, %q, %q) message %q, want an error: %v", c.scarf, c.ga, c.ads, msg, c.bad)
		}
	}
}

func TestAdminSavesAnalyticsAndPublicPagesOfferTheChoice(t *testing.T) {
	ts, root, _ := newAuthServer(t)
	srv := serverOf(ts)
	signup(t, ts, root, "root")
	anon := noRedirectClient()
	public := []string{"/", "/docs", "/status", "/terms", "/privacy"}

	// Off by default: no notice, no script, the strict policy.
	for _, p := range public {
		resp := mustGetResp(t, anon, ts.URL+p)
		if page := bodyOf(t, resp); strings.Contains(page, `id="consent"`) || strings.Contains(page, "consent.js") || strings.Contains(page, "data-consent-open") {
			t.Errorf("%s offers a choice although nothing is turned on", p)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); csp != edge.ContentSecurityPolicy {
			t.Errorf("%s: policy %q", p, csp)
		}
	}

	// A bad identifier is refused with a reason and stores nothing.
	resp := post(t, root, ts.URL+"/admin/analytics", analyticsForm("nope", testGA, ""))
	if msg := flashOf(t, resp); !strings.Contains(msg, "Scarf pixel ID is a UUID") {
		t.Errorf("flash %q", msg)
	}
	if v := storedSetting(t, srv, settingGoogleAnalytics); v != "" {
		t.Errorf("a refused save stored %q", v)
	}
	if srv.cfg().analytics.Any() {
		t.Error("a refused save is in force")
	}

	// Saved: stored, in force, and the public pages offer the choice with the widened policy.
	if msg := flashOf(t, post(t, root, ts.URL+"/admin/analytics", analyticsForm(" "+testScarf, testGA, testAds))); !strings.Contains(msg, "saved") {
		t.Fatalf("flash %q", msg)
	}
	if a := srv.cfg().analytics; a.ScarfPixelID != testScarf || a.GoogleAnalyticsID != testGA || a.AdSensePublisher != testAds {
		t.Errorf("live analytics %+v", a)
	}
	if v := storedSetting(t, srv, settingAdSense); v != testAds {
		t.Errorf("stored AdSense %q", v)
	}
	for _, p := range public {
		resp := mustGetResp(t, anon, ts.URL+p)
		page := bodyOf(t, resp)
		// The sign-in page asks for passwords: it offers no ads (they widen the script policy to inline
		// scripts), so it has the two other services only.
		signIn := p == "/"
		wants := []string{
			`id="consent"`, `data-scarf="` + testScarf + `"`, `data-ga="` + testGA + `"`, `data-adsense="` + testAds + `"`,
			"consent.js", "data-consent-open", `data-accept`, `data-decline`, `href="/privacy#analytics"`, "Scarf, Google Analytics and Google AdSense",
		}
		if signIn {
			wants = []string{`id="consent"`, `data-scarf="` + testScarf + `"`, `data-ga="` + testGA + `"`, "consent.js", "data-consent-open", `data-accept`, `data-decline`, `href="/privacy#analytics"`, "Scarf and Google Analytics"}
			if strings.Contains(page, `data-adsense="`+testAds+`"`) {
				t.Errorf("the sign-in page offers ads")
			}
		}
		for _, want := range wants {
			if !strings.Contains(page, want) {
				t.Errorf("%s lacks %q", p, want)
			}
		}
		// The identifiers are only data attributes: the page itself loads nothing from outside before
		// consent (the policy may name and link the services; a link is not a load).
		if offSite.MatchString(page) {
			t.Errorf("%s loads something from another site before consent: %v", p, offSite.FindAllString(page, 3))
		}
		csp := resp.Header.Get("Content-Security-Policy")
		for _, want := range []string{"https://static.scarf.sh", "https://www.googletagmanager.com", "https://pagead2.googlesyndication.com", "frame-ancestors 'none'", "form-action 'self'"} {
			if signIn && strings.Contains(want, "googlesyndication") {
				if strings.Contains(csp, want) {
					t.Errorf("%s: policy names the ad network: %s", p, csp)
				}
				continue
			}
			if !strings.Contains(csp, want) {
				t.Errorf("%s: policy lacks %q: %s", p, want, csp)
			}
		}
	}

	// Somebody who is signed in never gets it, on any page, and keeps the strict policy.
	for _, p := range []string{"/docs", "/status", "/terms", "/privacy", "/u/1/estimates", "/u/1/scale", "/u/1/settings", "/admin"} {
		resp := mustGetResp(t, root, ts.URL+p)
		page := bodyOf(t, resp)
		if strings.Contains(page, `id="consent"`) || strings.Contains(page, "consent.js") || strings.Contains(page, "data-consent-open") {
			t.Errorf("signed in, %s offers the choice", p)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); csp != edge.ContentSecurityPolicy {
			t.Errorf("signed in, %s: widened policy %q", p, csp)
		}
	}
	// ... and neither does a page before sign-in that is not one of the public ones.
	for _, p := range []string{"/login/2fa", "/reset"} {
		resp := mustGetResp(t, anon, ts.URL+p)
		if page := bodyOf(t, resp); strings.Contains(page, `id="consent"`) {
			t.Errorf("%s offers the choice", p)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); csp != edge.ContentSecurityPolicy {
			t.Errorf("%s: widened policy %q", p, csp)
		}
	}

	// The admin form shows what is saved, and an empty form turns it all off again.
	if _, page := body(t, root, ts.URL+"/admin"); !strings.Contains(page, `value="`+testGA+`"`) || !strings.Contains(page, `action="/admin/analytics"`) {
		t.Error("the admin page does not show the saved identifiers")
	}
	if msg := flashOf(t, post(t, root, ts.URL+"/admin/analytics", analyticsForm("", "", ""))); !strings.Contains(msg, "off") {
		t.Errorf("flash %q", msg)
	}
	if srv.cfg().analytics.Any() || storedSetting(t, srv, settingScarfPixel) != "" {
		t.Error("clearing did not turn it off")
	}
	if page := bodyOf(t, mustGetResp(t, anon, ts.URL+"/docs")); strings.Contains(page, `id="consent"`) {
		t.Error("the notice is still there after turning it off")
	}
}

// Only an administrator can change it, and each change is audited.
func TestAnalyticsSettingsNeedAnAdministratorAndAreAudited(t *testing.T) {
	ts, root, _ := newAuthServer(t)
	srv := serverOf(ts)
	signup(t, ts, root, "root")
	bob := noRedirectClient()
	signup(t, ts, bob, "bob")
	if resp := post(t, bob, ts.URL+"/admin/analytics", analyticsForm(testScarf, "", "")); resp.StatusCode != 404 {
		t.Errorf("a plain user posting: %d, want 404", resp.StatusCode)
	}
	if post(t, noRedirectClient(), ts.URL+"/admin/analytics", analyticsForm(testScarf, "", "")); srv.cfg().analytics.Any() {
		t.Error("an anonymous visitor turned analytics on")
	}
	flashOf(t, post(t, root, ts.URL+"/admin/analytics", analyticsForm(testScarf, "", "")))
	events, err := srv.store.ListAuditEvents(t.Context(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range events {
		found = found || (e.Action == auditSettingChanged && e.Detail == "analytics_settings")
	}
	if !found {
		t.Error("the change was not audited")
	}
}

func TestValidateAdsTxt(t *testing.T) {
	for _, c := range []struct {
		in, want string
		bad      bool
	}{
		{"", "", false},
		{"  \r\n ", "", false},
		{"google.com, pub-1, DIRECT\r\nfoo.com, 2, RESELLER\r\n", "google.com, pub-1, DIRECT\nfoo.com, 2, RESELLER\n", false},
		{"a\x00b", "", true},
		{"a\xffb", "", true},
		{strings.Repeat("x", maxAdsTxtBytes+1), "", true},
	} {
		got, msg := validateAdsTxt(c.in)
		if (msg != "") != c.bad || got != c.want {
			t.Errorf("validateAdsTxt(%q) = %q, %q; want %q, bad=%v", c.in, got, msg, c.want, c.bad)
		}
	}
}

func TestAdminSavesAdsTxtAndItIsServedAtTheRoot(t *testing.T) {
	ts, root, _ := newAuthServer(t)
	srv := serverOf(ts)
	signup(t, ts, root, "root")
	anon := noRedirectClient()

	if resp := mustGetResp(t, anon, ts.URL+"/ads.txt"); resp.StatusCode != 404 {
		t.Fatalf("unset ads.txt: status %d", resp.StatusCode)
	}
	if msg := flashOf(t, post(t, root, ts.URL+"/admin/ads-txt", url.Values{"ads_txt": {"a\x00b"}})); !strings.Contains(msg, "control") {
		t.Errorf("flash %q", msg)
	}
	if v := storedSetting(t, srv, settingAdsTxt); v != "" {
		t.Errorf("a refused save stored %q", v)
	}

	line := "google.com, pub-1234567890123456, DIRECT, f08c47fec0942fa0"
	if msg := flashOf(t, post(t, root, ts.URL+"/admin/ads-txt", url.Values{"ads_txt": {line + "\r\n"}})); !strings.Contains(msg, "saved") {
		t.Fatalf("flash %q", msg)
	}
	resp := mustGetResp(t, anon, ts.URL+"/ads.txt")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if body := bodyOf(t, resp); body != line+"\n" {
		t.Errorf("body %q", body)
	}
	if srv.cfg().analytics.Any() {
		t.Error("ads.txt turned on the consent-gated services")
	}
	if page := bodyOf(t, mustGetResp(t, anon, ts.URL+"/")); strings.Contains(page, `id="consent"`) {
		t.Error("ads.txt alone made the public pages offer a choice")
	}
	if page := bodyOf(t, mustGetResp(t, root, ts.URL+"/admin")); !strings.Contains(page, line) {
		t.Error("the Admin page does not show the saved ads.txt")
	}

	if msg := flashOf(t, post(t, root, ts.URL+"/admin/ads-txt", url.Values{"ads_txt": {"  "}})); !strings.Contains(msg, "removed") {
		t.Errorf("flash %q", msg)
	}
	if resp := mustGetResp(t, anon, ts.URL+"/ads.txt"); resp.StatusCode != 404 {
		t.Errorf("removed ads.txt: status %d", resp.StatusCode)
	}
}
