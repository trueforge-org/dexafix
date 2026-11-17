package frontend

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The Alt-Svc header says where HTTP/3 is. The port comes from what the administrator named, or from
// the Host header when it is a port number: a header a client chooses is not believed beyond that.
func TestAltSvcTakesItsPortOnlyFromWhatMayChooseIt(t *testing.T) {
	base := func(s string) *url.URL { u, _ := url.Parse(s); return u }
	for name, tc := range map[string]struct {
		settings frontendSettings
		host     string
		tls      bool
		want     string
	}{
		"the port of the base URL":            {frontendSettings{http3: true, httpsPort: "8443", baseURL: base("https://dexa.example.com:9443")}, "dexa.example.com:1", true, `h3=":9443"; ma=86400`},
		"the port the client used":            {frontendSettings{http3: true, httpsPort: "8443"}, "dexa.example.com:443", true, `h3=":443"; ma=86400`},
		"no port in Host":                     {frontendSettings{http3: true, httpsPort: "8443"}, "dexa.example.com", true, `h3=":8443"; ma=86400`},
		"a port that is not a number":         {frontendSettings{http3: true, httpsPort: "8443"}, `dexa.example.com:80"; evil="x`, true, `h3=":8443"; ma=86400`},
		"a port beyond the range":             {frontendSettings{http3: true, httpsPort: "8443"}, "dexa.example.com:70000", true, `h3=":8443"; ma=86400`},
		"a base URL without a port uses Host": {frontendSettings{http3: true, httpsPort: "8443", baseURL: base("https://dexa.example.com")}, "dexa.example.com:444", true, `h3=":444"; ma=86400`},
		"nothing over plain HTTP":             {frontendSettings{http3: true, httpsPort: "8443"}, "dexa.example.com:80", false, ``},
		"nothing when HTTP/3 is off":          {frontendSettings{httpsPort: "8443"}, "dexa.example.com:443", true, ``},
	} {
		f := &Frontend{}
		l := tc.settings
		f.live.Store(&l)
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = tc.host
		if tc.tls {
			req.TLS = &tls.ConnectionState{}
		}
		rec := httptest.NewRecorder()
		f.altSvc(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, req)
		if got := rec.Header().Get("Alt-Svc"); got != tc.want {
			t.Errorf("%s: Alt-Svc %q, want %q", name, got, tc.want)
		}
	}
}
