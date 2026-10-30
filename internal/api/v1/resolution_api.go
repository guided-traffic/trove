package v1

import (
	"context"
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
)

// The resolution explanation of a virtual repository (R-REPO-019): GET
// /api/v1/repositories/{repoKey}/resolution?path=. The operation declares the gate
// of step 1, read on the virtual repository, which the shared step decides before
// the handler runs; the per-member read, the claims and the state of the members are
// the engine's, for the calling principal.

// ResolutionExplainer is the virtual engine as the operation uses it;
// *virtual.Resolver implements it.
type ResolutionExplainer interface {
	Explain(ctx context.Context, c virtual.Caller, v repository.Repository, d virtual.Descriptor) (virtual.Explanation, error)
}

var _ ResolutionExplainer = (*virtual.Resolver)(nil)

// GetRepositoryResolution implements GET /api/v1/repositories/{repoKey}/resolution.
func (s *Server) GetRepositoryResolution(w http.ResponseWriter, r *http.Request, repoKey string, params gen.GetRepositoryResolutionParams) {
	ex, err := s.explainer()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	ctx, acc, err := requestHolder(r.Context(), s.o.Repositories.Authz, repoKey)
	if err == nil && acc == nil {
		err = errNoDecisionPoint()
	}
	var v repository.Repository
	if err == nil {
		v, err = s.o.Repositories.Reader.ByKey(ctx, repoKey)
	}
	if err == nil && v.Class != repository.Virtual {
		err = errs.E(errs.Invalid, errs.CodeNotApplicable,
			"Only a virtual repository has a resolution; "+strconv.Quote(repoKey)+" is a "+string(v.Class)+" repository.")
	}
	var d virtual.Descriptor
	if err == nil {
		d, err = resolutionDescriptor(v, params.Path)
	}
	var out virtual.Explanation
	if err == nil {
		out, err = ex.Explain(ctx, virtual.Caller{Subject: acc.Subject, Index: acc.Index()}, v, d)
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.getRepositoryResolution"))
		return
	}
	writeJSON(w, r, http.StatusOK, out)
}

func (s *Server) explainer() (ResolutionExplainer, error) {
	if s.o.Resolution == nil || s.o.Repositories.Reader == nil {
		return nil, unavailable("The resolution explanation")
	}
	return s.o.Resolution, nil
}

// resolutionDescriptor gives a client path the R-REPO-003 descriptor of the format of the
// repository.
func resolutionDescriptor(v repository.Repository, path string) (virtual.Descriptor, error) {
	cleaned, err := repository.CleanPath(path)
	if err != nil {
		return virtual.Descriptor{}, err
	}
	f, ok := formats.Lookup(v.Format)
	if !ok {
		return virtual.Descriptor{}, errs.E(errs.Internal, errs.CodeInternal, "The format of the repository is not registered.")
	}
	info := f.Classify(cleaned)
	return virtual.Descriptor{
		Format: v.Format, Path: cleaned.Path, Kind: info.Kind.RequestKind(),
		Package: info.Package, Version: info.Version, RoutingName: info.RoutingName,
	}, nil
}
