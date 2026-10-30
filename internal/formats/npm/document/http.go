package document

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
)

// Negotiate chooses the variant of a request (R-NPM-011): the abbreviated document
// when Accept gives application/vnd.npm.install-v1+json a higher quality than
// application/json, else the full one. A wildcard stands for application/json only:
// a client that accepts anything gets the full document.
func Negotiate(accept string) Variant {
	var abbreviated, plain, app, star float64 = -1, -1, -1, -1
	for _, part := range strings.Split(accept, ",") {
		media, q := splitMedia(part)
		switch media {
		case "application/vnd.npm.install-v1+json":
			abbreviated = max(abbreviated, q)
		case "application/json":
			plain = max(plain, q)
		case "application/*":
			app = max(app, q)
		case "*/*":
			star = max(star, q)
		}
	}
	if plain < 0 {
		plain = max(app, star)
	}
	plain = max(plain, 0)
	if abbreviated > plain {
		return Abbreviated
	}
	return Full
}

// splitMedia returns the lower-cased media type of an Accept member and its quality
// value, 1 when it has none.
func splitMedia(part string) (string, float64) {
	media, params, _ := strings.Cut(part, ";")
	q := 1.0
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				q = f
			}
		}
	}
	return strings.ToLower(strings.TrimSpace(media)), q
}

// AcceptsGzip reports whether Accept-Encoding allows gzip.
func AcceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		coding, q := splitMedia(part)
		if (coding == "gzip" || coding == "x-gzip") && q > 0 {
			return true
		}
	}
	return false
}

// Gzip compresses a rendering. The result is cached under the encoding, so a
// document is compressed once per revision and replica.
func Gzip(b []byte) []byte {
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	_, _ = zw.Write(b) // a buffer does not fail
	_ = zw.Close()
	return out.Bytes()
}

// Inputs are everything a rendering and its validator depend on (R-NPM-013). Every
// field is a value each replica reads from PostgreSQL or from the shared
// configuration; none is per replica, so every replica issues the same ETag for the
// same bytes.
type Inputs struct {
	Repo, Package, Rev int64
	// Variant is "full", "abbreviated" or "version/<ref>".
	Variant string
	// BaseURL is the response base URL: the rendered bytes hold absolute links.
	BaseURL string
	// Endpoint and Exposure are the endpoint of the request and its exposure
	// fingerprint (R-EP-018); Exposure is "" in mode all.
	Endpoint, Exposure string
	// Settings is the config_version of the serving repository and the byte-changing
	// keys of the npm section.
	Settings string
}

// ETag is the weak validator of a rendering: a hash of its inputs and
// [RenderVersion], computed without rendering.
func (in Inputs) ETag() string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	field("npm-document")
	for _, n := range []int64{in.Repo, in.Package, in.Rev, RenderVersion} {
		field(strconv.FormatInt(n, 10))
	}
	for _, s := range []string{in.Variant, in.BaseURL, in.Endpoint, in.Exposure, in.Settings} {
		field(s)
	}
	return `W/"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// IfNoneMatch reports whether an If-None-Match header holds the ETag, by the weak
// comparison of RFC 9110 §8.8.3.2. The wildcard never matches: a 200 costs bytes and
// never correctness.
func IfNoneMatch(header, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(part), "W/") == want {
			return true
		}
	}
	return false
}
