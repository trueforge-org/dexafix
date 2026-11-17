// Package netguard keeps the backend's own outgoing connections (to an administrator's sign-in
// providers and mail server) away from addresses that a typo or a hostile setting must not be able to
// reach: the cloud metadata services and the other addresses on a link, and, unless an administrator
// allows it, the private networks the server sits in. It takes nothing but the standard library, so
// both sides of the process boundary may use it.
//
// The check runs when the connection is made, on the address the name resolved to, so a name that
// answers with a different address on each lookup (DNS rebinding) and a redirect to a private address
// get the same answer as a private address typed in.
package netguard

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"syscall"
	"time"
)

// ErrBlocked is returned for an address the guard refuses. The text names no address: it may reach a
// person who must not learn what is behind a name.
var ErrBlocked = errors.New("refusing to connect to that address")

// Always refused, whatever the setting: the metadata services of the big clouds (169.254.169.254 is
// link-local and covered below; these two are not), and the whole "this network" block, which Linux
// routes to the machine itself.
var (
	alwaysBlocked = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud
		netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS, IPv6
	}
	// Refused unless private addresses are allowed: what IsPrivate does not cover (carrier-grade NAT,
	// where some clouds put their own services).
	privateExtra = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10")}
)

// Check says whether a connection to ip may be made. Loopback is allowed: the sign-in provider rule
// lets a development server on http://localhost through, and the mail client accepts an unencrypted
// server on this machine, so both are already deliberate.
func Check(ip netip.Addr, allowPrivate bool) error {
	ip = ip.Unmap() // ::ffff:10.0.0.1 is 10.0.0.1
	if ip.IsLoopback() {
		return nil
	}
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return ErrBlocked
	}
	for _, p := range alwaysBlocked {
		if p.Contains(ip) {
			return ErrBlocked
		}
	}
	if !allowPrivate {
		if ip.IsPrivate() {
			return ErrBlocked
		}
		for _, p := range privateExtra {
			if p.Contains(ip) {
				return ErrBlocked
			}
		}
	}
	return nil
}

// Control is a net.Dialer's Control function. allowPrivate is asked at each connection, so a
// setting changed on the Admin page applies to the next one.
func Control(allowPrivate func() bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return ErrBlocked
		}
		ip, err := netip.ParseAddr(host)
		if err != nil { // the dialer hands over an address, never a name
			return ErrBlocked
		}
		return Check(ip, allowPrivate())
	}
}

// maxRedirects is how many redirects an outside client follows.
const maxRedirects = 10

// proxyVariables are the environment variables that send a client through a proxy.
var proxyVariables = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"}

// OutsideClient returns an HTTP client for a worker's calls to an outside service whose address is a
// constant (Withings, Strength Level): its dialer refuses the addresses Check refuses, so a redirect or
// a name that points inside the network gets nowhere, and it follows no more than a few redirects and
// never from https to http. Behind a proxy the address it dials is the proxy's, which is usually a
// private one; the guard then lets private addresses through (it still refuses the metadata services
// and link-local ones).
func OutsideClient(timeout time.Duration) *http.Client {
	proxied := false
	for _, v := range proxyVariables {
		if os.Getenv(v) != "" {
			proxied = true
		}
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: Control(func() bool { return proxied })}).DialContext
	return &http.Client{Timeout: timeout, Transport: t, CheckRedirect: checkRedirect}
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("too many redirects")
	}
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("redirected from https to http")
	}
	return nil
}
