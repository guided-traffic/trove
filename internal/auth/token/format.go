package token

import (
	"crypto/rand"
	"crypto/sha256"
	"hash/crc32"
	"math/big"
	"strings"
)

// The token format of R-AUTH-022:
//
//	random   = 32 bytes from crypto/rand
//	body     = prefix + base62(random as unsigned big-endian integer, 43 digits)
//	checksum = base62(CRC-32/IEEE(body as ASCII), 6 digits)
//	token    = body + checksum                          // always 57 characters
const (
	// Length is the length of every token.
	Length         = 57
	randomDigits   = 43
	checksumDigits = 6
	prefixLength   = 8 // "trv_pat_"
	base62         = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	hintRandom     = 4
)

// Prefix returns the prefix of a token type, or "" for an unknown type.
func Prefix(t Type) string {
	switch t {
	case TypePAT, TypeSAT, TypeCIT:
		return "trv_" + string(t) + "_"
	}
	return ""
}

// encode writes v as exactly width base62 digits, most significant first,
// left-padded with '0'.
func encode(v *big.Int, width int) string {
	out := make([]byte, width)
	for i := range out {
		out[i] = base62[0]
	}
	n := new(big.Int).Set(v)
	m := new(big.Int)
	sixtyTwo := big.NewInt(62)
	for i := width - 1; i >= 0 && n.Sign() > 0; i-- {
		n.DivMod(n, sixtyTwo, m)
		out[i] = base62[m.Int64()]
	}
	return string(out)
}

// FromRandom builds the token of a type from 32 random bytes. It is exported for
// the format test vector; Generate is the only source of production secrets.
func FromRandom(t Type, random [32]byte) string {
	body := Prefix(t) + encode(new(big.Int).SetBytes(random[:]), randomDigits)
	sum := crc32.ChecksumIEEE([]byte(body))
	return body + encode(new(big.Int).SetUint64(uint64(sum)), checksumDigits)
}

// Generate creates a token of the type from crypto/rand. The value exists in
// memory only until the response that creates it is written (R-AUTH-022).
func Generate(t Type) (string, error) {
	if Prefix(t) == "" {
		return "", ErrMalformed
	}
	var r [32]byte
	if _, err := rand.Read(r[:]); err != nil {
		return "", err
	}
	return FromRandom(t, r), nil
}

// Check is the syntax step of the validation (R-AUTH-023 step 1): a known
// prefix, the length, the alphabet and the checksum. It needs no database and
// returns the type of the prefix, or [ErrMalformed].
func Check(raw string) (Type, error) {
	if len(raw) != Length || !strings.HasPrefix(raw, "trv_") {
		return "", ErrMalformed
	}
	t := Type(raw[4:7])
	if Prefix(t) == "" || raw[7] != '_' {
		return "", ErrMalformed
	}
	for i := prefixLength; i < Length; i++ {
		if !isBase62(raw[i]) {
			return "", ErrMalformed
		}
	}
	body := raw[:Length-checksumDigits]
	want := encode(new(big.Int).SetUint64(uint64(crc32.ChecksumIEEE([]byte(body)))), checksumDigits)
	if raw[Length-checksumDigits:] != want {
		return "", ErrMalformed
	}
	return t, nil
}

func isBase62(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// Hash is the SHA-256 of the full 57-character string, the only form in which
// the database knows a token (R-AUTH-023).
func Hash(raw string) [32]byte { return sha256.Sum256([]byte(raw)) }

// Hint is the public hint of a token: the prefix plus the first four characters
// of the random part (R-AUTH-022).
func Hint(raw string) string {
	if len(raw) < prefixLength+hintRandom {
		return ""
	}
	return raw[:prefixLength+hintRandom]
}
