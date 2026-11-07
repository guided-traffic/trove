package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// DB is what the run store needs of the database: transactions for the writes and reads with the failover retry for the
// replicas' views. *postgres.DB implements it.
type DB interface {
	meta.Runner
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// ErrSlotTaken is Begin meeting backup_run_one_active (SQLSTATE 23505): another target-writing run holds the slot. Active
// is that run, read in a later statement; it is the zero Run if the run ended in between and the caller should try again.
type ErrSlotTaken struct{ Active Run }

func (e *ErrSlotTaken) Error() string { return "another target-writing backup run holds the slot" }

// RunStore is the record of runs in PostgreSQL (R-BAK-009, R-BAK-010) and the pins of the garbage-collection hold
// (R-BAK-007). The backup pod uses it through its backup-role credential; the replicas only read (Get, List, Active,
// Last). Every write that follows a run's progress is fenced on the state the run expects, so a run that was abandoned
// meanwhile finds out at its next heartbeat (ErrSuperseded).
type RunStore interface {
	// Begin inserts the run row (state starting, hold none) in a transaction that first ends expired runs and deletes
	// their pins. It returns *ErrSlotTaken when another create or delete run is active.
	Begin(ctx context.Context, r NewRun) (Run, error)
	// Get returns one run; ok is false for an unknown ID.
	Get(ctx context.Context, id string) (run Run, ok bool, err error)
	// Active returns the run that holds the slot, if any, with the database's clock.
	Active(ctx context.Context) (run Run, ok bool, now time.Time, err error)
	// Heartbeat stamps the run, writes its progress and, when renew is true, extends the lease by lease. It returns
	// ErrSuperseded when the run is no longer in the expected state (R-BAK-010 fencing).
	Heartbeat(ctx context.Context, id string, expected State, p Progress, snapshotAt *time.Time, renew bool, lease time.Duration) error
	// SetState moves the run from one active state to the next, fenced on the expected state. backupID is recorded when
	// it is not empty.
	SetState(ctx context.Context, id string, from, to State, backupID string) error
	// SetHold is the compare-and-set of the hold, one transaction of its own (R-BAK-007 steps 1 and 4).
	SetHold(ctx context.Context, id string, from, to Hold) error
	// Fence is the fenced lease check before the manifest and before every deletion of the pruning phase (R-BAK-010): it
	// moves the run from one state to another only while the lease has more than margin left, and reports whether it did.
	Fence(ctx context.Context, id string, from, to State, margin time.Duration) (bool, error)
	// AddPins inserts the to-copy set in one transaction.
	AddPins(ctx context.Context, id string, entries []format.BlobEntry) error
	// DeletePins removes confirmed pins; the caller batches up to 1,000.
	DeletePins(ctx context.Context, id string, digests []format.Digest) error
	// Finish ends the run: state, result and error are written, the hold becomes none and the run's pins are deleted, in
	// one transaction.
	Finish(ctx context.Context, id string, state State, result, runErr json.RawMessage) error
	// PruneHistory deletes finished rows older than the history (backup.run_history).
	PruneHistory(ctx context.Context, older time.Duration) (int64, error)
}

// ListFilter selects runs for the API (R-API-050). Empty fields do not filter.
type ListFilter struct {
	Operation, State, Trigger string
	StartedFrom, StartedTo    time.Time
	// After is the keyset position of the previous page; nil for the first page.
	After *RunCursor
	Limit int
}

// RunCursor is the position after a run: its (started_at, id).
type RunCursor struct {
	StartedAt time.Time
	ID        string
}

// RunReader is what the replicas read: the API, the summary and the gauges (R-BAK-014, R-API-050). It never writes.
type RunReader interface {
	Get(ctx context.Context, id string) (Run, bool, error)
	List(ctx context.Context, f ListFilter) ([]Run, error)
	Count(ctx context.Context, f ListFilter) (int64, error)
	// Last returns the newest finished run of an operation among the states.
	Last(ctx context.Context, op Operation, states ...State) (Run, bool, error)
	// ActiveRuns returns every run in an active state, with the database's clock.
	ActiveRuns(ctx context.Context) ([]Run, time.Time, error)
	// HoldValid reports whether a valid hold of the kind exists (the gauge trove_backup_gc_hold).
	HoldValid(ctx context.Context, h Hold) (bool, error)
	// JournalPending returns the revocation-journal backlog: the rows of revocation_outbox not yet shipped, and the age of
	// the oldest (trove_revocation_journal_pending, R-HA-054).
	JournalPending(ctx context.Context) (rows int64, oldest time.Duration, err error)
}

// PGStore is the PostgreSQL implementation of [RunStore] and [RunReader].
type PGStore struct{ db DB }

// NewPGStore builds the store over a database.
func NewPGStore(db DB) *PGStore { return &PGStore{db: db} }

var (
	_ RunStore  = (*PGStore)(nil)
	_ RunReader = (*PGStore)(nil)
)

// sqlstateUniqueViolation is the SQLSTATE of backup_run_one_active.
const sqlstateUniqueViolation = "23505"

func (s *PGStore) write(ctx context.Context, fn func(qs *q.Queries) error) error {
	return s.db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("backup: the transaction is not a PostgreSQL transaction")
		}
		return fn(q.New(ptx))
	})
}

func (s *PGStore) read(ctx context.Context, fn func(qs *q.Queries) error) error {
	return s.db.Read(ctx, func(c q.DBTX) error { return fn(q.New(c)) })
}

// Begin implements RunStore.
func (s *PGStore) Begin(ctx context.Context, r NewRun) (Run, error) {
	err := s.write(ctx, func(qs *q.Queries) error {
		if _, err := qs.AbandonExpiredBackupRuns(ctx); err != nil {
			return err
		}
		if err := qs.DeleteBackupPinsOfAbandonedRuns(ctx); err != nil {
			return err
		}
		return qs.InsertBackupRun(ctx, q.InsertBackupRunParams{
			ID: r.ID, Operation: string(r.Operation), BackupID: r.BackupID, Trigger: string(r.Trigger), Target: r.Target,
			Pod: r.Pod, TroveVersion: r.TroveVersion, JobName: r.JobName, LeaseSecs: r.Lease.Seconds(),
		})
	})
	if postgres.IsState(err, sqlstateUniqueViolation) {
		taken := &ErrSlotTaken{}
		if run, ok, _, aerr := s.Active(ctx); aerr == nil && ok {
			taken.Active = run
		}
		return Run{}, taken
	}
	if err != nil {
		return Run{}, err
	}
	run, ok, err := s.Get(ctx, r.ID)
	if err == nil && !ok {
		err = errors.New("backup: the run row vanished after its insert")
	}
	return run, err
}

// Get implements RunStore and RunReader.
func (s *PGStore) Get(ctx context.Context, id string) (Run, bool, error) {
	var out []q.BackupRun
	err := s.read(ctx, func(qs *q.Queries) (err error) { out, err = qs.GetBackupRun(ctx, id); return })
	if err != nil || len(out) == 0 {
		return Run{}, false, err
	}
	return toRun(out[0]), true, nil
}

// Active implements RunStore.
func (s *PGStore) Active(ctx context.Context) (Run, bool, time.Time, error) {
	var rows []q.BackupRun
	var now time.Time
	err := s.read(ctx, func(qs *q.Queries) error {
		var err error
		if rows, err = qs.ActiveTargetWritingRun(ctx); err != nil {
			return err
		}
		n, err := qs.DatabaseNow(ctx)
		now = n.Time
		return err
	})
	if err != nil || len(rows) == 0 {
		return Run{}, false, now, err
	}
	return toRun(rows[0]), true, now, nil
}

// Heartbeat implements RunStore.
func (s *PGStore) Heartbeat(ctx context.Context, id string, expected State, p Progress, snapshotAt *time.Time, renew bool, lease time.Duration) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	arg := q.HeartbeatBackupRunParams{ID: id, ExpectedState: string(expected), Progress: string(raw)}
	if renew {
		arg.LeaseSecs = lease.Seconds()
	}
	if snapshotAt != nil {
		arg.SnapshotAt = snapshotAt.UTC().Format(time.RFC3339Nano)
	}
	return s.write(ctx, func(qs *q.Queries) error {
		n, err := qs.HeartbeatBackupRun(ctx, arg)
		if err == nil && n == 0 {
			return ErrSuperseded
		}
		return err
	})
}

// SetState implements RunStore.
func (s *PGStore) SetState(ctx context.Context, id string, from, to State, backupID string) error {
	return s.write(ctx, func(qs *q.Queries) error {
		n, err := qs.SetBackupRunState(ctx, q.SetBackupRunStateParams{ID: id, State: string(to), BackupID: backupID, ExpectedState: string(from)})
		if err == nil && n == 0 {
			return ErrSuperseded
		}
		return err
	})
}

// SetHold implements RunStore.
func (s *PGStore) SetHold(ctx context.Context, id string, from, to Hold) error {
	return s.write(ctx, func(qs *q.Queries) error {
		n, err := qs.SetBackupRunHold(ctx, q.SetBackupRunHoldParams{ID: id, Hold: string(to), ExpectedHold: string(from)})
		if err == nil && n == 0 {
			return ErrSuperseded
		}
		return err
	})
}

// Fence implements RunStore.
func (s *PGStore) Fence(ctx context.Context, id string, from, to State, margin time.Duration) (bool, error) {
	var n int64
	err := s.write(ctx, func(qs *q.Queries) (err error) {
		n, err = qs.FenceBackupRunFinalizing(ctx, q.FenceBackupRunFinalizingParams{ID: id, State: string(to), ExpectedState: string(from), MarginSecs: margin.Seconds()})
		return
	})
	return n == 1, err
}

// AddPins implements RunStore.
func (s *PGStore) AddPins(ctx context.Context, id string, entries []format.BlobEntry) error {
	if len(entries) == 0 {
		return nil
	}
	digests, sizes := make([][]byte, len(entries)), make([]int64, len(entries))
	for i, e := range entries {
		d := e.Digest
		digests[i], sizes[i] = d[:], e.Size
	}
	return s.write(ctx, func(qs *q.Queries) error {
		return qs.InsertBackupPins(ctx, q.InsertBackupPinsParams{RunID: id, Digests: digests, Sizes: sizes})
	})
}

// DeletePins implements RunStore.
func (s *PGStore) DeletePins(ctx context.Context, id string, digests []format.Digest) error {
	if len(digests) == 0 {
		return nil
	}
	raw := make([][]byte, len(digests))
	for i := range digests {
		d := digests[i]
		raw[i] = d[:]
	}
	return s.write(ctx, func(qs *q.Queries) error {
		_, err := qs.DeleteBackupPins(ctx, q.DeleteBackupPinsParams{RunID: id, Digests: raw})
		return err
	})
}

// Finish implements RunStore.
func (s *PGStore) Finish(ctx context.Context, id string, state State, result, runErr json.RawMessage) error {
	if state.Active() {
		return fmt.Errorf("backup: Finish with the active state %s", state)
	}
	return s.write(ctx, func(qs *q.Queries) error {
		n, err := qs.FinishBackupRun(ctx, q.FinishBackupRunParams{ID: id, State: string(state), Result: string(result), Error: string(runErr)})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrSuperseded
		}
		_, err = qs.DeleteBackupPinsOfRun(ctx, id)
		return err
	})
}

// PruneHistory implements RunStore.
func (s *PGStore) PruneHistory(ctx context.Context, older time.Duration) (n int64, err error) {
	err = s.write(ctx, func(qs *q.Queries) (e error) { n, e = qs.DeleteOldBackupRuns(ctx, older.Seconds()); return })
	return n, err
}

// List implements RunReader.
func (s *PGStore) List(ctx context.Context, f ListFilter) ([]Run, error) {
	arg := q.ListBackupRunsParams{
		Operation: f.Operation, State: f.State, Trigger: f.Trigger, StartedFrom: ts(f.StartedFrom), StartedTo: ts(f.StartedTo),
		RowLimit: int32(max(f.Limit, 1)), //nolint:gosec // a page size, bounded by the caller
	}
	if f.After != nil {
		arg.HasCursor, arg.CursorAt, arg.CursorID = true, f.After.StartedAt.UTC().Format(time.RFC3339Nano), f.After.ID
	} else {
		arg.CursorAt, arg.CursorID = "1970-01-01T00:00:00Z", "00000000-0000-0000-0000-000000000000"
	}
	var rows []q.BackupRun
	if err := s.read(ctx, func(qs *q.Queries) (err error) { rows, err = qs.ListBackupRuns(ctx, arg); return }); err != nil {
		return nil, err
	}
	return toRuns(rows), nil
}

// Count implements RunReader.
func (s *PGStore) Count(ctx context.Context, f ListFilter) (n int64, err error) {
	arg := q.CountBackupRunsParams{Operation: f.Operation, State: f.State, Trigger: f.Trigger, StartedFrom: ts(f.StartedFrom), StartedTo: ts(f.StartedTo)}
	err = s.read(ctx, func(qs *q.Queries) (e error) { n, e = qs.CountBackupRuns(ctx, arg); return })
	return n, err
}

// Last implements RunReader.
func (s *PGStore) Last(ctx context.Context, op Operation, states ...State) (Run, bool, error) {
	names := make([]string, len(states))
	for i, st := range states {
		names[i] = string(st)
	}
	var rows []q.BackupRun
	err := s.read(ctx, func(qs *q.Queries) (e error) {
		rows, e = qs.LastBackupRunByOutcome(ctx, q.LastBackupRunByOutcomeParams{Operation: string(op), States: names})
		return
	})
	if err != nil || len(rows) == 0 {
		return Run{}, false, err
	}
	return toRun(rows[0]), true, nil
}

// ActiveRuns implements RunReader.
func (s *PGStore) ActiveRuns(ctx context.Context) ([]Run, time.Time, error) {
	var rows []q.BackupRun
	var now time.Time
	err := s.read(ctx, func(qs *q.Queries) error {
		var err error
		if rows, err = qs.ActiveBackupRuns(ctx); err != nil {
			return err
		}
		n, err := qs.DatabaseNow(ctx)
		now = n.Time
		return err
	})
	return toRuns(rows), now, err
}

// BlobHeld is the hold check of the collector's per-blob transaction (R-STOR-024 step 2b, R-BAK-007) as one statement on
// the request pool. The collector runs the same query (BackupBlobHeld) inside its own transaction; this method is for
// tests and tools.
func (s *PGStore) BlobHeld(ctx context.Context, d format.Digest) (held bool, err error) {
	err = s.read(ctx, func(qs *q.Queries) (e error) {
		held, e = qs.BackupBlobHeld(ctx, d[:])
		return
	})
	return held, err
}

// HoldValid implements RunReader.
func (s *PGStore) HoldValid(ctx context.Context, h Hold) (held bool, err error) {
	err = s.read(ctx, func(qs *q.Queries) (e error) {
		switch h {
		case HoldAll:
			held, e = qs.BackupGlobalHoldValid(ctx)
		case HoldPinned:
			held, e = qs.BackupPinnedHoldValid(ctx)
		default:
			e = fmt.Errorf("backup: HoldValid(%q)", h)
		}
		return
	})
	return held, err
}

// JournalPending implements RunReader.
func (s *PGStore) JournalPending(ctx context.Context) (rows int64, oldest time.Duration, err error) {
	err = s.read(ctx, func(qs *q.Queries) error {
		var e error
		if rows, e = qs.CountRevocationOutbox(ctx); e != nil || rows == 0 {
			return e
		}
		ts, e := qs.OldestRevocationOutbox(ctx)
		if e != nil || len(ts) == 0 {
			return e
		}
		n, e := qs.DatabaseNow(ctx)
		if e != nil {
			return e
		}
		oldest = max(n.Time.Sub(ts[0].Time), 0)
		return nil
	})
	return rows, oldest, err
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toRuns(rows []q.BackupRun) []Run {
	out := make([]Run, len(rows))
	for i, r := range rows {
		out[i] = toRun(r)
	}
	return out
}

func toRun(r q.BackupRun) Run {
	run := Run{
		ID: r.ID.String(), Operation: Operation(r.Operation), State: State(r.State), Hold: Hold(r.Hold), Trigger: Trigger(r.Trigger),
		Target: r.Target, Pod: r.Pod, TroveVersion: r.TroveVersion, Result: r.Result, Error: r.Error,
		StartedAt: r.StartedAt.Time, HeartbeatAt: r.HeartbeatAt.Time, LeaseExpiresAt: r.LeaseExpiresAt.Time,
	}
	if r.BackupID.Valid {
		run.BackupID = r.BackupID.String
	}
	if r.JobName.Valid {
		run.JobName = r.JobName.String
	}
	if r.SnapshotAt.Valid {
		t := r.SnapshotAt.Time
		run.SnapshotAt = &t
	}
	if r.FinishedAt.Valid {
		t := r.FinishedAt.Time
		run.FinishedAt = &t
	}
	_ = json.Unmarshal(r.Progress, &run.Progress)
	return run
}
