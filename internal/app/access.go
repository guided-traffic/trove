package app

import (
	"context"
	"net/http"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// bootstrapAuthenticate is the authentication stage until the identity packages
// exist (M1). No credential can be valid yet, so any presented credential is an
// invalid one and gives 401 (R-ARCH-005 step 6); everything else is anonymous.
// WIRING POINT: replace with the identity packages' stage.
func bootstrapAuthenticate(r *http.Request) (httpserver.Principal, error) {
	if r.Header.Get("Authorization") != "" {
		return httpserver.Principal{}, errs.E(errs.Unauthenticated, errs.CodeUnauthenticated,
			"The credential is not valid.", errs.WithChallenge("Bearer"))
	}
	return httpserver.Principal{Anonymous: true}, nil
}

// bootstrapAccess decides permissions until authorization exists. Nobody can
// hold system.read yet, so system.bootstrap_anonymous_read is the explicit,
// off-by-default bridge that lets the dev profile and its load test read the
// replica registry.
// WIRING POINT: replace with the authorization package.
type bootstrapAccess struct{ anonymousRead bool }

var _ v1.Access = bootstrapAccess{}

func (a bootstrapAccess) Require(ctx context.Context, permission string) error {
	p := httpserver.PrincipalFrom(ctx)
	if !p.Anonymous {
		return nil
	}
	// The bridge opens system.read to anonymous callers on the default endpoint
	// only, and only while that endpoint admits anonymous requests (R-EP-010): a
	// further endpoint serves an outside audience, and a request on a listener
	// without an endpoint is none of the bridge's business (SA3).
	if ep := httpserver.EndpointFrom(ctx); a.anonymousRead && permission == v1.PermissionSystemRead &&
		ep != nil && ep.IsDefault() && ep.AnonymousAllowed() {
		return nil
	}
	return errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "This endpoint requires an authenticated caller.", errs.WithChallenge("Bearer"))
}
