package npm

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// The versions a caller's own grants hide (R-AUTHZ-005, Q-AUTHZ-255, Q-NPM-82). A version is decided on its tarball's
// path as well as on the package document's, so a grant or a token scope whose path pattern hides a tarball hides its
// version wherever the package is shown: the package document in both forms, a single-version document, the dist-tags,
// the document a write starts from. The filter is the one of an endpoint with rules (exposeRows): the versions go,
// latest moves to the highest version left, a tag whose version is gone is dropped, and the README of a latest that
// moved goes with it (Q-NPM-39). A caller whose grants carry no path pattern on the repository asks nothing more than
// before (formats.CallerVersions is nil for it).

// docVersions is what the caller's grants hide of a package's versions for one document request.
type docVersions struct {
	// view decides the versions; nil when the caller's grants decide none apart from the package, or the request
	// names its version by number and that version was found readable.
	view *formats.VersionView
	// rev is the revision the rendering is keyed and validated by: the package row's, or the revision whose versions the
	// hidden fingerprint was taken from.
	rev formats.RevSeq
}

// callerVersions decides what the caller's grants hide of the package before anything of it is read: a version named by
// number that is hidden is a missing one, a package whose every version is hidden is a missing package, and the
// fingerprint of the hidden versions joins the variant of the rendering, so a rendition is served only to callers from
// whom the same versions are hidden (an empty set shares the rendition of every caller).
func (f *Format) callerVersions(ctx context.Context, rq *formats.Request, n name.Name, row formats.PackageRow, rd *rendering) (docVersions, error) {
	out := docVersions{rev: row.Ref.RevSeq}
	view, err := formats.CallerVersions(ctx, rq, n.String())
	if err != nil || view == nil {
		return out, err
	}
	if rd.version != "" {
		// The document of one version is the same for every caller who may read the version.
		if !view.Version(rd.version) {
			return out, notFound()
		}
		return out, nil
	}
	live, rev, err := formats.LiveVersions(ctx, rq, row)
	if err != nil {
		return out, err
	}
	hidden := view.Hidden(live)
	if len(live) > 0 && len(hidden) == len(live) {
		return out, notFound()
	}
	if fp := formats.HiddenFingerprint(hidden); fp != "" {
		rd.variant += "#" + fp
	}
	return docVersions{view: view, rev: rev}, nil
}

// filterOf is the filter of a local document's rows: the endpoint's exposure view and, when the rows hold a version the
// caller's grants hide, the caller's view (view may be nil). It reports false when nothing is left. Rows the caller's
// view hides nothing of are filtered by the endpoint alone, so their rendering is the one every caller gets.
func filterOf(rq *formats.Request, n name.Name, view *formats.VersionView) func(p *rows.LocalPackage) bool {
	ev := viewOf(rq)
	return func(p *rows.LocalPackage) bool {
		tv := ev
		if view != nil && hidesAny(view, p) {
			tv = callerView{ev: ev, cv: view}
		}
		return tv == nil || exposeRows(tv, n.String(), p)
	}
}

// hidesAny reports whether the view hides a version of the rows.
func hidesAny(view *formats.VersionView, p *rows.LocalPackage) bool {
	for i := range p.Versions {
		if !view.Version(p.Versions[i].Version) {
			return true
		}
	}
	return false
}

// callerView joins the endpoint's view (nil: the endpoint narrows nothing) and the caller's versions into the view the
// filter reads. The caller's view belongs to the one package of the request.
type callerView struct {
	ev tagView
	cv *formats.VersionView
}

func (c callerView) Package(name string) bool { return c.ev == nil || c.ev.Package(name) }

func (c callerView) Version(name, version string) bool {
	return (c.ev == nil || c.ev.Version(name, version)) && c.cv.Version(version)
}

func (c callerView) Tag(name, tag, version string) bool {
	return (c.ev == nil || c.ev.Tag(name, tag, version)) && c.cv.Version(version)
}
