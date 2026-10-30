package v1

import (
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
)

// The items operations of R-API-025 (R-STOR-016 to R-STOR-018, R-STOR-015): the
// description of a file or folder, the listing of a folder, the download
// statistics and the deletion. The operations declare the repository action on the
// path (read, delete), which the shared step decides before the handler runs; the
// service decides again for the entries of a listing and the items of a subtree.
// The raw transfer under /files/ is internal/api/rawfiles.

// filesCaller builds the caller of a request for the files service: the holder of
// the index the request decides on, which the shared step of the operation began
// (edge.AccessFrom; R-AUTHZ-015) or, for a request that reaches the handler
// without it, a holder begun here with the repository key made known; the subject
// behind it, whose endpoint is the ceiling of every decision (R-EP-013); and the
// actor and source of the audit events.
func (s *Server) filesCaller(r *http.Request, repoKey string) (files.Caller, error) {
	c, _, err := s.filesAccess(r, repoKey)
	return c, err
}

// filesAccess is filesCaller with the holder of the request, for an operation
// that asks it more than the files service does.
func (s *Server) filesAccess(r *http.Request, repoKey string) (files.Caller, *edge.Access, error) {
	ctx := r.Context()
	if _, ok := edge.AccessFrom(ctx); !ok && s.o.FilesDecider == nil {
		return files.Caller{}, nil, unavailable("The files service")
	}
	acc, err := edge.Resolve(ctx, s.o.FilesDecider, edge.SubjectFrom(r), repoKey)
	if err != nil {
		return files.Caller{}, nil, err
	}
	return files.Caller{
		Access: acc, Subject: acc.Subject,
		Actor: edge.ActorFrom(httpserver.PrincipalFrom(ctx)), Source: auditSource(r),
	}, acc, nil
}

// WriteDeleteResult answers a delete of the files service: 204, or 202 with the
// job to poll and its location.
func WriteDeleteResult(w http.ResponseWriter, r *http.Request, res items.DeleteResult) {
	if res.Done {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	edge.WriteJob(w, r, res.JobID, items.DeleteJobKind, res.JobState)
}

func (s *Server) filesService() (*files.Service, error) {
	if s.o.Files == nil {
		return nil, unavailable("The files service")
	}
	return s.o.Files, nil
}

func (s *Server) links(r *http.Request) edge.Links {
	return edge.Links{Base: httpserver.InfoFrom(r.Context()).BaseURL}
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
	var it items.Item
	if err == nil {
		var c files.Caller
		if c, err = s.filesCaller(r, repoKey); err == nil {
			it, err = svc.ItemInfo(r.Context(), c, repoKey, path)
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, edge.DescribeItem(it, s.links(r)))
}

// DeleteItem implements DELETE /api/v1/repositories/{repoKey}/items (R-STOR-018).
// It deletes as DELETE /files/ does, the repository's patterns and its disabled
// flag included, for every caller but a holder of content:bypass, whose cleanup
// removes items stored before a pattern change and items of a disabled
// repository (R-REPO-005, Q-AUTHZ-162). Package ownership applies to every caller
// alike; content:bypass passes it as it passes every ownership check.
func (s *Server) DeleteItem(w http.ResponseWriter, r *http.Request, repoKey string, params gen.DeleteItemParams) {
	svc, err := s.filesService()
	var path string
	if err == nil {
		path, err = itemPath(params.Path)
	}
	var res items.DeleteResult
	if err == nil {
		var c files.Caller
		var acc *edge.Access
		if c, acc, err = s.filesAccess(r, repoKey); err == nil {
			cleanup := acc.Holds(string(authz.ContentBypass))
			res, err = svc.Delete(r.Context(), c, files.DeleteRequest{Repo: repoKey, Path: path, Strict: !cleanup})
		}
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
	caller, err := s.filesCaller(r, repoKey)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	page, err := svc.Children(r.Context(), caller, repoKey, cq)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := Page[items.Child]{Items: page.Items}
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
	var st items.DownloadStats
	if err == nil {
		var c files.Caller
		if c, err = s.filesCaller(r, repoKey); err == nil {
			st, err = svc.Stats(r.Context(), c, repoKey, path)
		}
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
