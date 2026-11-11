package ansible

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

func init() {
	registerHandler(formats.Remote, routeDocs, func(f *Format) formats.HandlerFunc { return f.counted(f.remoteDocs) })
}

// remoteDocs is the remote class: the route is an ordinary metadata document of the remote (R-ANS-035). The engine fetches the upstream's
// docs-blob document, runs PrepareFill and OnFill (which write the rows) and stores it as received; the answer is rendered from the
// parsed document through the Host's sanitizer, so the stored document keeps what the upstream sent and nothing unsanitized is served.
func (f *Format) remoteDocs(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	ns, name, err := remoteCollectionOf(rq)
	if err != nil {
		return err
	}
	ver := rq.Params.Get(paramVersion)
	if !version.Valid(ver) {
		return missing()
	}
	t := docsTarget(ns, name, ver)
	t.Path = DocsDocumentPath(ns, name, ver)
	if err := rq.Host.Authorize(ctx, rq, t); err != nil {
		return err
	}
	if !wikiReadable(ctx, rq, t) {
		return missing()
	}
	set, found, err := f.remoteDocSet(ctx, rq, ns, name, ver)
	if err != nil {
		return err
	}
	if !found {
		return errNoDocs(ns+"."+name, ver)
	}
	body, err := renderDocs(rq, set)
	if err != nil {
		return err
	}
	return writeDocument(rq, w, body)
}

// remoteDocSet reads the documentation of a version of a remote repository: through the engine, which serves the stored document while it is
// fresh and fetches it otherwise for a caller who may fill the cache (R-AUTHZ-008). found is false when the repository does not fetch
// documentation (ansible.fetchDocumentation), when the upstream has none, or when the version document named no usable address. A failure
// of the upstream is recorded in the fetch row of the version and returned.
func (f *Format) remoteDocSet(ctx context.Context, rq *formats.Request, ns, name, ver string) (rows.DocSet, bool, error) {
	if !SettingsOf(rq.Repo.Config).FetchDocumentation {
		return rows.DocSet{}, false, nil
	}
	pkg := ns + "." + name
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return rows.DocSet{}, false, err
	}
	repo := rq.RepoRef().ID
	if state, err := st.DocState(ctx, repo, pkg, ver); err != nil {
		return rows.DocSet{}, false, err
	} else if state.NoneKnown {
		return rows.DocSet{}, false, nil
	}
	doc, err := rq.Host.Remote().Metadata(ctx, rq, formats.Path{Path: DocsDocumentPath(ns, name, ver)}, documentVariant)
	if err != nil {
		if errs.KindOf(err) == errs.NotFound {
			_ = st.SetDocFetch(ctx, nil, repo, pkg, ver, rows.DocFetchNone, "") //nolint:errcheck // a note for the screens; the answer is the 404
			f.countFill(rq.Host.FormatData(ID), fillNone)
			return rows.DocSet{}, false, nil
		}
		// A failure to fetch or to use the document is the state unavailable; the previous documentation, if any, stays.
		reason := galaxydoc.FetchFailure(err)
		_ = st.SetDocFetch(ctx, nil, repo, pkg, ver, rows.DocFetchUnavailable, reason) //nolint:errcheck // the answer is the error
		f.countFill(rq.Host.FormatData(ID), reason)
		return rows.DocSet{}, false, err
	}
	if doc.Status.Negative {
		_ = st.SetDocFetch(ctx, nil, repo, pkg, ver, rows.DocFetchNone, "") //nolint:errcheck // a note for the screens; the answer is the 404
		f.countFill(rq.Host.FormatData(ID), fillNone)
		return rows.DocSet{}, false, nil
	}
	raw, err := doc.Bytes(ctx)
	if err != nil {
		return rows.DocSet{}, false, err
	}
	set, err := galaxydoc.ParseUpstreamDocs(raw)
	if err != nil {
		return rows.DocSet{}, false, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The stored documentation of the upstream cannot be used.", errs.WithCause(err))
	}
	return set, true, nil
}
