package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// issuerRecord is the discovery document and the key set as the instance serves them.
type issuerRecord struct {
	Discovery         map[string]any `json:"discovery"`
	DiscoveryResponse response       `json:"discovery_response"`
	JWKSResponse      response       `json:"jwks_response"`
	Keys              []keyShape     `json:"jwks_keys"`
	Signature         *signature     `json:"signature"`

	jwks []map[string]string
	kids map[string]int // kid → index in the key set
}

type response struct {
	ContentType  string `json:"content_type"`
	CacheControl string `json:"cache_control"`
	// ETag is "weak", "strong" or "none"; IfNoneMatch the status of a request that sends it back.
	ETag        string `json:"etag"`
	IfNoneMatch int    `json:"if_none_match_status,omitempty"`
}

// keyShape is a JWK without its key material.
type keyShape struct {
	Members         []string `json:"members"`
	KTY             string   `json:"kty"`
	Alg             string   `json:"alg,omitempty"`
	Use             string   `json:"use,omitempty"`
	E               string   `json:"e,omitempty"`
	ModulusBits     int      `json:"modulus_bits,omitempty"`
	KIDLength       int      `json:"kid_length"`
	KIDIsThumbprint bool     `json:"kid_is_rfc7638_thumbprint"`
	SignsIDTokens   bool     `json:"signs_id_tokens"`
}

// signature is the result of verifying one real ID token against the key set.
type signature struct {
	Alg      string `json:"alg"`
	KeyIndex int    `json:"key_index"`
	Verified bool   `json:"verified_rsassa_pkcs1_v1_5_sha256"`
}

func etagKind(v string) string {
	switch {
	case v == "":
		return "none"
	case strings.HasPrefix(v, "W/"):
		return "weak"
	}
	return "strong"
}

func recordIssuer(ctx context.Context, c *client) (*issuerRecord, error) {
	rec := &issuerRecord{kids: map[string]int{}}
	disc, err := c.get(ctx, c.base+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	if disc.status != http.StatusOK {
		return nil, fmt.Errorf("discovery: HTTP %d", disc.status)
	}
	dec := json.NewDecoder(bytes.NewReader(disc.body))
	dec.UseNumber()
	if err := dec.Decode(&rec.Discovery); err != nil {
		return nil, err
	}
	rec.DiscoveryResponse = response{
		ContentType: disc.header.Get("Content-Type"), CacheControl: disc.header.Get("Cache-Control"), ETag: etagKind(disc.header.Get("ETag")),
	}
	jwksURI, _ := rec.Discovery["jwks_uri"].(string)
	if !strings.HasPrefix(jwksURI, c.base+"/") {
		return nil, fmt.Errorf("jwks_uri %q is not below %s", jwksURI, c.base)
	}
	keys, err := c.get(ctx, jwksURI, nil)
	if err != nil {
		return nil, err
	}
	if keys.status != http.StatusOK {
		return nil, fmt.Errorf("key set: HTTP %d", keys.status)
	}
	etag := keys.header.Get("ETag")
	rec.JWKSResponse = response{ContentType: keys.header.Get("Content-Type"), CacheControl: keys.header.Get("Cache-Control"), ETag: etagKind(etag)}
	if etag != "" {
		again, err := c.get(ctx, jwksURI, http.Header{"If-None-Match": {etag}})
		if err != nil {
			return nil, err
		}
		rec.JWKSResponse.IfNoneMatch = again.status
	}
	var set struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(keys.body, &set); err != nil {
		return nil, err
	}
	for i, k := range set.Keys {
		flat := map[string]string{}
		shape := keyShape{}
		for m, v := range k {
			shape.Members = append(shape.Members, m)
			if s, ok := v.(string); ok {
				flat[m] = s
			}
		}
		sort.Strings(shape.Members)
		shape.KTY, shape.Alg, shape.Use, shape.E = flat["kty"], flat["alg"], flat["use"], flat["e"]
		shape.KIDLength = len(flat["kid"])
		if shape.KTY == "RSA" {
			n, err := base64.RawURLEncoding.DecodeString(flat["n"])
			if err != nil {
				return nil, fmt.Errorf("key %d: n: %w", i, err)
			}
			shape.ModulusBits = new(big.Int).SetBytes(n).BitLen()
			shape.KIDIsThumbprint = flat["kid"] == rsaThumbprint(flat["e"], flat["n"])
		}
		rec.Keys = append(rec.Keys, shape)
		rec.jwks = append(rec.jwks, flat)
		rec.kids[flat["kid"]] = i
	}
	return rec, nil
}

// rsaThumbprint is the RFC 7638 thumbprint of an RSA JWK: SHA-256 over {"e","kty","n"} in
// lexicographic order without whitespace.
func rsaThumbprint(e, n string) string {
	sum := sha256.Sum256([]byte(`{"e":"` + e + `","kty":"RSA","n":"` + n + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// checkSignature verifies the token that the job signature-check left as its artifact against the
// key set, in memory, deletes the artifact and marks which key signs ID tokens. The token is never
// written anywhere.
func checkSignature(ctx context.Context, c *client, pipes map[string]*pipe, logs map[string]map[string]*jobLog, rec *issuerRecord) (*signature, error) {
	j := logs["main"]["signature-check"]
	if j == nil {
		return nil, errors.New("the main pipeline has no job signature-check")
	}
	if j.status != "success" {
		return nil, fmt.Errorf("job signature-check: %s", j.status)
	}
	jobPath := projectPath(pipes["main"].projectID) + "/jobs/" + strconv.FormatInt(j.jobID, 10)
	raw, err := c.raw(ctx, jobPath+"/artifacts/"+signatureArtifact)
	if err != nil {
		return nil, err
	}
	defer func() {
		clear(raw)
		if err := c.call(ctx, http.MethodDelete, jobPath+"/artifacts", nil, nil); err != nil {
			logf("WARNING: deleting the artifact of job %d failed (%v); it expires within the hour", j.jobID, err)
		}
	}()
	sig, err := verifyRS256(strings.TrimSpace(string(raw)), rec)
	if err != nil {
		return nil, err
	}
	rec.Keys[sig.KeyIndex].SignsIDTokens = true
	return sig, nil
}

func verifyRS256(tok string, rec *issuerRecord) (*signature, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("the token has %d parts", len(parts))
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var h struct {
		Alg string `json:"alg"`
		KID string `json:"kid"`
	}
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, err
	}
	if h.Alg != "RS256" {
		return nil, fmt.Errorf("the token is signed %s; this check verifies RS256 only", h.Alg)
	}
	idx, ok := rec.kids[h.KID]
	if !ok {
		return nil, errors.New("the token's kid is not in the key set")
	}
	k := rec.jwks[idx]
	n, err := base64.RawURLEncoding.DecodeString(k["n"])
	if err != nil {
		return nil, err
	}
	e, err := base64.RawURLEncoding.DecodeString(k["e"])
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, fmt.Errorf("the signature does not verify with key %d: %w", idx, err)
	}
	return &signature{Alg: h.Alg, KeyIndex: idx, Verified: true}, nil
}
