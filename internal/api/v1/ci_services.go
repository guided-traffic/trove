package v1

import (
	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/config"
)

// The CI federation's handlers (R-API-035, docs/developer/ci-oidc.md), one file per work package:
//
//	ci_issuers_api.go     W1  issuers and signing keys
//	ci_mappings_api.go    W3  mappings and the dry run
//	ci_snippets_api.go    W3  the CI snippet
//	ci_exchange.go        W4  the token exchange
//	ci_issuances_api.go   W5  issuance records and the bulk revoke
//
// A replica without a service answers its operations with 503, the exchange in its OAuth shape. The
// authorization declaration of every operation is in api/openapi.yaml and runs before the handler (R-API-008);
// the shared step also refuses CI tokens on all of them (Q-CI-18).

// CIServices are the services behind the CI operations; a nil member answers its operations with 503.
type CIServices struct {
	Issuers   ci.Issuers
	Mappings  ci.Mappings
	Issuances ci.Issuances
	Revoker   ci.Revoker
	Exchanger ci.Exchanger
	// Exchange is ci.exchange: the switch, expose_reason and the body limit the handler applies (R-CI-007,
	// R-CI-008, R-CI-021).
	Exchange config.CIExchange
}
