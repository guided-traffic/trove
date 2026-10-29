package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Metrics counts what the writer does (R-OPS-008): trove_audit_events_total by
// category and outcome for every event that was stored, and
// trove_audit_events_dropped_total for access events that never reached the
// table. Nil in [Options] counts nothing.
type Metrics interface {
	Recorded(category string, outcome Outcome)
	Dropped()
}

type noMetrics struct{}

func (noMetrics) Recorded(string, Outcome) {}
func (noMetrics) Dropped()                 {}

// sink stores rows. The PostgreSQL implementation is pgSink; tests inject their own.
type sink interface {
	write(ctx context.Context, rows []row) error
}

// Options configure [New].
type Options struct {
	// DB is the main pool. Required.
	DB DB
	// Replica is the replica's name, stored in the replica column.
	Replica string
	// Log receives the log copy of every event (log: audit). Nil discards it.
	Log *slog.Logger
	// AppLog receives the writer's own messages (log: app). Nil discards them.
	AppLog *slog.Logger
	// Metrics counts events; nil counts nothing.
	Metrics Metrics
	// Config is the audit section: async_buffer, the two dedup windows and the
	// anonymous-denial and download switches.
	Config config.Audit
	// Now is the clock; nil means time.Now. Tests advance it.
	Now func() time.Time
}

// Batch writing, fixed by R-OPS-015: a batch every second or every 500 events.
const (
	flushEvery = time.Second
	batchMax   = 500
	// writeTimeout bounds one batch write, so a slow PostgreSQL never keeps the
	// writer from noticing the shutdown.
	writeTimeout = 15 * time.Second
	// retryMin and retryMax bound the backoff between attempts while PostgreSQL
	// is away.
	retryMin = 500 * time.Millisecond
	retryMax = 10 * time.Second
)

// Writer implements [Recorder] on PostgreSQL (R-OPS-015).
//
// Transactional events are inserted in the caller's transaction. Asynchronous
// events are prepared on the request path (a lookup, a few string copies and a
// non-blocking channel send), then written in batches by one goroutine. While
// PostgreSQL does not answer, that goroutine keeps its batch and retries with a
// backoff; the channel fills behind it, and an event that finds it full is
// dropped from the table, written to the log and counted. Nothing on the request
// path ever waits for PostgreSQL.
type Writer struct {
	o       Options
	sink    sink
	log     *slog.Logger
	app     *slog.Logger
	metrics Metrics
	now     func() time.Time
	dedup   *deduper
	isData  func(error) bool

	ch      chan row
	stop    chan struct{}
	done    chan struct{}
	started atomic.Bool
	closed  atomic.Bool

	stopCtx  context.Context //nolint:containedctx // handed to the loop through the stop channel
	finalErr error

	// lastDropWarn rate-limits the app log message of a full buffer.
	mu           sync.Mutex
	lastDropWarn time.Time
	droppedSince int
}

var _ Recorder = (*Writer)(nil)

// New returns a writer. Start must run before asynchronous events are written;
// transactional events need no start.
func New(o Options) (*Writer, error) {
	if o.DB == nil {
		return nil, errors.New("audit: the writer needs a database")
	}
	return newWriter(o, pgSink{db: o.DB}), nil
}

func newWriter(o Options, s sink) *Writer {
	if o.Config.AsyncBuffer <= 0 {
		o.Config.AsyncBuffer = 10000
	}
	w := &Writer{
		o: o, sink: s, log: o.Log, app: o.AppLog, metrics: o.Metrics, now: o.Now,
		ch: make(chan row, o.Config.AsyncBuffer), stop: make(chan struct{}), done: make(chan struct{}),
	}
	if w.log == nil {
		w.log = slog.New(slog.DiscardHandler)
	}
	if w.app == nil {
		w.app = slog.New(slog.DiscardHandler)
	}
	if w.metrics == nil {
		w.metrics = noMetrics{}
	}
	if w.now == nil {
		w.now = time.Now
	}
	w.isData = postgres.IsDataError
	w.dedup = newDeduper(o.Config, w.now)
	return w
}

// RecordTx inserts the event in tx, and its revocation_outbox row when its type
// is journal-flagged and the producer passed the entry. Any failure is returned
// and fails the caller's transaction: no change exists without its audit record
// (R-OPS-015). The log copy and the counter follow the commit.
func (w *Writer) RecordTx(ctx context.Context, tx meta.Tx, ev Event) error {
	t, ok := Lookup(ev.Type)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownType, ev.Type)
	}
	switch t.Mode {
	case None:
		return nil
	case Async:
		return fmt.Errorf("%w: %s is asynchronous", ErrWrongMode, t.Name)
	}
	journal, err := journalEntry(t, ev)
	if err != nil {
		return err
	}
	r, err := toRow(t, ev, newID(w.now()), time.Time{}, w.o.Replica)
	if err != nil {
		return err
	}
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return errors.New("audit: the transaction is not a PostgreSQL transaction")
	}
	payload, err := json.Marshal([]row{r})
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	qs := q.New(ptx)
	got, err := qs.InsertAuditEvents(ctx, string(payload))
	if err != nil {
		return fmt.Errorf("audit: insert %s: %w", t.Name, err)
	}
	if len(got) != 1 {
		return fmt.Errorf("audit: insert %s stored %d rows", t.Name, len(got))
	}
	r.At = got[0].OccurredAt.UTC().Format(time.RFC3339Nano)
	if journal != nil {
		if err := qs.InsertRevocationOutbox(ctx, q.InsertRevocationOutboxParams{
			ID: r.ID, At: r.At, Event: t.Name, Entry: string(journal),
		}); err != nil {
			return fmt.Errorf("audit: journal %s: %w", t.Name, err)
		}
	}
	tx.After(func() { w.stored(r) })
	return nil
}

// journalEntry applies the journal rules of R-OPS-014 and R-OPS-015: it returns
// the entry to write, nil for none, and an error for a missing required entry
// or an entry on an unflagged type.
func journalEntry(t EventType, ev Event) (json.RawMessage, error) {
	if len(ev.Journal) == 0 {
		if t.Journal == JournalRevocation && t.Entry == EntryRequired {
			return nil, fmt.Errorf("%w: %s", ErrJournalEntryRequired, t.Name)
		}
		return nil, nil
	}
	if t.Journal != JournalRevocation {
		return nil, fmt.Errorf("%w: %s", ErrJournalNotAllowed, t.Name)
	}
	e, err := object(ev.Journal)
	if err != nil {
		return nil, fmt.Errorf("%w: %s journal entry: %w", ErrInvalidEvent, t.Name, err)
	}
	return e, nil
}

// pgSink writes a batch with one statement in one transaction.
type pgSink struct{ db DB }

func (s pgSink) write(ctx context.Context, rows []row) error {
	payload, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return s.db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("audit: the transaction is not a PostgreSQL transaction")
		}
		_, err := q.New(ptx).InsertAuditEvents(ctx, string(payload))
		return err
	})
}
