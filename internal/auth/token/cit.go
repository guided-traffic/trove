package token

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The CI token seam (R-CI-017, R-AUTHZ-014, Q-CI-17). The exchange (internal/ci, work package W4)
// issues a trv_cit_ token through [Service.IssueCITx]; the use of the token reads the bundle back
// through [Info.CI], checked by [claims.DecodeFrozen]. The revocations of the token service record
// ci.token.revoked for the CI tokens among the ones they end (recordCIRevoked).

// CITRequest is the issuance of a CI token inside the exchange's transaction.
type CITRequest struct {
	// OwnerID is the principal ID of the matched mapping (kind ci_mapping) that owns the token; Frozen.Mappings
	// lists every contributing mapping.
	OwnerID string
	// Description is the token's description, "CI exchange <issuer> job <job_id>" (1 to 1024 characters).
	Description string
	// Frozen is stored as api_token.ci_context in the same INSERT; it is the bundle Info.CI returns. It must pass
	// [claims.Frozen.Check].
	Frozen claims.Frozen
	// Scope is the token's stored scope: {"inherit": true} without a `scope` parameter, otherwise the
	// grants of the request (R-CI-007). It is a ceiling at use time (R-AUTHZ-013).
	Scope scope.Scope
	// TTL is the computed lifetime (R-CI-016), at least one second and at most auth.tokens.cit.max_ttl; the
	// stored expiry is the database clock plus TTL, so every replica's token validation agrees with it.
	TTL time.Duration
	// Claims is the allowlisted claim snapshot of the issuance record (R-CI-020), recorded in token.created as
	// ciClaims (R-OPS-016); nil records none.
	Claims map[string]any
	Source audit.Source
}

// CITIssued is the result of an issuance: the row's ID, the secret, which exists nowhere else and is
// never shown again, and the stored expiry.
type CITIssued struct {
	ID        string
	Secret    string
	Hint      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// IssueCITx creates a trv_cit_ token in the caller's transaction (created_via 'ci-exchange'): the token row with
// ci_context and the expiry issued_at + ttl on the database clock, no user token list entry (R-CI-017: the owner
// is a mapping principal, never listed to a user), no per-principal limit of active tokens, and the event
// token.created with the CI fields (ciIssuer, ciMappings, ciClaims, R-OPS-016). A new token needs no cache
// invalidation: the caches hold positive results only, and none holds a token that did not exist. The owner is
// read FOR SHARE, so a concurrent disable of the mapping either sees the token or the issuance sees the disabled
// principal and fails. The caller (the exchange) records ci.exchange.succeeded and the issuance record. A failure
// rolls back with the exchange's transaction, so the jti is not consumed.
func (s *Service) IssueCITx(ctx context.Context, tx meta.Tx, r CITRequest) (CITIssued, error) {
	if err := s.checkCIT(r); err != nil {
		return CITIssued{}, err
	}
	bundle, err := json.Marshal(r.Frozen)
	if err != nil {
		return CITIssued{}, err
	}
	secret, err := Generate(TypeCIT)
	if err != nil {
		return CITIssued{}, err
	}
	hash := Hash(secret)
	id := principal.NewID()
	qs := principal.Queries(tx)
	owners, err := qs.CiTokenOwner(ctx, r.OwnerID)
	if err != nil {
		return CITIssued{}, principal.DBErr("token.issue_cit", err)
	}
	switch {
	case len(owners) == 0:
		return CITIssued{}, principal.NotFound("The owner")
	case owners[0].Kind != "ci_mapping":
		return CITIssued{}, errs.E(errs.Invalid, errs.CodeValidation, "A CI token belongs to a CI identity mapping.")
	case owners[0].Status != string(principal.StatusActive):
		return CITIssued{}, forbidden(errs.CodeForbidden, "The CI identity mapping is disabled.")
	}
	row, err := qs.CiTokenInsert(ctx, q.CiTokenInsertParams{
		ID: id, SecretSha256: hash[:], Hint: Hint(secret), PrincipalID: r.OwnerID, Description: r.Description,
		Scope: string(r.Scope.Marshal()), TtlSecs: r.TTL.Seconds(), CiContext: string(bundle),
	})
	if err != nil {
		return CITIssued{}, principal.DBErr("token.issue_cit", err)
	}
	out := CITIssued{ID: id, Secret: secret, Hint: Hint(secret), CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time}
	owner := Owner{ID: r.OwnerID, Kind: principal.KindCI, Name: owners[0].Name}.Ref()
	d := map[string]any{
		"type": string(TypeCIT), "owner": owner, "scope": json.RawMessage(r.Scope.Marshal()),
		"expiresAt": out.ExpiresAt.UTC().Format(time.RFC3339), "createdVia": "ci-exchange", "description": r.Description,
		"ciIssuer": r.Frozen.Issuer, "ciMappings": r.Frozen.Mappings,
	}
	if r.Claims != nil {
		d["ciClaims"] = r.Claims
	}
	// The exchange is anonymous to the authentication stage; the actor of the issuance is the mapping that owns it.
	actor := audit.Actor{Kind: string(principal.KindCI), ID: r.OwnerID, Name: owners[0].Name}
	if err := s.o.Recorder.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventTokenCreated, Outcome: audit.Success, Actor: actor, Source: r.Source,
		Subject: audit.Subject{Kind: "token", ID: id, Name: out.Hint}, Details: mustJSON(d),
	}); err != nil {
		return CITIssued{}, err
	}
	return out, nil
}

// checkCIT refuses an issuance the exchange should never ask for: an owner that is no ID, a description outside
// 1 to 1024 characters, a bundle that fails its own check, a lifetime below one second or above the CI token
// ceiling of R-AUTH-026.
func (s *Service) checkCIT(r CITRequest) error {
	maxTTL := s.o.Tokens.CIT.MaxTTL
	switch n := utf8.RuneCountInString(r.Description); {
	case !principal.IsID(r.OwnerID):
		return principal.NotFound("The owner")
	case n < 1 || n > 1024:
		return errs.E(errs.Invalid, errs.CodeDescriptionRequired, "A description of 1 to 1024 characters is required.")
	case r.TTL < time.Second || (maxTTL > 0 && r.TTL > maxTTL):
		return errs.E(errs.Invalid, errs.CodeValidation, "The lifetime of a CI token is out of range.", errs.WithOp("token.issue_cit"))
	}
	if err := r.Frozen.Check(); err != nil {
		return errs.E(errs.Internal, errs.CodeInternal, "", errs.WithOp("token.issue_cit"), errs.WithCause(err))
	}
	return nil
}

// decodeCIContext decodes api_token.ci_context of a CI token with [claims.DecodeFrozen]: an absent or null bundle,
// another version and an inconsistent bundle are errors, which the validator treats as a storage fault, never as
// an acceptance without the bundle (Q-CI-17).
func decodeCIContext(raw []byte) (*claims.Frozen, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("no CI context")
	}
	return claims.DecodeFrozen(raw)
}

// MaxCIRevokedIDs is the most token IDs one ci.token.revoked event carries (R-CI-020, R-OPS-014); a larger
// revocation writes several events.
const MaxCIRevokedIDs = 1000

// CI revocation causes the ci.token.revoked events of the token service carry (R-CI-017): the presenting token
// itself (DELETE /api/v1/tokens/current, reason owner) or a revocation of one token by ID or by value.
const (
	ciCauseSelf   = "self"
	ciCauseSingle = "single"
)

// recordCIRevoked records ci.token.revoked, with its journal entry, for the CI tokens among the ones a revocation of
// the token service ended (R-CI-017, R-CI-020): one event per issuer and per at most MaxCIRevokedIDs token IDs, the
// cause self for the presenting token's own revocation, single otherwise. token.revoked is written per token by the
// caller as for every type (R-AUTH-029).
func (s *Service) recordCIRevoked(ctx context.Context, tx meta.Tx, qs *q.Queries, c principal.Caller, revoked []Revoked, self bool) error {
	var ids []string
	for _, r := range revoked {
		if r.Type == TypeCIT {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := qs.CiTokenIssuers(ctx, ids)
	if err != nil {
		return err
	}
	cause := ciCauseSingle
	if self {
		cause = ciCauseSelf
	}
	byIssuer := map[string][]string{}
	var order []string
	for _, r := range rows {
		if _, ok := byIssuer[r.Issuer]; !ok {
			order = append(order, r.Issuer)
		}
		byIssuer[r.Issuer] = append(byIssuer[r.Issuer], r.ID)
	}
	for _, issuer := range order {
		if err := RecordCIRevoked(ctx, s.o.Recorder, tx, c.Actor, c.Source, issuer, "", cause, byIssuer[issuer], nil); err != nil {
			return err
		}
	}
	return nil
}

// RecordCIRevoked writes ci.token.revoked for the token IDs of one revocation in tx (R-CI-020): at most
// MaxCIRevokedIDs per event, so a larger revocation writes several, each with the journal entry of the IDs it
// names and the cause (R-HA-054 revokes each again on replay). mapping names the mapping of a mapping revocation,
// filter the filter of a bulk revocation; both are optional. The revoker of internal/ci and the token service
// share it, so every cause writes one shape.
func RecordCIRevoked(ctx context.Context, rec audit.Recorder, tx meta.Tx, actor audit.Actor, src audit.Source, issuer, mapping, cause string, ids []string, filter map[string]string) error {
	for start := 0; start < len(ids); start += MaxCIRevokedIDs {
		end := min(start+MaxCIRevokedIDs, len(ids))
		chunk := ids[start:end]
		d := map[string]any{"issuer": issuer, "cause": cause, "tokenIds": chunk}
		if mapping != "" {
			d["mapping"] = mapping
		}
		if len(filter) > 0 {
			d["filter"] = filter
		}
		if err := rec.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventCITokenRevoked, Outcome: audit.Success, Reason: cause, Actor: actor, Source: src,
			Subject: audit.Subject{Kind: "ci_issuer", Name: issuer}, Details: mustJSON(d),
			Journal: mustJSON(map[string]any{"tokenIds": chunk, "cause": cause}),
		}); err != nil {
			return err
		}
	}
	return nil
}
