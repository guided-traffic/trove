package nuget

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/merge"
	"github.com/guided-traffic/trove/internal/formats/nuget/registration"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// The virtual class (R-NUGET-017 to R-NUGET-019). The virtual engine plans a request
// (patterns, pin rules, claims, the members each caller may read and fill) and caches the
// merged document by the members that took part; NuGet contributes the document of a local
// member, the merge itself and the strategy per path.
//
// Two documents of an ID are merged, each at the canonical path the remote adapter stores
// it at (R-NUGET-015):
//
//   - the version list of the flat container, at "{lower_id}/index.json", variant
//     [VariantFlat]. A member's document is the list as the flat container serves it.
//   - the registration entries, at "{lower_id}/.registration/index.json", variant
//     [VariantRegistration]. A local member's document is the neutral document of package
//     merge; a remote member's is the upstream's registration index as the remote stores it,
//     which the adapter reads with the pages it does not inline (Q-NUGET-82).
//
// The merged bytes hold no address and no hive: the handler renders the answer for the
// repository the client addressed, in the hive the client asked, after the cache.

// The variants of the member documents (Descriptor.Variant of the merged-metadata cache).
const (
	// VariantFlat is the version list of the flat container.
	VariantFlat = "flat"
	// VariantRegistration is the registration entries of an ID; a remote member's pages are
	// read by the adapter (Q-NUGET-82).
	VariantRegistration = "registration"
)

// Virtual returns the format's virtual adapter.
func (f *Format) Virtual() formats.VirtualAdapter { return virtualAdapter{f: f} }

type virtualAdapter struct{ f *Format }

var _ formats.VirtualAdapter = virtualAdapter{}

// documentOf classifies a canonical path as one of the two merged documents of an ID.
func (f *Format) documentOf(p formats.Path) (kind merge.Kind, pkg string, ok bool) {
	info := f.Classify(p)
	if info.Kind != formats.PathMetadata || info.Package == "" {
		return 0, "", false
	}
	switch p.Path {
	case PackageDocumentPath(info.Package):
		return merge.Flat, info.Package, true
	case registrationDocumentPath(info.Package):
		return merge.Registration, info.Package, true
	}
	return 0, "", false
}

// registrationDocumentPath is the canonical path of the registration entries of an ID, the
// cache entry of its registration documents (R-NUGET-015).
func registrationDocumentPath(lowerID string) string {
	return lowerID + "/" + canonRegistration + "/" + indexFile
}

// Strategy: the version list and the registration entries are merged over all members
// (R-NUGET-017); any other path names a file of a version and is resolved to the first
// member that holds it (the tiers of R-REPO-019 step 6).
func (a virtualAdapter) Strategy(p formats.Path) formats.Strategy {
	if _, _, ok := a.f.documentOf(p); ok {
		return formats.Merge
	}
	return formats.FirstMatch
}

// ArtifactPrefix: the files of a package are cached below the directory of its version
// list, "{lower_id}/" (R-NUGET-015).
func (a virtualAdapter) ArtifactPrefix(name string) string {
	if id, ok := lowerID(name); ok && id == name {
		return name + "/"
	}
	return ""
}

// LocalDocument renders the document of a local member from its rows and returns the
// revision it was rendered at (R-ARCH-015). The revision is read before the rows, so a
// commit that raced the read can only make the recorded revision older than the document,
// which the next hit finds out and rebuilds. A member without a stored version holds no
// document; its revision is that of its package row, 0 when it has none.
func (a virtualAdapter) LocalDocument(ctx context.Context, rq *formats.Request, member formats.RepoRef, p formats.Path, _ string) ([]byte, formats.RevSeq, bool, error) {
	kind, id, ok := a.f.documentOf(p)
	if !ok {
		return nil, 0, false, errs.E(errs.Internal, errs.CodeInternal, "The path is no merged document of the NuGet format.")
	}
	st, err := documentStore(rq.Host)
	if err != nil {
		return nil, 0, false, err
	}
	row, found, err := rq.Host.Packages().Package(ctx, member, id)
	if err != nil || !found {
		return nil, 0, false, err
	}
	rev := row.Ref.RevSeq
	var doc []byte
	switch kind {
	case merge.Flat:
		entries, err := st.Versions(ctx, member.ID, id)
		if err != nil || len(entries) == 0 {
			return nil, rev, false, err
		}
		list := make([]string, len(entries))
		for i, e := range entries {
			list[i] = e.LowerVersion
		}
		doc, err = merge.EncodeFlat(list)
		if err != nil {
			return nil, 0, false, internalError(err)
		}
	default:
		pkg, found, err := st.Registration(ctx, member.ID, id)
		if err != nil || !found {
			return nil, rev, false, err
		}
		entries := make([]merge.Entry, len(pkg.Versions))
		for i, v := range pkg.Versions {
			entries[i] = entryOf(v)
		}
		if doc, err = merge.Encode(entries); err != nil {
			return nil, 0, false, internalError(err)
		}
	}
	return doc, rev, true, nil
}

// Merge applies the per-version source rule (R-NUGET-017): the entry of a version comes
// from the member that tier resolution would pick for its .nupkg, a member the caller may
// not fill supplies the versions it has cached only (R-AUTHZ-008). A remote member's
// document is the upstream's as the remote stores it, gzip-coded or not; its registration is
// read with the pages its index does not inline (Q-NUGET-82), within one budget for the whole
// merge (Q-NUGET-83).
func (a virtualAdapter) Merge(ctx context.Context, rq *formats.Request, p formats.Path, docs []formats.MemberDocument) ([]byte, error) {
	kind, id, ok := a.f.documentOf(p)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The path is no merged document of the NuGet format.")
	}
	budget := remote.NewBudget(MaxDocumentBytes)
	parts := make([]merge.Part, len(docs))
	for i, d := range docs {
		parts[i] = merge.Part{
			Member: d.Member.Name.String(), Tier: mergeTier(d.Tier), Position: d.Position, Doc: d.Doc,
			Cached: d.CachedVersions, Restricted: d.Restricted,
		}
		if d.Tier == formats.TierLocal {
			continue
		}
		var err error
		if kind == merge.Flat {
			parts[i].Doc, err = remoteFlatDocument(d.Doc)
		} else {
			parts[i].Entries, err = a.f.remoteMemberEntries(ctx, rq, d, id, budget)
		}
		if err != nil {
			return nil, err
		}
	}
	return merge.Do(kind, parts)
}

func mergeTier(t formats.Tier) merge.Tier {
	switch t {
	case formats.TierLocal:
		return merge.Local
	case formats.TierRemoteCached:
		return merge.RemoteCached
	}
	return merge.Remote
}

// entryOf converts a stored version to the entry of the merge.
func entryOf(v rows.RegistrationVersion) merge.Entry {
	return merge.Entry{
		Version: v.LowerVersion, Full: v.FullVersion, DeclaredID: v.DeclaredID, Listed: v.Listed,
		Prerelease: v.IsPrerelease, Semver2: v.IsSemver2, Published: v.PublishedAt,
		Types: v.PackageTypes, Metadata: v.Metadata,
	}
}

// versionOf converts an entry of a merged document to the row the registration documents
// render from, and the pass-through members of the upstream.
func versionOf(e merge.Entry) (rows.RegistrationVersion, registration.Passthrough) {
	return rows.RegistrationVersion{
		VersionEntry: rows.VersionEntry{
			LowerVersion: e.Version, FullVersion: e.Full, DeclaredID: e.DeclaredID, Listed: e.Listed,
			IsPrerelease: e.Prerelease, IsSemver2: e.Semver2, PublishedAt: e.Published,
		},
		PackageTypes: e.Types, Metadata: e.Metadata,
	}, registration.Passthrough{Deprecation: e.Deprecation, Vulnerabilities: e.Vulnerabilities, Other: e.Members()}
}
