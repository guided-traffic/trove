package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
)

// Outcome is the result an event reports (R-AUTH-041).
type Outcome string

// The outcomes. Denied is an authorization refusal (authz.denied); the table
// accepts these three.
const (
	Success Outcome = "success"
	Failure Outcome = "failure"
	Denied  Outcome = "denied"
)

// Actor is who did it. Kind is "user", "service_account", "ci", "break_glass",
// "anonymous" or "admin-cli" (R-AUTH-041); ID and Name are empty where the kind
// has none. It is plain data so that no domain package has to import another to
// record an event.
type Actor struct {
	Kind string
	ID   string
	Name string
	// TokenID is the token the request authenticated with, empty for a session,
	// a job or a command. AuthMethod is "bearer", "basic", "session", "api_key" or
	// empty (R-OPS-017). Neither is ever a token value.
	TokenID    string
	AuthMethod string
}

// Subject is what the event affected: a principal, a token, a repository, a
// policy. Kind is the object kind in the API's vocabulary ("user", "token",
// "repository", …).
type Subject struct {
	Kind string
	ID   string
	Name string
}

// Source is where the request came from (R-AUTH-041). Empty outside a request.
type Source struct {
	ClientAddr string
	UserAgent  string
	// TraceID is the request's trace ID (httpserver.RequestID).
	TraceID string
}

// Event is one audit event. Details holds the data of the type's schema; it
// never holds a secret, a token value or hash, a password or an authorization
// code (R-AUTH-041).
type Event struct {
	// Type is a registered event type name.
	Type    string
	Actor   Actor
	Outcome Outcome
	// Reason is a stable reason code, empty on success.
	Reason  string
	Source  Source
	Subject Subject
	// Repository, Path and Endpoint are the columns of the same names: the
	// repository key and the path the event concerns, and the endpoint the request
	// arrived on (empty for jobs). Repository falls back to the subject's name
	// when the subject is a repository.
	Repository string
	Path       string
	Endpoint   string
	// Project is the key of the project the event concerns, the audit_event.project column (Q-AUTHZ-209);
	// empty for an event of no project. Repository holds the qualified name <project>/<key>.
	Project string
	// At, when set, is the time the event happened (occurred_at) for an asynchronous event
	// whose producer learned of it later: the download intake of the Bastion interface
	// records the time the Bastion reports (R-BAS-016). The zero value is the time of the
	// call. A transactional event is always stamped by the database (the transaction's now()).
	At time.Time
	// Details is a JSON object of the type's data schema; nil is {}.
	Details json.RawMessage
	// Journal is the producer's journal entry (R-HA-054): IDs and states, never
	// a secret. The recorder writes a revocation_outbox row in the same
	// transaction when the type is flagged and the entry is present. Nil means
	// no entry; a type with EntryRequired fails the transaction without one.
	Journal json.RawMessage
}

// Recorder writes audit events. The implementation is WP1.7's; every domain
// package takes this interface.
type Recorder interface {
	// RecordTx inserts the event, and its revocation_outbox row when it carries a
	// journal entry, in tx. The insert fails the caller's transaction (R-OPS-015:
	// no change exists without its audit record). It is for Transactional types.
	RecordTx(ctx context.Context, tx meta.Tx, ev Event) error
	// Record queues an event of an Async type in the bounded buffer. It never
	// blocks and never fails the request: a full buffer writes the log copy only
	// and counts trove_audit_events_dropped_total.
	Record(ctx context.Context, ev Event)
}

// BatchRecorder is a Recorder that writes many transactional events of one
// transaction with a fixed number of statements. *Writer implements it.
type BatchRecorder interface {
	Recorder
	// RecordAllTx is RecordTx for every event of evs, in order, in tx: one
	// invalid event fails the call before anything is written.
	RecordAllTx(ctx context.Context, tx meta.Tx, evs []Event) error
}

// RecordAllTx records evs in tx: in batches when rec is a [BatchRecorder], one
// RecordTx each otherwise. A producer whose change records a number of events
// that only the data bounds (one token.revoked per token of a principal) calls
// it, so the change does not cost a round trip per event.
func RecordAllTx(ctx context.Context, rec Recorder, tx meta.Tx, evs []Event) error {
	if b, ok := rec.(BatchRecorder); ok {
		return b.RecordAllTx(ctx, tx, evs)
	}
	for _, ev := range evs {
		if err := rec.RecordTx(ctx, tx, ev); err != nil {
			return err
		}
	}
	return nil
}

// Nop is a Recorder that discards everything, for tests and for components
// built before the recorder exists.
type Nop struct{}

var _ BatchRecorder = Nop{}

// RecordTx discards the event.
func (Nop) RecordTx(context.Context, meta.Tx, Event) error { return nil }

// RecordAllTx discards the events.
func (Nop) RecordAllTx(context.Context, meta.Tx, []Event) error { return nil }

// Record discards the event.
func (Nop) Record(context.Context, Event) {}
