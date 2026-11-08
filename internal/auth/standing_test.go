package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
)

// standingPrincipals is the principal store of the standing tests: one principal with its status and groups, or an error.
type standingPrincipals struct {
	p      principal.Principal
	groups []principal.GroupRef
	err    error
}

func (s standingPrincipals) ByID(_ context.Context, id string) (principal.Principal, error) {
	if s.err != nil {
		return principal.Principal{}, s.err
	}
	if id != s.p.ID {
		return principal.Principal{}, principal.ErrNotFound
	}
	return s.p, nil
}

func (standingPrincipals) ByName(context.Context, principal.Kind, string) (principal.Principal, error) {
	return principal.Principal{}, principal.ErrNotFound
}

func (s standingPrincipals) Groups(context.Context, string) ([]principal.GroupRef, error) {
	return s.groups, nil
}

type standingTokens struct {
	info token.Info
	err  error
}

func (s standingTokens) Standing(context.Context, string) (token.Info, error) { return s.info, s.err }

func TestJobStanding(t *testing.T) {
	const id = "0190a8c2-0000-7000-8000-000000000001"
	ctx := context.Background()
	active := principal.Principal{ID: id, Kind: principal.KindUser, Name: "deleter", Status: principal.StatusActive}
	devs := []principal.GroupRef{{ID: "g1", Name: "developers"}}
	narrow := scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"it/bulk"}, Actions: []string{"read"}}}}
	viaToken := authz.JobSubject{Kind: principal.KindUser, ID: id, TokenID: "tok-1"}
	viaSession := authz.JobSubject{Kind: principal.KindUser, ID: id, SessionID: "ses-1"}
	noSession := func(context.Context, string) (string, error) { return "", errors.New("no session asked") }
	read := func(p standingPrincipals, tok standingTokens, ses SessionStanding, j authz.JobSubject) (authz.Standing, error) {
		return NewJobStanding(p, tok, ses).Standing(ctx, j)
	}
	withdrawn := func(t *testing.T, err error, code, detail string) {
		t.Helper()
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.Unauthenticated || e.Code != code || !strings.Contains(e.Detail, detail) {
			t.Fatalf("want the withdrawal %s %q, got %v", code, detail, err)
		}
	}

	// R-AUTHZ-002, R-AUTHZ-013: the groups, kind and name now; the scope and bundle of the row.
	t.Run("R-STOR-018 a token's standing is the principal's kind, name and groups now and the scope and bundle of its row", func(t *testing.T) {
		ci := &claims.Frozen{Mappings: []string{"ci:gitlab/acme"}}
		renamed := active
		renamed.Name = "deleter-renamed"
		got, err := read(standingPrincipals{p: renamed, groups: devs}, standingTokens{info: token.Info{Owner: active, Scope: narrow, CI: ci}}, noSession, viaToken)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != principal.KindUser || got.Name != "deleter-renamed" || len(got.Groups) != 1 || got.Groups[0].ID != "g1" ||
			got.Scope == nil || len(got.Scope.Repositories) != 1 || got.CI != ci {
			t.Fatalf("standing = %+v", got)
		}
	})

	// R-AUTH-016: a disabled, deleted or changed principal.
	t.Run("R-STOR-018 a principal that is no longer active, no longer exists or is of another kind withdraws the job's subject", func(t *testing.T) {
		disabled := active
		disabled.Status = principal.StatusDisabled
		_, err := read(standingPrincipals{p: disabled}, standingTokens{info: token.Info{Owner: active}}, noSession, viaToken)
		withdrawn(t, err, errs.CodePrincipalDisabled, "no longer active")
		_, err = read(standingPrincipals{p: principal.Principal{ID: "someone-else"}}, standingTokens{}, noSession, viaToken)
		withdrawn(t, err, errs.CodePrincipalDisabled, "no longer exists")
		other := active
		other.Kind = principal.KindServiceAccount
		_, err = read(standingPrincipals{p: other}, standingTokens{info: token.Info{Owner: active}}, noSession, viaToken)
		withdrawn(t, err, errs.CodePrincipalDisabled, "kind")
	})

	// R-AUTH-023: a revoked token; Q-STOR-194: a purged one has a reason of its own.
	t.Run("R-STOR-018 a revoked token withdraws the job's subject, a purged token and a token of another owner too, each with its reason", func(t *testing.T) {
		_, err := read(standingPrincipals{p: active}, standingTokens{err: token.ErrRevoked}, noSession, viaToken)
		withdrawn(t, err, errs.CodeInvalidToken, "was revoked")
		_, err = read(standingPrincipals{p: active}, standingTokens{err: token.ErrUnknown}, noSession, viaToken)
		withdrawn(t, err, errs.CodeInvalidToken, "no longer exists")
		_, err = read(standingPrincipals{p: active}, standingTokens{info: token.Info{Owner: principal.Principal{ID: "other"}}}, noSession, viaToken)
		withdrawn(t, err, errs.CodeInvalidToken, "another principal")
	})

	// R-AUTH-010: an ended session; Q-STOR-194: a purged one.
	t.Run("R-STOR-018 a withdrawn or purged session withdraws the job's subject; a standing session carries no scope", func(t *testing.T) {
		ended := func(context.Context, string) (string, error) { return "", session.ErrWithdrawn }
		_, err := read(standingPrincipals{p: active}, standingTokens{}, ended, viaSession)
		withdrawn(t, err, errs.CodeSessionExpired, "was ended")
		purged := func(context.Context, string) (string, error) { return "", session.ErrPurged }
		_, err = read(standingPrincipals{p: active}, standingTokens{}, purged, viaSession)
		withdrawn(t, err, errs.CodeSessionExpired, "no longer exists")
		other := func(context.Context, string) (string, error) { return "other", nil }
		_, err = read(standingPrincipals{p: active}, standingTokens{}, other, viaSession)
		withdrawn(t, err, errs.CodeSessionExpired, "another principal")
		mine := func(context.Context, string) (string, error) { return id, nil }
		got, err := read(standingPrincipals{p: active, groups: devs}, standingTokens{}, mine, viaSession)
		if err != nil || got.Scope != nil || got.CI != nil || len(got.Groups) != 1 {
			t.Fatalf("standing = %+v, %v", got, err)
		}
	})

	// R-AUTH-001: every request that is not anonymous carries a credential.
	t.Run("R-STOR-018 a subject that names no credential cannot be asked again; an anonymous one has nothing to ask", func(t *testing.T) {
		_, err := read(standingPrincipals{p: active}, standingTokens{}, noSession, authz.JobSubject{Kind: principal.KindUser, ID: id})
		withdrawn(t, err, errs.CodeInvalidToken, "names no credential")
		got, err := read(standingPrincipals{err: errors.New("never asked")}, standingTokens{}, noSession, authz.JobSubject{Anonymous: true})
		if err != nil || got.Groups != nil || got.Scope != nil {
			t.Fatalf("anonymous standing = %+v, %v", got, err)
		}
	})

	// R-AUTH-001: a storage outage is a 503, not a 401.
	t.Run("R-STOR-018 a store that does not answer fails the attempt, never with a withdrawal", func(t *testing.T) {
		_, err := read(standingPrincipals{err: errors.New("connection refused")}, standingTokens{}, noSession, viaToken)
		if errs.KindOf(err) != errs.Unavailable {
			t.Fatalf("principal store down: %v", err)
		}
		_, err = read(standingPrincipals{p: active}, standingTokens{err: errors.New("connection refused")}, noSession, viaToken)
		if errs.KindOf(err) != errs.Unavailable {
			t.Fatalf("token row unreadable: %v", err)
		}
	})
}
