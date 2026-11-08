package token

import (
	"context"
	"errors"
	"fmt"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Standing reads a token by its ID for a job that continues a request the token authenticated (Q-AUTHZ-260, Q-AUTHZ-261): the row as it
// is now, read from PostgreSQL and never from the token cache, so a revocation that committed before the call is seen at once. A revoked
// token answers [ErrRevoked] and a token that no longer exists [ErrUnknown]. An expired token answers its facts: a token that reached its
// expiry was ended by no one, and the request it authenticated was accepted while it lived. The owner's status is not read here; the
// caller reads the principal, whose cache keeps the revocation bound of R-AUTH-023. The scope and a CI token's frozen bundle are parsed
// as the validation parses them, and a row that does not parse is an error, never a token without its ceiling.
func (s *Service) Standing(ctx context.Context, id string) (Info, error) {
	if !principal.IsID(id) {
		return Info{}, ErrUnknown
	}
	var info Info
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).IdentityTokenRow(ctx, id)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrUnknown
		}
		r := rows[0]
		if r.RevokedAt.Valid {
			return ErrRevoked
		}
		sc, err := scope.ParseStored(r.Scope)
		if err != nil {
			return fmt.Errorf("token %s: stored scope: %w", r.ID, err)
		}
		info = Info{
			ID: r.ID, Type: Type(r.Type), Scope: sc,
			Owner: principal.Principal{ID: r.OwnerID, Kind: principal.KindFromRow(r.OwnerKind, false), Name: r.OwnerName},
		}
		if r.ExpiresAt.Valid {
			t := r.ExpiresAt.Time
			info.ExpiresAt = &t
		}
		if info.Type == TypeCIT {
			ci, err := decodeCIContext(r.CiContext)
			if err != nil {
				return fmt.Errorf("token %s: stored CI context: %w", r.ID, err)
			}
			info.CI = ci
		}
		return nil
	})
	switch {
	case err == nil:
		return info, nil
	case errors.Is(err, ErrUnknown), errors.Is(err, ErrRevoked):
		return Info{}, err
	}
	return Info{}, principal.DBErr("token.standing", err)
}
