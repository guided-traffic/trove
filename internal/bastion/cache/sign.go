package cache

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Domain separates what a signature covers, so a record signed as one thing is never accepted as another
// (R-BAS-032): an entry cannot be replayed as an answer, nor an answer for another kind.
type Domain string

// The domains of signed records.
const (
	DomainEntry    Domain = "entry"
	DomainNegative Domain = "negative"
	DomainAnswer   Domain = "answer"
)

// MinKeyBytes is the shortest signing key accepted: the 32 random bytes of secrets.signingKey (R-BAS-032).
const MinKeyBytes = 32

// Signer signs and verifies records with HMAC-SHA-256 (R-BAS-032, Q-BAS-32). A record is the JSON payload, a
// separator and the hex MAC over the domain, the cache key and the payload. A holder of write access to the
// bucket or Valkey without the key can delete records and cannot make the Bastion serve other bytes or accept
// a forged answer (H-387).
type Signer struct{ key []byte }

// NewSigner returns a signer for a key of at least [MinKeyBytes] bytes.
func NewSigner(key []byte) (*Signer, error) {
	if len(key) < MinKeyBytes {
		return nil, fmt.Errorf("cache: the signing key has %d bytes, at least %d are required", len(key), MinKeyBytes)
	}
	return &Signer{key: append([]byte(nil), key...)}, nil
}

const separator = "\n"

func (s *Signer) mac(d Domain, key string, payload []byte) string {
	h := hmac.New(sha256.New, s.key)
	for _, part := range [][]byte{[]byte(d), {0}, []byte(key), {0}, payload} {
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Sign returns the record for payload under the cache key (or answer key) of the domain.
func (s *Signer) Sign(d Domain, key string, payload []byte) []byte {
	return append(append(append([]byte(nil), payload...), separator...), s.mac(d, key, payload)...)
}

// Verify checks a record and returns its payload, or [ErrBadSignature]. The comparison is constant-time.
func (s *Signer) Verify(d Domain, key string, record []byte) ([]byte, error) {
	i := strings.LastIndex(string(record), separator)
	if i < 0 {
		return nil, ErrBadSignature
	}
	payload, got := record[:i], string(record[i+len(separator):])
	if !hmac.Equal([]byte(got), []byte(s.mac(d, key, payload))) {
		return nil, ErrBadSignature
	}
	return payload, nil
}

// SignEntry marshals and signs an entry under its cache key. A nil signer (the configuration allows no key with
// the volume backend and no Valkey, Q-BAS-98) stores the plain JSON.
func (s *Signer) SignEntry(cacheKey string, e Entry) ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return b, nil
	}
	return s.Sign(DomainEntry, cacheKey, b), nil
}

// VerifyEntry verifies and unmarshals an entry record read under cacheKey. A record that fails its signature,
// or that does not decode, is [ErrBadSignature]: the caller deletes it and counts the metric.
func (s *Signer) VerifyEntry(cacheKey string, record []byte) (Entry, error) {
	payload := record
	if s != nil {
		var err error
		if payload, err = s.Verify(DomainEntry, cacheKey, record); err != nil {
			return Entry{}, err
		}
	}
	var e Entry
	if err := json.Unmarshal(payload, &e); err != nil {
		return Entry{}, errors.Join(ErrBadSignature, err)
	}
	return e, nil
}

// KeyHash is the name of an entry under entries/: the SHA-256 of the cache key in hex.
func KeyHash(cacheKey string) string {
	sum := sha256.Sum256([]byte(cacheKey))
	return hex.EncodeToString(sum[:])
}
