// Package oidc is the worker for sign-in providers: it makes the connections the backend used to
// make to an identity provider, so the backend connects to nothing. "oidc.discover" fetches a
// provider's discovery document and signing keys, and "oidc.exchange" swaps the authorisation code
// of a sign-in for the ID token (or, for plain OAuth 2.0, for the account's userinfo).
//
// The worker has no database and no keys. The client secret comes with the job (for Apple already
// as the signed JWT), and the worker does not judge what it fetched beyond its shape: the backend
// verifies the ID token against the keys of the discovery.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/trueforge-org/dexafix/pkg/shared/netguard"
	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
	"github.com/trueforge-org/dexafix/pkg/workers/runner"
)

// Provider is the provider's name: the first part of its kinds.
const Provider = workerapi.ProviderOIDC

// The kinds this worker runs.
const (
	KindDiscover = Provider + "." + workerapi.ActionDiscover
	KindExchange = Provider + "." + workerapi.ActionExchange
)

const (
	requestTimeout = 15 * time.Second
	maxRedirects   = 5
	// maxResponse is the most a provider's answer may hold (discovery documents, key sets, tokens).
	maxResponse = 1 << 20
)

// Worker runs the oidc kinds of job.
type Worker struct {
	// client returns the HTTP client for one job; allowPrivate is the administrator's setting.
	client func(allowPrivate bool) *http.Client
}

// New returns a worker. A nil client makes each job use a guarded one (see providerClient); tests
// pass their own.
func New(client *http.Client) *Worker {
	w := &Worker{client: providerClient}
	if client != nil {
		w.client = func(bool) *http.Client { return client }
	}
	return w
}

// Handlers returns the handler of each kind, for runner.Runner.Handle.
func (w *Worker) Handlers() map[string]runner.Handler {
	return map[string]runner.Handler{
		KindDiscover: func(ctx context.Context, job *runner.Job) (any, error) {
			var p workerapi.DiscoverPayload
			if err := json.Unmarshal(job.Payload, &p); err != nil {
				return nil, runner.Permanent(errors.New("unreadable job"))
			}
			res, err := w.discover(ctx, p)
			if err != nil {
				return nil, err
			}
			return res, nil
		},
		KindExchange: func(ctx context.Context, job *runner.Job) (any, error) {
			var p workerapi.ExchangePayload
			if err := json.Unmarshal(job.Payload, &p); err != nil {
				return nil, runner.Permanent(errors.New("unreadable job"))
			}
			res, err := w.exchange(ctx, p)
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	}
}

// providerClient is the HTTP client for the sign-in providers: the default transport (HTTP/2) with
// the network guard on its dialer, an answer cut at maxResponse bytes, and redirects held to the
// same rule as the address an administrator typed. The environment's proxy is used only when the
// administrator allowed private addresses: the guard checks the address that is dialled, which
// behind a proxy is the proxy's, so a provider address would otherwise not be checked at all.
func providerClient(allowPrivate bool) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = func(r *http.Request) (*url.URL, error) {
		if allowPrivate {
			return http.ProxyFromEnvironment(r)
		}
		return nil, nil
	}
	t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second,
		Control: netguard.Control(func() bool { return allowPrivate })}).DialContext
	return &http.Client{Timeout: requestTimeout, Transport: limitedTransport{t}, CheckRedirect: checkRedirect}
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("too many redirects")
	}
	if !validURL(req.URL.String()) {
		return errors.New("redirected to an address that is not allowed")
	}
	return nil
}

// validURL accepts https, and http only for a provider on this machine.
func validURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	h := u.Hostname()
	return u.Scheme == "https" || (u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1"))
}

// limitedTransport cuts what a provider answers at maxResponse bytes: an endless body would
// otherwise be read into memory.
type limitedTransport struct{ base http.RoundTripper }

func (t limitedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err == nil {
		resp.Body = &limitedBody{ReadCloser: resp.Body, left: maxResponse}
	}
	return resp, err
}

type limitedBody struct {
	io.ReadCloser
	left int64
}

var errTooLarge = errors.New("the provider's answer is too large")

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 { // at the limit: the body may end exactly here, or go on
		var one [1]byte
		if n, err := b.ReadCloser.Read(one[:]); n > 0 {
			return 0, errTooLarge
		} else {
			return 0, err
		}
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err
}

// fail turns a failure into what the runner needs. The text is stored with the job and shown to an
// administrator, so it never holds what the provider answered. A refusal (a 4xx, a bad document, an
// address the guard blocks) is permanent; a transport problem or a 5xx is worth another try.
func fail(ctx context.Context, what string, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, netguard.ErrBlocked):
		return runner.Permanent(errors.New(what + ": " + workerapi.BlockedMessage))
	}
	var status statusError
	if errors.As(err, &status) && status.code < 500 {
		return runner.Permanent(fmt.Errorf("%s: the provider answered HTTP %d", what, status.code))
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.Response != nil && re.Response.StatusCode < 500 {
		return runner.Permanent(fmt.Errorf("%s: the provider answered HTTP %d", what, re.Response.StatusCode))
	}
	var bad badAnswer
	if errors.As(err, &bad) {
		return runner.Permanent(fmt.Errorf("%s: %s", what, bad.msg))
	}
	return runner.Retryable(errors.New(what+": the provider could not be reached or answered with an error"), 0)
}

// statusError is a provider that answered with a status other than 200.
type statusError struct{ code int }

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// badAnswer is an answer that cannot be used (not JSON, a missing field, an address that is not allowed).
type badAnswer struct{ msg string }

func (e badAnswer) Error() string { return e.msg }

// get fetches a URL and returns the body, which is at most maxResponse bytes.
func get(ctx context.Context, c *http.Client, target string, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, badAnswer{"the address is not valid"}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "DexaFix")
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError{resp.StatusCode}
	}
	b, err := io.ReadAll(resp.Body)
	if errors.Is(err, errTooLarge) {
		return nil, badAnswer{"the answer is too large"}
	}
	return b, err
}

// discover fetches the discovery document and the key set it points to.
func (w *Worker) discover(ctx context.Context, p workerapi.DiscoverPayload) (workerapi.DiscoverResult, error) {
	const what = "discovery"
	c := w.client(p.AllowPrivate)
	issuer := strings.TrimRight(p.Issuer, "/")
	if !validURL(issuer) {
		return workerapi.DiscoverResult{}, runner.Permanent(errors.New(what + ": the issuer address is not allowed"))
	}
	body, err := get(ctx, c, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return workerapi.DiscoverResult{}, fail(ctx, what, err)
	}
	var doc struct {
		Issuer      string `json:"issuer"`
		AuthURL     string `json:"authorization_endpoint"`
		TokenURL    string `json:"token_endpoint"`
		UserinfoURL string `json:"userinfo_endpoint"`
		JWKSURI     string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return workerapi.DiscoverResult{}, fail(ctx, what, badAnswer{"the answer is not a discovery document"})
	}
	// The issuer must be the one asked for; only a trailing slash may differ.
	if strings.TrimRight(doc.Issuer, "/") != issuer {
		return workerapi.DiscoverResult{}, fail(ctx, what, badAnswer{"the provider states another issuer"})
	}
	for _, u := range []string{doc.AuthURL, doc.TokenURL, doc.JWKSURI} {
		if !validURL(u) {
			return workerapi.DiscoverResult{}, fail(ctx, what, badAnswer{"the document lacks an endpoint or names one that is not allowed"})
		}
	}
	if doc.UserinfoURL != "" && !validURL(doc.UserinfoURL) {
		doc.UserinfoURL = ""
	}
	keys, err := get(ctx, c, doc.JWKSURI, nil)
	if err != nil {
		return workerapi.DiscoverResult{}, fail(ctx, "fetching the signing keys", err)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(keys, &set); err != nil || set.Keys == nil {
		return workerapi.DiscoverResult{}, fail(ctx, "fetching the signing keys", badAnswer{"the answer is not a key set"})
	}
	return workerapi.DiscoverResult{Issuer: doc.Issuer, AuthURL: doc.AuthURL, TokenURL: doc.TokenURL,
		UserinfoURL: doc.UserinfoURL, JWKS: keys}, nil
}

// exchange swaps the authorisation code at the token endpoint and returns the ID token, or for
// plain OAuth 2.0 the account's userinfo.
func (w *Worker) exchange(ctx context.Context, p workerapi.ExchangePayload) (workerapi.ExchangeResult, error) {
	const what = "code exchange"
	if p.Code == "" || p.ClientID == "" || !validURL(p.TokenURL) || (p.UserinfoURL != "" && !validURL(p.UserinfoURL)) {
		return workerapi.ExchangeResult{}, runner.Permanent(errors.New(what + ": the job is incomplete"))
	}
	c := w.client(p.AllowPrivate)
	cfg := &oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret, RedirectURL: p.RedirectURL,
		Endpoint: oauth2.Endpoint{TokenURL: p.TokenURL}}
	if p.SecretInParams {
		cfg.Endpoint.AuthStyle = oauth2.AuthStyleInParams
	}
	tok, err := cfg.Exchange(context.WithValue(ctx, oauth2.HTTPClient, c), p.Code, oauth2.VerifierOption(p.Verifier))
	if err != nil {
		return workerapi.ExchangeResult{}, fail(ctx, what, err)
	}
	if p.UserinfoURL != "" {
		body, err := get(ctx, c, p.UserinfoURL, http.Header{"Authorization": {"Bearer " + tok.AccessToken}})
		if err != nil {
			return workerapi.ExchangeResult{}, fail(ctx, "fetching the account", err)
		}
		if !json.Valid(body) {
			return workerapi.ExchangeResult{}, fail(ctx, "fetching the account", badAnswer{"the answer is not JSON"})
		}
		return workerapi.ExchangeResult{Userinfo: body}, nil
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return workerapi.ExchangeResult{}, fail(ctx, what, badAnswer{"the token response has no id_token"})
	}
	return workerapi.ExchangeResult{IDToken: raw}, nil
}
