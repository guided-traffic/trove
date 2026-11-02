package ci

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The revoker of CI tokens (R-CI-017, R-CI-020), work package W5. Every revocation runs in one transaction, the
// caller's for a mapping or an issuer (W1 and W3 call it in the transaction that disables or deletes the
// object), its own for the bulk revoke: it locks the token rows FOR UPDATE, sets revoked_at, revoked_by and the
// reason, records the change of the area auth with the token IDs and hashes (R-AUTH-023: the writing replica
// evicts at once, Valkey carries an early eviction, the change-log poll applies it on every replica whatever
// Valkey does, so every replica refuses the tokens within auth.revocation_bound), and writes ci.token.revoked
// with the cause, at most 1,000 token IDs per event, each with its journal entry (R-HA-054 revokes each again on
// replay). One token by ID and the token itself (DELETE /api/v1/tokens/current) are revoked by the token service
// (internal/auth/token), which writes the same event with the causes single and self.
//
// The reason on the token row is one of the token table's (Q-CI-38): mapping_deleted for a mapping disabled or
// deleted, admin for an issuer and for the bulk revoke; the event's cause tells them apart.

// RevokerOptions configure [NewRevoker].
type RevokerOptions struct {
	// DB runs the bulk revoke's transaction.
	DB meta.Runner
	// Changes records the change of the area auth in the transaction (*cluster.Changes).
	Changes principal.Notifier
	// Recorder records ci.token.revoked in the transaction.
	Recorder audit.Recorder
}

// TokenRevoker implements [Revoker].
type TokenRevoker struct{ o RevokerOptions }

var _ Revoker = (*TokenRevoker)(nil)

// NewRevoker creates the revoker.
func NewRevoker(o RevokerOptions) (*TokenRevoker, error) {
	if o.DB == nil || o.Changes == nil {
		return nil, errors.New("ci: the revoker needs the database and the change log")
	}
	if o.Recorder == nil {
		o.Recorder = audit.Nop{}
	}
	return &TokenRevoker{o: o}, nil
}

// Revocation reasons of the token rows (the CHECK of api_token.revocation_reason, Q-CI-38).
const (
	reasonMapping = "mapping_deleted"
	reasonAdmin   = token.ReasonAdmin
)

// victim is a token a revocation ends: its ID and its hash.
type victim struct {
	id   string
	hash []byte
}

// RevokeMappingTx revokes the unexpired tokens issued through a mapping, in the caller's transaction: the tokens
// its principal owns and those whose issuance record lists it as a contributing mapping of another tier.
func (r *TokenRevoker) RevokeMappingTx(ctx context.Context, tx meta.Tx, actor Actor, m Mapping, cause RevokeCause) (Revoked, error) {
	if m.ID == "" || m.PrincipalID == "" || m.IssuerID == "" {
		return Revoked{}, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithOp("ci.revoke_mapping"), errs.WithCause(errors.New("a mapping without its IDs")))
	}
	qs, err := queriesOf(tx)
	if err != nil {
		return Revoked{}, err
	}
	rows, err := qs.CiIssuanceTokensOfMapping(ctx, q.CiIssuanceTokensOfMappingParams{PrincipalID: m.PrincipalID, IssuerID: m.IssuerID, MappingID: m.ID})
	if err != nil {
		return Revoked{}, err
	}
	vs := make([]victim, 0, len(rows))
	for _, row := range rows {
		vs = append(vs, victim{id: row.ID, hash: row.SecretSha256})
	}
	return r.end(ctx, tx, qs, actor, vs, reasonMapping, revocationEvent{issuer: m.Issuer, mapping: m.Name, cause: cause})
}

// RevokeIssuerTx revokes every unexpired token issued through an issuer, in the caller's transaction (disable,
// delete, and the removal from ci.issuers that `trove migrate` applies).
func (r *TokenRevoker) RevokeIssuerTx(ctx context.Context, tx meta.Tx, actor Actor, issuerID, issuerName string, cause RevokeCause) (Revoked, error) {
	qs, err := queriesOf(tx)
	if err != nil {
		return Revoked{}, err
	}
	rows, err := qs.CiIssuanceTokensOfIssuer(ctx, issuerID)
	if err != nil {
		return Revoked{}, err
	}
	vs := make([]victim, 0, len(rows))
	for _, row := range rows {
		vs = append(vs, victim{id: row.ID, hash: row.SecretSha256})
	}
	return r.end(ctx, tx, qs, actor, vs, reasonAdmin, revocationEvent{issuer: issuerName, cause: cause})
}

// RevokeMatching is the bulk revoke of POST /api/v1/ci/issuances/revoke (R-CI-020): issuer is required and at
// least one of projectId, pipelineId, jobId, mapping (422 otherwise); an unknown issuer is 404. One transaction.
func (r *TokenRevoker) RevokeMatching(ctx context.Context, actor Actor, f RevokeFilter) (Revoked, error) {
	if err := checkRevokeFilter(f); err != nil {
		return Revoked{}, err
	}
	var out Revoked
	err := r.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		known, err := qs.CiIssuerIDByName(ctx, f.Issuer)
		if err != nil {
			return err
		}
		if len(known) == 0 {
			return errs.E(errs.NotFound, errs.CodeNotFound, "No CI issuer "+f.Issuer+" exists.")
		}
		rows, err := qs.CiIssuanceTokensToRevoke(ctx, q.CiIssuanceTokensToRevokeParams{
			Issuer: f.Issuer, ProjectID: f.ProjectID, PipelineID: f.PipelineID, JobID: f.JobID, Mapping: f.Mapping,
		})
		if err != nil {
			return err
		}
		vs := make([]victim, 0, len(rows))
		for _, row := range rows {
			vs = append(vs, victim{id: row.TokenID, hash: row.SecretSha256})
		}
		out, err = r.end(ctx, tx, qs, actor, vs, reasonAdmin, revocationEvent{issuer: f.Issuer, cause: CauseBulk, filter: f.record()})
		return err
	})
	return out, err
}

// checkRevokeFilter is the rule of the bulk revoke's body (R-CI-020).
func checkRevokeFilter(f RevokeFilter) error {
	var fields []errs.FieldError
	if f.Issuer == "" {
		fields = append(fields, errs.FieldError{Pointer: "/issuer", Code: "required", Detail: "issuer is required"})
	}
	if f.ProjectID == "" && f.PipelineID == "" && f.JobID == "" && f.Mapping == "" {
		fields = append(fields, errs.FieldError{Pointer: "", Code: "filter_required", Detail: "name at least one of projectId, pipelineId, jobId, mapping"})
	}
	if len(fields) == 0 {
		return nil
	}
	return errs.E(errs.Unprocessable, errs.CodeValidation, "The revoke filter is incomplete.", errs.WithFields(fields...))
}

// record is the filter as the event shows it: the members that were given.
func (f RevokeFilter) record() map[string]string {
	out := map[string]string{"issuer": f.Issuer}
	for k, v := range map[string]string{"projectId": f.ProjectID, "pipelineId": f.PipelineID, "jobId": f.JobID, "mapping": f.Mapping} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// revocationEvent is what ci.token.revoked names beside the token IDs.
type revocationEvent struct {
	issuer, mapping string
	cause           RevokeCause
	filter          map[string]string
}

// end revokes the locked tokens, records the change of the area auth and the events. A revocation that finds
// nothing writes nothing.
func (r *TokenRevoker) end(ctx context.Context, tx meta.Tx, qs *q.Queries, actor Actor, vs []victim, reason string, ev revocationEvent) (Revoked, error) {
	if len(vs) == 0 {
		return Revoked{}, nil
	}
	ids := make([]string, 0, len(vs))
	for _, v := range vs {
		ids = append(ids, v.id)
	}
	by := ""
	if !actor.Anonymous && principal.IsID(actor.ID) {
		by = actor.ID
	}
	done, err := qs.CiRevokeTokens(ctx, q.CiRevokeTokensParams{Ids: ids, Reason: reason, RevokedBy: by})
	if err != nil {
		return Revoked{}, err
	}
	if len(done) == 0 {
		return Revoked{}, nil
	}
	refs := make([]principal.TokenRef, 0, len(done))
	out := Revoked{TokenIDs: make([]string, 0, len(done))}
	for _, d := range done {
		refs = append(refs, principal.TokenRef{ID: d.ID, Hash: hex.EncodeToString(d.SecretSha256)})
		out.TokenIDs = append(out.TokenIDs, d.ID)
	}
	if err := principal.NotifyTokens(r.o.Changes, tx, refs...); err != nil {
		return Revoked{}, err
	}
	err = token.RecordCIRevoked(ctx, r.o.Recorder, tx, actorOf(actor), audit.Source{}, ev.issuer, ev.mapping, string(ev.cause), out.TokenIDs, ev.filter)
	return out, err
}
