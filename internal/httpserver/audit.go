package httpserver

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
)

// SourceFrom is the source of an audit event for the request in ctx: the client
// address after the trusted-proxy rule (R-ARCH-006), the User-Agent and the trace
// ID the client received as X-Request-Id. It is the one place that builds a
// source from a request; a field that is not known (outside a request) is empty.
func SourceFrom(ctx context.Context) audit.Source {
	info := InfoFrom(ctx)
	var src audit.Source
	if info.Client.IsValid() {
		src.ClientAddr = info.Client.String()
	}
	src.UserAgent = info.UserAgent
	src.TraceID = RequestID(ctx)
	return src
}
