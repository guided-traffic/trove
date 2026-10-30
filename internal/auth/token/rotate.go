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
	if err := checkOverlap(r); err != nil {
		return Created{}, err
	}
	old, err := s.Get(ctx, r.ID)
	if err != nil {
		return Created{}, err
	}
	sp, err := s.prepareRotate(ctx, r, old)
	if err != nil {
		return Created{}, err
	}
	return s.insert(ctx, sp)
}

// RotateTx is [Service.Rotate] inside the caller's transaction, with the rules of
// [Service.CreateTx]: the token it reads, its successor, the end of the
// predecessor and the audit events belong to tx.
func (s *Service) RotateTx(ctx context.Context, tx meta.Tx, r RotateRequest) (Created, error) {
	if err := checkOverlap(r); err != nil {
		return Created{}, err
	}
	old, err := s.getTx(ctx, tx, r.ID)
	if err != nil {
		return Created{}, err
	}
	sp, err := s.prepareRotate(ctx, r, old)
	if err != nil {
		return Created{}, err
	}
	created, err := s.insertTx(ctx, tx, sp)
	return created, principal.DBErr("token.rotate", err)
}

func checkOverlap(r RotateRequest) error {
	if r.OverlapSeconds < 0 || time.Duration(r.OverlapSeconds)*time.Second > maxOverlap {
		return errs.E(errs.Invalid, errs.CodeValidation, "overlapSeconds must be between 0 and 86400.")
	}
	return nil
}

// prepareRotate checks the rules that follow from the token's own data and
// resolves the successor's lifetime; it writes nothing.
func (s *Service) prepareRotate(ctx context.Context, r RotateRequest, old Record) (insertSpec, error) {
	switch {
	case old.State != "active":
		return insertSpec{}, principal.NotFound("The token")
	case old.Type == TypeCIT:
		return insertSpec{}, forbidden(errs.CodeCITokenRestricted, "A CI token cannot be rotated.")
	case r.Self && !old.SelfRotatable:
		return insertSpec{}, forbidden(errs.CodeForbidden, "The token was not created with selfRotatable.")
	}
	eff, err := s.o.Settings.Effective(ctx)
	if err != nil {
		return insertSpec{}, err
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
			return insertSpec{}, forbidden(errs.CodeCITokenRestricted, "A CI token cannot rotate tokens.")
		}
		if !old.Scope.Within(r.Parent.Scope) {
			return insertSpec{}, forbidden(errs.CodeDerivationExceedsParent, "The scope is broader than the presenting token's.")
		}
		if r.Parent.ExpiresAt != nil && (life.nonExpiring || now.Add(life.ttl).After(*r.Parent.ExpiresAt)) {
			return insertSpec{}, forbidden(errs.CodeDerivationExceedsParent, "The new token would outlive the presenting token.")
		}
	}
	// The successor keeps the lineage of the token it replaces, so revoking the
	// token that one was derived from still revokes it, and it never outlives that
	// token (Q-AUTH-89). Rotated through another token of the owner, it is derived
	// from that one, as a create would be (R-AUTH-027).
	lineage := old.DerivedFrom
	if r.Parent != nil && r.Parent.ID != old.ID {
		lineage = r.Parent.ID
	}
	if lineage != "" && life.nonExpiring {
		life = lifetime{ttl: maxTTL}
	}
	if old.Type == TypePAT {
		// Rotation hands out a new personal token, so the group restriction of a
		// create applies (R-AUTH-017).
		if err := s.checkPATAllowed(ctx, old.Owner.ID); err != nil {
			return insertSpec{}, err
		}
	}
	overlap := time.Duration(r.OverlapSeconds) * time.Second
	return insertSpec{
		req: CreateRequest{
			Caller: r.Caller, Type: old.Type, OwnerID: old.Owner.ID, Description: old.Description, SelfRotatable: old.SelfRotatable,
		},
		scope: old.Scope, life: life, maxActive: s.maxActive(old.Type), via: "rotation", rotatedFrom: old.ID, capMicros: capMicros(parentExpiry(r.Parent)),
		derivedFrom: lineage, presented: derivedID(r.Parent),
		afterInsert: func(ctx context.Context, tx meta.Tx, qs *q.Queries, created Record) error {
			return s.endPredecessor(ctx, tx, qs, r.Caller, old.ID, overlap, created)
		},
	}, nil
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
	if len(refs) == 0 {
		// The predecessor ended while the successor was being created; the lineage
		// lock makes this unreachable, and a successor of an ended token must not
		// commit (R-AUTH-030: an expired or revoked token cannot be rotated).
		return principal.NotFound("The token")
	}
	if err := principal.NotifyTokens(s.o.Notifier, tx, refs...); err != nil {
		return err
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
