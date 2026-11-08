package formats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/qname"
)

// The versions a caller's own grants let it read (R-AUTHZ-005, Q-AUTHZ-255). The decision point decides a version on
// the path of its file as well as on the package document's path, so a grant or a token scope whose path pattern hides
// one version's file hides that version: a document of the package the caller may read lists only the versions it may
// read, and a write leaves the others alone. A caller whose grants carry no path pattern on the repository - the common
// case, and every caller who reads it whole - gets no view and pays nothing for it: no statement and no decision beyond
// the request's own (R-ARCH-027). The endpoint's rules are the exposure view's ([EndpointFacts.Exposure]); this view
// is the caller's grants alone, so a format filters with both.

// VersionView is what a caller may read of the versions of one package of the request's repository: the decision of
// [ReadScope.MayRead] for each version, remembered for the request. The zero value and nil hide nothing.
type VersionView struct {
	scope ReadScope
	key   qname.Repo
	name  string
	seen  map[string]bool
}

// CallerVersions returns the view of the request's caller on the versions of a package of the request's repository, nil
// when its grants decide no version apart from the package's document ([ReadScope.VersionsByPath] is false): then every
// version of a package the caller may read is readable, as far as its grants go. name is the normalized name. The read
// of the package's document is the caller's to decide first; the view does not repeat it.
func CallerVersions(ctx context.Context, rq *Request, name string) (*VersionView, error) {
	key := rq.Repo.Name
	scope, err := rq.Host.Resolver().ReadScope(ctx, rq, []qname.Repo{key})
	if err != nil {
		return nil, err
	}
	if scope.Shape(key) == ReadAll || !scope.VersionsByPath(key) {
		return nil, nil
	}
	return &VersionView{scope: scope, key: key, name: name, seen: map[string]bool{}}, nil
}

// Version reports whether the caller may read a version of the package. A nil view allows every version. A view is used
// by one request at a time.
func (v *VersionView) Version(version string) bool {
	if v == nil {
		return true
	}
	ok, seen := v.seen[version]
	if !seen {
		ok = v.scope.MayRead(v.key, v.name, version)
		v.seen[version] = ok
	}
	return ok
}

// Hidden returns the versions of a list the view hides, sorted, without duplicates; nil for a nil view and for a list
// it hides nothing of.
func (v *VersionView) Hidden(versions []string) []string {
	if v == nil {
		return nil
	}
	var out []string
	for _, ver := range versions {
		if !v.Version(ver) {
			out = append(out, ver)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// HiddenFingerprint names a set of hidden versions in the key and the validator of a rendering: "" for none, so a
// caller from whom the view hides nothing of a package shares the rendition of every other caller, and otherwise a
// digest of the sorted versions, so a rendering is never served to a caller from whom another set is hidden.
func HiddenFingerprint(hidden []string) string {
	if len(hidden) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(hidden, "\n")))
	return "h" + hex.EncodeToString(sum[:12])
}

// liveVersionsVariant is the variant under which [LiveVersions] keeps the version list of a revision; no format renders
// a document of that name.
const liveVersionsVariant = "\x00live-versions"

// LiveVersions returns the versions of a package of the request's repository at the revision of its row, and the
// revision they were read at. The list of a revision never changes (every write raises packages.rev_seq, R-ARCH-015),
// so it is kept in the replica's rendition cache under that revision, and a request that finds it there reads nothing
// (R-ARCH-027). A miss reads the revision and the versions in one statement (PackageStore.Versions), so they are one
// snapshot, and keeps the list under the revision it was read with, which a write that raced the row's lookup may have
// raised: a list is never kept under a revision it does not belong to.
func LiveVersions(ctx context.Context, rq *Request, row PackageRow) ([]string, RevSeq, error) {
	repo := rq.RepoRef()
	key := RenditionKey{Repo: repo, Package: row.Ref.Name, RevSeq: row.Ref.RevSeq, Variant: liveVersionsVariant}
	if b, ok := rq.Host.Renditions().Get(key); ok {
		if len(b) == 0 {
			return nil, row.Ref.RevSeq, nil
		}
		return strings.Split(string(b), "\n"), row.Ref.RevSeq, nil
	}
	rows, rev, err := rq.Host.Packages().Versions(ctx, repo, row.Ref.Name)
	if err != nil {
		return nil, 0, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Version
	}
	key.RevSeq = rev
	rq.Host.Renditions().Put(key, []byte(strings.Join(out, "\n")))
	return out, rev, nil
}
