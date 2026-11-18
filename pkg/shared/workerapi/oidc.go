package workerapi

import "encoding/json"

// Sign-in providers: the connections the backend used to make to an identity provider, made by a
// worker so the backend itself connects to nothing. The backend keeps the administrator's
// configuration and the secrets; a lease carries what one job needs and nothing else. The jobs
// belong to no user: they run before a visitor is known.
//
//	oidc.discover  DiscoverPayload - fetch a provider's discovery document and its signing keys.
//	oidc.exchange  ExchangePayload - swap an authorisation code for the ID token (OpenID Connect,
//	                                 Apple) or for the account's userinfo (plain OAuth 2.0).
//
// The backend verifies the ID token itself, against the keys of the last discovery, so what a worker
// returns is checked and never trusted.

// Actions of the oidc provider.
const (
	ActionDiscover = "discover"
	ActionExchange = "exchange"
)

// BlockedMessage is the error text of a job that failed because the address of a provider or of the
// mail server is one the network guard refuses (private or link-local). The backend recognises it to
// point the administrator to the setting that allows private addresses.
const BlockedMessage = "the address is on a private or link-local network"

// DiscoverPayload is the payload of oidc.discover.
type DiscoverPayload struct {
	Issuer string `json:"issuer"`
	// AllowPrivate is the administrator's setting for sign-in providers on private network addresses.
	AllowPrivate bool `json:"allow_private,omitempty"`
}

// DiscoverResult is what the provider published.
type DiscoverResult struct {
	// Issuer is the issuer as the provider states it, which can differ from the configured one by a
	// trailing slash.
	Issuer      string          `json:"issuer"`
	AuthURL     string          `json:"auth_url"`
	TokenURL    string          `json:"token_url"`
	UserinfoURL string          `json:"userinfo_url,omitempty"`
	JWKS        json.RawMessage `json:"jwks"` // the key set, as served
}

// ExchangePayload is the payload of oidc.exchange.
type ExchangePayload struct {
	TokenURL string `json:"token_url"`
	ClientID string `json:"client_id"`
	// ClientSecret is the secret as it goes to the token endpoint; for Apple it is the signed JWT
	// the backend made, never the private key.
	ClientSecret string `json:"client_secret"`
	// SecretInParams sends the client credentials in the form body (Apple) instead of letting the
	// library choose.
	SecretInParams bool   `json:"secret_in_params,omitempty"`
	RedirectURL    string `json:"redirect_url"`
	Code           string `json:"code"`
	Verifier       string `json:"verifier"` // the PKCE verifier
	// UserinfoURL is set for plain OAuth 2.0: the worker fetches the account with the access token.
	UserinfoURL  string `json:"userinfo_url,omitempty"`
	AllowPrivate bool   `json:"allow_private,omitempty"`
}

// ExchangeResult holds what the provider answered. Exactly one is set: IDToken for OpenID
// Connect and Apple, Userinfo (the provider's JSON, as served) for plain OAuth 2.0. It is a
// credential, so the backend keeps it encrypted and drops it once the sign-in has read it.
type ExchangeResult struct {
	IDToken  string          `json:"id_token,omitempty"`
	Userinfo json.RawMessage `json:"userinfo,omitempty"`
}
