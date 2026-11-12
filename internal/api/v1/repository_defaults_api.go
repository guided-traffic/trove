package v1

import (
	"encoding/json"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

// The defaults of a new repository (R-REPO-063, Q-REPO-324): what a create of only key, class and format in the project would
// store, and a key that is free in the project. The creation form reads them for the values of its fields. The permission is that
// of a create (repositories:write-config, or Owner of the project), declared in api/openapi.yaml and checked before the handler;
// the handler writes nothing and records no audit event, so it answers during maintenance as every GET does.

// GetRepositoryDefaults implements GET /api/v1/projects/{projectKey}/repository-defaults.
func (s *Server) GetRepositoryDefaults(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, params gen.GetRepositoryDefaultsParams) {
	svc, err := s.repositoryService()
	var caller repository.Caller
	if err == nil {
		caller, err = s.writeCaller(r)
	}
	var def repository.Defaults
	if err == nil {
		def, err = svc.Defaults(r.Context(), caller, projectKey, params.Format, repository.Class(params.Class))
	}
	var doc map[string]json.RawMessage
	if err == nil {
		doc, err = withoutKey(def.Document)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, struct {
		SuggestedKey *string                    `json:"suggestedKey"`
		Repository   map[string]json.RawMessage `json:"repository"`
	}{def.SuggestedKey, doc})
}

// withoutKey renders a document as a read shows it, without its key: the defaults are no repository yet.
func withoutKey(d repository.Document) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	delete(out, "key")
	return out, nil
}
