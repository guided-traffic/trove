package registry

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/errs"
)

// Challenge is the refusal of a request that carries no credential where one is needed: 401 with the challenge of R-OCI-004
// step 1 (the realm and the service of the request's base URL, no scope: the request names no image), so that a client learns
// where to ask for a token. The version check answers it (R-OCI-003, Q-OCI-20). realm is the function of the endpoint
// (EnforcerOptions.Realm, VersionOptions.Realm).
func Challenge(realm func(*http.Request) (string, string), r *http.Request) error {
	rl, service := realm(r)
	return errs.E(errs.Unauthenticated, errs.CodeCredentialsRequired, "The request carries no credential.",
		errs.WithChallenge(registrytoken.Challenge(rl, service, "", "")))
}
