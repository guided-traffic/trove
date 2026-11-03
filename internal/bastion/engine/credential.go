package engine

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/bastion/mainclient"
)

// anonymousKey is the credential key of every anonymous caller (R-BAS-019): the main installation's decision
// does not depend on the client address, so the answers are shared.
const anonymousKey = "anonymous"

// credential is what a request presents (R-BAS-019). The token string is held for the duration of the request
// only; every answer is keyed by [credential.key], the SHA-256 of the token string.
type credential struct {
	anonymous bool
	token     string
	key       string
	// hint is the public prefix of the token (prefix and four characters), for logs.
	hint string
	// method is "bearer" or "basic", for the request log.
	method string
}

// hintLength is the length of the public prefix of a token in logs: the prefix and four characters.
const hintLength = 12

// credentialKey is the SHA-256 of the token string in hex.
func credentialKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// authorization reads the Authorization header the way the main installation does (internal/auth stage.go):
// Bearer, or Basic with the token as the password. present is true when the header carries any value, so a
// header that cannot be read is a refused credential and never a fall-through to anonymous. Cookies and
// X-NuGet-ApiKey are never read (R-BAS-019).
func authorization(h http.Header) (token, method string, present bool) {
	v := strings.TrimSpace(h.Get("Authorization"))
	if v == "" {
		return "", "", false
	}
	scheme, rest, _ := strings.Cut(v, " ")
	rest = strings.TrimSpace(rest)
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		return rest, "bearer", true
	case strings.EqualFold(scheme, "Basic"):
		dec, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return "", "basic", true
		}
		_, pw, ok := strings.Cut(string(dec), ":")
		if !ok {
			return "", "basic", true
		}
		return pw, "basic", true
	}
	return "", "", true
}

// parseCredential classifies the credential of a request. ok is false for a credential the Bastion refuses
// itself: an unreadable header, a token that fails the syntax or the checksum, a token of the type bst or of an
// unknown prefix (R-BAS-019). Such a request is never sent to the main installation.
func parseCredential(h http.Header) (c credential, ok bool) {
	token, method, present := authorization(h)
	if !present {
		return credential{anonymous: true, key: anonymousKey}, true
	}
	typ, valid := mainclient.TokenType(token)
	if !valid || typ == "bst" {
		return credential{method: method}, false
	}
	return credential{token: token, key: credentialKey(token), hint: token[:hintLength], method: method}, true
}

// challenge is the WWW-Authenticate value of a 401 for the path family (R-AUTH-002): Basic for the package
// clients, Bearer for a browser, which Fetch Metadata headers reveal (the choice of internal/auth, Q-AUTH-173).
func challenge(r *http.Request) string {
	if r.Header.Get("Sec-Fetch-Mode") != "" || r.Header.Get("Sec-Fetch-Dest") != "" {
		return `Bearer realm="trove"`
	}
	return `Basic realm="trove"`
}
