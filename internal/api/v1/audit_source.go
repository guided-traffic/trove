package v1

import (
	"context"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

// requestRecorder completes the source of an event from the request in its
// context: the client address (after the trusted-proxy rule), the trace ID and the
// endpoint. Domain packages below the edge cannot read the request, so an event
// they build carries an actor and no source; this is the one place that adds it
// (Q-OPS-49). The writer's deduplication of an anonymous denial keys on the client
// address (R-OPS-015), and the filter traceId of R-OPS-017 finds the event by the
// ID the client received as X-Request-Id. A field the producer set stays; outside
// a request (a job, a command) nothing is added.
type requestRecorder struct{ audit.Recorder }

// WithRequestSource wraps a recorder so that every event it records carries the
// source of the request in the context.
func WithRequestSource(r audit.Recorder) audit.Recorder {
	if r == nil {
		return nil
	}
	return requestRecorder{r}
}

func (r requestRecorder) RecordTx(ctx context.Context, tx meta.Tx, ev audit.Event) error {
	return r.Recorder.RecordTx(ctx, tx, completeSource(ctx, ev))
}

func (r requestRecorder) Record(ctx context.Context, ev audit.Event) {
	r.Recorder.Record(ctx, completeSource(ctx, ev))
}

func completeSource(ctx context.Context, ev audit.Event) audit.Event {
	from := httpserver.SourceFrom(ctx)
	if ev.Source.ClientAddr == "" {
		ev.Source.ClientAddr = from.ClientAddr
	}
	if ev.Source.UserAgent == "" {
		ev.Source.UserAgent = from.UserAgent
	}
	if ev.Source.TraceID == "" {
		ev.Source.TraceID = from.TraceID
	}
	if ev.Endpoint == "" {
		if ep := httpserver.EndpointFrom(ctx); ep != nil {
			ev.Endpoint = ep.Name()
		}
	}
	return ev
}
