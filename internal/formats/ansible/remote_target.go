package ansible

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

// docKind is the document or file a canonical path of a remote repository addresses (R-ANS-002).
type docKind int

const (
	kindRoot docKind = iota + 1
	kindCollection
	kindVersions
	kindVersion
	kindArchive
	kindRoleLookup
	kindRoleVersions
	kindRoleArchive
	kindDocs
)

// The variants of the stored documents (Q-ANS-12, R-ANS-020): the upstream's bytes as they came, per document. A version list is
// stored in pages of the upstream's size, each a document of its own with the variant "offset=<n>"; a role lookup is one document
// per owner and name, a role's version list one per page.
const (
	documentVariant   = "doc"
	offsetVariantKey  = "offset="
	pageVariantKey    = "page="
	lookupVariantKey  = "owner="
	lookupNameKey     = "&name="
	upstreamListLimit = 100
	// MaxListVersions bounds the versions of one collection a remote reads per request (R-ANS-017): 512 stored pages of 100.
	MaxListVersions = 512 * upstreamListLimit
	// maxRolePage bounds the page number of a role's version list.
	maxRolePage = 1000
	// roleVersionsPageSize is the page_size a remote asks the upstream's role API for, the size the client asks for.
	roleVersionsPageSize = 50
)

// target is a canonical path of a remote repository taken apart.
type target struct {
	kind docKind
	// ns, name and version name the collection (lower case); file is the archive's file name.
	ns, name, version, file string
	// roleID and roleRef name the role and the version of a role archive.
	roleID, roleRef string
}

// pkg is "<ns>.<name>" of a collection target, "role/<id>" of a role target.
func (t target) pkg() string {
	switch t.kind {
	case kindCollection, kindVersions, kindVersion, kindArchive, kindDocs:
		return t.ns + "." + t.name
	case kindRoleVersions, kindRoleArchive:
		return RolePackage(t.roleID)
	}
	return ""
}

func missing() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
}

// parseTarget resolves a canonical path of a remote repository; a path no document of the remote class lives at is the 404 of a
// missing resource.
func parseTarget(p formats.Path) (target, error) {
	if p.Path == RootDocument {
		return target{kind: kindRoot}, nil
	}
	segs := strings.Split(p.Path, "/")
	switch segs[0] {
	case dirCollections:
		return parseCollectionTarget(segs[1:])
	case dirRoles:
		return parseRoleTarget(segs[1:])
	}
	return target{}, missing()
}

func parseCollectionTarget(rest []string) (target, error) {
	if len(rest) == 4 && rest[2] == dirDocs && collection.ValidPart(rest[0]) && collection.ValidPart(rest[1]) {
		// The docs-blob document of a version (R-ANS-035).
		if v, ok := strings.CutSuffix(rest[3], jsonSuffix); ok && version.Valid(v) {
			return target{kind: kindDocs, ns: rest[0], name: rest[1], version: v}, nil
		}
		return target{}, missing()
	}
	if len(rest) != 3 || !collection.ValidPart(rest[0]) || !collection.ValidPart(rest[1]) {
		return target{}, missing()
	}
	t := target{ns: rest[0], name: rest[1]}
	switch file := rest[2]; {
	case file == CollectionDocument:
		t.kind = kindCollection
	case file == VersionsDocument:
		t.kind = kindVersions
	case strings.HasSuffix(file, collection.ArchiveSuffix):
		ns, name, ver, ok := collection.ParseFileName(file)
		if !ok || ns != t.ns || name != t.name || !version.Valid(ver) {
			return target{}, missing()
		}
		t.kind, t.version, t.file = kindArchive, ver, file
	case strings.HasSuffix(file, jsonSuffix) && version.Valid(strings.TrimSuffix(file, jsonSuffix)):
		t.kind, t.version = kindVersion, strings.TrimSuffix(file, jsonSuffix)
	default:
		return target{}, missing()
	}
	return t, nil
}

func parseRoleTarget(rest []string) (target, error) {
	switch {
	case len(rest) == 1 && rest[0] == "lookup"+jsonSuffix:
		return target{kind: kindRoleLookup}, nil
	case len(rest) == 2 && digits(rest[0], maxRoleIDDigits):
		if rest[1] == roleVersionsDocument {
			return target{kind: kindRoleVersions, roleID: rest[0]}, nil
		}
		if ref, ok := strings.CutSuffix(rest[1], collection.ArchiveSuffix); ok && roleRefOK(ref) {
			return target{kind: kindRoleArchive, roleID: rest[0], roleRef: ref}, nil
		}
	}
	return target{}, missing()
}

// offsetVariant is the variant of the stored version-list page that starts at offset.
func offsetVariant(offset int) string { return offsetVariantKey + strconv.Itoa(offset) }

// parseOffsetVariant reads "offset=<n>" with n in [0, MaxListVersions).
func parseOffsetVariant(variant string) (int, bool) {
	s, ok := strings.CutPrefix(variant, offsetVariantKey)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n >= MaxListVersions || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

// pageVariant is the variant of the stored version-list page n (from 1) of a role.
func pageVariant(n int) string { return pageVariantKey + strconv.Itoa(n) }

func parsePageVariant(variant string) (int, bool) {
	s, ok := strings.CutPrefix(variant, pageVariantKey)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > maxRolePage || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

// lookupVariant is the variant of the stored role lookup for an owner and a role name.
func lookupVariant(owner, name string) string { return lookupVariantKey + owner + lookupNameKey + name }

func parseLookupVariant(variant string) (owner, name string, ok bool) {
	rest, ok := strings.CutPrefix(variant, lookupVariantKey)
	if !ok {
		return "", "", false
	}
	owner, name, ok = strings.Cut(rest, lookupNameKey)
	if !ok || !roleWordOK(owner) || !roleWordOK(name) {
		return "", "", false
	}
	return owner, name, true
}

// roleWordOK is the grammar of the owner and the name of a role in a lookup, [A-Za-z0-9._-]{1,64} (R-ANS-020).
func roleWordOK(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// escapeSeg escapes a path segment of an upstream address.
func escapeSeg(s string) string { return url.PathEscape(s) }
