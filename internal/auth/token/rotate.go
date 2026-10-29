package token

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// maxOverlap is the longest overlap a rotation may keep the predecessor valid
// (R-AUTH-030).
const maxOverlap = 24 * time.Hour

// RotateRequest is a rotation (R-AUTH-030). The authorization of the caller
// (the owner, an administrator for a service-account token, the derivation rule)
// is decided by the caller of Rotate; the rules that follow from the token's own
// data are enforced here.
type RotateRequest struct {
	Caller principal.Caller
	// ID is the token to rotate.
	ID string
	// OverlapSeconds keeps the predecessor valid that long; 0 revokes it with
	// reason rotated. At most 86400.
	OverlapSeconds int64
	// Self is true when the token rotates itself (`tokens/current/rotate`): it
	// must have been created with selfRotatable.
	Self bool
	// Parent is the presenting token of a token-authenticated rotation: the
	// derivation rule applies to the new token's scope and expiry.
	Parent *Info
}

// Rotate replaces a token's secret and keeps its owner, description and scope
// (R-AUTH-030). The new token has the original lifetime capped by the current
// maximum, records rotatedFrom and createdVia rotation; the predecessor expires
// at the earlier of its expiry and now plus the overlap, or is revoked with
// reason rotated when the overlap is 0. Expired and revoked tokens answer 404,
// CI tokens 403 ci_token_restricted.
func (s *Service) Rotate(ctx context.Context, r RotateRequest) (Created, error) {
	if r.OverlapSeconds < 0 || time.Duration(r.OverlapSeconds)*time.Second > maxOverlap {
		return Created{}, errs.E(errs.Invalid, errs.CodeValidation, "overlapSeconds must be between 0 and 86400.")
	}
	old, err := s.Get(ctx, r.ID)
	if err != nil {
		return Created{}, err
	}
	switch {
	case old.State != "active":
		return Created{}, principal.NotFound("The token")
	case old.Type == TypeCIT:
		return Created{}, forbidden(errs.CodeCITokenRestricted, "A CI token cannot be rotated.")
	case r.Self && !old.SelfRotatable:
		return Created{}, forbidden(errs.CodeForbidden, "The token was not created with selfRotatable.")
	}
	eff, err := s.o.Settings.Effective(ctx)
	if err != nil {
		return Created{}, err
	}
	now := s.o.Now()
	def, maxTTL := eff.PATDefaultTTL, eff.PATMaxTTL
	if old.Type == TypeSAT {
		def, maxTTL = eff.SATDefaultTTL, eff.SATMaxTTL
	}
	life := lifetime{ttl: min(def, maxTTL)}
	switch {
	case old.ExpiresAt != nil:
		life.ttl = min(old.ExpiresAt.Sub(old.CreatedAt), maxTTL)
	case old.Type == TypeSAT && s.o.Tokens.SAT.AllowNonExpiring:
		life = lifetime{nonExpiring: true}
	}
	if r.Parent != nil {
		if r.Parent.Type == TypeCIT {
			return Created{}, forbidden(errs.CodeCITokenRestricted, "A CI token cannot rotate tokens.")
		}
		if !old.Scope.Within(r.Parent.Scope) {
			return Created{}, forbidden(errs.CodeDerivationExceedsParent, "The scope is broader than the presenting token's.")
		}
		if r.Parent.ExpiresAt != nil && (life.nonExpiring || now.Add(life.ttl).After(*r.Parent.ExpiresAt)) {
			return Created{}, forbidden(errs.CodeDerivationExceedsParent, "The new token would outlive the presenting token.")
		}
	}
	overlap := time.Duration(r.OverlapSeconds) * time.Second
	return s.insert(ctx, insertSpec{
		req: CreateRequest{
			Caller: r.Caller, Type: old.Type, OwnerID: old.Owner.ID, Description: old.Description, SelfRotatable: old.SelfRotatable,
		},
		scope: old.Scope, life: life, maxActive: s.maxActive(old.Type), via: "rotation", rotatedFrom: old.ID, capMicros: capMicros(parentExpiry(r.Parent)),
		afterInsert: func(ctx context.Context, tx meta.Tx, qs *q.Queries, created Record) error {
			return s.endPredecessor(ctx, tx, qs, r.Caller, old.ID, overlap, created)
		},
	})
}

// endPredecessor applies the overlap to the rotated token in the transaction
// that creates its successor, records the change in the area auth and writes
// token.rotated with its journal entry (R-AUTH-041).
func (s *Service) endPredecessor(ctx context.Context, tx meta.Tx, qs *q.Queries, c principal.Caller, id string, overlap time.Duration, created Record) error {
	var expires *time.Time
	var refs []principal.TokenRef
	if overlap == 0 {
		rows, err := qs.IdentityRevokeCascade(ctx, q.IdentityRevokeCascadeParams{ID: id, Reason: ReasonRotated, RevokedBy: c.Actor.ID})
		if err != nil {
			return err
		}
		for _, r := range rows {
			refs = append(refs, principal.TokenRef{ID: r.ID, Hash: r.SecretHex})
		}
		now := s.o.Now()
		expires = &now
	} else {
		rows, err := qs.IdentitySetTokenExpiry(ctx, q.IdentitySetTokenExpiryParams{ID: id, OverlapSecs: overlap.Seconds()})
		if err != nil {
			return err
		}
		for _, r := range rows {
			refs = append(refs, principal.TokenRef{ID: id, Hash: r.SecretHex})
			if r.ExpiresAt.Valid {
				t := r.ExpiresAt.Time
				expires = &t
			}
		}
	}
	if len(refs) > 0 {
		if err := principal.NotifyTokens(s.o.Notifier, tx, refs...); err != nil {
			return err
		}
	}
	var exp any
	if expires != nil {
		exp = expires.UTC().Format(time.RFC3339)
	}
	entry := map[string]any{"predecessor": id, "expiresAt": exp, "overlapSeconds": int64(overlap.Seconds())}
	return s.o.Recorder.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventTokenRotated, Outcome: audit.Success, Actor: c.Actor, Source: c.Source,
		Subject: audit.Subject{Kind: "token", ID: created.ID, Name: created.Hint},
		Details: mustJSON(map[string]any{"predecessor": id, "overlapSeconds": int64(overlap.Seconds()), "predecessorExpiresAt": exp}),
		Journal: mustJSON(entry),
	})
}

func parentExpiry(p *Info) *time.Time {
	if p == nil {
		return nil
	}
	return p.ExpiresAt
}
