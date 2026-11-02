package token

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// The CI token seam (R-CI-017, R-AUTHZ-014, Q-CI-17). The exchange (internal/ci, work package W4)
// issues a trv_cit_ token through [Service.IssueCITx]; the use of the token reads the bundle back
// through [Info.CI]. Work package W4 fills IssueCITx; the rest of this file is real.

// CITRequest is the issuance of a CI token inside the exchange's transaction.
type CITRequest struct {
	// OwnerID is the principal ID of the matched mapping (kind ci_mapping). With one tier in the MVP
	// the token has exactly one owner principal; Frozen.Mappings lists every contributing mapping.
	OwnerID string
	// Description is the token's description, "CI exchange <issuer> job <job_id>" (1 to 1024 characters).
	Description string
	// Frozen is stored as api_token.ci_context in the same INSERT; it is the bundle Info.CI returns.
	Frozen claims.Frozen
	// Scope is the token's stored scope: {"inherit": true} without a `scope` parameter, otherwise the
	// grants of the request (R-CI-007). It is a ceiling at use time (R-AUTHZ-013).
	Scope scope.Scope
	// TTL is the computed lifetime (R-CI-016); the stored expiry is the database clock plus TTL, so
	// every replica's token validation agrees with it.
	TTL    time.Duration
	Source audit.Source
}

// CITIssued is the result of an issuance: the row's ID, the secret, which exists nowhere else and is
// never shown again, and the stored expiry.
type CITIssued struct {
	ID        string
	Secret    string
	Hint      string
	ExpiresAt time.Time
}

// IssueCITx creates a trv_cit_ token in the caller's transaction (created_via 'ci-exchange'): the
// token row with ci_context, no user token list entry (R-CI-017: the owner is a mapping principal,
// never listed to a user), no per-principal limit of active tokens, the cache invalidation of the
// area auth in the same transaction as every token write, and the event token.created with the CI
// fields (ci_mapping, ci_claims, R-OPS-016). The caller (the exchange) records ci.exchange.succeeded.
// A failure rolls back with the exchange's transaction, so the jti is not consumed.
func (s *Service) IssueCITx(_ context.Context, _ meta.Tx, _ CITRequest) (CITIssued, error) {
	return CITIssued{}, errs.E(errs.NotImplemented, errs.CodeCINotImplemented, "The CI token issuance is not built in this release yet (W4).")
}

// decodeCIContext decodes api_token.ci_context of a CI token. An absent or null bundle is an error:
// every CI token the exchange issues carries one.
func decodeCIContext(raw []byte) (*claims.Frozen, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("no CI context")
	}
	var f claims.Frozen
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	return &f, nil
}
