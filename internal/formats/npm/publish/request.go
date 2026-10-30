package publish

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/manifest"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// File name suffixes of the attachments (R-NPM-007 step 3).
const (
	TarballSuffix  = ".tgz"
	SigstoreSuffix = ".sigstore"
)

// Request is a publish body that passed the checks of R-NPM-007 step 3 which need
// no stored state and no tarball: one new name, one canonical version, one tag,
// one tarball attachment and at most one provenance bundle.
type Request struct {
	// Name is the package name, valid for a new package.
	Name name.Name
	// Version is the canonical version (no build metadata, no "v").
	Version semver.Version
	// Tag is the dist-tag the request carries for the version.
	Tag string
	// Manifest is the manifest of the version as the client sent it, bounded and
	// parsed.
	Manifest manifest.Manifest
	// Tarball and Sigstore are the attachments; Sigstore is nil when the request
	// has no provenance bundle.
	Tarball, Sigstore *Attachment

	sha512 [][]byte
	sha1   []byte
}

// Request checks the envelope against the package name of the request path
// (decoded and normalized) and returns the publish it describes. Every failure
// is an *errs.Error: 400 npm_invalid_name, npm_invalid_version, npm_invalid_tag,
// npm_bad_publish or npm_manifest_mismatch.
func (e *Envelope) Request(pathName string, lim manifest.Limits) (*Request, error) {
	if e.Name != pathName {
		return nil, manifest.BadPublish("the name in the publish request is not the package name of the request path")
	}
	if e.ID != "" && e.ID != e.Name {
		return nil, manifest.BadPublish("the _id of the publish request is not its name")
	}
	n, err := name.ParseNew(e.Name)
	if err != nil {
		return nil, err
	}
	if len(e.Versions) != 1 {
		return nil, manifest.BadPublish(fmt.Sprintf("a publish request holds exactly one version, this one holds %d", len(e.Versions)))
	}
	var key string
	var raw []byte
	for k, v := range e.Versions {
		key, raw = k, v
	}
	v, err := parseVersion(key)
	if err != nil {
		return nil, err
	}
	if len(e.DistTags) != 1 {
		return nil, manifest.BadPublish(fmt.Sprintf("a publish request holds exactly one dist-tag, this one holds %d", len(e.DistTags)))
	}
	var tag, target string
	for k, t := range e.DistTags {
		tag, target = k, t
	}
	if err := semver.ValidateTag(tag); err != nil {
		return nil, err
	}
	if c, ok := semver.Clean(target); !ok || c != v.Canonical() {
		return nil, manifest.BadPublish("the dist-tag of the publish request does not point to the version it publishes")
	}
	m, err := manifest.Parse(raw, lim)
	if err != nil {
		return nil, err
	}
	if err := checkRequestManifest(m, n.String(), v.Canonical()); err != nil {
		return nil, err
	}
	r := &Request{Name: n, Version: v, Tag: tag, Manifest: m}
	if err := r.attachments(e); err != nil {
		return nil, err
	}
	if err := r.digests(m); err != nil {
		return nil, err
	}
	return r, nil
}

// parseVersion applies the version check of R-NPM-007 step 3: the version after
// semver.Clean, parsed as SemVer 2.0.0, without build metadata (Q-NPM-15).
func parseVersion(key string) (semver.Version, error) {
	cleaned, ok := semver.Clean(key)
	if !ok {
		return semver.Version{}, errs.E(errs.Invalid, semver.CodeInvalidVersion, "the version of the publish request is not a SemVer 2.0.0 version")
	}
	v, err := semver.Parse(cleaned)
	if err != nil {
		return semver.Version{}, err
	}
	if strings.Contains(key, "+") {
		return semver.Version{}, errs.E(errs.Invalid, semver.CodeInvalidVersion, "the version of the publish request carries build metadata, which a published version does not")
	}
	return v, nil
}

// checkRequestManifest requires that the manifest of the request names the
// package and the version of its key, and holds no NUL, which the stored form
// (jsonb) cannot represent.
func checkRequestManifest(m manifest.Manifest, pkg, version string) error {
	norm := manifest.Normalize(manifest.Manifest{"name": m["name"], "version": m["version"]}, manifest.Facts{})
	if got, _ := norm["name"].(string); got != pkg {
		return manifest.Mismatch("the name in the manifest of the publish request is not the package name")
	}
	if got, _ := norm["version"].(string); got != version {
		return manifest.Mismatch("the version in the manifest of the publish request is not the version it is published under")
	}
	if hasNUL(m) {
		return manifest.BadPublish("the manifest of the publish request holds a NUL character")
	}
	return nil
}

func hasNUL(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.IndexByte(t, 0) >= 0
	case map[string]any:
		for k, x := range t {
			if strings.IndexByte(k, 0) >= 0 || hasNUL(x) {
				return true
			}
		}
	case manifest.Manifest:
		return hasNUL(map[string]any(t))
	case []any:
		for _, x := range t {
			if hasNUL(x) {
				return true
			}
		}
	}
	return false
}

// attachments picks the tarball and the bundle and checks the declared lengths.
func (r *Request) attachments(e *Envelope) error {
	for i := range e.Attachments {
		a := &e.Attachments[i]
		var slot **Attachment
		var kind string
		switch {
		case strings.HasSuffix(a.Name, TarballSuffix):
			slot, kind = &r.Tarball, TarballSuffix
		case strings.HasSuffix(a.Name, SigstoreSuffix):
			slot, kind = &r.Sigstore, SigstoreSuffix
		default:
			return manifest.BadPublish("the publish request carries an attachment that is neither a .tgz nor a .sigstore file")
		}
		if *slot != nil {
			return manifest.BadPublish("the publish request carries more than one " + kind + " attachment")
		}
		if a.Length < 0 {
			return manifest.BadPublish("an attachment of the publish request declares no length")
		}
		if a.Length != a.Decoded {
			return manifest.BadPublish("the length an attachment declares is not the length of its data")
		}
		*slot = a
	}
	if r.Tarball == nil {
		return manifest.BadPublish("the publish request carries no .tgz attachment")
	}
	return nil
}

// digests reads dist.integrity and dist.shasum: at least one must give a digest
// to check.
func (r *Request) digests(m manifest.Manifest) error {
	dist, _ := m["dist"].(map[string]any)
	if integrity, ok := dist["integrity"].(string); ok {
		for _, tok := range strings.Fields(integrity) {
			alg, rest, found := strings.Cut(tok, "-")
			if !found || !strings.EqualFold(alg, "sha512") {
				continue
			}
			b64, _, _ := strings.Cut(rest, "?")
			sum, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				sum, err = base64.RawStdEncoding.DecodeString(b64)
			}
			if err != nil || len(sum) != 64 {
				return manifest.BadPublish("dist.integrity of the publish request holds a sha512 entry that is not a SHA-512 digest")
			}
			r.sha512 = append(r.sha512, sum)
		}
	}
	if shasum, ok := dist["shasum"].(string); ok && shasum != "" {
		sum, err := hex.DecodeString(strings.ToLower(shasum))
		if err != nil || len(sum) != 20 {
			return manifest.BadPublish("dist.shasum of the publish request is not a SHA-1 digest in hex")
		}
		r.sha1 = sum
	}
	if len(r.sha512) == 0 && r.sha1 == nil {
		return manifest.BadPublish("dist of the publish request carries neither a sha512 integrity nor a shasum")
	}
	return nil
}

// CheckDigests compares the digests of the tarball attachment with those the
// manifest announced: every sha512 entry of dist.integrity and dist.shasum must
// match. A mismatch is 409, as for any announced digest (R-STOR-004).
func (r *Request) CheckDigests(sum512, sum1 []byte) error {
	for _, want := range r.sha512 {
		if subtle.ConstantTimeCompare(want, sum512) != 1 {
			return errs.E(errs.Conflict, errs.CodeConflict, "the sha512 in dist.integrity of the publish request differs from the digest of the tarball")
		}
	}
	if r.sha1 != nil && !bytes.Equal(r.sha1, sum1) {
		return errs.E(errs.Conflict, errs.CodeConflict, "dist.shasum of the publish request differs from the SHA-1 of the tarball")
	}
	return nil
}
