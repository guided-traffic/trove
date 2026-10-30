package npm

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/document"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/remote"
)

// The documents and tarballs of virtual repositories (R-NPM-029 to R-NPM-032, flow F4).
// The virtual engine plans the request and merges the members' documents; the handler
// derives the form the client asked for from the merge and rewrites the tarball
// addresses onto the virtual repository's registry URL.

// virtualSource is the merged full document of a virtual repository (R-NPM-030). The
// validator and the cache key are functions of the merge fingerprint, which changes
// whenever the bytes could: a member's revision, a remote member's stored document,
// the versions it has cached, the callers' populate rights (D-7).
func (f *Format) virtualSource(ctx context.Context, rq *formats.Request, n name.Name, fm form, ref string) (fullSource, error) {
	info := f.Classify(formats.Path{Path: n.String()})
	m, err := rq.Host.Resolver().Metadata(ctx, rq, formats.Path{Path: n.String()}, info, documentVariant)
	if err != nil {
		return fullSource{}, err
	}
	bytes := m.Bytes
	return fullSource{
		form: fm, ref: ref, kind: "virtual", servingKey: rq.Repo.Key, repoID: rq.RepoRef().ID,
		content: hex.EncodeToString(m.MergeFingerprint[:]), status: m.CacheStatus,
		bytes: func(context.Context) ([]byte, error) { return bytes, nil },
		rev:   "1-" + hex.EncodeToString(m.MergeFingerprint[:16]),
	}, nil
}

// virtualDocument serves a form of the merged document of a virtual repository.
func (f *Format) virtualDocument(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name, fm form, ref string) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	src, err := f.virtualSource(ctx, rq, n, fm, ref)
	if err != nil {
		return err
	}
	return f.serveFull(ctx, rq, w, n, src)
}

// virtualTarball serves a tarball through a virtual repository: the tiers of
// R-REPO-019 step 6 (local members, the caches of the remote members, then their
// upstreams), the first hit wins.
func (f *Format) virtualTarball(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name, version string) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Version: version, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	if err := hiddenVersion(rq, n, version); err != nil {
		return err
	}
	p := formats.Path{Path: TarballPath(n, version)}
	return rq.Host.Resolver().Artifact(ctx, rq, p, f.Classify(p), immutableWriter{w})
}

// virtualWriteDocument is GET {name}?write=true on a virtual repository (R-NPM-029): the
// full document of the deployment target, read from PostgreSQL in the request and never
// cached, with its _rev, so that a read-modify-write client (npm deprecate, npm
// unpublish) never sends merged upstream versions back. The tarball addresses are those
// of the virtual repository. A package the target does not hold is 404.
func (f *Format) virtualWriteDocument(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name) error {
	target, err := rq.Host.Resolver().DeployTarget(ctx, rq)
	if err != nil {
		return err
	}
	trq := *rq
	trq.Repo = target
	st, _, err := f.authorizeRead(ctx, &trq, n, "")
	if err != nil {
		return err
	}
	p, found, err := st.LocalDocument(ctx, trq.RepoRef().ID, n.String())
	if err != nil {
		return err
	}
	if !found {
		return notFound()
	}
	rep := f.replicaOf(rq.Host)
	body, err := document.FullDocument(&p, document.Options{
		Name: n.String(), Basename: n.Package(),
		TarballPrefix: remote.TarballPrefix(rq.Host.Links(rq).Registry(rq.Repo.Key), n.String()),
		Rev:           Rev(trq.RepoRef().ID, formats.PackageID(p.PackageID), formats.RevSeq(p.RevSeq)), AbbreviatedTime: rep.cfg.AbbreviatedTime,
	})
	if err != nil {
		return errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return nil
	}
	_, err = w.Write(body)
	return err
}
