package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The CI federation's handlers (R-API-035, docs/developer/ci-oidc.md), one file per work package:
//
//	ci_issuers_api.go     W1  issuers and signing keys
//	ci_mappings_api.go    W3  mappings and the dry run
//	ci_snippets_api.go    W3  the CI snippet
//	ci_exchange.go        W4  the token exchange
//	ci_issuances_api.go   W5  issuance records and the bulk revoke
//
// Until a work package builds its handlers, each answers 501 ci_not_implemented through the error
// model (the exchange too), after the 503 of a replica without the service, so the server starts and
// the document is complete. The authorization declaration of every operation is in api/openapi.yaml
// and runs before the handler (R-API-008); the shared step also refuses CI tokens on all of them
// (Q-CI-18).

// CIServices are the services behind the CI operations; a nil member answers its operations with 503.
type CIServices struct {
	Issuers   ci.Issuers
	Mappings  ci.Mappings
	Issuances ci.Issuances
	Revoker   ci.Revoker
	Exchanger ci.Exchanger
}

// ciPending answers an operation whose work package is not built: the 503 of a replica without the
// service first, as every handler answers it, then 501.
func ciPending(w http.ResponseWriter, r *http.Request, available bool, what, workPackage string) {
	if !available {
		httpserver.Problem(w, r, unavailable(what))
		return
	}
	httpserver.Problem(w, r, ci.NotImplemented(workPackage))
}
