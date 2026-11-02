package ci

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/guided-traffic/trove/internal/auth/jws"
	"github.com/guided-traffic/trove/internal/ci/subject"
)

// The stored form of a key set (R-CI-003 "Data model": the last good key set, public keys only) and its reading.
// The key rules of R-CI-003 are applied by one implementation, jws.ParseJWKS of the leaf internal/auth/jws, which
// the login's verifier shares (Q-CI-19, Q-CI-42): at the fetch on the issuer's own document, and again on the
// stored form when a replica reads the row. The key status (Infos) and the keys the validation tries (find, with
// jws.Candidates) both come from that one parse, so they cannot disagree.
//
// A fetched JWK Set is reduced before it is stored: a usable key keeps the public members the rules and the
// verification read, a key the rules ignore keeps its kid, kty, alg and the reason. A private member that a
// hostile or careless issuer publishes therefore never reaches the table, and two fetches of the same keys
// compare equal whatever the issuer's member order or extra members are.

// maxStoredKeys bounds the keys kept of one fetched set. The response is bounded in bytes already
// (ci.signing_keys.max_response_bytes); this bounds the stored row and the work per token.
const maxStoredKeys = 100

// jwkDoc is the stored form of one key: the public members that the key rules and the verification read, or, for
// a key the rules ignore, Ignored with the reason of jws.ParseJWKS and no key material. KeyOps is a pointer so that
// an absent member and an empty list differ: an empty `key_ops` does not contain `verify`.
type jwkDoc struct {
	KTY     string    `json:"kty,omitempty"`
	KID     string    `json:"kid,omitempty"`
	Use     string    `json:"use,omitempty"`
	KeyOps  *[]string `json:"key_ops,omitempty"`
	Alg     string    `json:"alg,omitempty"`
	N       string    `json:"n,omitempty"`
	E       string    `json:"e,omitempty"`
	Crv     string    `json:"crv,omitempty"`
	X       string    `json:"x,omitempty"`
	Y       string    `json:"y,omitempty"`
	Ignored string    `json:"ignored,omitempty"`
}

// storedSet is the stored JSON of a key set.
type storedSet struct {
	Keys []jwkDoc `json:"keys"`
}

var errNotAKeySet = errors.New("the document is not a JWK Set")

// parseOne applies the key rules to one JWK entry.
func parseOne(raw []byte) (*jws.JWK, jws.IgnoredKey) {
	set, err := jws.ParseJWKS(append(append([]byte(`{"keys":[`), raw...), ']', '}'))
	switch {
	case err != nil:
		return nil, jws.IgnoredKey{Reason: jws.IgnoredInvalid}
	case len(set.Keys) == 1:
		return set.Keys[0], jws.IgnoredKey{}
	case len(set.Ignored) == 1:
		return nil, set.Ignored[0]
	}
	return nil, jws.IgnoredKey{Reason: jws.IgnoredInvalid}
}

// reduceJWKS decodes a fetched JWK Set document (RFC 7517 §5) and returns the stored form, at most maxStoredKeys
// keys: the key rules decide each entry, a usable key keeps its whitelisted public members, an ignored one its
// identification and the reason. The error never carries bytes of the document.
func reduceJWKS(body []byte) ([]byte, error) {
	var top struct {
		Keys *[]json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &top); err != nil || top.Keys == nil {
		return nil, errNotAKeySet
	}
	out := storedSet{Keys: make([]jwkDoc, 0, min(len(*top.Keys), maxStoredKeys))}
	for _, raw := range *top.Keys {
		if len(out.Keys) >= maxStoredKeys {
			break
		}
		if key, ign := parseOne(raw); key == nil {
			out.Keys = append(out.Keys, jwkDoc{KID: ign.KID, KTY: ign.KTY, Alg: ign.Alg, Ignored: ign.Reason})
			continue
		}
		out.Keys = append(out.Keys, publicMembers(raw))
	}
	return json.Marshal(out)
}

// publicMembers is the stored form of a usable key: the members of the whitelist, read as strings (a usable key's
// members have the right types, jws.ParseJWKS checked them).
func publicMembers(raw json.RawMessage) jwkDoc {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	str := func(name string) string {
		var v string
		if json.Unmarshal(m[name], &v) != nil {
			return ""
		}
		return v
	}
	k := jwkDoc{
		KTY: str("kty"), KID: str("kid"), Use: str("use"), Alg: str("alg"),
		N: str("n"), E: str("e"), Crv: str("crv"), X: str("x"), Y: str("y"),
	}
	if ops, ok := m["key_ops"]; ok {
		var list []string
		_ = json.Unmarshal(ops, &list)
		if list == nil {
			list = []string{}
		}
		k.KeyOps = &list
	}
	return k
}

// keySet is a stored key set read under the key rules: the status of every key, the usable keys as the
// validation's subject.Key and as jws keys for the candidate selection.
type keySet struct {
	Infos  []KeyInfo
	Usable []subject.Key
	keys   []*jws.JWK
}

// parseStored reads a stored set: an entry recorded as ignored keeps its reason, every other entry goes through
// jws.ParseJWKS again (a set stored before this rule, or one that a later rule refuses, is judged by the rules of
// this code). The rules that depend on the token (the key's own alg, the key type and curve fitting the algorithm)
// are applied by the candidate selection and by subject.Unverified.Verify.
func parseStored(stored []byte) (keySet, error) {
	var set storedSet
	if err := json.Unmarshal(stored, &set); err != nil {
		return keySet{}, fmt.Errorf("stored key set: %w", err)
	}
	out := keySet{Infos: make([]KeyInfo, 0, len(set.Keys))}
	for _, doc := range set.Keys {
		if doc.Ignored != "" {
			out.Infos = append(out.Infos, KeyInfo{KID: doc.KID, KTY: doc.KTY, Alg: doc.Alg, Ignored: true, IgnoredReason: doc.Ignored})
			continue
		}
		raw, _ := json.Marshal(doc)
		key, ign := parseOne(raw)
		if key == nil {
			out.Infos = append(out.Infos, KeyInfo{KID: doc.KID, KTY: doc.KTY, Alg: doc.Alg, Ignored: true, IgnoredReason: ign.Reason})
			continue
		}
		out.Infos = append(out.Infos, KeyInfo{KID: key.KID, KTY: key.KTY, Alg: key.Alg})
		out.Usable = append(out.Usable, subject.Key{KID: key.KID, KTY: key.KTY, Alg: key.Alg, Public: key.Public})
		out.keys = append(out.keys, key)
	}
	return out, nil
}

// kids lists the key IDs of the usable keys, for the audit event of a fetch (at most the stored keys).
func (s keySet) kids() []string {
	out := make([]string, 0, len(s.Usable))
	for _, k := range s.Usable {
		out = append(out, k.KID)
	}
	return out
}

// find returns the keys that may verify a token, the selection of jws.Candidates: with a kid, the usable keys of
// that kid that fit alg; without one, every usable key that fits alg, at most subject.MaxKeysWithoutKID. A key that
// does not fit is not a candidate, so a token whose kid names a key of another type counts as an unknown kid and
// gets the re-read and refetch of R-CI-003.
func (s keySet) find(kid, alg string) []subject.Key {
	var out []subject.Key
	for _, k := range jws.Candidates(s.keys, kid, alg, subject.MaxKeysWithoutKID) {
		out = append(out, subject.Key{KID: k.KID, KTY: k.KTY, Alg: k.Alg, Public: k.Public})
	}
	return out
}
