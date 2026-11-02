package jws

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The error classes. Every error of this package wraps exactly one of them and carries a fixed
// detail text, never token material.
var (
	// ErrMalformed: the token, its header or a decoded JSON object does not have the required shape.
	ErrMalformed = errors.New("jws: malformed")
	// ErrAlgorithm: the header names an algorithm this package never verifies (none, HMAC, unknown).
	ErrAlgorithm = errors.New("jws: algorithm not supported")
	// ErrKey: the key cannot verify the token's algorithm (type, curve, size or its own alg).
	ErrKey = errors.New("jws: key does not fit the algorithm")
	// ErrSignature: the signature does not verify with the key.
	ErrSignature = errors.New("jws: signature does not verify")
)

// jwsError is one error: its class and a fixed detail.
type jwsError struct {
	class  error
	detail string
}

func (e *jwsError) Error() string { return e.class.Error() + ": " + e.detail }
func (e *jwsError) Unwrap() error { return e.class }

// Detail returns the fixed detail text of an error of this package, "" for any other error.
func Detail(err error) string {
	var e *jwsError
	if errors.As(err, &e) {
		return e.detail
	}
	return ""
}

func errorOf(class error, detail string) error { return &jwsError{class: class, detail: detail} }

// MaxKIDLength is the longest `kid` a header may carry, in characters (R-CI-004 step 3).
const MaxKIDLength = 256

// maxNesting bounds the nesting of a decoded JSON value. A JOSE header or a claims set nests a
// few levels; a deeper one is malformed rather than a recursion the size limit alone would allow.
const maxNesting = 64

// Header is what the verifier reads of the protected header. Every other member is ignored, in
// particular `jku`, `x5u`, `jwk` and `x5c`: no key and no URL ever comes from a token.
type Header struct {
	// Alg is the `alg` member; Parse refuses a header without it.
	Alg string
	// Kid is the `kid` member, "" when absent; it is only a lookup key.
	Kid string
	// Typ is the `typ` member and HasTyp whether it is present (an empty string is present).
	Typ    string
	HasTyp bool
	// Cty is the `cty` member, "" when absent; "JWT" marks a nested JWT (RFC 7519 §5.2).
	Cty string
}

// Token is a compact JWS split and decoded, not yet verified.
type Token struct {
	Header Header
	// Payload is the decoded payload, not yet trusted.
	Payload []byte

	signingInput string
	signature    []byte
}

// Unsecured reports whether the token is an unsecured JWS: `alg` "none" in any spelling, or an
// empty signature (RFC 7519 §6). Such a token never verifies.
func (t *Token) Unsecured() bool {
	return strings.EqualFold(t.Header.Alg, "none") || len(t.signature) == 0
}

// decodePart decodes one part: base64url without padding and with zero trailing bits. The alphabet
// check comes first, because the standard library's decoder skips CR and LF, which a compact
// serialization never contains.
func decodePart(s string) ([]byte, bool) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return nil, false
		}
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return b, err == nil
}

// Parse splits and decodes a compact JWS (RFC 7515 §7.1): three base64url parts, a protected
// header that is a UTF-8 JSON object with unique member names, a string `alg`, a string `kid` of
// at most MaxKIDLength characters, string `typ` and `cty` members and no `crit` member. A value of
// five parts is a compact JWE and refused like any other count. Parse does not read the payload:
// [DecodeObject] does, under the caller's rules. An unsecured token parses; [Token.Unsecured]
// tells it, and [Token.VerifyWith] never accepts it.
func Parse(compact string) (*Token, error) {
	switch n := strings.Count(compact, ".") + 1; {
	case n == 5:
		return nil, errorOf(ErrMalformed, "five parts: a compact JWE, which is not supported")
	case n != 3:
		return nil, errorOf(ErrMalformed, strconv.Itoa(n)+" parts, a compact JWS has three")
	}
	first := strings.IndexByte(compact, '.')
	second := first + 1 + strings.IndexByte(compact[first+1:], '.')
	hb, ok := decodePart(compact[:first])
	if !ok {
		return nil, errorOf(ErrMalformed, "the header is not base64url")
	}
	payload, ok := decodePart(compact[first+1 : second])
	if !ok {
		return nil, errorOf(ErrMalformed, "the payload is not base64url")
	}
	sig, ok := decodePart(compact[second+1:])
	if !ok {
		return nil, errorOf(ErrMalformed, "the signature is not base64url")
	}
	h, err := parseHeader(hb)
	if err != nil {
		return nil, err
	}
	return &Token{Header: h, Payload: payload, signingInput: compact[:second], signature: sig}, nil
}

// parseHeader reads the members the verifier knows from a decoded header.
func parseHeader(b []byte) (Header, error) {
	m, err := DecodeObject(b)
	if err != nil {
		return Header{}, errorOf(ErrMalformed, "the header is not a UTF-8 JSON object with unique member names")
	}
	if _, has := m["crit"]; has {
		return Header{}, errorOf(ErrMalformed, "a crit header parameter is not supported")
	}
	var h Header
	alg, ok := m["alg"].(string)
	if !ok || alg == "" {
		return Header{}, errorOf(ErrMalformed, "the header has no alg string")
	}
	h.Alg = alg
	if v, has := m["kid"]; has {
		kid, ok := v.(string)
		if !ok {
			return Header{}, errorOf(ErrMalformed, "the kid header parameter is not a string")
		}
		if utf8.RuneCountInString(kid) > MaxKIDLength {
			return Header{}, errorOf(ErrMalformed, "the kid header parameter is longer than 256 characters")
		}
		h.Kid = kid
	}
	if v, has := m["typ"]; has {
		typ, ok := v.(string)
		if !ok {
			return Header{}, errorOf(ErrMalformed, "the typ header parameter is not a string")
		}
		h.Typ, h.HasTyp = typ, true
	}
	if v, has := m["cty"]; has {
		cty, ok := v.(string)
		if !ok {
			return Header{}, errorOf(ErrMalformed, "the cty header parameter is not a string")
		}
		h.Cty = cty
	}
	return h, nil
}

// DecodeObject decodes a JSON object under the rules of RFC 7515 §4 and RFC 7519 §4 that the
// standard library's decoder does not enforce: valid UTF-8, exactly one value, an object at the
// top, and no duplicate member names at any level (a parser that kept the first or the last of two
// would let a token mean two things). Numbers stay json.Number, so a large NumericDate is not
// rounded; nesting deeper than 64 levels is malformed.
func DecodeObject(b []byte) (map[string]any, error) {
	if !utf8.Valid(b) {
		return nil, errorOf(ErrMalformed, "not UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := decodeValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errorOf(ErrMalformed, "data after the JSON value")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errorOf(ErrMalformed, "not a JSON object")
	}
	return m, nil
}

func decodeValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxNesting {
		return nil, errorOf(ErrMalformed, "JSON nested deeper than 64 levels")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, errorOf(ErrMalformed, "not valid JSON")
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil // string, json.Number, bool or nil
	}
	switch d {
	case '{':
		m := map[string]any{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, errorOf(ErrMalformed, "not valid JSON")
			}
			k, _ := kt.(string)
			if _, dup := m[k]; dup {
				return nil, errorOf(ErrMalformed, "a duplicate member name")
			}
			v, err := decodeValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
		if _, err := dec.Token(); err != nil {
			return nil, errorOf(ErrMalformed, "not valid JSON")
		}
		return m, nil
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := decodeValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, errorOf(ErrMalformed, "not valid JSON")
		}
		return arr, nil
	}
	return nil, errorOf(ErrMalformed, "not valid JSON")
}
