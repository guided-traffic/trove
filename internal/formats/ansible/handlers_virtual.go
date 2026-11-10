package ansible

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/merge"
	"github.com/guided-traffic/trove/internal/qname"
)

// The Galaxy API of a virtual repository (R-ANS-022 to R-ANS-024). Every handler decides the read on the package first; the members the
// caller may not read take no part, silently (R-ANS-023, R-AUTHZ-009), which the virtual engine's plan does before any merge. The
// routes whose class list names the virtual class are the ones registered here; a publish is handed to the deployment target.
func init() {
	for route, h := range map[string]func(*Format) formats.HandlerFunc{
		routeRoot:       func(f *Format) formats.HandlerFunc { return f.virtualRoot },
		routeCollection: func(f *Format) formats.HandlerFunc { return f.virtualCollection },
		routeVersions:   func(f *Format) formats.HandlerFunc { return f.virtualVersions },
		routeVersion:    func(f *Format) formats.HandlerFunc { return f.virtualVersion },
		routeArtifact:   func(f *Format) formats.HandlerFunc { return f.virtualArtifact },
		routePublish:    func(f *Format) formats.HandlerFunc { return f.virtualPublish },
		routeImport:     func(f *Format) formats.HandlerFunc { return f.virtualImport },
		routeRolesFind:  func(f *Format) formats.HandlerFunc { return f.virtualRolesFind },

		routeRoleVersions: func(f *Format) formats.HandlerFunc { return f.virtualRoleVersions },
		routeRoleArchive:  func(f *Format) formats.HandlerFunc { return f.virtualRoleArchive },
	} {
		registerHandler(formats.Virtual, route, h)
	}
}

func errNoSuchCollection() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "No member of this repository holds the collection.")
}

func errNoSuchVersion() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "No member of this repository holds the version.")
}

// partialError is the answer to a merge that left out a member that could not be asked: partial results are never cached and never
// served as complete (R-ANS-023, R-REPO-020).
func partialError() error {
	return errs.E(errs.Unavailable, errs.CodeUnavailable,
		"A remote member of this repository could not be asked, so the list of versions would be incomplete. Try again.",
		errs.WithRetryAfter(time.Second), errs.WithHeader("Cache-Status", "detail=partial"))
}

// collectionParams reads and validates the collection a request names; a name outside the grammar is a missing collection.
func collectionParams(rq *formats.Request) (ns, name string, err error) {
	ns = strings.ToLower(rq.Params.Get(paramNamespace))
	name = strings.ToLower(rq.Params.Get(paramName))
	if !collection.ValidPart(ns) || !collection.ValidPart(name) {
		return "", "", errNoSuchCollection()
	}
	return ns, name, nil
}

// writeDocument answers a rendered document with a weak validator that answers If-None-Match with 304 (R-ANS-004).
func writeDocument(rq *formats.Request, w http.ResponseWriter, body []byte) error {
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Content-Type", "application/json")
	for _, c := range strings.Split(rq.Header.Get("If-None-Match"), ",") {
		if c = strings.TrimSpace(c); c == etag || c == "*" {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return nil
}

// authorizeRead decides the read of a path of a package on the virtual repository.
func authorizeRead(ctx context.Context, rq *formats.Request, pkg, ver, path string) error {
	return rq.Host.Authorize(ctx, rq, formats.Target{Package: pkg, Version: ver, Path: path, Actions: []formats.Action{formats.ActionRead}})
}

// versionFilter decides which versions of a collection the caller may read when its grants decide versions by path (R-AUTHZ-005,
// Q-AUTHZ-255): a path pattern that hides the archive of a version hides the version in every document of the virtual repository. A
// version is decided on the member that serves it, the winner of the merge, as the read of its archive is (R-ANS-023). A nil filter
// hides nothing.
type versionFilter struct {
	scope   formats.ReadScope
	project string
	name    string
	seen    map[[2]string]bool
}

// callerFilter returns the filter of the request's caller on a collection, nil when no member's grants decide versions by path: then
// every version of a readable collection is readable and no per-version decision is asked.
func (f *Format) callerFilter(ctx context.Context, rq *formats.Request, name string) (*versionFilter, error) {
	members := rq.Repo.Members()
	keys := make([]qname.Repo, len(members))
	for i, m := range members {
		keys[i] = m.Name
	}
	scope, err := rq.Host.Resolver().ReadScope(ctx, rq, keys)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if scope.Shape(k) != formats.ReadAll && scope.VersionsByPath(k) {
			return &versionFilter{scope: scope, project: rq.Repo.Name.Project, name: name, seen: map[[2]string]bool{}}, nil
		}
	}
	return nil, nil
}

// allows reports whether the caller may read a version served by the member.
func (v *versionFilter) allows(member, ver string) bool {
	if v == nil {
		return true
	}
	k := [2]string{member, ver}
	ok, seen := v.seen[k]
	if !seen {
		key := qname.Repo{Project: v.project, Key: member}
		ok = v.scope.Shape(key) == formats.ReadAll || !v.scope.VersionsByPath(key) || v.scope.MayRead(key, v.name, ver)
		v.seen[k] = ok
	}
	return ok
}

// mergedList reads the merged version list of a collection through the engine's merged-metadata cache, which checks the revision of
// every local member on each hit, so a publish to a member is visible at once on every replica (R-ARCH-015). The list is whole: the
// caller's view of it is [versionFilter.apply].
func (f *Format) mergedList(ctx context.Context, rq *formats.Request, w http.ResponseWriter, ns, name string) (listDoc, error) {
	p := formats.Path{Path: VersionsDocumentPath(ns, name)}
	m, err := rq.Host.Resolver().Metadata(ctx, rq, p, f.Classify(p), variantFirstPage)
	if err != nil {
		return listDoc{}, err
	}
	if m.Partial {
		return listDoc{}, partialError()
	}
	if m.CacheStatus != "" {
		w.Header().Set("Cache-Status", m.CacheStatus)
	}
	l, err := decodeList(m.Bytes)
	if err != nil {
		return l, err
	}
	if len(l.Entries) == 0 {
		return l, errNoSuchCollection()
	}
	return l, nil
}

// apply drops the versions the caller may not read; hidden says that it dropped any. A collection of which the caller may read no
// version does not exist for it.
func (v *versionFilter) apply(l listDoc) (out listDoc, hidden bool, err error) {
	if v == nil {
		return l, false, nil
	}
	out.Entries = make([]listEntry, 0, len(l.Entries))
	for _, e := range l.Entries {
		if v.allows(e.Member, e.Version) {
			out.Entries = append(out.Entries, e)
		}
	}
	if len(out.Entries) == 0 {
		return out, true, errNoSuchCollection()
	}
	return out, len(out.Entries) != len(l.Entries), nil
}

// mergedVersions is the merged list as the caller may read it.
func (f *Format) mergedVersions(ctx context.Context, rq *formats.Request, w http.ResponseWriter, ns, name string) (l listDoc, hidden bool, err error) {
	if l, err = f.mergedList(ctx, rq, w, ns, name); err != nil {
		return l, false, err
	}
	flt, err := f.callerFilter(ctx, rq, ns+"."+name)
	if err != nil {
		return l, false, err
	}
	return flt.apply(l)
}

// virtualVersions is the version list of a collection (R-ANS-022): the union of the members' versions, newest first, cut into the
// client's page after the merge.
func (f *Format) virtualVersions(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	ns, name, err := collectionParams(rq)
	if err != nil {
		return err
	}
	if err := authorizeRead(ctx, rq, ns+"."+name, "", VersionsDocumentPath(ns, name)); err != nil {
		return err
	}
	l, _, err := f.mergedVersions(ctx, rq, w, ns, name)
	if err != nil {
		return err
	}
	limit, offset, err := pageParams(rq)
	if err != nil {
		return err
	}
	all := make([]merge.VersionEntry, len(l.Entries))
	for i, e := range l.Entries {
		all[i] = e.galaxy()
	}
	a := addrOf(rq)
	a.Namespace, a.Name = ns, name
	return writeDocument(rq, w, galaxydoc.RenderVersionList(merge.Page(all, limit, offset), len(all), limit, offset, a))
}

// virtualCollection is the collection document (R-ANS-022): the highest version of the merged list, the earliest creation and the
// latest update of the members.
func (f *Format) virtualCollection(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	ns, name, err := collectionParams(rq)
	if err != nil {
		return err
	}
	pkg := ns + "." + name
	if err := authorizeRead(ctx, rq, pkg, "", CollectionDocumentPath(ns, name)); err != nil {
		return err
	}
	l, hidden, err := f.mergedVersions(ctx, rq, w, ns, name)
	if err != nil {
		return err
	}
	p := formats.Path{Path: CollectionDocumentPath(ns, name)}
	m, err := rq.Host.Resolver().Metadata(ctx, rq, p, f.Classify(p), documentVariant)
	if err != nil && errs.KindOf(err) != errs.NotFound {
		return err
	}
	var t timesDoc
	if err == nil {
		if m.Partial {
			return partialError()
		}
		if err := json.Unmarshal(m.Bytes, &t); err != nil {
			return internalDoc("A merged collection document of the Ansible format does not decode.", err)
		}
	}
	all := make([]merge.VersionEntry, len(l.Entries))
	for i, e := range l.Entries {
		all[i] = e.galaxy()
	}
	times := []merge.Times{{CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}}
	if hidden {
		// The caller's grants hide versions: the times of the versions it may read are the document's, as in a local repository.
		times = times[:0]
		for _, e := range l.Entries {
			times = append(times, merge.Times{CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt})
		}
	}
	c := merge.MergeCollection(all, times)
	sum := galaxydoc.CollectionSummary{Namespace: ns, Name: name, HighestVersion: c.HighestVersion, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	return writeDocument(rq, w, galaxydoc.RenderCollection(sum, addrOf(rq)))
}

// virtualVersion is the document of one version: the first member that holds it (R-ANS-022), rendered neutrally for the repository the
// client addressed, so no address of a member or an upstream reaches the client.
func (f *Format) virtualVersion(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	ns, name, err := collectionParams(rq)
	if err != nil {
		return err
	}
	ver := rq.Params.Get(paramVersion)
	pkg := ns + "." + name
	path := VersionDocumentPath(ns, name, ver)
	if !versionValid(ver) {
		return errNoSuchVersion()
	}
	// The read of a version is decided on the path of its archive, as the local class does: a pattern that hides the file hides the version.
	if err := authorizeRead(ctx, rq, pkg, ver, ArtifactPath(ns, name, ver)); err != nil {
		return err
	}
	if err := f.versionVisible(ctx, rq, w, ns, name, ver); err != nil {
		return err
	}
	p := formats.Path{Path: path}
	m, err := rq.Host.Resolver().Metadata(ctx, rq, p, f.Classify(p), documentVariant)
	if err != nil {
		if errs.KindOf(err) == errs.NotFound {
			return errNoSuchVersion()
		}
		return err
	}
	v, _, err := galaxydoc.ParseUpstreamVersion(m.Bytes)
	if err != nil {
		return errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "A member's version document is not a document the registry reads.", errs.WithCause(err))
	}
	if v.Namespace != ns || v.Name != name || v.Version != ver {
		return errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "A member's version document names another collection or version than the one asked for.")
	}
	if m.CacheStatus != "" {
		w.Header().Set("Cache-Status", m.CacheStatus)
	}
	return writeDocument(rq, w, galaxydoc.RenderVersion(v, addrOf(rq)))
}

// virtualArtifact serves the archive of the first member that holds it (R-ANS-023): the tiers of R-REPO-019 step 6, with the headers
// of R-ANS-005.
func (f *Format) virtualArtifact(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	ns, name, ver, ok := collection.ParseFileName(rq.Params.Get(paramFile))
	if !ok || !versionValid(ver) {
		return errNoSuchVersion()
	}
	path := ArtifactPath(ns, name, ver)
	if err := authorizeRead(ctx, rq, ns+"."+name, ver, path); err != nil {
		return err
	}
	p := formats.Path{Path: path}
	return rq.Host.Resolver().Artifact(ctx, rq, p, f.Classify(p), w)
}

// virtualRoot is the discovery document (R-ANS-003): it lists v1 when the upstream of any remote member does (R-ANS-022).
func (f *Format) virtualRoot(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return writeDocument(rq, w, galaxydoc.RenderRoot(f.memberHasV1(ctx, rq)))
}

// memberHasV1 asks the stored discovery document of each remote member the caller may read. A member whose document is not stored and
// may not be fetched by this caller, or cannot be asked, does not list v1 for this answer.
func (f *Format) memberHasV1(ctx context.Context, rq *formats.Request) bool {
	members, err := f.remoteMembers(ctx, rq, RootDocument)
	if err != nil {
		return false
	}
	for _, m := range members {
		doc, err := m.rq.Host.Remote().Metadata(ctx, m.rq, formats.Path{Path: RootDocument}, documentVariant)
		if err != nil || doc.Status.Negative {
			continue
		}
		raw, err := doc.Bytes(ctx)
		if err != nil {
			continue
		}
		var root struct {
			Available map[string]json.RawMessage `json:"available_versions"`
		}
		if json.Unmarshal(raw, &root) == nil {
			if _, ok := root.Available["v1"]; ok {
				return true
			}
		}
	}
	return false
}

// versionVisible answers errNoSuchVersion for a version the caller's grants hide. It asks nothing of the engine when no member's grants
// decide versions by path.
func (f *Format) versionVisible(ctx context.Context, rq *formats.Request, w http.ResponseWriter, ns, name, ver string) error {
	flt, err := f.callerFilter(ctx, rq, ns+"."+name)
	if err != nil || flt == nil {
		return err
	}
	l, err := f.mergedList(ctx, rq, w, ns, name)
	if err != nil {
		if errs.KindOf(err) == errs.NotFound {
			return errNoSuchVersion()
		}
		return err
	}
	for _, e := range l.Entries {
		if merge.Compare(e.Version, ver) == 0 {
			if !flt.allows(e.Member, e.Version) {
				return errNoSuchVersion()
			}
			return nil
		}
	}
	return nil
}
