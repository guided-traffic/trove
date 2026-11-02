package oidc

import (
	"errors"
	"fmt"
	"slices"

	"github.com/guided-traffic/trove/internal/auth/jws"
)

// The login's view of the shared verifier (R-AUTH-006, Q-AUTH-31, Q-CI-19): the compact JWS, the
// algorithms and the JWK set rules live in internal/auth/jws, which the subject-token validation of
// the CI exchange uses too. This file keeps the names the ID-token validation and the provider use,
// so their code reads as before: parse, check the algorithm against allowed_signing_algs, select the
// key of the kid, verify.

// errJWS is the reason code of every form, algorithm and signature failure
// (id_token_invalid); the cause stays in the log.
var errJWS = errors.New("id_token_invalid")

// jwk is one verification key of a JWK set.
type jwk = jws.JWK

// jwsHeader is what the login reads of the protected header.
type jwsHeader struct {
	Alg string
	Kid string
}

// parsedJWS is a compact JWS split and decoded, not yet verified.
type parsedJWS struct {
	header  jwsHeader
	payload []byte
	token   *jws.Token
}

// parseJWS splits and decodes a compact JWS. A token with five parts is a JWE,
// which is not supported (R-AUTH-006).
func parseJWS(token string) (parsedJWS, error) {
	t, err := jws.Parse(token)
	if err != nil {
		return parsedJWS{}, fmt.Errorf("%w: %w", errJWS, err)
	}
	return parsedJWS{header: jwsHeader{Alg: t.Header.Alg, Kid: t.Header.Kid}, payload: t.Payload, token: t}, nil
}

// algorithm reports whether an algorithm is supported, with the key type it
// needs. none and HS* are never supported, whatever allowed_signing_algs says.
func algorithm(alg string) (string, bool) { return jws.KeyType(alg) }

// verify checks the signature of j with key under the header's algorithm, which
// must be in allowed.
func verify(j parsedJWS, key *jwk, allowed []string) error {
	alg := j.header.Alg
	if !jws.Supported(alg) || !slices.Contains(allowed, alg) {
		return fmt.Errorf("%w: alg %q is not allowed", errJWS, alg)
	}
	if j.token == nil || key == nil {
		return fmt.Errorf("%w: no token or no key", errJWS)
	}
	if err := j.token.VerifyWith(key.Public, key.Alg); err != nil {
		return fmt.Errorf("%w: %w", errJWS, err)
	}
	return nil
}

// parseJWKS decodes a JWK Set into its signature keys. A key of another type or
// use, or one that does not parse, is left out; the others still count.
func parseJWKS(body []byte) ([]*jwk, error) {
	set, err := jws.ParseJWKS(body)
	if err != nil {
		return nil, fmt.Errorf("JWK Set: %w", err)
	}
	return set.Keys, nil
}

// selectKey returns the key of a token's kid. A token without kid is matched
// only when the set holds exactly one key that fits its algorithm.
func selectKey(keys []*jwk, kid, alg string) *jwk { return jws.SelectKey(keys, kid, alg) }
