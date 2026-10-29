package app

import (
	"context"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// bridgeAccess is the permission check of the management API: the decision
// point of the authz area, with the one exception that
// system.bootstrap_anonymous_read makes (Q-AUTHZ-16). The switch is an explicit
// dev-profile bridge that answers settings:read for anonymous callers of the
// system information and the replica list (bootstrapPath), on the default
// endpoint only, and only while that endpoint admits anonymous requests
// (R-EP-010); it is refused with an https base URL by the configuration check,
// and it grants nothing else. It sits in front of the engine instead of
// inside it because the endpoint of the request is part of the condition and
// package authz does not see the edge's context. Without a decision point
// (inner is nil) nobody holds any permission.
type bridgeAccess struct {
	inner         v1.Access
	anonymousRead bool
}

var (
	_ v1.Access       = bridgeAccess{}
	_ v1.Prober       = bridgeAccess{}
	_ v1.ActionAccess = bridgeAccess{}
)

func newBridgeAccess(a authz.Authorizer, anonymousRead bool) bridgeAccess {
	b := bridgeAccess{anonymousRead: anonymousRead}
	if a != nil {
		b.inner = v1.NewAccess(a)
	}
	return b
}

// bootstrapPath reports whether path is one of the two operations the bridge
// opens: the system information and the replica list (api-and-ui.md, the
// decision on system.bootstrap_anonymous_read; Q-AUTHZ-71, security H-63).
// settings:read covers more operations since M1, the scheduled tasks, the
// encryption status and the authentication settings among them; those stay
// closed to anonymous callers.
func bootstrapPath(path string) bool {
	return path == "/api/v1/system/info" || path == "/api/v1/system/replicas"
}

// requestPathKey carries the path of a request on an endpoint's chain to the
// bridge; endpointStage sets it.
type requestPathKey struct{}

func withRequestPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, requestPathKey{}, path)
}

// bootstrapOpen reports whether the bridge opens the permission for this request.
func (b bridgeAccess) bootstrapOpen(ctx context.Context, permission string) bool {
	p := httpserver.PrincipalFrom(ctx)
	path, _ := ctx.Value(requestPathKey{}).(string)
	if p.Anonymous && b.anonymousRead && permission == v1.PermissionSystemRead && bootstrapPath(path) {
		if ep := httpserver.EndpointFrom(ctx); ep != nil && ep.IsDefault() && ep.AnonymousAllowed() {
			return true
		}
	}
	return false
}

// Holds implements [v1.Prober]: Require as a question, without the audit event of
// a refusal. Without a decision point nobody holds a permission.
func (b bridgeAccess) Holds(ctx context.Context, permission string) (bool, error) {
	if b.bootstrapOpen(ctx, permission) {
		return true, nil
	}
	if b.inner == nil {
		return false, nil
	}
	return v1.Holds(ctx, b.inner, permission)
}

func (b bridgeAccess) Require(ctx context.Context, permission string) error {
	p := httpserver.PrincipalFrom(ctx)
	if b.bootstrapOpen(ctx, permission) {
		return nil
	}
	if b.inner == nil {
		if p.Anonymous {
			return authz.Unauthenticated()
		}
		return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
	}
	return b.inner.Require(ctx, permission)
}

func (b bridgeAccess) RequireAction(ctx context.Context, c authz.Coordinate) error {
	if aa, ok := b.inner.(v1.ActionAccess); ok {
		return aa.RequireAction(ctx, c)
	}
	if httpserver.PrincipalFrom(ctx).Anonymous {
		return authz.Unauthenticated()
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
}
