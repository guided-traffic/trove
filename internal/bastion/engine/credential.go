package engine

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash/crc32"
	"net/http"
	"strings"
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

// The token format of R-AUTH-022 as far as the Bastion checks it: the Bastion links none of the main
// installation's token code (R-BAS-001), so the syntax is written here; TestR_BAS_019_TheSyntaxCheckAgreesWithTheMainInstallationsFormat
// holds it equal to internal/auth/token.
const (
	tokenLength    = 57
	prefixLength   = 8
	checksumDigits = 6
	base62         = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	hintLength     = 12
)

// tokenType returns the type of a syntactically valid token string ("pat", "sat", "cit", "bst") and whether the
// prefix, length, alphabet and checksum hold.
func tokenType(raw string) (string, bool) {
	if len(raw) != tokenLength || !strings.HasPrefix(raw, "trv_") || raw[7] != '_' {
		return "", false
	}
	typ := raw[4:7]
	switch typ {
	case "pat", "sat", "cit", "bst":
	default:
		return "", false
	}
	for i := prefixLength; i < tokenLength; i++ {
		if strings.IndexByte(base62, raw[i]) < 0 {
			return "", false
		}
	}
	if raw[tokenLength-checksumDigits:] != base62Digits(uint64(crc32.ChecksumIEEE([]byte(raw[:tokenLength-checksumDigits]))), checksumDigits) {
		return "", false
	}
	return typ, true
}

func base62Digits(v uint64, width int) string {
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = base62[v%62]
		v /= 62
	}
	return string(out)
}

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
	typ, valid := tokenType(token)
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
