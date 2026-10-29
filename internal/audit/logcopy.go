package audit

import (
	"context"
	"log/slog"
)

// logCopy writes the event to the log stream as a log: audit record with the
// fields of the table row (R-OPS-015, INV-16). The replica and role fields come
// from the logger. The database copy is authoritative; this copy follows the
// commit and is the only one of an event that could not be stored.
func (w *Writer) logCopy(r row) {
	attrs := []slog.Attr{
		slog.String("id", r.ID),
		slog.String("occurred_at", r.At),
		slog.String("action", r.Action),
		slog.String("category", r.Category),
		slog.String("outcome", r.Outcome),
		slog.Group("actor", anys("type", r.ActorType, "id", r.ActorID, "name", r.ActorName,
			"token_id", r.TokenID, "auth_method", r.AuthMethod)...),
		slog.Group("source", anys("ip", r.SourceIP, "user_agent", r.UserAgent)...),
		slog.Group("resource", anys("type", r.ResourceType, "id", r.ResourceID)...),
	}
	attrs = append(attrs, attrs2("trace_id", r.TraceID, "endpoint", r.Endpoint, "repository", r.Repository,
		"path", r.Path, "reason", r.Reason)...)
	attrs = append(attrs, slog.Any("details", r.Details))
	w.log.LogAttrs(context.Background(), slog.LevelInfo, "audit", attrs...)
}

// attrs2 turns key/value pairs into attributes and leaves out the empty values.
func attrs2(kv ...string) []slog.Attr {
	out := make([]slog.Attr, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			out = append(out, slog.String(kv[i], kv[i+1]))
		}
	}
	return out
}

// anys is attrs2 for slog.Group, which takes its members as any.
func anys(kv ...string) []any {
	as := attrs2(kv...)
	out := make([]any, len(as))
	for i, a := range as {
		out[i] = a
	}
	return out
}
