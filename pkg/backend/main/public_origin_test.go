package mainbackend

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The api's own public listener may be reached without the web process in front of it. A page of
// another origin (a sibling host is "same-site") must not be able to act as the signed-in user with
// a plain form or text/plain post, which a browser sends with the cookie and without a preflight.
func TestThePublicListenerRefusesRequestsFromOtherOrigins(t *testing.T) {
	g := newRig(t)
	uid, session := g.signUp("alice")
	path := fmt.Sprintf("/api/v1/users/%d/scale-readings", uid)
	const reading = `[{"measured_at":"2026-03-01T08:00:00Z","weight_kg":80}]`

	post := func(contentType string, hdr map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(reading)).WithContext(context.Background())
		req.RemoteAddr = "192.0.2.50:1234"
		req.AddCookie(session)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		g.b.PublicHandler().ServeHTTP(rec, req)
		return rec
	}
	count := func() int {
		rs, err := g.st.ListScaleReadings(context.Background(), uid)
		if err != nil {
			t.Fatal(err)
		}
		return len(rs)
	}
	evil := map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "same-site"}

	for name, tc := range map[string]struct {
		contentType string
		hdr         map[string]string
		want        int
	}{
		"text/plain from another origin (the audit's request)": {"text/plain", evil, http.StatusForbidden},
		"JSON from another origin":                             {"application/json", evil, http.StatusForbidden},
		"a form from another site":                             {"application/x-www-form-urlencoded", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		"text/plain from a script (no Origin)":                 {"text/plain", nil, http.StatusUnsupportedMediaType},
		"a form body from a script":                            {"application/x-www-form-urlencoded", nil, http.StatusUnsupportedMediaType},
		"a bad content type":                                   {"application/json; charset", nil, http.StatusUnsupportedMediaType},
	} {
		if rec := post(tc.contentType, tc.hdr); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body, tc.want)
		}
		if n := count(); n != 0 {
			t.Fatalf("%s: a reading was stored (%d)", name, n)
		}
	}

	// Scripts and the pages' own origin still work.
	for name, hdr := range map[string]map[string]string{
		"a script":                      nil,
		"the same origin":               {"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"},
		"a media type with a parameter": {"X-Case": "charset"},
	} {
		ct := "application/json"
		if name == "a media type with a parameter" {
			ct = "application/json; charset=utf-8"
		}
		req := httptest.NewRequest("POST", path, strings.NewReader(reading))
		req.Host = "example.com"
		req.RemoteAddr = "192.0.2.50:1234"
		req.AddCookie(session)
		req.Header.Set("Content-Type", ct)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		g.b.PublicHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

// An origin the administrator named as the base URL is trusted, and a change of it takes effect.
func TestTheBaseURLIsATrustedOriginOfThePublicListener(t *testing.T) {
	g := newRig(t)
	uid, session := g.signUp("alice")
	path := fmt.Sprintf("/api/v1/users/%d/settings", uid)
	put := func() int {
		t.Helper()
		req := httptest.NewRequest("PUT", path, strings.NewReader(`{"athlete_default":true,"method_ranking":["dexa","scale"]}`))
		req.RemoteAddr = "192.0.2.50:1234"
		req.AddCookie(session)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://dexa.example.com")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		g.b.PublicHandler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := put(); code != http.StatusForbidden {
		t.Fatalf("before the base URL is set: %d", code)
	}
	base, _ := url.Parse("https://dexa.example.com")
	l := *g.b.cfg()
	l.baseURL = base
	g.b.swapSettings(&l)
	if code := put(); code == http.StatusForbidden {
		t.Fatalf("the base URL is not trusted: %d", code)
	}
	other, _ := url.Parse("https://other.example.com")
	l2 := *g.b.cfg()
	l2.baseURL = other
	g.b.swapSettings(&l2)
	if code := put(); code != http.StatusForbidden {
		t.Fatalf("the old base URL is still trusted: %d", code)
	}
}

// Older scripts sent a JSON body with no Content-Type at all; a browser always names one, so that is
// still read (the cross-origin check refuses a browser of another origin before this).
func TestABodyWithoutAContentTypeIsStillRead(t *testing.T) {
	g := newRig(t)
	uid, session := g.signUp("alice")
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/users/%d/scale-readings", uid), strings.NewReader(`[{"measured_at":"2026-03-01T08:00:00Z","weight_kg":80}]`))
	req.RemoteAddr = "192.0.2.50:1234"
	req.AddCookie(session)
	req.Header.Del("Content-Type")
	rec := httptest.NewRecorder()
	g.b.PublicHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
