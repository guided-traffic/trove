package auth

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
)

// TokenStanding reads a token by its ID as it is now ([token.Service.Standing]).
type TokenStanding interface {
	Standing(ctx context.Context, id string) (token.Info, error)
}

// SessionStanding reads whether a session still stands and returns its principal ([session.Standing] over the replica's database).
type SessionStanding func(ctx context.Context, id string) (principalID string, err error)

// JobStanding reads the standing of the subject a job carries for the request that queued it (Q-AUTHZ-260): whether the principal is
// still active and of the job's kind, its name and groups now, and what its credential holds now. It is the authentication stage's
// question asked again for a request that is over, without the secret: the principal from the principal cache under the freshness
// gate, so a disable, a rename or a membership change reaches the job within auth.revocation_bound as it reaches a request
// (R-AUTH-023); the token and the session by their ID from PostgreSQL, so a revocation or a withdrawal that committed before the read
// is seen at once. A principal that is no longer active, and a credential that was withdrawn, refuse with the 401 a request would get
// now (Q-AUTHZ-261, Q-AUTHZ-262); a credential whose row is gone refuses with a reason of its own (Q-STOR-194); a token that expired
// and a session that timed out do not refuse, because no one withdrew them.
type JobStanding struct {
	principals principal.Store
	tokens     TokenStanding
	sessions   SessionStanding
}

// NewJobStanding creates the reader.
func NewJobStanding(principals principal.Store, tokens TokenStanding, sessions SessionStanding) *JobStanding {
	return &JobStanding{principals: principals, tokens: tokens, sessions: sessions}
}

// Standing reads the standing of j. An error of kind Unauthenticated is a withdrawal; any other error is a store that did not answer.
func (s *JobStanding) Standing(ctx context.Context, j authz.JobSubject) (authz.Standing, error) {
	if j.Anonymous {
		// An anonymous caller has no principal and no credential; the decision point reads the runtime switch of R-AUTH-037 itself.
		return authz.Standing{}, nil
	}
	p, err := s.principals.ByID(ctx, j.ID)
	switch {
	case errors.Is(err, principal.ErrNotFound):
		return authz.Standing{}, withdrawn(errs.CodePrincipalDisabled, "The principal that asked for this delete no longer exists; the job stopped.")
	case err != nil:
		return authz.Standing{}, unavailable(err)
	case p.Status != principal.StatusActive:
		return authz.Standing{}, withdrawn(errs.CodePrincipalDisabled, "The principal that asked for this delete is no longer active; the job stopped.")
	case p.Kind != j.Kind:
		return authz.Standing{}, withdrawn(errs.CodePrincipalDisabled, "The principal that asked for this delete is no longer of the kind it was; the job stopped.")
	}
	groups, err := s.principals.Groups(ctx, j.ID)
	if err != nil {
		return authz.Standing{}, unavailable(err)
	}
	out := authz.Standing{Kind: p.Kind, Name: p.Name, Groups: groups}
	switch {
	case j.TokenID != "":
		sc, ci, err := s.token(ctx, j)
		if err != nil {
			return authz.Standing{}, err
		}
		out.Scope, out.CI = sc, ci
	case j.SessionID != "":
		if err := s.session(ctx, j); err != nil {
			return authz.Standing{}, err
		}
	default:
		// Every request that is not anonymous carries a token or a session; a subject that names neither cannot be asked again.
		return authz.Standing{}, withdrawn(errs.CodeInvalidToken, "The delete job names no credential that can be checked again; the job stopped.")
	}
	return out, nil
}

// token reads the token of j: its scope and a CI token's bundle, or the refusal of a revoked, purged or foreign token.
func (s *JobStanding) token(ctx context.Context, j authz.JobSubject) (*scope.Scope, *claims.Frozen, error) {
	info, err := s.tokens.Standing(ctx, j.TokenID)
	switch {
	case errors.Is(err, token.ErrRevoked):
		return nil, nil, withdrawn(errs.CodeInvalidToken, "The token that asked for this delete was revoked; the job stopped.")
	case errors.Is(err, token.ErrUnknown):
		return nil, nil, withdrawn(errs.CodeInvalidToken,
			"The token that asked for this delete no longer exists: its row was removed after it ended, so the job can no longer check it; the job stopped.")
	case err != nil:
		return nil, nil, unavailable(err)
	case info.Owner.ID != j.ID:
		return nil, nil, withdrawn(errs.CodeInvalidToken, "The token that asked for this delete belongs to another principal; the job stopped.")
	}
	sc := info.Scope
	return &sc, info.CI, nil
}

// session reads the session of j: nothing when it still stands, else the refusal of a withdrawn, purged or foreign session.
func (s *JobStanding) session(ctx context.Context, j authz.JobSubject) error {
	owner, err := s.sessions(ctx, j.SessionID)
	switch {
	case errors.Is(err, session.ErrWithdrawn):
		return withdrawn(errs.CodeSessionExpired, "The session that asked for this delete was ended; the job stopped.")
	case errors.Is(err, session.ErrPurged):
		return withdrawn(errs.CodeSessionExpired,
			"The session that asked for this delete no longer exists: its row was removed after it ended, so the job can no longer check it; the job stopped.")
	case err != nil:
		return unavailable(err)
	case owner != j.ID:
		return withdrawn(errs.CodeSessionExpired, "The session that asked for this delete belongs to another principal; the job stopped.")
	}
	return nil
}

func withdrawn(code, detail string) error { return errs.E(errs.Unauthenticated, code, detail) }

// unavailable passes on an error of the stores' own kind and turns any other into a 503, so a store that does not answer fails the
// attempt and never the job.
func unavailable(err error) error {
	if _, ok := errs.As(err); ok {
		return err
	}
	return errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "The principal cannot be resolved right now.", errs.WithCause(err))
}
