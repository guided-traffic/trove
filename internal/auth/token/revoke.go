package token

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Revocation reasons (the CHECK of api_token.revocation_reason).
const (
	ReasonOwner             = "owner"
	ReasonAdmin             = "admin"
	ReasonRotated           = "rotated"
	ReasonNPMLogout         = "npm-logout"
	ReasonLeaked            = "leaked"
	ReasonPrincipalDisabled = "principal_disabled"
	ReasonParentRevoked     = "parent_revoked"
)

// Revoked is one token a revocation ended.
type Revoked struct {
	ID      string
	OwnerID string
	Type    Type
	// Root is true for the token that was asked for; the others are cascaded.
	Root bool
}

// Revoke revokes a token and, in the same transaction, every unrevoked token
// whose derived_from chain leads to it, with reason parent_revoked (R-AUTH-029).
// Revoking an already revoked or expired token ends nothing and succeeds, so the
// operation is idempotent; an unknown ID is a 404. The change is recorded in the
// area auth, so every replica rejects the tokens within auth.revocation_bound
// whatever Valkey does.
func (s *Service) Revoke(ctx context.Context, c principal.Caller, id, reason string) ([]Revoked, error) {
	if !principal.IsID(id) {
		return nil, principal.NotFound("The token")
	}
	var out []Revoked
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs := principal.Queries(tx)
		rows, err := qs.IdentityTokenRow(ctx, id)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return principal.NotFound("The token")
		}
		out, err = s.revokeTree(ctx, tx, qs, c, id, reason, false)
		return err
	})
	return out, principal.DBErr("token.revoke", err)
}

// revokeTree revokes the tree of id inside tx: the cascade, the change record
// and one token.revoked event per ended token, each with its journal entry.
// The tree is locked first, in a statement of its own: a create that derives
// from a token of the tree holds the same lock until it commits, and the
// cascade, which reads with a newer snapshot, then includes its token
// (R-AUTH-029).
func (s *Service) revokeTree(ctx context.Context, tx meta.Tx, qs *q.Queries, c principal.Caller, id, reason string, followRotation bool) ([]Revoked, error) {
	if _, err := qs.IdentityLockTokenTree(ctx, q.IdentityLockTokenTreeParams{ID: id, FollowRotation: followRotation}); err != nil {
		return nil, err
	}
	rows, err := qs.IdentityRevokeCascade(ctx, q.IdentityRevokeCascadeParams{
		ID: id, Reason: reason, RevokedBy: c.Actor.ID, FollowRotation: followRotation,
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	refs := make([]principal.TokenRef, 0, len(rows))
	out := make([]Revoked, 0, len(rows))
	for _, r := range rows {
		refs = append(refs, principal.TokenRef{ID: r.ID, Hash: r.SecretHex})
		out = append(out, Revoked{ID: r.ID, OwnerID: r.OwnerID, Type: Type(r.Type), Root: r.IsRoot})
	}
	if err := principal.NotifyTokens(s.o.Notifier, tx, refs...); err != nil {
		return nil, err
	}
	for _, r := range out {
		why := reason
		if !r.Root {
			why = ReasonParentRevoked
		}
		if err := s.recordRevoked(ctx, tx, c, r.ID, why, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) recordRevoked(ctx context.Context, tx meta.Tx, c principal.Caller, id, reason string, cascaded []string) error {
	d := map[string]any{"reason": reason, "cascaded": orEmpty(cascaded)}
	return s.o.Recorder.RecordTx(ctx, tx, audit.Event{
		Type: audit.EventTokenRevoked, Outcome: audit.Success, Reason: reason, Actor: c.Actor, Source: c.Source,
		Subject: audit.Subject{Kind: "token", ID: id}, Details: mustJSON(d),
		Journal: mustJSON(map[string]string{"tokenId": id, "reason": reason}),
	})
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ByValue is the result of RevokeByValue.
type ByValue struct {
	// Found is true when a row with this secret exists, whether or not it was
	// still live. Callers other than administrators must not learn it.
	Found   bool
	ID      string
	OwnerID string
	// Owner is the owner's reference (username or sa:<name>).
	Owner   string
	Revoked []Revoked
}

// RevokeByValue revokes by the token value, for finders of a leaked token
// (R-AUTH-029, Q-AUTH-6): the reason is leaked, the caller's address is in the
// event, and the cascade also follows rotated_from forward, because a thief may
// have rotated the stolen token first. It runs when the presented token is
// already revoked or expired, as long as its row exists; the presented token's
// own revocation fields stay unchanged. A value that fails the syntax check
// answers [ErrMalformed]; an unknown but well-formed value is Found == false.
func (s *Service) RevokeByValue(ctx context.Context, c principal.Caller, raw string) (ByValue, error) {
	if _, err := Check(raw); err != nil {
		return ByValue{}, ErrMalformed
	}
	hash := Hash(raw)
	var out ByValue
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs := principal.Queries(tx)
		rows, err := qs.IdentityTokenBySecretForRevoke(ctx, hash[:])
		if err != nil {
			return err
		}
		out = ByValue{}
		if len(rows) == 0 {
			return nil
		}
		r := rows[0]
		out.Found, out.ID, out.OwnerID = true, r.ID, r.OwnerID
		out.Owner = Owner{ID: r.OwnerID, Kind: principal.KindFromRow(r.OwnerKind, false), Name: r.OwnerName}.Ref()
		out.Revoked, err = s.revokeTree(ctx, tx, qs, c, r.ID, ReasonLeaked, true)
		return err
	})
	return out, principal.DBErr("token.revoke_by_value", err)
}

// RevokeAll revokes every active token of a principal (R-AUTH-029
// `DELETE /users/{u}/tokens`). The cache entries of the principal's tokens go
// with a change of kind principal.
func (s *Service) RevokeAll(ctx context.Context, c principal.Caller, principalID, reason string) (int, error) {
	if !principal.IsID(principalID) {
		return 0, principal.NotFound("The principal")
	}
	var n int
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs := principal.Queries(tx)
		// The lock a create takes on the owner (lockOwner): a token created at the
		// same time either commits before the revocation reads, and is revoked with
		// the others, or is created after the revocation.
		if rows, err := qs.IdentityLockPrincipal(ctx, principalID); err != nil {
			return err
		} else if len(rows) == 0 {
			return principal.NotFound("The principal")
		}
		ids, err := qs.IdentityRevokeAllOfPrincipal(ctx, q.IdentityRevokeAllOfPrincipalParams{PrincipalID: principalID, RevokedBy: c.Actor.ID, Reason: reason})
		if err != nil {
			return err
		}
		n = len(ids)
		if n == 0 {
			return nil
		}
		if err := principal.NotifyPrincipals(s.o.Notifier, tx, principal.ChangePrincipal, principalID); err != nil {
			return err
		}
		for _, id := range ids {
			if err := s.recordRevoked(ctx, tx, c, id, reason, nil); err != nil {
				return err
			}
		}
		return nil
	})
	return n, principal.DBErr("token.revoke_all", err)
}
