package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// ActorFrom converts the caller into the actor of an audit event ([edge.ActorFrom]).
func ActorFrom(p httpserver.Principal) audit.Actor { return edge.ActorFrom(p) }

// SubjectFrom is the subject of the request whose context ctx is
// ([edge.SubjectOf]): the caller the authentication stage stored and the
// endpoint whose listener accepted the request. The one conversion lives in
// internal/api/edge, where the raw /files/ routes and the format dispatcher use
// it too; the architecture test refuses a subject built anywhere else.
func SubjectFrom(ctx context.Context) authz.Subject { return edge.SubjectOf(ctx) }
