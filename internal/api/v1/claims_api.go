package v1

import (
	"context"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

// The claims of a repository (R-REPO-023): one revisioned resource, read with
// `read` and replaced with `manage` on the repository. The shared step decides
// both and demands If-Match on the write (428); the domain compares claims_version
// (412) and writes. A claim of scope global also needs repositories:write-config,
// which the handler asks the request's access holder for inside the write
// transaction, because whether a global claim is involved can depend on the
// stored claims (a replace that drops one).

// claimRep is a claim as the API shows it.
type claimRep struct {
	Pattern   string    `json:"pattern"`
	Source    string    `json:"source"`
	Scope     string    `json:"scope"`
	CreatedAt time.Time `json:"createdAt"`
}

// claimsRep is the representation of the claims resource.
type claimsRep struct {
	Claims   []claimRep            `json:"claims"`
	Warnings *[]repository.Warning `json:"warnings,omitempty"`
}

func toClaimsRep(set repository.ClaimSet, withWarnings bool) claimsRep {
	rep := claimsRep{Claims: make([]claimRep, len(set.Claims))}
	for i, c := range set.Claims {
		rep.Claims[i] = claimRep{Pattern: c.Pattern, Source: c.Source, Scope: c.Scope, CreatedAt: c.CreatedAt.UTC()}
	}
	if withWarnings {
		w := set.Warnings
		if w == nil {
			w = []repository.Warning{}
		}
		rep.Warnings = &w
	}
	return rep
}

// claimsBody is the body of a replace.
type claimsBody struct {
	Claims []struct {
		Pattern string `json:"pattern"`
		Scope   string `json:"scope"`
	} `json:"claims"`
	RemoveAuto []string `json:"removeAuto"`
}

func (b claimsBody) input() repository.ClaimInput {
	in := repository.ClaimInput{RemoveAuto: b.RemoveAuto}
	for _, c := range b.Claims {
		in.Claims = append(in.Claims, repository.ClaimEntry{Pattern: c.Pattern, Scope: c.Scope})
	}
	return in
}

// GetRepositoryClaims implements GET /api/v1/repositories/{repoKey}/claims.
func (s *Server) GetRepositoryClaims(w http.ResponseWriter, r *http.Request, repoKey string, _ gen.GetRepositoryClaimsParams) {
	svc, err := s.repositoryService()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	set, err := svc.Claims(r.Context(), repoKey)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	tag := ETag(set.Version, "")
	if NotModified(w, r, tag) {
		return
	}
	w.Header().Set("ETag", tag)
	writeJSON(w, r, http.StatusOK, toClaimsRep(set, false))
}

// PutRepositoryClaims implements PUT /api/v1/repositories/{repoKey}/claims.
func (s *Server) PutRepositoryClaims(w http.ResponseWriter, r *http.Request, repoKey string, _ gen.PutRepositoryClaimsParams) {
	svc, err := s.repositoryService()
	var pre Precondition
	if err == nil {
		pre, err = ParseIfMatch(r)
	}
	var body claimsBody
	if err == nil {
		var raw []byte
		if raw, err = ReadBody(w, r, MediaJSON, int64(s.o.API.Config.MaxConfigurationBodyBytes)); err == nil {
			err = StrictUnmarshal(raw, &body)
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	set, err := svc.PutClaims(r.Context(), repositoryCaller(r), repoKey, repositoryPrecondition(pre), body.input(), s.globalClaimGuard)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	SetETag(w, set.Version, "")
	writeJSON(w, r, http.StatusOK, toClaimsRep(set, true))
}

// globalClaimGuard is the repository.GlobalGuard of the request: a global claim
// needs repositories:write-config, which manage on the repository does not imply
// (R-REPO-023).
func (s *Server) globalClaimGuard(ctx context.Context) error {
	ok, err := s.holds(ctx, string(authz.RepositoriesWrite))
	if err != nil {
		return err
	}
	if !ok {
		return errs.E(errs.Forbidden, errs.CodeForbidden,
			"A claim of scope global needs the permission "+string(authz.RepositoriesWrite)+" besides manage on the repository.")
	}
	return nil
}
