package mainbackend

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2"

	"github.com/trueforge-org/dexafix/pkg/models"
	"github.com/trueforge-org/dexafix/pkg/shared/must"
	"github.com/trueforge-org/dexafix/pkg/shared/workerapi"
	"github.com/trueforge-org/dexafix/pkg/store"
)

// Sign-in providers: OpenID Connect, Apple and plain OAuth 2.0. The worker (pkg/workers/oidc) makes
// the connections to the provider; this is what the backend does around it. It holds the
// administrator's configuration and the client secrets and hands them to a worker only inside a
// lease, it keeps what discovery returned (endpoints and signing keys) in the store, and it verifies
// every ID token itself, against those keys. Nothing here connects to anything.
//
// The jobs belong to no user: a visitor who signs in is not known yet.

const (
	providerOIDC = workerapi.ProviderOIDC

	// oidcWait is how long a page waits for a worker; oidcExpiry is how long a queued job stays good
	// (an authorisation code is short-lived, and nobody waits longer than the page does).
	oidcWait   = 20 * time.Second
	oidcExpiry = 25 * time.Second

	// providerKeyCooldown is the least time between two refreshes of one provider's keys that a
	// token asked for, so a stream of tokens signed with an unknown key does not become a stream of
	// jobs.
	providerKeyCooldown = time.Minute
	// providerKeyMaxAge is how old stored keys may get before the maintenance loop fetches them again.
	providerKeyMaxAge = 24 * time.Hour
)

var (
	kindOIDCDiscover = workerapi.Kind(providerOIDC, workerapi.ActionDiscover)
	kindOIDCExchange = workerapi.Kind(providerOIDC, workerapi.ActionExchange)

	// errStaleKeys: an ID token is signed with a key the stored set lacks. The job fails with this
	// text, which the callback recognises, and a refresh of the keys is queued.
	errStaleKeys = errors.New("the provider's signing keys changed")
)

// oidcDiscoverJob is the parameters of an oidc.discover job. Without a slug the result is only
// reported (an administrator trying an issuer before saving it); with one it is also stored.
type oidcDiscoverJob struct {
	Slug   string `json:"slug,omitempty"`
	Issuer string `json:"issuer"`
}

// oidcExchangeJob is the parameters of an oidc.exchange job: what one sign-in callback brought. The
// nonce is not sent to the worker; the backend checks it in the ID token.
type oidcExchangeJob struct {
	Slug     string `json:"slug"`
	Code     string `json:"code"`
	Verifier string `json:"verifier"`
	Redirect string `json:"redirect"`
	Nonce    string `json:"nonce,omitempty"`
}

// oidcIdentity is what an exchange keeps: who the provider says signed in. It is read once by the
// callback that waited for it, and cleared.
type oidcIdentity struct {
	Subject string          `json:"subject"`
	Claims  json.RawMessage `json:"claims"`
}

func oidcProviderSpec() workerProvider {
	return workerProvider{
		Name: providerOIDC, Label: "Sign-in providers",
		Kinds: []kindSpec{
			{
				Kind: kindOIDCDiscover, Label: "Sign-in provider discovery",
				Defaults: kindDefaults{MaxRunning: 4, MaxAttempts: 2, RetryBase: 2 * time.Second, Timeout: time.Minute},
				Lease:    leaseOIDCDiscover,
				Apply:    applyOIDCDiscover,
			},
			{
				Kind: kindOIDCExchange, Label: "Sign-in code exchange",
				// The code works once: a second attempt only helps when the first never reached the provider.
				Defaults: kindDefaults{MaxRunning: 16, MaxAttempts: 2, RetryBase: time.Second, Timeout: time.Minute},
				Lease:    leaseOIDCExchange,
				Apply:    applyOIDCExchange,
			},
		},
	}
}

func leaseOIDCDiscover(_ context.Context, s *Backend, j models.Job) (any, error) {
	var in oidcDiscoverJob
	if err := json.Unmarshal([]byte(j.Payload), &in); err != nil || in.Issuer == "" {
		return nil, permanent(errors.New("unreadable job"))
	}
	return workerapi.DiscoverPayload{Issuer: in.Issuer, AllowPrivate: s.allowPrivateProviders()}, nil
}

func applyOIDCDiscover(ctx context.Context, s *Backend, j models.Job, result json.RawMessage) (string, error) {
	var in oidcDiscoverJob
	if err := json.Unmarshal([]byte(j.Payload), &in); err != nil {
		return "", errBadResult
	}
	var res workerapi.DiscoverResult
	if err := json.Unmarshal(result, &res); err != nil {
		return "", errBadResult
	}
	// What a worker brings is checked again: the issuer is the one asked for (a trailing slash apart),
	// the endpoints are addresses a provider may have, and the key set parses.
	if strings.TrimRight(res.Issuer, "/") != strings.TrimRight(in.Issuer, "/") || !validProviderURL(res.AuthURL) || !validProviderURL(res.TokenURL) ||
		(res.UserinfoURL != "" && !validProviderURL(res.UserinfoURL)) {
		return "", errBadResult
	}
	if _, err := parseKeySet(string(res.JWKS)); err != nil {
		return "", errBadResult
	}
	if in.Slug != "" {
		p, err := s.store.GetAuthProvider(ctx, in.Slug)
		if errors.Is(err, store.ErrNotFound) {
			return string(result), nil // removed meanwhile: nothing to keep it for
		}
		if err != nil {
			return "", err
		}
		if strings.TrimRight(p.Issuer, "/") != strings.TrimRight(in.Issuer, "/") {
			return string(result), nil // changed meanwhile: this is the old issuer's
		}
		err = s.store.PutAuthProviderMetadata(ctx, models.AuthProviderMetadata{
			Slug: p.Slug, Issuer: res.Issuer, AuthURL: res.AuthURL, TokenURL: res.TokenURL,
			UserinfoURL: res.UserinfoURL, JWKS: string(res.JWKS), FetchedAt: time.Now(),
		})
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	return string(result), nil
}

func leaseOIDCExchange(ctx context.Context, s *Backend, j models.Job) (any, error) {
	var in oidcExchangeJob
	if err := json.Unmarshal([]byte(j.Payload), &in); err != nil {
		return nil, permanent(errors.New("unreadable job"))
	}
	p, err := s.store.GetAuthProvider(ctx, in.Slug)
	if errors.Is(err, store.ErrNotFound) {
		return nil, permanent(errors.New("the provider was removed"))
	}
	if err != nil {
		return nil, err
	}
	out := workerapi.ExchangePayload{
		ClientID: p.ClientID, ClientSecret: p.ClientSecret, RedirectURL: in.Redirect, Code: in.Code,
		Verifier: in.Verifier, AllowPrivate: s.allowPrivateProviders(),
	}
	if p.Kind == models.ProviderKindOAuth2 {
		out.TokenURL, out.UserinfoURL = p.TokenURL, p.UserinfoURL
		return out, nil
	}
	m, err := s.store.GetAuthProviderMetadata(ctx, p.Slug)
	if errors.Is(err, store.ErrNotFound) {
		return nil, permanent(errors.New("the provider's endpoints are not known yet"))
	}
	if err != nil {
		return nil, err
	}
	out.TokenURL = m.TokenURL
	if p.Kind == models.ProviderKindApple {
		// Apple's client secret is a short-lived JWT signed here: the worker never sees the private key.
		out.SecretInParams = true
		if out.ClientSecret, err = appleClientSecret(p, time.Now()); err != nil {
			return nil, permanent(err)
		}
	}
	return out, nil
}

func applyOIDCExchange(ctx context.Context, s *Backend, j models.Job, result json.RawMessage) (string, error) {
	var in oidcExchangeJob
	if err := json.Unmarshal([]byte(j.Payload), &in); err != nil {
		return "", errBadResult
	}
	var res workerapi.ExchangeResult
	if err := json.Unmarshal(result, &res); err != nil {
		return "", errBadResult
	}
	p, err := s.store.GetAuthProvider(ctx, in.Slug)
	if errors.Is(err, store.ErrNotFound) {
		return "", errBadResult
	}
	if err != nil {
		return "", err
	}
	if p.Kind == models.ProviderKindOAuth2 {
		// Plain OAuth 2.0 has no signed token: the account is whatever the userinfo endpoint says
		// about the access token, as before.
		var claims map[string]any
		dec := json.NewDecoder(bytes.NewReader(res.Userinfo))
		dec.UseNumber() // numeric account ids must not turn into floats
		if dec.Decode(&claims) != nil || claimString(claims, cmpOr(p.SubjectClaim, "sub")) == "" {
			return "", errBadResult
		}
		return marshalIdentity(claimString(claims, cmpOr(p.SubjectClaim, "sub")), res.Userinfo), nil
	}
	m, err := s.store.GetAuthProviderMetadata(ctx, p.Slug)
	if errors.Is(err, store.ErrNotFound) {
		return "", errBadResult
	}
	if err != nil {
		return "", err
	}
	idt, stale, err := verifyIDToken(ctx, p, m, res.IDToken)
	if err != nil {
		if stale {
			s.refreshProviderKeys(ctx, p)
			return "", errStaleKeys
		}
		log.Warn().Err(err).Str("provider", p.Slug).Msg("an ID token was not accepted")
		return "", errBadResult
	}
	if !hmac.Equal([]byte(idt.Nonce), []byte(in.Nonce)) {
		log.Warn().Str("provider", p.Slug).Msg("an ID token had the wrong nonce")
		return "", errBadResult
	}
	var claims json.RawMessage
	must.Do(idt.Claims(&claims)) // cannot fail: the verified token's payload already parsed as an object
	return marshalIdentity(idt.Subject, claims), nil
}

func marshalIdentity(subject string, claims json.RawMessage) string {
	return string(must.Value(json.Marshal(oidcIdentity{Subject: subject, Claims: claims}))) // plain strings and valid JSON
}

// ---- what the pages and the API use ----

// jobFailed is the error of a worker's job that did not end well; its text is the job's error.
func jobFailed(j models.Job) error { return errors.New(j.Error) }

// runOIDCJob queues an interactive job and waits for it. A job that is still going when the wait
// ends is canceled: nobody is waiting for it any more, and it holds a code or a credential.
func (s *Backend) runOIDCJob(ctx context.Context, e enqueueSpec) (models.Job, error) {
	e.Interactive = true
	if e.Expires == 0 {
		e.Expires = oidcExpiry
	}
	j, _, err := s.enqueueJob(ctx, e)
	if err != nil {
		return j, err
	}
	if j, err = s.waitJob(ctx, j.PublicID, oidcWait); err != nil {
		return j, err
	}
	if models.JobActive(j.State) {
		if _, cerr := s.store.CancelJob(context.WithoutCancel(ctx), j.PublicID, 0, time.Now()); cerr != nil {
			log.Warn().Err(cerr).Msg("could not cancel a sign-in job nobody waits for")
		}
		return j, errors.New("the worker did not answer in time")
	}
	if j.State != models.JobSucceeded {
		return j, jobFailed(j)
	}
	return j, nil
}

// discoverIssuer has a worker read the discovery document of an issuer and returns what it found.
// slug, when set, also has the result stored for that provider.
func (s *Backend) discoverIssuer(ctx context.Context, slug, issuer string) (workerapi.DiscoverResult, error) {
	key := "oidc.discover:" + slug
	if slug == "" {
		key = "oidc.discover:" + randomHex(8)
	}
	j, err := s.runOIDCJob(ctx, enqueueSpec{
		Kind: kindOIDCDiscover, Origin: models.JobOriginAdmin, Dedupe: key, Payload: oidcDiscoverJob{Slug: slug, Issuer: issuer},
	})
	if err != nil {
		return workerapi.DiscoverResult{}, err
	}
	var res workerapi.DiscoverResult
	if err := json.Unmarshal([]byte(j.Result), &res); err != nil { // not reachable: applying a result that does not parse failed the job
		return res, err
	}
	return res, nil
}

// providerMetadata returns what a worker found out about an OpenID Connect or Apple provider. The
// first time (or after the administrator changed the issuer) it has a worker fetch it and waits.
func (s *Backend) providerMetadata(ctx context.Context, p models.AuthProvider) (models.AuthProviderMetadata, error) {
	m, err := s.store.GetAuthProviderMetadata(ctx, p.Slug)
	if err == nil || !errors.Is(err, store.ErrNotFound) {
		return m, err
	}
	if _, err := s.discoverIssuer(ctx, p.Slug, p.Issuer); err != nil {
		return m, err
	}
	return s.store.GetAuthProviderMetadata(ctx, p.Slug)
}

// authCodeURL is where a visitor is sent to sign in at the provider.
func (s *Backend) authCodeURL(ctx context.Context, p models.AuthProvider, redirect, state string, opts ...oauth2.AuthCodeOption) (string, error) {
	cfg := &oauth2.Config{ClientID: p.ClientID, RedirectURL: redirect, Scopes: strings.Fields(p.Scopes), Endpoint: oauth2.Endpoint{AuthURL: p.AuthURL}}
	if p.Kind == models.ProviderKindApple { // a key that cannot sign is found now, not at the callback
		if _, err := appleClientSecret(p, time.Now()); err != nil {
			return "", err
		}
	}
	if p.Kind != models.ProviderKindOAuth2 {
		m, err := s.providerMetadata(ctx, p)
		if err != nil {
			return "", err
		}
		cfg.Endpoint.AuthURL = m.AuthURL
	}
	return cfg.AuthCodeURL(state, opts...), nil
}

// exchangeCode has a worker swap the authorisation code of a callback, and returns the verified
// subject and claims.
func (s *Backend) exchangeCode(ctx context.Context, p models.AuthProvider, redirect, code string, st providerState) (string, map[string]any, error) {
	j, err := s.runOIDCJob(ctx, enqueueSpec{
		Kind: kindOIDCExchange, Origin: models.JobOriginUser, Dedupe: "oidc.exchange:" + st.State,
		Payload: oidcExchangeJob{Slug: p.Slug, Code: code, Verifier: st.Verifier, Redirect: redirect, Nonce: st.Nonce},
	})
	if err != nil {
		return "", nil, err
	}
	var id oidcIdentity
	if err := json.Unmarshal([]byte(j.Result), &id); err != nil { // not reachable: applying a result that does not parse failed the job
		return "", nil, err
	}
	// The identity was for this one callback.
	if err := s.store.ClearJobResult(context.WithoutCancel(ctx), j.PublicID); err != nil {
		log.Warn().Err(err).Msg("could not clear a sign-in result")
	}
	var claims map[string]any
	dec := json.NewDecoder(bytes.NewReader(id.Claims))
	dec.UseNumber()
	if err := dec.Decode(&claims); err != nil { // not reachable: the claims were checked to be an object
		return "", nil, err
	}
	return id.Subject, claims, nil
}

// refreshProviderKeys asks a worker to fetch a provider's keys again, at most once a minute per
// provider and without waiting. A token signed with an unknown key means the provider rotated them.
func (s *Backend) refreshProviderKeys(ctx context.Context, p models.AuthProvider) {
	if p.Kind == models.ProviderKindOAuth2 {
		return
	}
	if last, ok := s.keyRefresh.Load(p.Slug); ok && time.Since(last.(time.Time)) < providerKeyCooldown {
		return
	}
	s.keyRefresh.Store(p.Slug, time.Now())
	_, _, err := s.enqueueJob(ctx, enqueueSpec{
		Kind: kindOIDCDiscover, Origin: models.JobOriginSystem, Dedupe: "oidc.discover:" + p.Slug,
		Payload: oidcDiscoverJob{Slug: p.Slug, Issuer: p.Issuer}, Expires: 5 * time.Minute,
	})
	if err != nil && !errors.Is(err, errKindPaused) {
		log.Warn().Err(err).Str("provider", p.Slug).Msg("could not queue a refresh of a provider's signing keys")
	}
}

// refreshStaleProviderKeys is the maintenance loop's part: keys are fetched again once a day, so a
// provider that rotates them is followed before a token with the new key arrives.
func (s *Backend) refreshStaleProviderKeys(ctx context.Context) {
	providers, err := s.store.ListAuthProviders(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Error().Err(err).Msg("list the sign-in providers")
		}
		return
	}
	for _, p := range providers {
		if p.Kind == models.ProviderKindOAuth2 {
			continue
		}
		m, err := s.store.GetAuthProviderMetadata(ctx, p.Slug)
		if err == nil && time.Since(m.FetchedAt) < providerKeyMaxAge {
			continue
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			continue
		}
		s.refreshProviderKeys(ctx, p)
	}
}
