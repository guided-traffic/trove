package npm

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/remote"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// The remote class (R-NPM-023 to R-NPM-027). The remote engine owns the request path,
// the freshness, the collapsing of concurrent fetches, the negative cache and the
// circuit (R-REPO-025 to R-REPO-031); this file is what npm adds to it: which request
// fetches a document, what a fetched document must be before it is stored, what is
// recorded of it in the transaction that stores it, how it is served, and which
// tarballs may be served.

// documentVariant is the variant npm stores: the full document, for both request
// variants (R-NPM-024). The abbreviated form is derived from it.
const documentVariant = "full"

// Remote returns the format's remote adapter.
func (f *Format) Remote() formats.RemoteAdapter { return remoteAdapter{f: f} }

type remoteAdapter struct{ f *Format }

var (
	_ formats.RemoteAdapter = remoteAdapter{}
	_ formats.ArtifactGate  = remoteAdapter{}
	_ formats.TypeExpecter  = remoteAdapter{}
)

// TracksVersionRemoval is true: npm's removal marks (npm_remote_versions.listed)
// replace the generic gone-package rule (R-REPO-029, R-NPM-026).
func (remoteAdapter) TracksVersionRemoval() bool { return true }

// target resolves a canonical path of a remote repository: the package, and whether it
// is the package document. Anything else the engine asks for is a tarball.
func (a remoteAdapter) target(p formats.Path) (n name.Name, document bool, err error) {
	info := a.f.Classify(p)
	if info.Package == "" {
		return name.Name{}, false, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	}
	n, err = name.Parse(info.Package)
	if err != nil {
		return name.Name{}, false, err
	}
	return n, p.Path == n.String(), nil
}

// UpstreamRequest maps a path to the upstream request: the document of a package by its
// name with %2f (R-NPM-024), a tarball by the path itself (R-REPO-043).
func (a remoteAdapter) UpstreamRequest(ctx context.Context, rc formats.RemoteContext, p formats.Path, _ string) (*http.Request, error) {
	_, document, err := a.target(p)
	if err != nil {
		return nil, err
	}
	return remote.Request(ctx, rc.Upstream, p.Path, document)
}

// AcceptResponse lets every 2xx response through: a media type that does not belong
// (an HTML page in place of a document) is refused by the engine's block list before a
// byte is stored (R-REPO-042), and a body that is no document by PrepareFill.
func (remoteAdapter) AcceptResponse(formats.Path, *http.Response) error { return nil }

// ExpectsType declares the media types of npm's two kinds of resource: a document is
// JSON, a tarball a gzip stream or an octet stream. None is on the default block list,
// so the declaration changes nothing today; it keeps the answer with the format when
// the list grows.
func (a remoteAdapter) ExpectsType(p formats.Path, mediaType string) bool {
	_, document, err := a.target(p)
	if err != nil {
		return false
	}
	switch strings.ToLower(mediaType) {
	case "application/json", "application/vnd.npm.install-v1+json":
		return document
	case "application/octet-stream", "application/gzip", "application/x-gzip", "application/x-tar":
		return !document
	}
	return false
}

// PrepareFill checks a fetched document before anything is stored, outside every
// transaction (R-NPM-024): it is read once as a stream, and an invalid one is 502
// npm_invalid_upstream_document with the previous entry kept. It returns the facts
// OnFill records and the URL-map rows of the tarball URLs that are not the default
// (R-REPO-043).
func (a remoteAdapter) PrepareFill(_ context.Context, rc formats.RemoteContext, p formats.Path, variant string, body io.ReaderAt, size int64) (formats.Prepared, error) {
	n, document, err := a.target(p)
	if err != nil || !document || variant != documentVariant {
		return formats.Prepared{}, errs.E(errs.Internal, errs.CodeInternal, "npm fills package documents of variant full only.")
	}
	doc, err := remote.Check(io.NewSectionReader(body, 0, size), n.String())
	if err != nil {
		return formats.Prepared{}, err
	}
	var maps []formats.URLMapping
	for _, v := range doc.Versions {
		file := TarballPath(n, v.Version)
		def, err := remote.URL(rc.Upstream, file, false)
		if err != nil {
			continue
		}
		if got := doc.Tarballs[v.Version]; got != def.String() {
			maps = append(maps, formats.URLMapping{Path: formats.Path{Path: file}, Upstream: got})
		}
	}
	return formats.Prepared{Mappings: maps, Data: doc}, nil
}

// OnFill runs in the transaction that upserts the document's cache entry (R-NPM-024).
// The records of the document are written only when the upsert replaces the stored
// document: when the core keeps a stored entry that was fetched later, or the document
// is a stale copy of the stored one, the fill keeps the stored document, renews its
// freshness and changes no npm row, so the stored document and its records always
// describe one document, whichever fill commits last. Both are decided in the data port,
// under the lock of the package's record and against what is stored at that moment.
func (a remoteAdapter) OnFill(ctx context.Context, tx formats.Tx, ev formats.FillEvent) (bool, error) {
	doc, ok := ev.Prepared.Data.(*remote.Doc)
	if !ok {
		return false, errs.E(errs.Internal, errs.CodeInternal, "The fill carries no checked document.")
	}
	st, err := storeOf(ev.Data)
	if err != nil {
		return false, err
	}
	rep := a.f.replicaOfPort(ev.Data)
	res, err := st.ApplyFill(ctx, tx, rows.RemoteFill{
		RepoID: ev.Repo.ID, Name: doc.Name, DocSHA256: ev.Digest, DocModified: doc.Modified, FetchedAt: ev.FetchedAt,
		Versions: doc.Versions, Audit: rows.Audit{RepoKey: ev.Repo.Key},
	})
	if err != nil {
		return false, err
	}
	if res.Superseded {
		return true, nil
	}
	if res.Stale {
		rep.met.staleDocument(ev.Repo.Key)
		return true, nil
	}
	rep.met.removedVersions(ev.Repo.Key, len(res.Removed))
	// A refresh with new content evicts the merged documents that contain the package
	// early (R-REPO-020); nothing depends on the signal for correctness.
	return false, ev.Packages.Notify(areaMetadataChanged, formats.PackageChanged(ev.Repo.ID, doc.Name))
}

// Rewrite renders a stored full document for one response of the repository the links
// belong to: every dist.tarball under its registry URL (R-NPM-025, R-REPO-043), every
// other member unchanged (R-NPM-027).
func (a remoteAdapter) Rewrite(_ context.Context, _ formats.RemoteContext, p formats.Path, doc []byte, links formats.LinkBuilder) ([]byte, error) {
	n, _, err := a.target(p)
	if err != nil {
		return nil, err
	}
	return remote.Rewrite(doc, n.String(), remote.TarballPrefix(links.Repository(), n.String()))
}

// BeforeArtifact is the road every tarball of a remote repository takes (R-NPM-025,
// R-NPM-026, Q-NPM-13): it applies the removal marks and returns the digests the
// document announced. A cached tarball is served only while its package document is
// current, so a document that is expired, or was last validated before a restore,
// is revalidated first (one conditional request in one flight, whichever replicas ask),
// and so is the document of a version the repository has no record of yet: a lockfile
// install asks for tarballs without documents. When the upstream cannot be asked the
// stored marks decide, as stale serving does. The cache view never contacts the
// upstream.
func (a remoteAdapter) BeforeArtifact(ctx context.Context, h formats.Host, rq *formats.Request, p formats.Path) (formats.Digests, error) {
	n, document, err := a.target(p)
	if err != nil || document {
		return formats.Digests{}, err
	}
	version, ok := VersionOfFile(n, lastSegment(p.Path), tarballSuffix)
	if !ok {
		return formats.Digests{}, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	}
	st, err := storeOf(h.FormatData(ID))
	if err != nil {
		return formats.Digests{}, err
	}
	repo := rq.RepoRef()
	facts, err := st.TarballFacts(ctx, repo.ID, n.String(), version)
	if err != nil {
		return formats.Digests{}, err
	}
	if !rq.CacheView && (!facts.Known || !facts.DocStored || !facts.DocCurrent) {
		outcome := "revalidated"
		_, _, rerr := h.Remote().EnsureCurrent(ctx, rq, formats.Path{Path: n.String()}, documentVariant)
		switch {
		case rerr == nil:
		case facts.Known:
			outcome = "unreachable"
		default:
			return formats.Digests{}, rerr
		}
		a.f.replicaOf(h).met.revalidation(repo.Key, outcome)
		if facts, err = st.TarballFacts(ctx, repo.ID, n.String(), version); err != nil {
			return formats.Digests{}, err
		}
	}
	if err := refuseRemoved(facts); err != nil {
		return formats.Digests{}, err
	}
	if !facts.Known {
		return formats.Digests{}, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	}
	// The sha512 of dist.integrity decides; dist.shasum (SHA-1) only when the document has none
	// (R-NPM-025). A tarball is not refused for a legacy shasum a stronger digest outvotes.
	if sum := remote.SHA512(facts.Integrity); sum != nil {
		return formats.Digests{SHA512: sum}, nil
	}
	return formats.Digests{SHA1: remote.SHA1(facts.Shasum)}, nil
}

// refuseRemoved is the rule of R-NPM-026: with serveRemovedVersions off, a version the
// upstream's document no longer lists and every version of a package the upstream
// withdrew answer 404, cached or not.
func refuseRemoved(facts rows.TarballFacts) error {
	if facts.ServeRemoved {
		return nil
	}
	if (facts.Known && !facts.Listed) || !facts.DocGone.IsZero() {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	}
	return nil
}

// lastSegment is the last segment of a canonical path.
func lastSegment(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
