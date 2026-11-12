package v1

import (
	"encoding/json"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/project"
	"github.com/guided-traffic/trove/internal/visibility"
)

// The visibility operations (R-API-056, R-API-057). The shared authorization step decided the caller (the permission and the
// recent authentication of the declaration); the services behind them do the rest.

func (s *Server) visibilitySettings() (*visibility.Service, error) {
	if s.o.Authz.Visibility == nil {
		return nil, unavailable("The visibility settings")
	}
	return s.o.Authz.Visibility, nil
}

func toVisibilitySettings(v visibility.Settings) gen.VisibilitySettings {
	return gen.VisibilitySettings{
		DefaultLevel: gen.VisibilityLevel(v.DefaultLevel), PublicAvailable: v.PublicAvailable,
		PublicObjects: gen.PublicObjects{Projects: v.PublicObjects.Projects, Repositories: v.PublicObjects.Repositories, Truncated: v.PublicObjects.Truncated},
	}
}

// GetVisibilitySettings implements GET /api/v1/settings/visibility (R-API-056). It reads PostgreSQL, so a change another replica
// committed shows at once.
func (s *Server) GetVisibilitySettings(w http.ResponseWriter, r *http.Request, _ gen.GetVisibilitySettingsParams) {
	svc, err := s.visibilitySettings()
	var v visibility.Settings
	if err == nil {
		v, err = svc.Get(r.Context())
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "visibility.settings.get"))
		return
	}
	// The ETag moves with the default level only: the objects that are public change with the projects and repositories, and a
	// PATCH decides nothing on them.
	tag := ETag(v.Version, "")
	if NotModified(w, r, tag) {
		return
	}
	w.Header().Set("ETag", tag)
	writeJSON(w, r, http.StatusOK, toVisibilitySettings(v))
}

// visibilityPatchBody is the merge patch of the settings: the two read-only members of a body obtained by GET are accepted and
// ignored (R-API-056).
type visibilityPatchBody struct {
	DefaultLevel    *string         `json:"defaultLevel"`
	PublicAvailable json.RawMessage `json:"publicAvailable"`
	PublicObjects   json.RawMessage `json:"publicObjects"`
}

// UpdateVisibilitySettings implements PATCH /api/v1/settings/visibility (R-API-056).
func (s *Server) UpdateVisibilitySettings(w http.ResponseWriter, r *http.Request, _ gen.UpdateVisibilitySettingsParams) {
	svc, err := s.visibilitySettings()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	var raw []byte
	if err == nil {
		raw, err = ReadMergePatch(w, r, s.bodyLimit())
	}
	var in visibilityPatchBody
	if err == nil {
		err = StrictUnmarshal(raw, &in)
	}
	var v visibility.Settings
	if err == nil {
		level := ""
		if in.DefaultLevel != nil {
			level = *in.DefaultLevel
		} else if v, err = svc.Get(r.Context()); err == nil { // nothing to change: the level it has, under the version check
			level = v.DefaultLevel
		}
		if err == nil {
			c := s.caller(r)
			v, err = svc.Update(r.Context(), visibility.Caller{Actor: c.Actor, Source: c.Source}, expectedRevision(pre), level)
		}
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "visibility.settings.update"))
		return
	}
	SetETag(w, v.Version, "")
	writeJSON(w, r, http.StatusOK, toVisibilitySettings(v))
}

// SetProjectVisibility implements PUT /api/v1/projects/{projectKey}/visibility (R-API-057). The answer is the project and the
// repositories the change lowered, with the levels they had.
func (s *Server) SetProjectVisibility(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, _ gen.SetProjectVisibilityParams) {
	svc, err := s.projects()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	var in gen.ProjectVisibilityRequest
	if err == nil {
		err = DecodeJSON(w, r, &in, s.bodyLimit())
	}
	if err == nil && in.Level == "" {
		err = errs.E(errs.Invalid, errs.CodeValidation, "The level is required.", errs.WithFields(required("/level")))
	}
	var p project.Project
	var lowered []project.Lowered
	var epoch int64
	if err == nil {
		p, lowered, epoch, err = svc.SetVisibility(r.Context(), caller(r), projectKey, expectedRevision(pre), string(in.Level))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	res := gen.ProjectVisibilityResult{Project: toProject(p, authz.ProjectRoleNone)}
	res.Lowered = make([]struct {
		Before     gen.RepositoryVisibility `json:"before"`
		Repository string                   `json:"repository"`
	}, 0, len(lowered))
	for _, l := range lowered {
		res.Lowered = append(res.Lowered, struct {
			Before     gen.RepositoryVisibility `json:"before"`
			Repository string                   `json:"repository"`
		}{
			Before:     gen.RepositoryVisibility{Artifacts: gen.VisibilityLevel(l.Before.Artifacts), Wiki: gen.VisibilityLevel(l.Before.Wiki)},
			Repository: l.Repository,
		})
	}
	SetETag(w, p.Version, "")
	setEpoch(w, epoch)
	writeJSON(w, r, http.StatusOK, res)
}
