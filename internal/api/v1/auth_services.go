package v1

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// AuthServices are what the identity and login operations need beyond the
// system operations (users, groups, service accounts, tokens, sessions, login
// options, the runtime authentication settings and the user preferences). The
// composition root fills it; a nil Manager disables the identity operations with
// 503 unavailable instead of a panic, a nil Sessions the session operations.
type AuthServices struct {
	// Manager is the write side of the principal table. Its database is a
	// principal.Joinable, so an idempotent create is one transaction with its
	// idempotency row.
	Manager *principal.Manager
	// Tokens is the token service; its database is the same Joinable.
	Tokens *token.Service
	// Settings is the runtime authentication settings store.
	Settings *token.SettingsStore
	// Sessions is the session store; nil on a replica without one.
	Sessions SessionService
	// Login renders the login options; nil on a replica without a login.
	Login LoginService
	// FlowStarts is the limiter auth.flow_starts of the unauthenticated endpoints
	// that run on the client address (R-AUTH-038); the revocation by value takes
	// it. Nil: no limit.
	FlowStarts FlowLimiter
	// EndpointConfigured reports whether the configuration holds an endpoint of
	// that name. A token listing shows the bound names it does not hold under
	// unknownEndpoints (R-EP-011). Nil: every name counts as configured.
	EndpointConfigured func(name string) bool
	// Config is the auth section: the session windows, the provider issuers for a
	// pending identity link, the break-glass username, the ceilings.
	Config config.Auth
}

// FlowLimiter counts a request of an address against auth.flow_starts and says
// whether it may go on, and if not for how long to wait (*cluster.Limiter behind
// an adapter).
type FlowLimiter interface {
	Allow(ctx context.Context, bucket string) (ok bool, retryAfter time.Duration)
}

// SessionService is the part of the session store the API calls
// (*session.Store).
type SessionService interface {
	// Validate resolves the session cookie of the request; it is how an operation
	// learns the session's last interactive authentication (R-AUTH-011).
	Validate(ctx context.Context, cookie string) (session.Info, error)
	List(ctx context.Context, principalID string) ([]session.LiveSession, error)
	EndOne(ctx context.Context, principalID, sessionID, reason string, actor audit.Actor) (bool, error)
	EndExcept(ctx context.Context, principalID, keepID, reason string, actor audit.Actor) ([]string, error)
	EndPrincipalSessions(ctx context.Context, principalID string, actor audit.Actor) ([]string, error)
}

// LoginService renders the options of the sign-in screen (*oidc.Service).
type LoginService interface {
	LoginOptions(anonymousAccess bool) oidc.LoginOptions
}

// identityReady answers 503 and reports false when the replica has no identity
// services (a worker, or the HTTP shell of a test).
func (s *Server) identityReady(w http.ResponseWriter, r *http.Request) bool {
	if s.o.Auth.Manager == nil || s.o.Auth.Tokens == nil || s.idem == nil {
		httpserver.Problem(w, r, unavailable("The identity API"))
		return false
	}
	return true
}

// caller is who acts and from where, for the audit events of a domain call. Its
// RoleHolders asks the decision point for roles:write only when the domain finds
// that the principal it changes holds a system role (R-AUTHZ-011, Q-AUTHZ-69),
// so a refusal is decided, and audited, only where it applies; a token's scope
// is its ceiling as for any other permission.
func (s *Server) caller(r *http.Request) principal.Caller {
	p := httpserver.PrincipalFrom(r.Context())
	return principal.Caller{
		Actor:  audit.Actor{Kind: string(p.Kind), ID: p.ID, Name: p.Name, TokenID: p.Credential.TokenID, AuthMethod: string(p.Credential.Method)},
		Source: auditSource(r),
		RoleHolders: func(ctx context.Context) error {
			if s.o.Access == nil {
				return errs.E(errs.Forbidden, errs.CodeForbidden, "The permission roles:write is required.")
			}
			return s.o.Access.Require(ctx, string(authz.RolesWrite))
		},
	}
}

// failIdentity answers a domain error: the refusals of the principal package that
// are not problems yet become the problems of R-AUTH-016, everything else goes
// through httpserver.Problem as it is.
func failIdentity(w http.ResponseWriter, r *http.Request, err error) {
	httpserver.Problem(w, r, identityErr(err))
}

func identityErr(err error) error {
	switch {
	case errors.Is(err, principal.ErrBreakGlass):
		return errs.E(errs.Conflict, errs.CodeBreakGlassImmutable, "The break-glass account cannot be disabled or deleted; it is managed by configuration.")
	case errors.Is(err, principal.ErrAdminBinding):
		return errs.E(errs.Unavailable, errs.CodeUnavailable, "The administrator role binding is not available on this replica.")
	}
	return err
}

// ascending refuses a descending sort of a list that has only one order.
func ascending(lq ListQuery) error {
	if lq.Desc {
		return errs.E(errs.Invalid, errs.CodeInvalid, "This list has one order; a descending sort is not supported.")
	}
	return nil
}

// afterName is the name the keyset query continues after, from a cursor of one
// sort-key value.
func afterName(lq ListQuery) (string, error) {
	switch len(lq.After) {
	case 0:
		return "", nil
	case 1:
		return lq.After[0], nil
	}
	return "", invalidCursor()
}

// oneOf answers 400 for a filter value outside the allowed set.
func oneOf(name, value string, allowed ...string) error {
	if value == "" {
		return nil
	}
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter "+strconv.Quote(name)+" must be one of "+strings.Join(allowed, ", ")+".")
}

func refTo[T any](v T) *T { return &v }

// optString is nil for the empty string, so "no value" is the JSON null.
func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func utc(t time.Time) time.Time { return t.UTC() }

func uuidOf(id string) openapi_types.UUID { return parseUUID(id) }

// location is the absolute URL of a path on the public base URL.
func location(r *http.Request, path string) string {
	return strings.TrimSuffix(httpserver.InfoFrom(r.Context()).BaseURL, "/") + path
}

// deref returns the slice a pointer holds, nil for a nil pointer.
func deref[T any](p *[]T) []T {
	if p == nil {
		return nil
	}
	return *p
}
