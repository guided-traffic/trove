package repository

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/openpgp"
)

// The rules of the Debian format's member of a document that the document alone decides (R-DEB-020): the upstream keys of a remote are
// checked when they are saved, their fingerprints are stored read-only beside them, a mirror needs them, and a remote without them
// carries a warning. What needs the other repositories (a remote that is a member of a virtual repository needs keys) is decided with
// the members (members.go, debMemberUnverified).

const (
	// debFormat is the ID of the Debian format.
	debFormat = "deb"
	// warningDebUpstreamUnverified is raised for a remote without upstream keys: it stores what its upstream sends, and its clients
	// verify with their own keyrings (R-DEB-020).
	warningDebUpstreamUnverified = "deb_upstream_unverified"
	// debFingerprints is the read-only member of the deb member that shows the fingerprints of upstreamKeys.
	debFingerprints = "upstreamKeyFingerprints"
	debKeysPointer  = "/deb/upstreamKeys"
)

// stripDebReadOnly takes the read-only member out of the deb member before the member is validated against its schema: a client that
// writes back what it read sends it, and it is derived again from the keys in validateDeb.
func stripDebReadOnly(info catalog.Entry, d *Document) {
	if string(info.ID) != debFormat || len(d.Deb) == 0 {
		return
	}
	m := debMemberMap(d.Deb)
	if m == nil {
		return
	}
	if _, ok := m[debFingerprints]; ok {
		delete(m, debFingerprints)
		d.Deb = remarshalMember(m, d.Deb)
	}
}

// validateDeb checks deb.upstreamKeys of a remote repository and derives deb.upstreamKeyFingerprints (R-DEB-020). A block that does
// not parse, holds secret key material, or holds a revoked, expired or non-signing key or an RSA key below 2048 bits answers
// invalid_value at /deb/upstreamKeys with the reason; the keys of a mirror are required (deb_upstream_keys_required).
func validateDeb(v *validation, info catalog.Entry, d *Document) {
	if string(info.ID) != debFormat || d.Class != Remote {
		return
	}
	m := debMemberMap(d.Deb)
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	keys := debKeysOf(m)
	if keys != "" {
		certs, err := openpgp.ParseCertificates([]byte(keys), time.Now(), openpgp.Limits{})
		if err != nil {
			v.add(debKeysPointer, codeInvalidValue, err.Error())
			return
		}
		fps := make([]string, 0, len(certs))
		for _, c := range certs {
			fps = append(fps, c.Fingerprint)
		}
		raw, _ := json.Marshal(fps) // strings always marshal
		m[debFingerprints] = raw
		d.Deb = remarshalMember(m, d.Deb)
	} else {
		v.warnings = append(v.warnings, Warning{
			Code: warningDebUpstreamUnverified, Pointer: debKeysPointer,
			Detail: "the remote has no upstream keys: it stores what its upstream sends and its clients verify with their own keyrings",
		})
	}
	validateDebMirror(v, d)
	if keys == "" && remoteMode(d) == ModeMirror {
		v.add(debKeysPointer, errs.CodeDebUpstreamKeysRequired, "a mirror hands the upstream's content on to its clients and needs deb.upstreamKeys to verify it")
	}
}

// remoteMode reads remote.mode of a document without resolving the whole section.
func remoteMode(d *Document) string {
	var s struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(d.Remote, &s) //nolint:errcheck // the section was resolved before; a section that does not read is pull-through
	return s.Mode
}

// debMemberMap reads a deb member as an object, nil when it is none.
func debMemberMap(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// remarshalMember writes the member in the canonical form; it keeps the old text when the new one does not marshal.
func remarshalMember(m map[string]json.RawMessage, old json.RawMessage) json.RawMessage {
	out, err := json.Marshal(m)
	if err != nil {
		return old
	}
	return canonicalObject(out)
}

// debKeysOf is deb.upstreamKeys of a member: the armored text, "" for none or null.
func debKeysOf(m map[string]json.RawMessage) string {
	var s *string
	if raw, ok := m["upstreamKeys"]; ok && json.Unmarshal(raw, &s) == nil && s != nil {
		return trimKeys(*s)
	}
	return ""
}

func trimKeys(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\r' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// debMemberUnverified reports whether a repository row is a remote Debian repository without upstream keys: such a repository cannot
// be a member of a virtual repository, because a virtual repository signs what it merges with its own key and would hand on content
// nobody checked (R-DEB-020, R-DEB-025).
func debMemberUnverified(format, class string, config []byte) bool {
	if format != debFormat || Class(class) != Remote {
		return false
	}
	var doc struct {
		Deb map[string]json.RawMessage `json:"deb"`
	}
	_ = json.Unmarshal(config, &doc) //nolint:errcheck // a document that does not read has no keys
	return debKeysOf(doc.Deb) == ""
}

// debFingerprintsOf lists the fingerprints of the certificates in a deb member's upstreamKeys, in the order they are saved. The keys
// were checked when they were saved; a certificate that has expired since still has its fingerprint. A block that does not read has
// none.
func debFingerprintsOf(member json.RawMessage) []string {
	keys := debKeysOf(debMemberMap(member))
	if keys == "" {
		return nil
	}
	certs, err := openpgp.ParseStoredCertificates([]byte(keys), openpgp.Limits{})
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(certs))
	for _, c := range certs {
		out = append(out, c.Fingerprint)
	}
	return out
}

// debKeysRemoved lists the fingerprints the update takes out of deb.upstreamKeys of a remote repository (R-DEB-020, Q-DEB-42): the
// fingerprints of the stored member that the new member lacks. A repository that is no remote Debian repository removes none.
func debKeysRemoved(cur, nd *Document) []string {
	if cur.Format != debFormat || cur.Class != Remote {
		return nil
	}
	after := debFingerprintsOf(nd.Deb)
	var out []string
	for _, fp := range debFingerprintsOf(cur.Deb) {
		if !slices.Contains(after, fp) && !slices.Contains(out, fp) {
			out = append(out, fp)
		}
	}
	return out
}

// checkDebKeysKept refuses an update that takes the last upstream key from a remote Debian repository that is a member of a virtual
// repository (R-DEB-020): the virtual repository would sign content nobody verified. The document rules refuse the same for a mirror
// (validateDeb).
func (s *Service) checkDebKeysKept(ctx context.Context, qs *q.Queries, v *validation, id int64, cur, nd *Document) error {
	if nd.Format != debFormat || nd.Class != Remote || debKeysOf(debMemberMap(nd.Deb)) != "" || debKeysOf(debMemberMap(cur.Deb)) == "" {
		return nil
	}
	refs, err := qs.ListReferencingVirtuals(ctx, id)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		v.add(debKeysPointer, errs.CodeDebMemberUnverified,
			"the remote is a member of the virtual repository "+refs[0].Key+", which signs what it merges with its own key; it keeps deb.upstreamKeys while it is a member")
	}
	return nil
}
