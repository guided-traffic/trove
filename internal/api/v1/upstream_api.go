package v1

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The upstream-test operations of R-REPO-024. Both need repositories:write-config
// or, for a project administrator, manageRepositories of the repository's project
// (any project with the switch for the unsaved test, R-AUTHZ-026); the shared step
// admits them and the handler narrows. The egress rules of the installation apply to
// a probe whoever sends it: they are checked when the connection is made. The handlers are thin: the
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
	if _, err := s.writeDelegate(r.Context(), key); err != nil {
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
	if err == nil {
		err = s.requireAnyRepositoriesSwitch(r.Context())
	}
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

// requireAnyRepositoriesSwitch is the narrowing of the unsaved upstream test, which
// names no repository: a holder of repositories:write-config, or an administrator of
// some project whose manageRepositories is on (R-AUTHZ-026). The project with the
// switch off answers 403 project_switch_off.
func (s *Server) requireAnyRepositoriesSwitch(ctx context.Context) error {
	d, err := s.delegatedScope(ctx, capabilityWriteConfig)
	if err != nil || d.Global {
		return err
	}
	for _, p := range d.Projects {
		if p.Holds(authz.ProjectManageRepositories) {
			return nil
		}
	}
	if len(d.Projects) == 0 {
		return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
	}
	return refuseProject(d, d.Projects[0].Key, authz.ProjectManageRepositories)
}
