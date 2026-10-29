package v1

import (
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The items operations of R-API-025 (R-STOR-016 to R-STOR-018, R-STOR-015): the
// description of a file or folder, the listing of a folder, the download
// statistics and the deletion. The operations declare the repository action on the
// path (read, delete), which the shared step decides before the handler runs; the
// service decides again for the entries of a listing and the items of a subtree.
// The raw transfer under /files/ is internal/api/rawfiles.

// CallerFrom builds the caller of a request for the files service: the subject the
// decision point evaluates, the endpoint (the exposure of an endpoint is a ceiling,
// R-EP-013), and the actor and source of the audit events.
func CallerFrom(r *http.Request) files.Caller {
	ctx := r.Context()
	p := httpserver.PrincipalFrom(ctx)
	c := files.Caller{
		Subject: SubjectFrom(p),
		Actor: audit.Actor{
			Kind: string(p.Kind), ID: p.ID, Name: p.Name,
			TokenID: p.Credential.TokenID, AuthMethod: string(p.Credential.Method),
		},
		Source: auditSource(r),
	}
	if ep := httpserver.EndpointFrom(ctx); ep != nil {
		c.Endpoint = ep.Name()
	}
	return c
}

// WriteJSON answers a JSON body; the files transfer uses it for the item
// description it returns from /files/.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) { writeJSON(w, r, status, v) }

// InvalidCursor is the 400 of a cursor that does not belong to the request.
func InvalidCursor() error { return invalidCursor() }

// WriteDeleteResult answers a delete of the files service: 204, or 202 with the
// job to poll and its location.
func WriteDeleteResult(w http.ResponseWriter, r *http.Request, res files.DeleteResult) {
	if res.Done {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Location", jobLocation(r, res.JobID))
	writeJSON(w, r, http.StatusAccepted, gen.JobReferenceEnvelope{Job: jobReference(res.JobID, files.DeleteJobKind, res.JobState)})
}

func (s *Server) filesService() (*files.Service, error) {
	if s.o.Files == nil {
		return nil, unavailable("The files service")
	}
	return s.o.Files, nil
}

func (s *Server) links(r *http.Request) files.Links {
	return files.Links{Base: httpserver.InfoFrom(r.Context()).BaseURL}
}

// itemPath validates the path parameter of an items operation: an item path
// without a trailing slash, empty for the root (R-API-004).
func itemPath(p *string) (string, error) {
	if p == nil {
		return "", nil
	}
	return CheckItemPath(*p, PathOptions{AllowRoot: true})
}

// GetItem implements GET /api/v1/repositories/{repoKey}/items (R-STOR-016).
func (s *Server) GetItem(w http.ResponseWriter, r *http.Request, repoKey string, params gen.GetItemParams) {
	svc, err := s.filesService()
	var path string
	if err == nil {
		path, err = itemPath(params.Path)
	}
	var it files.Item
	if err == nil {
		it, err = svc.ItemInfo(r.Context(), CallerFrom(r), repoKey, path)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, it.View(s.links(r)))
}

// DeleteItem implements DELETE /api/v1/repositories/{repoKey}/items (R-STOR-018).
func (s *Server) DeleteItem(w http.ResponseWriter, r *http.Request, repoKey string, params gen.DeleteItemParams) {
	svc, err := s.filesService()
	var path string
	if err == nil {
		path, err = itemPath(params.Path)
	}
	var res files.DeleteResult
	if err == nil {
		res, err = svc.Delete(r.Context(), CallerFrom(r), files.DeleteRequest{Repo: repoKey, Path: path})
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	WriteDeleteResult(w, r, res)
}

// childrenListSpec declares the parameters of the listing; its page sizes are
// those of R-STOR-017, not the general ones.
func childrenListSpec() ListSpec {
	return ListSpec{Filters: []string{"path", "recursive", "maxDepth", "folders"}}
}

func boolParam(lq ListQuery, name string, def bool) (bool, error) {
	switch lq.First(name) {
	case "":
		return def, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, badParam(name, "must be true or false")
}

// ListChildren implements GET /api/v1/repositories/{repoKey}/children (R-STOR-017).
func (s *Server) ListChildren(w http.ResponseWriter, r *http.Request, repoKey string, _ gen.ListChildrenParams) {
	svc, err := s.filesService()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	cfg := s.o.API.Config
	cfg.PageSizeDefault, cfg.PageSizeMax = 1000, 10_000
	lq, err := ParseList(r, childrenListSpec(), cfg)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	cq := files.ChildrenQuery{Limit: lq.Limit}
	if cq.Path, err = itemPath(optional(lq.Filter["path"])); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if cq.Recursive, err = boolParam(lq, "recursive", false); err == nil {
		cq.Folders, err = boolParam(lq, "folders", true)
	}
	if err == nil {
		if v := lq.First("maxDepth"); v != "" {
			n, perr := strconv.Atoi(v)
			if perr != nil || n < 0 {
				err = badParam("maxDepth", "must be an integer of 0 or more")
			}
			cq.MaxDepth = n
		}
	}
	if err == nil && len(lq.After) > 1 {
		err = invalidCursor()
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if len(lq.After) == 1 {
		cq.After = lq.After[0]
	}
	page, err := svc.Children(r.Context(), CallerFrom(r), repoKey, cq)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := Page[files.Child]{Items: page.Items}
	if page.Next != "" {
		c := lq.EncodeCursor([]string{page.Next})
		out.NextCursor = &c
	}
	WritePage(w, r, out)
}

func optional(v []string) *string {
	if len(v) == 0 {
		return nil
	}
	return &v[0]
}

// GetItemStats implements GET /api/v1/repositories/{repoKey}/stats (R-STOR-015).
func (s *Server) GetItemStats(w http.ResponseWriter, r *http.Request, repoKey string, params gen.GetItemStatsParams) {
	svc, err := s.filesService()
	var path string
	if err == nil {
		path, err = CheckItemPath(params.Path, PathOptions{})
	}
	var st files.StatsInfo
	if err == nil {
		st, err = svc.Stats(r.Context(), CallerFrom(r), repoKey, path)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := struct {
		Downloads      int64   `json:"downloads"`
		LastDownloadAt *string `json:"lastDownloadAt"`
		LastDownloader *string `json:"lastDownloader"`
	}{Downloads: st.Downloads}
	if st.LastDownloadAt != nil {
		t := st.LastDownloadAt.UTC().Format("2006-01-02T15:04:05Z")
		out.LastDownloadAt = &t
	}
	if st.LastDownloader != "" {
		out.LastDownloader = &st.LastDownloader
	}
	writeJSON(w, r, http.StatusOK, out)
}
