// Package document holds the rendering helpers every rendered document of the NuGet
// format shares (R-NUGET-002, R-NUGET-030): deterministic JSON, the fixed timestamp, the
// gzip variant, the one weak validator that gzip and identity share (Q-NUGET-9) and the
// response of a document, with the HEAD rule.
//
// A document is a function of the rows it renders, the base URL, the hive, the content
// coding and the release (R-NUGET-030): nothing replica-specific, no clock reading, no map
// order enters its bytes. The renderers write structs, whose members have a fixed order,
// never maps; [Marshal] writes them without the escaping of HTML characters, which would
// only make descriptions harder to read.
package document

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ContentType is the Content-Type of every JSON document of the protocol (R-NUGET-002).
const ContentType = "application/json; charset=utf-8"

// Marshal renders v as compact JSON, the form every document is stored and sent in. Struct
// members come in declaration order and map keys sorted, so the same value always gives
// the same bytes. HTML characters are not escaped.
func Marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// TimestampLayout is the one form of every instant a document shows: UTC, whole seconds,
// "Z". PostgreSQL keeps microseconds; a document shows what a client uses, and the same
// instant is the same text on every replica (R-NUGET-030, Q-NUGET-41).
const TimestampLayout = "2006-01-02T15:04:05Z"

// Timestamp formats an instant in [TimestampLayout]; the fraction of a second is cut, not
// rounded, so a time never reads later than it was.
func Timestamp(t time.Time) string { return t.UTC().Truncate(time.Second).Format(TimestampLayout) }

// versionList is the version list of the flat container (R-NUGET-008).
type versionList struct {
	Versions []string `json:"versions"`
}

// VersionList renders the version list of the flat container: the versions in URL form, in
// the order given (ascending by precedence). An empty list renders as [].
func VersionList(versions []string) ([]byte, error) {
	if versions == nil {
		versions = []string{}
	}
	return Marshal(versionList{Versions: versions})
}

// Gzip compresses a rendering. The writer carries no name and no modification time, so the
// bytes depend on the input alone.
func Gzip(b []byte) []byte {
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	_, _ = zw.Write(b) // a buffer does not fail
	_ = zw.Close()
	return out.Bytes()
}

// AcceptsGzip reports whether an Accept-Encoding header allows gzip: the coding or its
// alias is listed with a quality above zero. A wildcard is not read as consent, because a
// client that accepts anything gets the identity coding, which every client reads.
func AcceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(part, ";")
		coding = strings.ToLower(strings.TrimSpace(coding))
		if coding != "gzip" && coding != "x-gzip" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		if q > 0 {
			return true
		}
	}
	return false
}

// Inputs are everything a rendering and its validator depend on (R-NUGET-002). Every field
// is a value each replica reads from PostgreSQL or from the shared configuration; none is
// per replica, so every replica of a release issues the same ETag for the same bytes, and
// the replicas of two releases the same one while RenderVersion is the same. The content
// coding is not an input: gzip and identity share the value.
type Inputs struct {
	// RenderVersion is the version of the rendering of the NuGet format.
	RenderVersion int
	// Repo and Package are the ids of the repository and of the package row.
	Repo, Package int64
	// Rev is packages.rev_seq of the package.
	Rev int64
	// Variant tells the documents of one package apart: "flat", or the hive with the
	// document, "registration-gz/page/1.0.0/2.0.0".
	Variant string
	// Serving is the key the document is served under; the URLs it holds carry it.
	Serving string
	// BaseURL is the response base URL: the rendered bytes hold absolute links.
	BaseURL string
	// Endpoint and Exposure are the endpoint of the request and its exposure fingerprint
	// (R-NUGET-034); Exposure is "" in mode all.
	Endpoint, Exposure string
	// Settings is the config_version of the serving repository and the keys of the
	// nuget section that change bytes (the paging of the registrations).
	Settings string
	// Content is the [ContentDigest] of the identity rendering, set on an endpoint with
	// exposure rules in place of Rev (and of the merge fingerprint in a virtual document's
	// Variant), which moves with versions the endpoint hides (Q-NUGET-87). Empty, it is left
	// out of the value, so the validator of every other document stays what it was.
	Content string
}

// ETag is the weak validator of a rendering: a hash of its inputs and the render version,
// computed without rendering (Q-NUGET-9).
func (in Inputs) ETag() string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	field("nuget-document")
	for _, n := range []int64{int64(in.RenderVersion), in.Repo, in.Package, in.Rev} {
		field(strconv.FormatInt(n, 10))
	}
	for _, s := range []string{in.Variant, in.Serving, in.BaseURL, in.Endpoint, in.Exposure, in.Settings} {
		field(s)
	}
	if in.Content != "" {
		field("content")
		field(in.Content)
	}
	return `W/"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// ContentDigest is the SHA-256 of a rendering in hex, the [Inputs.Content] of a document
// whose validator follows its bytes. A rendering is a function of its inputs alone
// (R-NUGET-030), so every replica computes the same value for the same document.
func ContentDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// IfNoneMatch reports whether an If-None-Match header holds the ETag, by the weak
// comparison of RFC 9110 section 8.8.3.2. The wildcard never matches: a 200 costs bytes and
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

// Reply is a document ready to send.
type Reply struct {
	// ETag is the weak validator.
	ETag string
	// Body is the variant to send: the gzip bytes when Gzip is set, else the JSON.
	Body []byte
	Gzip bool
	// Vary says the document has a gzip variant, so the answer varies with
	// Accept-Encoding whichever form it takes.
	Vary bool
}

// NotModified answers a conditional request whose validator matches: 304 with the headers
// that identify the representation.
func NotModified(w http.ResponseWriter, etag string, vary bool) {
	h := w.Header()
	h.Set("ETag", etag)
	if vary {
		h.Set("Vary", "Accept-Encoding")
	}
	w.WriteHeader(http.StatusNotModified)
}

// Write sends a document: the headers, then the body unless the request is a HEAD. The
// Content-Length is the length of the variant sent, so a HEAD tells what the GET carries
// (R-NUGET-002).
func (r Reply) Write(w http.ResponseWriter, method string) error {
	h := w.Header()
	h.Set("ETag", r.ETag)
	if r.Vary {
		h.Set("Vary", "Accept-Encoding")
	}
	h.Set("Content-Type", ContentType)
	if r.Gzip {
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(r.Body)))
	w.WriteHeader(http.StatusOK)
	if method == http.MethodHead {
		return nil
	}
	_, err := w.Write(r.Body)
	return err
}
