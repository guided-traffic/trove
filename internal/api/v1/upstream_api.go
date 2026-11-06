package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/httpserver"
)

// The upstream-test operations of R-REPO-024. The saved test needs repositories:write-config or, for an
// Owner of the project, the project; the unsaved test of a project needs the same. The egress rules of the
// installation apply to a probe whoever sends it: they are checked when the connection is made. The
// handlers are thin: the repository service validates the section and the prober in internal/proxy/upstream
// sends the one request; a probe that fails is a result, not an error, so the answer is 200 whatever the
// upstream does and the body says what happened.

// TestRepositoryUpstream implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/upstream-test.
func (s *Server) TestRepositoryUpstream(w http.ResponseWriter, r *http.Request, projectKey, repoKeyText string) {
	key := qname.Repo{Project: projectKey, Key: repoKeyText}
	svc, err := s.repositoryService()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	res, err := svc.UpstreamTest(r.Context(), key)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, res)
}

// TestUpstream implements POST /api/v1/projects/{projectKey}/upstream-tests.
func (s *Server) TestUpstream(w http.ResponseWriter, r *http.Request, _ string) {
	svc, err := s.repositoryService()
	var body []byte
	if err == nil {
		body, err = ReadBody(w, r, MediaJSON, int64(s.o.API.Config.MaxConfigurationBodyBytes))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	res, err := svc.UpstreamTestUnsaved(r.Context(), repositoryCaller(r), body)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, res)
}
