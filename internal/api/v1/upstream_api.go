package v1

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

// The upstream-test operations of R-REPO-024. Both need repositories:write-config
// or, for a project administrator, manageRepositories of the repository's project
// (any project with the switch for the unsaved test, R-AUTHZ-026); the shared step
// admits them and the handler narrows. The service checks the project of the saved
// repository again, and a project administrator's unsaved section names no secret
// reference and no outbound path of its own (Q-REPO-127, Q-REPO-128). The egress rules
// of the installation apply to a probe whoever sends it: they are checked when the
// connection is made. The handlers are thin: the
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
	delegate, err := s.writeDelegate(r.Context(), key)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	res, err := svc.UpstreamTestIn(r.Context(), key, delegate)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, res)
}

// TestUpstream implements POST /api/v1/upstream-tests.
func (s *Server) TestUpstream(w http.ResponseWriter, r *http.Request) {
	svc, err := s.repositoryService()
	var delegate string
	if err == nil {
		delegate, err = s.requireAnyRepositoriesSwitch(r.Context())
	}
	var body []byte
	if err == nil {
		body, err = ReadBody(w, r, MediaJSON, int64(s.o.API.Config.MaxConfigurationBodyBytes))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	res, err := svc.UpstreamTestUnsavedIn(r.Context(), body, repository.Write{Delegate: delegate})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, res)
}

// requireAnyRepositoriesSwitch is the narrowing of the unsaved upstream test, which
// names no repository: a holder of repositories:write-config, or an administrator of
// some project whose manageRepositories is on (R-AUTHZ-026). The project with the
// switch off answers 403 project_switch_off. It returns the project the caller acts
// for, "" for a holder of the capability: a project administrator's section follows
// the rules of its creates (Q-REPO-127, Q-REPO-128).
func (s *Server) requireAnyRepositoriesSwitch(ctx context.Context) (string, error) {
	d, err := s.delegatedScope(ctx, capabilityWriteConfig)
	if err != nil || d.Global {
		return "", err
	}
	for _, p := range d.Projects {
		if p.Holds(authz.ProjectManageRepositories) {
			return p.Key, nil
		}
	}
	if len(d.Projects) == 0 {
		return "", errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
	}
	return "", refuseProject(d, d.Projects[0].Key, authz.ProjectManageRepositories)
}
