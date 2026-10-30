// Package remote holds what the npm format does with the package documents of an
// upstream registry (R-NPM-024 to R-NPM-027): the checks a fetched document passes
// before it is stored, the facts recorded per version, the rewriting of tarball URLs
// onto the serving repository, the abbreviated and single-version forms derived from
// the stored full document, and the upstream request URLs.
//
// The package is a leaf: it works on bytes and plain values, with the standard
// library, errs, name, semver and rows. The adapter that binds it to the remote
// engine (the registration of the format's remote class) is in the package above.
package remote

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// invalid is the 502 npm_invalid_upstream_document of R-NPM-024.
func invalid(format string, a ...any) error {
	return errs.E(errs.UpstreamFailed, errs.CodeNPMInvalidUpstreamDocument, "The upstream package document is not valid: "+fmt.Sprintf(format, a...))
}

// Doc is what a document that passed the checks yields: the facts recorded per
// version and the time.modified the stale-copy rule compares.
type Doc struct {
	Name string
	// Modified is time.modified, zero when the document has none or none that parses.
	Modified time.Time
	// Versions are the facts of every version the document lists, in the order of
	// their version numbers as text, so that two fills of one document record alike.
	Versions []rows.RemoteVersion
	// Tarballs maps a version to the tarball URL the document names for it.
	Tarballs map[string]string
}

// versionEntry is the part of a version entry the checks read; every other member is
// skipped by the decoder without being kept.
type versionEntry struct {
	Name    json.RawMessage `json:"name"`
	Version json.RawMessage `json:"version"`
	Dist    json.RawMessage `json:"dist"`
}

type distEntry struct {
	Tarball   json.RawMessage `json:"tarball"`
	Integrity json.RawMessage `json:"integrity"`
	Shasum    json.RawMessage `json:"shasum"`
}

// Check validates the document r holds against the package name that was requested
// and reads the facts of R-NPM-024. It reads the document once as a stream and keeps
// only the facts; an error is the 502 npm_invalid_upstream_document and the previous
// entry stays.
//
// The checks: valid JSON, a top-level object whose name equals the requested name,
// every versions[v].version equal to v and every versions[v].name equal to the name,
// every dist.tarball an absolute http or https URL, every dist.integrity a
// subresource integrity string.
func Check(r io.Reader, name string) (*Doc, error) {
	dec := json.NewDecoder(r)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, invalid("it is not a JSON object")
	}
	doc := &Doc{Tarballs: map[string]string{}}
	var gotName bool
	var timeRaw json.RawMessage
	var versions []rows.RemoteVersion
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, invalid("it is not valid JSON")
		}
		key, _ := keyTok.(string)
		switch key {
		case "name":
			var s json.RawMessage
			if err := dec.Decode(&s); err != nil {
				return nil, invalid("it is not valid JSON")
			}
			var got string
			if json.Unmarshal(s, &got) != nil || got != name {
				return nil, invalid("its name is not the requested package")
			}
			gotName = true
		case "time":
			if err := dec.Decode(&timeRaw); err != nil {
				return nil, invalid("it is not valid JSON")
			}
		case "versions":
			if versions, err = readVersions(dec, name, doc.Tarballs); err != nil {
				return nil, err
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, invalid("it is not valid JSON")
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, invalid("it is not valid JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, invalid("it holds more than one JSON value")
	}
	if !gotName {
		return nil, invalid("it has no name")
	}
	doc.Name = name
	times := map[string]string{}
	if len(timeRaw) > 0 {
		var m map[string]json.RawMessage
		if json.Unmarshal(timeRaw, &m) == nil {
			for k, v := range m {
				var s string
				if json.Unmarshal(v, &s) == nil {
					times[k] = s
				}
			}
		}
	}
	doc.Modified = ParseTime(times["modified"])
	for i := range versions {
		versions[i].PublishedAt = ParseTime(times[versions[i].Version])
	}
	doc.Versions = versions
	return doc, nil
}

// readVersions walks the versions object, decoding each entry on its own so that a
// document of tens of megabytes is never held as a tree.
func readVersions(dec *json.Decoder, name string, tarballs map[string]string) ([]rows.RemoteVersion, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, invalid("it is not valid JSON")
	}
	if tok == nil {
		return nil, nil
	}
	if tok != json.Delim('{') {
		return nil, invalid("versions is not an object")
	}
	var out []rows.RemoteVersion
	seen := map[string]bool{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, invalid("it is not valid JSON")
		}
		v, _ := keyTok.(string)
		var e versionEntry
		if err := dec.Decode(&e); err != nil {
			return nil, invalid("the entry of version %s is not an object", quote(v))
		}
		if seen[v] {
			return nil, invalid("version %s is listed twice", quote(v))
		}
		seen[v] = true
		if !isString(e.Version, v) {
			return nil, invalid("the entry of version %s names another version", quote(v))
		}
		if !isString(e.Name, name) {
			return nil, invalid("the entry of version %s names another package", quote(v))
		}
		var d distEntry
		if len(e.Dist) == 0 || json.Unmarshal(e.Dist, &d) != nil {
			return nil, invalid("version %s has no dist", quote(v))
		}
		tarball, ok := asString(d.Tarball)
		if !ok || !absoluteURL(tarball) {
			return nil, invalid("the tarball of version %s is not an absolute http or https URL", quote(v))
		}
		rv := rows.RemoteVersion{Version: v}
		if len(d.Integrity) > 0 && !bytes.Equal(d.Integrity, []byte("null")) {
			s, ok := asString(d.Integrity)
			if !ok || !ValidSRI(s) {
				return nil, invalid("the integrity of version %s is not a subresource integrity string", quote(v))
			}
			rv.Integrity = s
		}
		if s, ok := asString(d.Shasum); ok && validShasum(s) {
			rv.Shasum = strings.ToLower(s)
		}
		tarballs[v] = tarball
		out = append(out, rv)
	}
	if _, err := dec.Token(); err != nil {
		return nil, invalid("it is not valid JSON")
	}
	sortVersions(out)
	return out, nil
}

func sortVersions(vs []rows.RemoteVersion) {
	// A plain text order, not SemVer: the facts are keyed by the text and must not
	// depend on the order the upstream listed them in.
	sort.Slice(vs, func(i, j int) bool { return vs[i].Version < vs[j].Version })
}

// quote shows a version text from a hostile document: cut and printable.
func quote(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return fmt.Sprintf("%q", strings.ToValidUTF8(s, "?"))
}

func isString(raw json.RawMessage, want string) bool {
	s, ok := asString(raw)
	return ok && s == want
}

func asString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// absoluteURL reports whether s is an absolute http or https URL with a host.
func absoluteURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// ParseTime reads a time of the time map ("2024-05-01T10:00:00.000Z"); the zero time
// for text that is not one.
func ParseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// ValidSRI reports whether s is a subresource integrity string (W3C SRI): one or more
// whitespace-separated entries "<algorithm>-<base64>[?options]" whose algorithm is
// sha1, sha256, sha384 or sha512 and whose digest has the length of its algorithm.
func ValidSRI(s string) bool {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		algo, rest, ok := strings.Cut(f, "-")
		if !ok {
			return false
		}
		sum, _, _ := strings.Cut(rest, "?")
		n, known := map[string]int{"sha1": 20, "sha256": 32, "sha384": 48, "sha512": 64}[algo]
		if !known {
			return false
		}
		raw, err := base64.StdEncoding.DecodeString(sum)
		if err != nil || len(raw) != n {
			return false
		}
	}
	return true
}

// SHA512 returns the sha512 digest an SRI string announces, nil when it has none.
func SHA512(integrity string) []byte {
	for _, f := range strings.Fields(integrity) {
		sum, ok := strings.CutPrefix(f, "sha512-")
		if !ok {
			continue
		}
		sum, _, _ = strings.Cut(sum, "?")
		if raw, err := base64.StdEncoding.DecodeString(sum); err == nil && len(raw) == 64 {
			return raw
		}
	}
	return nil
}

// SHA1 returns the digest of a dist.shasum, nil for text that is no SHA-1.
func SHA1(shasum string) []byte {
	if !validShasum(shasum) {
		return nil
	}
	raw, err := hex.DecodeString(shasum)
	if err != nil {
		return nil
	}
	return raw
}

func validShasum(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
