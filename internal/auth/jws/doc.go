// Package jws is the compact-JWS verifier that the browser login (internal/auth/oidc, R-AUTH-006,
// Q-AUTH-31) and the subject-token validation of the CI exchange (internal/ci/subject, R-CI-004)
// share (Q-CI-19): one place where an algorithm is added or an RFC 8725 finding is fixed.
//
// It knows the JWS compact serialization (RFC 7515 §7.1), the signature algorithms RS256, RS384,
// RS512, PS256, PS384, PS512, ES256, ES384 and EdDSA (RFC 7518 §3, RFC 8037) and JWK sets
// (RFC 7517). It refuses by construction what RFC 8725 warns about:
//
//   - `none` and every HMAC algorithm are never supported, whatever a caller's allow list says, so a
//     token signed with HS256 and the RSA public key as the secret never verifies (§3.1, §3.2);
//   - a key verifies only the algorithm family of its type (RSA, EC with the curve of the algorithm,
//     OKP Ed25519), only when its own `alg`, if present, equals the token's, and RSA keys have at
//     least 2048 bits (§3.5);
//   - no key ever comes from a token header: `jku`, `x5u`, `jwk` and `x5c` are not read (§3.10); a
//     `kid` is at most [MaxKIDLength] characters and is only a lookup key;
//   - a `crit` header, a header that is not a UTF-8 JSON object with unique member names, and a
//     part that is not strict base64url are malformed.
//
// [ParseJWKS] applies the key rules of R-CI-003 (`use` absent or `sig`, `key_ops` absent or holding
// `verify`, the key type, the 2048-bit minimum, the key's own `alg`, no private key material) and
// reports every key it leaves out with a stable reason, which the key status of a CI issuer shows.
//
// Every error message is a fixed text: it never holds a byte of the token, so a caller may log it
// and the CI exchange may put it in an audit event (R-CI-008). The package does no I/O, holds no
// state and imports only the standard library. It is a leaf (internal/archtest/layers.go).
package jws
