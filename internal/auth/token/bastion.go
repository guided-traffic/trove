package token

import (
	"context"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The Bastion token (type bst, R-BAS-004, R-BAS-005): issued for a Bastion registration, validated for the
// Bastion interface only. It follows the format, the storage, the cache with its revocation bound and the
// revocation of every other token (R-AUTH-022, R-AUTH-023, R-AUTH-029); what is its own is the lifetime rule
// (always expiring, auth.tokens.bst), the limit per registration, the owner (a registration, which holds no
// grant) and the audit subject (resource_type bastion). Service.Check refuses the type before any lookup.

// BastionRequest is the issuance of a Bastion token (POST /api/v1/bastions/{name}/tokens).
type BastionRequest struct {
	Caller principal.Caller
	// BastionID and BastionName identify the registration the token belongs to.
	BastionID   string
	BastionName string
	Description string
	// TTL is the lifetime; zero applies auth.tokens.bst.default_ttl. A value outside 60 seconds to
	// auth.tokens.bst.max_ttl is 400 invalid_ttl.
	TTL time.Duration
	// Via is created_via: ui, api, admin-cli.
	Via string
}

// BastionInfo is what the validation of a Bastion token yields: the token and the enabled registration it
// belongs to. It holds no secret and no hash.
type BastionInfo struct {
	TokenID   string
	Hint      string
	BastionID string
	Name      string
	ExpiresAt time.Time
}

// CreateBastion issues a Bastion token in a transaction of its own.
func (s *Service) CreateBastion(ctx context.Context, r BastionRequest) (Created, error) {
	var created Created
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		var err error
		created, err = s.CreateBastionTx(ctx, tx, r)
		return err
	})
	if err != nil {
		return Created{}, principal.DBErr("token.create_bastion", err)
	}
	return created, nil
}

// CreateBastionTx is [Service.CreateBastion] inside the caller's transaction. The creating transaction
// locks the registration's principal row before it counts its unrevoked tokens, so concurrent issuances on
// different replicas cannot exceed auth.tokens.max_active_per_principal.bst (409 token_limit_reached).
func (s *Service) CreateBastionTx(ctx context.Context, tx meta.Tx, r BastionRequest) (Created, error) {
	ttl, err := s.bastionTTL(r.TTL)
	if err != nil {
		return Created{}, err
	}
	sc, err := scope.Parse(nil, s.o.Scope)
	if err != nil {
		return Created{}, err
	}
	sp := insertSpec{
		req: CreateRequest{
			Caller: r.Caller, Type: TypeBST, OwnerID: r.BastionID, Description: r.Description, Via: r.Via,
		},
		scope: sc, life: lifetime{ttl: ttl}, maxActive: s.o.Tokens.MaxActive.BST, via: r.Via,
	}
	created, err := s.insertTx(ctx, tx, sp)
	return created, principal.DBErr("token.create_bastion", err)
}

// bastionTTL applies the lifetime rule of R-BAS-004: a Bastion token always expires, and a lifetime outside 60
// seconds to max_ttl is 400 invalid_ttl (not the 400 ttl_exceeds_maximum of the other types: the page names the
// one code).
func (s *Service) bastionTTL(ttl time.Duration) (time.Duration, error) {
	cfg := s.o.Tokens.BST
	if ttl == 0 {
		ttl = cfg.DefaultTTL
	}
	if ttl < minTTL || ttl > cfg.MaxTTL {
		return 0, ttlErr(errs.CodeInvalidTTL, "ttlSeconds must be at least 60 and at most "+cfg.MaxTTL.String()+".")
	}
	return ttl, nil
}

// RevokeBastionToken revokes one token of a registration (R-BAS-004): token.revoked with resource_type bastion
// and the journal entry (R-HA-054). A token that is revoked already, or has expired, ends nothing and
// succeeds; a token that is no Bastion token of that registration is a 404.
func (s *Service) RevokeBastionToken(ctx context.Context, c principal.Caller, bastion, id string) error {
	if !principal.IsID(id) {
		return principal.NotFound("The token")
	}
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs := principal.Queries(tx)
		rows, err := qs.BastionTokenOwner(ctx, q.BastionTokenOwnerParams{ID: id, Name: bastion})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return principal.NotFound("The token")
		}
		_, err = s.revokeTree(ctx, tx, qs, c, id, ReasonAdmin, false)
		return err
	})
	return principal.DBErr("token.revoke_bastion", err)
}

// RevokeAllOfBastionTx revokes every unrevoked token of a registration in tx, one token.revoked event with its
// journal entry each, and tells the replicas' caches (deleting a registration, R-BAS-004). It returns the IDs.
func (s *Service) RevokeAllOfBastionTx(ctx context.Context, tx meta.Tx, c principal.Caller, bastionID, reason string) ([]string, error) {
	qs := principal.Queries(tx)
	ids, err := qs.IdentityRevokeAllOfPrincipal(ctx, q.IdentityRevokeAllOfPrincipalParams{PrincipalID: bastionID, RevokedBy: c.Actor.ID, Reason: reason})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	if err := principal.NotifyPrincipals(s.o.Notifier, tx, principal.ChangePrincipal, bastionID); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := s.recordRevoked(ctx, tx, c, id, bastionID, reason, TypeBST, nil); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// AuthenticateBastion validates a raw Bastion token (R-BAS-005): the syntax without database access, the
// replica's cache while the freshness gate is open, then the database through BastionTokenBySecret. A token of
// another type, a malformed, unknown, revoked, expired token and a token whose registration is disabled are
// one of the Err values; PostgreSQL not answering is an *errs.Error of kind Unavailable, never a cached
// acceptance. The cache, its invalidation by the change area auth and the revocation bound are those of every
// token (R-AUTH-023); a revoked or expired token logs token.rejected at most once an hour.
func (s *Service) AuthenticateBastion(ctx context.Context, raw string, req Request) (BastionInfo, error) {
	typ, err := Check(raw)
	if err != nil || typ != TypeBST {
		return BastionInfo{}, ErrMalformed
	}
	hash := Hash(raw)
	gateOpen := s.o.Gate.Open()
	if gateOpen {
		if ct, ok := s.tokens.Get(hash); ok && ct.info.Type == TypeBST {
			if ct.info.ExpiresAt != nil && s.o.Now().Before(*ct.info.ExpiresAt) {
				return bastionInfo(raw, ct.info), nil
			}
			s.tokens.Remove(hash) // expired since the fill: the database path logs the rejection
		}
	}
	load := func(ctx context.Context) (cachedToken, error) { return s.loadBastion(ctx, hash) }
	var ct cachedToken
	if gateOpen {
		ct, err = s.tokens.Load(ctx, hash, load)
	} else {
		ct, err = s.flights.Do(ctx, hash, s.o.Gate.Bound, load)
	}
	if err != nil {
		var rej *rejection
		if errors.As(err, &rej) {
			if rej.due {
				s.logRejection(ctx, rej, req.Source)
			}
			return BastionInfo{}, rej.err
		}
		return BastionInfo{}, err
	}
	return bastionInfo(raw, ct.info), nil
}

func bastionInfo(raw string, i Info) BastionInfo {
	out := BastionInfo{TokenID: i.ID, Hint: Hint(raw), BastionID: i.Owner.ID, Name: i.Owner.Name}
	if i.ExpiresAt != nil {
		out.ExpiresAt = *i.ExpiresAt
	}
	return out
}

func (s *Service) loadBastion(ctx context.Context, hash [32]byte) (cachedToken, error) {
	var ct cachedToken
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).BastionTokenBySecret(ctx, hash[:])
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrUnknown
		}
		r := rows[0]
		ref := Owner{ID: r.BastionID, Kind: principal.KindBastion, Name: r.Name}.Ref()
		switch {
		case r.Revoked:
			return &rejection{err: ErrRevoked, id: r.TokenID, hint: r.Hint, owner: ref, due: r.RejectionDue}
		case r.Expired:
			return &rejection{err: ErrExpired, id: r.TokenID, hint: r.Hint, owner: ref, due: r.RejectionDue}
		case !r.Enabled:
			return ErrOwner
		case !r.ExpiresAt.Valid:
			// A Bastion token always expires (R-BAS-004); a row without an expiry is a storage fault, never an
			// acceptance.
			return ErrUnknown
		}
		exp := r.ExpiresAt.Time
		ct.info = Info{
			ID: r.TokenID, Type: TypeBST, Scope: scope.Default(), ExpiresAt: &exp,
			Owner: principal.Principal{ID: r.BastionID, Kind: principal.KindBastion, Name: r.Name, Status: principal.StatusActive},
		}
		return nil
	})
	if err != nil {
		var rej *rejection
		switch {
		case errors.As(err, &rej), errors.Is(err, ErrUnknown), errors.Is(err, ErrOwner):
			return cachedToken{}, err
		}
		return cachedToken{}, principal.DBErr("token.authenticate_bastion", err)
	}
	return ct, nil
}
