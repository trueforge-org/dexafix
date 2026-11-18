package mainbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"

	"github.com/trueforge-org/dexafix/pkg/models"
)

// The backend verifies ID tokens against the signing keys a worker fetched (oidc.discover) and the
// store keeps (auth_provider_metadata). It never fetches keys itself, so verifying a token makes no
// connection; a token signed with a key the stored set lacks is a sign the provider rotated its keys,
// and asks a worker to fetch them again.

// signatureAlgs are the algorithms a stored key set is read with; which ones a token may use is the
// verifier's rule (oidc.Config), as it is for a remote key set.
var signatureAlgs = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512, jose.ES256, jose.ES384, jose.ES512,
	jose.PS256, jose.PS384, jose.PS512, jose.EdDSA,
}

// storedKeys is a key set held in memory: oidc.KeySet without a network.
type storedKeys struct{ keys []jose.JSONWebKey }

func parseKeySet(raw string) (storedKeys, error) {
	var set jose.JSONWebKeySet
	if err := json.Unmarshal([]byte(raw), &set); err != nil {
		return storedKeys{}, err
	}
	return storedKeys{keys: set.Keys}, nil
}

// keyID reads the key a token says it is signed with.
func keyID(jwt string) (string, error) {
	jws, err := jose.ParseSigned(jwt, signatureAlgs)
	if err != nil {
		return "", fmt.Errorf("oidc: malformed jwt: %v", err)
	}
	if len(jws.Signatures) == 0 { // not reachable: ParseSigned refuses a token without a signature
		return "", errors.New("oidc: token has no signature")
	}
	return jws.Signatures[0].Header.KeyID, nil
}

// knows reports whether the set has the key a token names (a token that names none matches any).
func (k storedKeys) knows(jwt string) bool {
	kid, err := keyID(jwt)
	if err != nil {
		return true // a token that does not parse says nothing about the keys
	}
	for _, key := range k.keys {
		if kid == "" || key.KeyID == kid {
			return true
		}
	}
	return false
}

// VerifySignature implements oidc.KeySet. Like the remote key set it tries the keys whose id matches.
func (k storedKeys) VerifySignature(_ context.Context, jwt string) ([]byte, error) {
	jws, err := jose.ParseSigned(jwt, signatureAlgs)
	if err != nil {
		return nil, fmt.Errorf("oidc: malformed jwt: %v", err)
	}
	kid := jws.Signatures[0].Header.KeyID // ParseSigned refuses a token without a signature
	for _, key := range k.keys {
		if kid == "" || key.KeyID == kid {
			if payload, err := jws.Verify(&key); err == nil {
				return payload, nil
			}
		}
	}
	return nil, errors.New("failed to verify id token signature")
}

// verifyIDToken checks a token against the stored metadata of its provider: signature, issuer,
// audience (the provider's client id) and expiry. stale is true when the token names a key the stored
// set lacks.
func verifyIDToken(ctx context.Context, p models.AuthProvider, m models.AuthProviderMetadata, raw string) (idt *oidc.IDToken, stale bool, err error) {
	keys, err := parseKeySet(m.JWKS)
	if err != nil {
		return nil, false, fmt.Errorf("the stored signing keys of %s are not readable", p.Slug)
	}
	idt, err = oidc.NewVerifier(m.Issuer, keys, &oidc.Config{ClientID: p.ClientID}).Verify(ctx, raw)
	if err != nil {
		return nil, !keys.knows(raw), err
	}
	return idt, false, nil
}
