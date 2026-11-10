package ansible

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

// The handlers of the local class (R-ANS-003 to R-ANS-005, R-ANS-009): the documents are computed from the rows of the data port on every
// request and carry a weak validator of their bytes. The publish is publish.go.

func init() {
	for route, h := range map[string]func(*Format) formats.HandlerFunc{
		routeRoot:       func(f *Format) formats.HandlerFunc { return f.handleRoot },
		routeCollection: func(f *Format) formats.HandlerFunc { return f.handleCollection },
		routeVersions:   func(f *Format) formats.HandlerFunc { return f.handleVersions },
		routeVersion:    func(f *Format) formats.HandlerFunc { return f.handleVersion },
		routeArtifact:   func(f *Format) formats.HandlerFunc { return f.handleArtifact },
		routeImport:     func(f *Format) formats.HandlerFunc { return f.handleImport },
	} {
		registerHandler(formats.Local, route, h)
	}
}

// maxVersions bounds the versions of one collection a request reads when the caller's grants hide some of them and the list has to be
// filtered after it was read (R-AUTHZ-005, Q-ANS-35).
const maxVersions = 50_000

func itoa(n int) string { return strconv.Itoa(n) }

func jsonString(s string) []byte {
	b, _ := json.Marshal(s) //nolint:errcheck // a string marshals
	return b
}

func errNoCollection(name string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The collection "+name+" does not exist in this repository.")
}

func errNoVersion(name, ver string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The collection "+name+" has no version "+ver+" in this repository.")
}

// reply sends a computed document with a weak validator of its bytes and answers If-None-Match with 304 (R-ANS-004).
func reply(w http.ResponseWriter, rq *formats.Request, body []byte) error {
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("ETag", etag)
	if matches(rq.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	h.Set("Content-Length", itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return nil
}

func matches(header, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(header, ",") {
		p := strings.TrimSpace(part)
		if p == "*" || strings.TrimPrefix(p, "W/") == want {
			return true
		}
	}
	return false
}

func addrOf(rq *formats.Request) galaxydoc.Addr {
	return galaxydoc.Addr{Repo: rq.Host.Links(rq).Registry(rq.Addressed())}
}

// collectionOf normalizes the namespace and the name of a request to the name of the package: upper case is folded, anything that breaks
// the grammar is a collection that does not exist.
func collectionOf(rq *formats.Request) (name string, err error) {
	raw := rq.Params.Get(paramNamespace) + "." + rq.Params.Get(paramName)
	name, ok := normalizeName(raw)
	if !ok {
		return "", errNoCollection(strings.ToLower(raw))
	}
	return name, nil
}

// view is what a read of a collection works on: the data port and the versions of the collection the caller may read.
type view struct {
	st   Store
	repo formats.RepoRef
	name string
	// hidden is the caller's view when its grants hide some versions of the collection, nil when it may read all of them.
	hidden *formats.VersionView
}

// open authorizes the read of the collection's document and returns the view of the caller on it.
func (f *Format) open(ctx context.Context, rq *formats.Request, name string) (*view, error) {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: CollectionDocumentPath(split(name))}); err != nil {
		return nil, err
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return nil, err
	}
	cv, err := formats.CallerVersions(ctx, rq, name)
	if err != nil {
		return nil, err
	}
	return &view{st: st, repo: rq.RepoRef(), name: name, hidden: cv}, nil
}

func split(name string) (ns, n string) {
	ns, n, _ = collection.SplitName(name)
	return ns, n
}

// versions returns the page [offset, offset+limit) of the versions the caller may read, newest first, and the count of all of them.
func (v *view) versions(ctx context.Context, limit, offset int) ([]rows.VersionRow, int, error) {
	if v.hidden == nil {
		return v.st.Versions(ctx, v.repo.ID, v.name, limit, offset)
	}
	all, _, err := v.st.Versions(ctx, v.repo.ID, v.name, maxVersions, 0)
	if err != nil {
		return nil, 0, err
	}
	visible := all[:0]
	for _, r := range all {
		if v.hidden.Version(r.Version) {
			visible = append(visible, r)
		}
	}
	total := len(visible)
	if offset >= total {
		return nil, total, nil
	}
	return visible[offset:min(offset+limit, total)], total, nil
}

func (f *Format) handleRoot(_ context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return reply(w, rq, galaxydoc.RenderRoot(false))
}

// handleCollection is GET v3/collections/{ns}/{name}/ (R-ANS-004).
func (f *Format) handleCollection(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name, err := collectionOf(rq)
	if err != nil {
		return err
	}
	v, err := f.open(ctx, rq, name)
	if err != nil {
		return err
	}
	sum, found, err := v.st.Collection(ctx, v.repo.ID, name)
	if err != nil {
		return err
	}
	if v.hidden != nil && found {
		// The grants hide versions: the newest the caller may read and the times of the versions it may read are the document's.
		list, total, err := v.versions(ctx, maxVersions, 0)
		if err != nil {
			return err
		}
		found = total > 0
		if found {
			sum.HighestVersion, sum.CreatedAt, sum.UpdatedAt = list[0].Version, list[0].PublishedAt, list[0].PublishedAt
			for _, r := range list {
				if r.PublishedAt.Before(sum.CreatedAt) {
					sum.CreatedAt = r.PublishedAt
				}
				if r.PublishedAt.After(sum.UpdatedAt) {
					sum.UpdatedAt = r.PublishedAt
				}
			}
		}
	}
	if !found {
		return errNoCollection(name)
	}
	return reply(w, rq, galaxydoc.RenderCollection(sum, addrOf(rq)))
}

// pageParams reads limit and offset of a version list (R-ANS-004): limit defaults to 100 and is clamped to 1..100, a value that is no
// non-negative integer is a malformed request.
func pageParams(rq *formats.Request) (limit, offset int, err error) {
	q := rq.URL.Query()
	if s := q.Get("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil {
			return 0, 0, invalidRequest("The query parameter limit is not an integer.")
		}
	}
	if s := q.Get("offset"); s != "" {
		if offset, err = strconv.Atoi(s); err != nil || offset < 0 {
			return 0, 0, invalidRequest("The query parameter offset is not a non-negative integer.")
		}
	}
	return galaxydoc.PageLimit(limit), offset, nil
}

// handleVersions is GET v3/collections/{ns}/{name}/versions/ (R-ANS-004): newest first by precedence, paged with absolute links.
func (f *Format) handleVersions(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name, err := collectionOf(rq)
	if err != nil {
		return err
	}
	limit, offset, err := pageParams(rq)
	if err != nil {
		return err
	}
	v, err := f.open(ctx, rq, name)
	if err != nil {
		return err
	}
	list, total, err := v.versions(ctx, limit, offset)
	if err != nil {
		return err
	}
	if total == 0 {
		return errNoCollection(name)
	}
	page := make([]galaxydoc.VersionEntry, len(list))
	for i, r := range list {
		page[i] = galaxydoc.VersionEntry{Version: r.Version, RequiresAnsible: r.RequiresAnsible, CreatedAt: r.PublishedAt, UpdatedAt: r.PublishedAt}
	}
	ns, n := split(name)
	a := addrOf(rq)
	a.Namespace, a.Name = ns, n
	return reply(w, rq, galaxydoc.RenderVersionList(page, total, limit, offset, a))
}

// versionOf reads the version of a request: it must be a semantic version, else the collection has no such version.
func versionOf(rq *formats.Request, name string) (string, error) {
	ver := rq.Params.Get(paramVersion)
	if !version.Valid(ver) {
		return "", errNoVersion(name, ver)
	}
	return ver, nil
}

// handleVersion is GET v3/collections/{ns}/{name}/versions/{version}/ (R-ANS-004).
func (f *Format) handleVersion(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name, err := collectionOf(rq)
	if err != nil {
		return err
	}
	ver, err := versionOf(rq, name)
	if err != nil {
		return err
	}
	ns, n := split(name)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Version: ver, Path: ArtifactPath(ns, n, ver), Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	cv, err := formats.CallerVersions(ctx, rq, name)
	if err != nil {
		return err
	}
	if !cv.Version(ver) {
		return errNoVersion(name, ver)
	}
	row, found, err := st.Version(ctx, rq.RepoRef().ID, name, ver)
	if err != nil {
		return err
	}
	if !found {
		return errNoVersion(name, ver)
	}
	return reply(w, rq, galaxydoc.RenderVersion(galaxydoc.Version{
		Namespace: ns, Name: n, Version: row.Version, RequiresAnsible: row.RequiresAnsible, SHA256: row.SHA256, Size: row.Size,
		Dependencies: row.Dependencies, Info: row.CollectionInfo, CreatedAt: row.PublishedAt, UpdatedAt: row.PublishedAt,
		CollectionID: rows.CollectionUUID(row.RepoID, name),
	}, addrOf(rq)))
}

// handleArtifact is GET v3/artifacts/collections/{file} (R-ANS-005): the stored archive with the streaming, range and conditional rules
// of the core, after the read permission on the version is decided. A name that does not parse, a version that is not stored and one the
// caller's grants hide answer 404.
func (f *Format) handleArtifact(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	file := rq.Params.Get(paramFile)
	ns, n, ver, ok := collection.ParseFileName(file)
	if !ok || !version.Valid(ver) {
		return errs.E(errs.NotFound, errs.CodeNotFound, "The file "+file+" is not the archive of a collection version.")
	}
	name := ns + "." + n
	path := ArtifactPath(ns, n, ver)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Version: ver, Path: path, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	cv, err := formats.CallerVersions(ctx, rq, name)
	if err != nil {
		return err
	}
	if !cv.Version(ver) {
		return errNoVersion(name, ver)
	}
	it, found, err := rq.Host.Items().Stat(ctx, rq.RepoRef(), formats.Path{Path: path})
	if err != nil {
		return err
	}
	if !found || it.Folder {
		return errNoVersion(name, ver)
	}
	return rq.Host.Items().Serve(ctx, rq, mediaTypeWriter{ResponseWriter: w, mediaType: archiveMediaType}, it, formats.ServeMayRevalidate)
}

// mediaTypeWriter sets the Content-Type of the archive on the status line of a response that carries it: the type of R-ANS-005 is the
// protocol's, whatever the item row recorded.
type mediaTypeWriter struct {
	http.ResponseWriter
	mediaType string
}

func (w mediaTypeWriter) WriteHeader(code int) {
	switch code {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
		w.Header().Set("Content-Type", w.mediaType)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w mediaTypeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// handleImport is GET v3/imports/collections/{id}/ (R-ANS-009): the task of a publish, final when it was created.
func (f *Format) handleImport(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	t, found, err := st.Task(ctx, rq.RepoRef().ID, rq.Params.Get(paramID))
	if err != nil {
		return err
	}
	if !found {
		return errs.E(errs.NotFound, errs.CodeNotFound, "No import task "+rq.Params.Get(paramID)+" exists in this repository; a task is kept for a limited time.")
	}
	return reply(w, rq, galaxydoc.RenderTask(t))
}
