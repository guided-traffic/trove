package ansible

import (
	"cmp"
	"context"
	"net/http"
	"slices"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// The route ansible.version.docs, GET v3/collections/{ns}/{name}/versions/{version}/docs-blob/ (R-ANS-035): the documentation of a version
// in the Galaxy's shape. A local repository renders it from its rows, a virtual repository from the first member that has it (R-ANS-041),
// a remote repository serves the upstream's document as a stored metadata document (handlers_remote_docs.go). Every one of them asks the
// wiki check before it reads a row or a stored document (R-ANS-037) and answers an absent resource when the check says no.

func init() {
	registerHandler(formats.Local, routeDocs, func(f *Format) formats.HandlerFunc { return f.counted(f.handleDocs) })
	registerHandler(formats.Virtual, routeDocs, func(f *Format) formats.HandlerFunc { return f.counted(f.virtualDocs) })
}

// counted counts the outcome of a handler of the route in trove_ansible_docs_read_total (R-ANS-045).
func (f *Format) counted(h formats.HandlerFunc) formats.HandlerFunc {
	return func(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
		err := h(ctx, rq, w)
		f.countRead(rq.Host, err)
		return err
	}
}

func errNoDocs(name, ver string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The collection "+name+" "+ver+" has no documentation in this repository.")
}

// docsTarget is the target of the wiki check and of the read decision of a version's documentation.
func docsTarget(ns, name, ver string) formats.Target {
	return formats.Target{Package: ns + "." + name, Version: ver, Path: ArtifactPath(ns, name, ver), Actions: []formats.Action{formats.ActionRead}}
}

// renderDocs renders the document of a set under the Host's renderer. A Host without one fails the request: nothing unrendered is served.
func renderDocs(rq *formats.Request, set rows.DocSet) ([]byte, error) {
	body, err := galaxydoc.RenderDocsBlob(set, rq.Host.Markup())
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The documentation of the version cannot be rendered on this replica.", errs.WithCause(err))
	}
	return body, nil
}

// handleDocs is the local class: the document is rendered from the rows of the data port on every request and carries a weak validator
// of its bytes.
func (f *Format) handleDocs(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name, err := collectionOf(rq)
	if err != nil {
		return err
	}
	ver, err := versionOf(rq, name)
	if err != nil {
		return err
	}
	ns, n := split(name)
	t := docsTarget(ns, n, ver)
	if err := rq.Host.Authorize(ctx, rq, t); err != nil {
		return err
	}
	if !wikiReadable(ctx, rq, t) {
		return errNoVersion(name, ver)
	}
	cv, err := formats.CallerVersions(ctx, rq, name)
	if err != nil {
		return err
	}
	if !cv.Version(ver) {
		return errNoVersion(name, ver)
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	set, found, err := st.DocSet(ctx, rq.RepoRef().ID, name, ver)
	if err != nil {
		return err
	}
	if !found {
		return errNoDocs(name, ver)
	}
	body, err := renderDocs(rq, set)
	if err != nil {
		return err
	}
	return reply(w, rq, body)
}

// virtualDocs serves the documentation of the first member that has it, in the order of the plan (R-ANS-041). The walk is the format's
// own and not the merged-metadata cache: the wiki check depends on the caller, and a document cached for one caller must not reach another.
func (f *Format) virtualDocs(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	ns, n, err := collectionParams(rq)
	if err != nil {
		return err
	}
	ver := rq.Params.Get(paramVersion)
	name := ns + "." + n
	if !versionValid(ver) {
		return errNoSuchVersion()
	}
	t := docsTarget(ns, n, ver)
	if err := rq.Host.Authorize(ctx, rq, t); err != nil {
		return err
	}
	if !wikiReadable(ctx, rq, t) {
		return errNoSuchVersion()
	}
	if err := f.versionVisible(ctx, rq, w, ns, n, ver); err != nil {
		return err
	}
	p := formats.Path{Path: DocsDocumentPath(ns, n, ver)}
	plan, err := rq.Host.Resolver().Plan(ctx, rq, p, f.Classify(p))
	if err != nil {
		return err
	}
	if !plan.Admitted {
		return errNoSuchVersion()
	}
	// The members are tried by tier and then by position, as the first match of R-ANS-022 is: a local member before a remote one.
	members := slices.Clone(plan.Members)
	slices.SortStableFunc(members, func(a, b formats.PlannedMember) int {
		return cmp.Or(cmp.Compare(a.Tier, b.Tier), cmp.Compare(a.Position, b.Position))
	})
	for _, pm := range members {
		mrq, err := memberRequest(rq, pm.Member.Name)
		if err != nil {
			return err
		}
		if !wikiReadable(ctx, mrq, t) {
			continue
		}
		set, found, err := f.memberDocs(ctx, mrq, pm.Member, ns, n, ver)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		body, err := renderDocs(rq, set)
		if err != nil {
			return err
		}
		return writeDocument(rq, w, body)
	}
	return errNoDocs(name, ver)
}

// memberDocs reads the documentation of a version from one member: the rows of a local one, the stored document of a remote one (through the
// engine, which contacts the upstream only for a caller who may fill the cache, R-AUTHZ-008).
func (f *Format) memberDocs(ctx context.Context, mrq *formats.Request, member formats.RepoRef, ns, n, ver string) (rows.DocSet, bool, error) {
	name := ns + "." + n
	switch member.Class {
	case formats.Local:
		st, err := storeOf(mrq.Host.FormatData(ID))
		if err != nil {
			return rows.DocSet{}, false, err
		}
		return st.DocSet(ctx, member.ID, name, ver)
	case formats.Remote:
		return f.remoteDocSet(ctx, mrq, ns, n, ver)
	}
	return rows.DocSet{}, false, nil
}
