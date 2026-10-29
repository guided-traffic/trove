package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/secrets"
)

// Services are what the handlers of the API conventions and of the system
// operations of this milestone (jobs, tasks, key-ring status) need beyond the
// M0 options. The composition root fills it; a field that is nil disables the
// operation that needs it with 503 unavailable instead of a panic.
type Services struct {
	// Config is the api section: page sizes, body limits, retention.
	Config config.API
	// Store is the main pool (*postgres.DB).
	Store Store
	// Queue enqueues the jobs of task runs (*jobs.Queue).
	Queue Enqueuer
	// Jobs is the replica's job registry; the task list is its tasks.
	Jobs *jobs.Registry
	// Maintenance is the configuration of the schedules.
	Maintenance config.Maintenance
	// Audit records the events of the mutating operations; nil discards them.
	Audit audit.Recorder
	// TaskArgs gives, per task name, a new value of the type that task's JSON
	// parameters decode into (blob.GCJobArgs for gc and gc-full). A task without
	// an entry takes an empty object only.
	TaskArgs map[string]func() any
	// TaskPreconditions run in the transaction of a task trigger, before the job
	// is enqueued; an error refuses the trigger and enqueues nothing. The
	// precondition of secrets-reencrypt (409 key_ring_incompatible) is built in.
	TaskPreconditions map[string]func(ctx context.Context) error
	// KeyRing is this replica's ring and Sealed counts the values under each key
	// ID (R-API-049); Rings reads the key IDs of every live replica.
	KeyRing *secrets.KeyRing
	Sealed  secrets.ValueCounter
	Rings   RingSource
}

// Enqueuer enqueues a job in the caller's transaction (*jobs.Queue).
type Enqueuer interface {
	Enqueue(ctx context.Context, tx meta.Tx, e jobs.EnqueueSpec) (jobs.Result, error)
}

// ReplicaRing is the key ring one replica published with its heartbeat
// (R-OPS-003): key IDs only.
type ReplicaRing struct {
	Name        string
	Role        string
	Lost        bool
	ActiveKeyID string
	KeyIDs      []string
}

// RingSource lists the rings of the replicas in the registry.
type RingSource interface {
	Rings(ctx context.Context) ([]ReplicaRing, error)
}

func unavailable(what string) error {
	return errs.E(errs.Unavailable, errs.CodeUnavailable, what+" is not available on this replica.")
}

// holds reports whether the caller holds a permission. A refusal (403, 401) is
// "no"; any other error, such as an unavailable authorization state, is
// returned.
func (s *Server) holds(ctx context.Context, permission string) (bool, error) {
	return Holds(ctx, s.o.Access, permission)
}

// callerID returns the caller's principal ID when it is one, "" otherwise
// (anonymous, or a principal without a table row such as the break-glass session).
func callerID(ctx context.Context) string {
	return principal.IDOrEmpty(httpserver.PrincipalFrom(ctx).ID)
}

// auditSource describes where the request comes from for an audit event.
func auditSource(r *http.Request) audit.Source { return httpserver.SourceFrom(r.Context()) }

func (s *Server) recorder() audit.Recorder {
	if s.o.API.Audit == nil {
		return audit.Nop{}
	}
	return s.o.API.Audit
}

// readOptionalBody reads a request body that is optional, bounded by limit. No
// body, or only white space, returns nil.
func readOptionalBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errs.E(errs.TooLarge, errs.CodeTooLarge, "The request body is too large.")
		}
		return nil, errs.E(errs.Invalid, errs.CodeInvalid, "The request body could not be read.", errs.WithCause(err))
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	return b, nil
}

// decodeTaskParams validates the parameters of a task trigger against the type
// the task declared and returns them as the job's args.
func decodeTaskParams(body []byte, r *http.Request, proto func() any) (json.RawMessage, error) {
	if body == nil {
		return nil, nil
	}
	if err := requireJSON(r, MediaJSON); err != nil {
		return nil, err
	}
	var target any = &map[string]json.RawMessage{}
	if proto != nil {
		target = proto()
	}
	if err := StrictUnmarshal(body, target); err != nil {
		return nil, err
	}
	if proto == nil {
		if m := *target.(*map[string]json.RawMessage); len(m) > 0 {
			for k := range m {
				return nil, malformed("/"+pointerEscape(k), "unknown", "the task takes no parameters", nil)
			}
		}
	}
	return json.RawMessage(body), nil
}
