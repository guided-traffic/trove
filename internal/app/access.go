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
// system.bootstrap_anonymous_read makes (Q-AUTHZ-15). The switch is an explicit
// dev-profile bridge that opens the capability settings:read to anonymous callers
// on the default endpoint only, and only while that endpoint admits anonymous
// requests (R-EP-010); it is refused with an https base URL by the configuration
// check, and it grants nothing else. It sits in front of the engine instead of
// inside it because the endpoint of the request is part of the condition and
// package authz does not see the edge's context. Without a decision point
// (inner is nil) nobody holds any permission.
type bridgeAccess struct {
	inner         v1.Access
	anonymousRead bool
}

var (
	_ v1.Access       = bridgeAccess{}
	_ v1.ActionAccess = bridgeAccess{}
)

func newBridgeAccess(a authz.Authorizer, anonymousRead bool) bridgeAccess {
	b := bridgeAccess{anonymousRead: anonymousRead}
	if a != nil {
		b.inner = v1.NewAccess(a)
	}
	return b
}

func (b bridgeAccess) Require(ctx context.Context, permission string) error {
	p := httpserver.PrincipalFrom(ctx)
	if p.Anonymous && b.anonymousRead && permission == v1.PermissionSystemRead {
		if ep := httpserver.EndpointFrom(ctx); ep != nil && ep.IsDefault() && ep.AnonymousAllowed() {
			return nil
		}
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
