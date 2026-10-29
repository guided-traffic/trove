package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// DB is what the audit package needs of the database: transactions for the
// batch writer and reads with the failover retry for the query side.
// *postgres.DB implements it.
type DB interface {
	meta.Runner
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

const nilUUID = "00000000-0000-0000-0000-000000000000"

// Limits of the query (R-OPS-017).
const (
	// DefaultLimit and MaxLimit bound a page.
	DefaultLimit = 100
	MaxLimit     = 1000
	// DefaultWindow is the range of a query without a lower bound.
	DefaultWindow = 24 * time.Hour
	// MaxExportRange is the longest range an export may cover.
	MaxExportRange = 400 * 24 * time.Hour
)

// Entry is a stored audit event, in the shape of the API resource (R-OPS-017).
type Entry struct {
	ID         string
	OccurredAt time.Time
	Action     string
	Category   string
	Outcome    Outcome
	// Actor.Kind is the stored actor_type ("breakglass" for a break-glass login).
	Actor    Actor
	Source   Source
	Replica  string
	Endpoint string
	// Resource is the object the event concerns: Kind is resource_type, ID is resource_id.
	Resource   Subject
	Repository string
	Path       string
	Reason     string
	Details    json.RawMessage
}

// Filter selects events (R-OPS-017). Empty fields do not filter.
type Filter struct {
	// From (inclusive) and To (exclusive). A zero From is now minus
	// [DefaultWindow]; a zero To is open.
	From, To time.Time
	// Action is an exact name, or a prefix ending in ".*".
	Action   string
	Category string
	Outcome  Outcome
	// Actor is an actor ID or an actor name.
	Actor        string
	Repository   string
	ResourceType string
	ResourceID   string
	TraceID      string
}

// Cursor is the keyset position after an event: its (occurred_at, id).
type Cursor struct {
	OccurredAt time.Time
	ID         string
}

// Page is the paging input. Newest first unless Oldest is set (the export).
type Page struct {
	// Limit is the page size: 0 means [DefaultLimit], above [MaxLimit] is cut to it.
	Limit int
	// After continues after the last event of the previous page; nil starts at the beginning.
	After  *Cursor
	Oldest bool
}

// Result is one page. Next is nil on the last page; otherwise it is the
// position of the last item, to pass as Page.After.
type Result struct {
	Items []Entry
	Next  *Cursor
}

// Store reads the audit trail and maintains its partitions and retention.
type Store struct {
	db  DB
	now func() time.Time
}

// NewStore returns a store on db.
func NewStore(db DB) *Store { return &Store{db: db, now: time.Now} }

// CheckExportRange is the check of the export operation: both bounds given, the
// range positive and at most [MaxExportRange] (R-OPS-017). It returns a 400 with
// the code invalid_audit_range.
func CheckExportRange(from, to time.Time) error {
	switch {
	case from.IsZero() || to.IsZero():
		return errs.E(errs.Invalid, errs.CodeInvalidAuditRange, "an export needs both from and to")
	case !to.After(from):
		return errs.E(errs.Invalid, errs.CodeInvalidAuditRange, "to must be after from")
	case to.Sub(from) > MaxExportRange:
		return errs.E(errs.Invalid, errs.CodeInvalidAuditRange, "an export covers at most 400 days")
	}
	return nil
}

// Query returns one page of events in (occurred_at, id) order, newest first by
// default. Keyset pagination: pass the previous Result.Next as Page.After; no
// event is repeated or skipped by events that arrive between pages, and no page
// holds a transaction open (R-OPS-017).
func (s *Store) Query(ctx context.Context, f Filter, p Page) (Result, error) {
	to := "infinity"
	if !f.To.IsZero() {
		to = f.To.UTC().Format(time.RFC3339Nano)
	}
	from := f.From
	if from.IsZero() {
		from = s.now().Add(-DefaultWindow)
	}
	if !f.To.IsZero() && !f.To.After(from) {
		return Result{}, errs.E(errs.Invalid, errs.CodeInvalidAuditRange, "to must be after from")
	}
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)

	exact, prefix := f.Action, ""
	if pre, ok := strings.CutSuffix(f.Action, ".*"); ok {
		exact, prefix = "", pre+"."
	}
	actorID := nilUUID
	if id := uuidOrEmpty(f.Actor); id != "" {
		actorID = id
	}
	cursorAt, cursorID := "1970-01-01T00:00:00Z", nilUUID
	hasCursor := p.After != nil
	if hasCursor {
		cursorAt, cursorID = p.After.OccurredAt.UTC().Format(time.RFC3339Nano), strings.ToLower(p.After.ID)
		if _, ok := parseUUID(cursorID); !ok {
			return Result{}, errs.E(errs.Invalid, errs.CodeInvalid, "the cursor position is not an event ID")
		}
	}

	var entries []Entry
	err := s.db.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows := make([]Entry, 0, min(limit+1, 64))
		if p.Oldest {
			got, err := qs.ListAuditEventsOldest(ctx, q.ListAuditEventsOldestParams{
				FromAt: from.UTC().Format(time.RFC3339Nano), ToAt: to, Action: exact, ActionPrefix: prefix,
				Category: f.Category, Outcome: string(f.Outcome), Actor: f.Actor, ActorID: actorID,
				Repository: f.Repository, ResourceType: f.ResourceType, ResourceID: f.ResourceID, TraceID: f.TraceID,
				HasCursor: hasCursor, CursorAt: cursorAt, CursorID: cursorID, RowLimit: int32(limit + 1), //nolint:gosec // limit <= MaxLimit
			})
			if err != nil {
				return err
			}
			for _, r := range got {
				rows = append(rows, fromOldest(r))
			}
		} else {
			got, err := qs.ListAuditEventsNewest(ctx, q.ListAuditEventsNewestParams{
				FromAt: from.UTC().Format(time.RFC3339Nano), ToAt: to, Action: exact, ActionPrefix: prefix,
				Category: f.Category, Outcome: string(f.Outcome), Actor: f.Actor, ActorID: actorID,
				Repository: f.Repository, ResourceType: f.ResourceType, ResourceID: f.ResourceID, TraceID: f.TraceID,
				HasCursor: hasCursor, CursorAt: cursorAt, CursorID: cursorID, RowLimit: int32(limit + 1), //nolint:gosec // limit <= MaxLimit
			})
			if err != nil {
				return err
			}
			for _, r := range got {
				rows = append(rows, fromNewest(r))
			}
		}
		entries = rows
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("audit query: %w", err)
	}
	res := Result{Items: entries}
	if len(entries) > limit {
		res.Items = entries[:limit]
		last := res.Items[limit-1]
		res.Next = &Cursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	return res, nil
}

// idWindow is how far from the time an event ID carries the event's own time may
// lie: a transactional event takes the transaction's start from the database
// clock, its ID the instant the producer built it.
const idWindow = time.Hour

// Get returns one event by ID, or an errs.NotFound error. Only the partitions
// around the time the UUIDv7 carries are read.
func (s *Store) Get(ctx context.Context, id string) (Entry, error) {
	id = strings.ToLower(id)
	at, ok := idTime(id)
	if !ok {
		return Entry{}, errs.E(errs.NotFound, errs.CodeNotFound, "no such audit event")
	}
	var out []q.GetAuditEventRow
	err := s.db.Read(ctx, func(db q.DBTX) error {
		var err error
		out, err = q.New(db).GetAuditEvent(ctx, q.GetAuditEventParams{
			ID: id, FromAt: at.Add(-idWindow).Format(time.RFC3339Nano), ToAt: at.Add(idWindow).Format(time.RFC3339Nano),
		})
		return err
	})
	if err != nil {
		return Entry{}, fmt.Errorf("audit get: %w", err)
	}
	if len(out) == 0 {
		return Entry{}, errs.E(errs.NotFound, errs.CodeNotFound, "no such audit event")
	}
	return fromGet(out[0]), nil
}

// Maintain calls audit_maintain_partitions(): it creates the partitions of the
// current and the next three months and drops those past the stored retention
// (R-OPS-015). It is the body of the audit-retention task and runs at start-up.
func (s *Store) Maintain(ctx context.Context) error {
	return s.db.Read(ctx, func(db q.DBTX) error { return q.New(db).MaintainAuditPartitions(ctx) })
}

// PartitionsReady reports whether the partitions of the current and the next
// month exist, the property of the startup check (R-OPS-001).
func (s *Store) PartitionsReady(ctx context.Context) (bool, error) {
	var n int32
	err := s.db.Read(ctx, func(db q.DBTX) error {
		var err error
		n, err = q.New(db).CountReadyAuditPartitions(ctx)
		return err
	})
	return n == 2, err
}

// Prepare is start-up step 4 (R-ARCH-024): it reconciles the retention (see
// SyncRetention), calls audit_maintain_partitions() and fails unless the
// partitions of the current and the next month exist afterwards, because an
// insert must never meet a missing partition. One attempt; the caller retries.
func (s *Store) Prepare(ctx context.Context, retentionDays int, log *slog.Logger) error {
	if err := s.SyncRetention(ctx, retentionDays, log); err != nil {
		return err
	}
	if err := s.Maintain(ctx); err != nil {
		return fmt.Errorf("audit_maintain_partitions: %w", err)
	}
	ok, err := s.PartitionsReady(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the audit partitions of the current and the next month do not exist yet")
	}
	return nil
}

// SyncRetention compares audit.retention_days with audit_policy. In two-role
// mode the runtime role may only read the row and trove migrate writes it, so a
// difference is a warning that names trove migrate (R-OPS-015). In single-role
// mode the replica owns the schema and writes the row itself (H-39). A value
// below 90 days logs a warning (R-OPS-017).
func (s *Store) SyncRetention(ctx context.Context, days int, log *slog.Logger) error {
	if days > 0 && days < 90 {
		log.Warn("audit.retention_days is below 90", "retention_days", days)
	}
	var stored int32
	err := s.db.Read(ctx, func(db q.DBTX) error {
		var err error
		stored, err = q.New(db).GetAuditRetention(ctx)
		return err
	})
	if err != nil {
		return fmt.Errorf("audit_policy: %w", err)
	}
	if int(stored) == days {
		return nil
	}
	err = s.db.Read(ctx, func(db q.DBTX) error {
		_, err := q.New(db).SetAuditRetention(ctx, int32(days)) //nolint:gosec // a day count
		return err
	})
	switch {
	case err == nil:
		log.Info("audit retention written (single-role mode)", "retention_days", days, "was", stored)
	case postgres.IsPrivilegeError(err):
		log.Warn("audit.retention_days differs from the stored retention; run trove migrate to apply it",
			"configured", days, "stored", stored)
	default:
		return fmt.Errorf("audit_policy: %w", err)
	}
	return nil
}

func entryOf(id string, at time.Time, action, category, outcome, actorType, actorID, actorName, tokenID, authMethod,
	ip, ua, trace, replica, endpoint, rtype, rid, repo, path, reason string, details []byte,
) Entry {
	return Entry{
		ID: id, OccurredAt: at, Action: action, Category: category, Outcome: Outcome(outcome),
		Actor:  Actor{Kind: actorType, ID: actorID, Name: actorName, TokenID: tokenID, AuthMethod: authMethod},
		Source: Source{ClientAddr: ip, UserAgent: ua, TraceID: trace}, Replica: replica, Endpoint: endpoint,
		Resource: Subject{Kind: rtype, ID: rid}, Repository: repo, Path: path, Reason: reason, Details: details,
	}
}

func fromNewest(r q.ListAuditEventsNewestRow) Entry {
	return entryOf(r.ID, r.OccurredAt, r.Action, r.Category, r.Outcome, r.ActorType, r.ActorID, r.ActorName, r.TokenID,
		r.AuthMethod, r.SourceIp, r.UserAgent, r.TraceID, r.Replica, r.Endpoint, r.ResourceType, r.ResourceID,
		r.Repository, r.Path, r.Reason, r.Details)
}

func fromOldest(r q.ListAuditEventsOldestRow) Entry {
	return entryOf(r.ID, r.OccurredAt, r.Action, r.Category, r.Outcome, r.ActorType, r.ActorID, r.ActorName, r.TokenID,
		r.AuthMethod, r.SourceIp, r.UserAgent, r.TraceID, r.Replica, r.Endpoint, r.ResourceType, r.ResourceID,
		r.Repository, r.Path, r.Reason, r.Details)
}

func fromGet(r q.GetAuditEventRow) Entry {
	return entryOf(r.ID, r.OccurredAt, r.Action, r.Category, r.Outcome, r.ActorType, r.ActorID, r.ActorName, r.TokenID,
		r.AuthMethod, r.SourceIp, r.UserAgent, r.TraceID, r.Replica, r.Endpoint, r.ResourceType, r.ResourceID,
		r.Repository, r.Path, r.Reason, r.Details)
}
