package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/httpserver"
)

// The upstream-test operations of R-REPO-024. Both need repositories:write-config,
// which the shared step enforces before a handler runs. The handlers are thin: the
// repository service validates the section and the prober in internal/proxy/upstream
// sends the one request; a probe that fails is a result, not an error, so the
// answer is 200 whatever the upstream does and the body says what happened.

// TestRepositoryUpstream implements POST /api/v1/repositories/{key}/upstream-test.
func (s *Server) TestRepositoryUpstream(w http.ResponseWriter, r *http.Request, key string) {
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

// TestUpstream implements POST /api/v1/upstream-tests.
func (s *Server) TestUpstream(w http.ResponseWriter, r *http.Request) {
	svc, err := s.repositoryService()
	var body []byte
	if err == nil {
		body, err = ReadBody(w, r, MediaJSON, int64(s.o.API.Config.MaxConfigurationBodyBytes))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	res, err := svc.UpstreamTestUnsaved(r.Context(), body)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, res)
}
