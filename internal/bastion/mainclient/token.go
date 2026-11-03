package mainclient

import (
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"strings"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// The token format of R-AUTH-022 as far as the Bastion checks it (R-BAS-006): "trv_bst_", 43 base62 digits of
// the random part and 6 base62 digits of CRC-32/IEEE over everything before them, 57 characters in all. The
// Bastion links none of the main installation's token code (R-BAS-001), so the check is written here and
// TestR_BAS_006_TheSyntaxCheckAgreesWithTheMainInstallationsFormat holds it equal to internal/auth/token.
const (
	tokenLength    = 57
	prefixLength   = 8
	checksumDigits = 6
	base62         = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	hintLength     = 12
)

// ErrTokenMalformed is the error of a token file whose content is not a Bastion token: the wrong prefix, the
// wrong length, a character outside base62, or a failed checksum. It names no part of the value.
var ErrTokenMalformed = errors.New("mainclient: the token is not a well-formed Bastion token (trv_bst_, 57 characters, checksum)")

// TokenType returns the type ("pat", "sat", "cit" or "bst") of a syntactically valid trove token string:
// the prefix, the length, the alphabet and the checksum hold (R-AUTH-022). It is the one syntax check of the
// Bastion: the request flow refuses a credential that fails it before any request (R-BAS-019), and
// [ParseToken] reads the token file with it.
func TokenType(raw string) (string, bool) {
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

// ParseToken checks the syntax and the checksum of a Bastion token (R-BAS-006) and returns it with its hint.
func ParseToken(raw string) (Token, error) {
	if typ, ok := TokenType(raw); !ok || typ != "bst" || !strings.HasPrefix(raw, iface.TokenPrefix) {
		return Token{}, ErrTokenMalformed
	}
	return Token{Value: raw, Hint: raw[:hintLength]}, nil
}

// base62Digits writes v as exactly width base62 digits, most significant first, left-padded with '0'.
func base62Digits(v uint64, width int) string {
	out := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		out[i] = base62[v%62]
		v /= 62
	}
	return string(out)
}

// ReadTokenFile reads a token file as the chart mounts it: the token, with the trailing newline of an editor
// or a Secret tool removed. Any other white space is part of the content and fails the syntax check. The error
// never contains the content.
func ReadTokenFile(path string) (Token, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is bastion.main.token_file, set by the chart
	if err != nil {
		return Token{}, fmt.Errorf("mainclient: cannot read the token file: %w", err)
	}
	return ParseToken(strings.TrimRight(string(b), "\r\n"))
}

// Holder is the token in use: a [TokenSource] whose value the token reloader replaces atomically (R-BAS-006).
// A request in flight keeps the token it took; the next request takes the new one.
type Holder struct{ v atomic.Pointer[Token] }

var _ TokenSource = (*Holder)(nil)

// NewHolder returns a holder with tok in use.
func NewHolder(tok Token) *Holder {
	h := &Holder{}
	h.Set(tok)
	return h
}

// Set replaces the token in use.
func (h *Holder) Set(tok Token) { h.v.Store(&tok) }

// Token implements [TokenSource]; it is the zero Token before the first Set.
func (h *Holder) Token() Token {
	if t := h.v.Load(); t != nil {
		return *t
	}
	return Token{}
}
