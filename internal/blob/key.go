package blob

import (
	"encoding/hex"
	"strings"
)

// Digest is the SHA-256 that identifies a blob.
type Digest [32]byte

// ParseDigest reads 64 lowercase hexadecimal characters.
func ParseDigest(s string) (Digest, bool) {
	var d Digest
	if len(s) != 64 || s != strings.ToLower(s) {
		return d, false
	}
	if _, err := hex.Decode(d[:], []byte(s)); err != nil {
		return Digest{}, false
	}
	return d, true
}

// Hex is the lowercase hexadecimal form.
func (d Digest) Hex() string { return hex.EncodeToString(d[:]) }

// String is Hex.
func (d Digest) String() string { return d.Hex() }

// Bytes returns the digest as a slice, the form PostgreSQL columns take.
func (d Digest) Bytes() []byte { return d[:] }

// Key returns the object key of a blob: <prefix>/sha256/<h0h1>/<h2h3>/<hex>
// (R-STOR-001). With an empty prefix the key starts at sha256/. The key
// depends on the digest alone: no repository, path or user input reaches it.
func Key(prefix string, d Digest) string {
	h := d.Hex()
	k := "sha256/" + h[0:2] + "/" + h[2:4] + "/" + h
	if prefix == "" {
		return k
	}
	return prefix + "/" + k
}

// KeyPrefix is the key prefix under which all blobs live.
func KeyPrefix(prefix string) string {
	if prefix == "" {
		return "sha256/"
	}
	return prefix + "/sha256/"
}

// ParseKey is the inverse of Key. It reports false for any key that does not
// follow the layout exactly, which the orphan sweep reports as foreign.
func ParseKey(prefix, key string) (Digest, bool) {
	rest, ok := strings.CutPrefix(key, KeyPrefix(prefix))
	if !ok || len(rest) != 2+1+2+1+64 || rest[2] != '/' || rest[5] != '/' {
		return Digest{}, false
	}
	d, ok := ParseDigest(rest[6:])
	if !ok || rest[0:2] != d.Hex()[0:2] || rest[3:5] != d.Hex()[2:4] {
		return Digest{}, false
	}
	return d, true
}

// Digests is the digest set of one blob (R-STOR-002), computed in one pass.
type Digests struct {
	SHA256 Digest
	SHA1   [20]byte
	MD5    [16]byte
	SHA512 [64]byte
	Size   int64
}
