// Package postrestore is the clean-up of a platform (point-in-time) restore: `trove admin post-restore` (R-HA-025 step 4).
package postrestore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/fixup"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// pitrOrders are the orders of the registered fix-ups a platform restore needs (R-HA-025 step 4): the jobs that were running
// go back to the queue, the keyspace and the incarnation are new, and the revision columns carry the offset of R-BAK-022. The
// storage recount and the mirror state belong to the restore of a backup, whose database comes from a snapshot rather than
// from the live database; a PITR keeps its own usage counters and mirror rows.
func pitrOrders() []int { return []int{fixup.OrderJobs, fixup.OrderSystemState, fixup.OrderRevisions} }

// Options configure [New].
type Options struct {
	// DB runs the one transaction of the database fix-ups and the transactions of the replay. It holds the owner credential
	// of the restored database.
	DB meta.Runner
	// Fixups are the registered fix-ups; nil means fixup.Fixups(). The subset of [pitrOrders] runs.
	Fixups []fixup.Fixup
	// Replayer replays the revocation journal; a call to OpenReplayer builds it with the credentials of the file the
	// operator passed, or the runtime credential when empty.
	OpenReplayer func(ctx context.Context, credentialsFile string) (journal.Replayer, error)
	// Margin is backup.revocation_journal.replay_margin.
	Margin time.Duration
	// Recorder writes system.restored.
	Recorder audit.Recorder
	Log      *slog.Logger
	Now      func() time.Time
}

// PostRestorer is the [backup.PostRestorer].
type PostRestorer struct{ o Options }

var _ backup.PostRestorer = (*PostRestorer)(nil)

// New builds the post-restore.
func New(o Options) *PostRestorer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Fixups == nil {
		o.Fixups = fixup.Fixups()
	}
	return &PostRestorer{o: o}
}

// The keys of system_state that post-restore keeps.
const (
	// MarkerKey gates every replica while a post-restore runs: the schema check of the readiness (internal/meta/postgres,
	// checkRestoreMarker) fails with ErrRestoreInProgress while it exists, as it does for restore_in_progress. It is committed
	// in the first transaction of the command and deleted in the last, after the journal replay and system.restored
	// (R-HA-025 step 4, Q-HA-157).
	MarkerKey = postgres.PostRestoreMarkerKey
	// DoneKey records the restore point of the last post-restore that completed. A repeat with the same value changes nothing:
	// no session ends, no revision offset is added, the keyspace is not rotated (audit finding L3).
	DoneKey = "post_restore_done"
)

type markerValue struct {
	RestoredTo string `json:"restoredTo"`
	// Phase is "cleaned" once the database fix-ups of the first transaction committed.
	Phase     string `json:"phase"`
	StartedAt string `json:"startedAt,omitempty"`
}

type doneValue struct {
	RestoredTo string `json:"restoredTo"`
	Journal    string `json:"journal"`
	At         string `json:"at"`
}

// PostRestore runs the steps of R-HA-025 step 4 and reports them one line each:
//
//  1. in one transaction, the first: the marker that gates the replicas is committed, the rows of replica go, the deletion
//     pause is set, every session ends, the fix-ups of jobs, system state and revision columns run (a new keyspace and
//     incarnation, the offset 2^32 on the revision columns) and system_state.restored_at is set;
//  2. the revocation journal is replayed (unless the operator skipped it) and its lines are listed;
//  3. in the last transaction system.restored is written, the restore point is recorded and the marker is deleted.
//
// The marker makes every replica, running or started later, not ready until step 3 has committed, so no pod serves the
// restored database's old access between the restore and the end of the replay (audit finding H1). The command is safe to run
// again: a failed run is resumed at the replay (the first transaction is not repeated), and a run for a restore point that
// already completed changes nothing (audit finding L3). A run for another restore point while one is unfinished is refused.
func (p *PostRestorer) PostRestore(ctx context.Context, opts backup.PostRestoreOptions) (*backup.PostRestoreReport, error) {
	fx, err := p.pitrFixups()
	if err != nil {
		return nil, err
	}
	rep := &backup.PostRestoreReport{}
	step := func(s string) { rep.Steps = append(rep.Steps, s) }
	t := opts.RestoredTo.UTC().Format(time.RFC3339)

	st, err := p.readState(ctx)
	if err != nil {
		return nil, backup.Fail(backup.ExitFailed, backup.CodeInternal, err)
	}
	if st.marker != nil && st.marker.RestoredTo != t {
		return nil, backup.Failf(backup.ExitPrecondition, backup.CodeInternal,
			"a post-restore for the restore point %s did not finish: run it again with that --restored-to first", st.marker.RestoredTo)
	}
	if st.done != nil && st.done.RestoredTo == t && (st.done.Journal == "replayed" || opts.SkipJournal) {
		step(fmt.Sprintf("post-restore for %s completed at %s (journal %s): nothing changed", t, st.done.At, st.done.Journal))
		return rep, nil
	}
	cleaned := (st.marker != nil && st.marker.Phase == "cleaned") || (st.done != nil && st.done.RestoredTo == t)

	if cleaned {
		step("resuming after the database clean-up of an earlier run; it is not repeated")
		err = p.inTx(ctx, func(qs *q.Queries) error { return p.setMarker(ctx, qs, t) })
	} else {
		err = p.cleanUp(ctx, fx, t, step)
	}
	if err != nil {
		return nil, backup.Fail(backup.ExitFailed, backup.CodeInternal, err)
	}
	step("the replicas are held not ready until the replay and system.restored are done (" + MarkerKey + ")")
	// The reference to the blobs that are missing is the job of `trove admin storage verify`, which this release does not
	// have; the line says so rather than report zero.
	step("storage verification not run: `trove admin storage verify` is not available in this release")
	p.reportQuarantine(ctx, step)

	journalState := "skipped"
	if opts.SkipJournal {
		step("revocation journal skipped (--skip-revocation-journal): revocations newer than the restore point are undone, and NuGet version numbers published after it are not reserved (R-HA-054)")
	} else {
		sum, err := p.replay(ctx, opts)
		if err != nil {
			return rep, err
		}
		journalState = "replayed"
		rep.Replay = mustJSON(sum)
		step(fmt.Sprintf("revocation journal replayed: %d applied, %d already in effect, %d subject unknown, %d skipped, %d suspended, %d revived, %d unreadable",
			sum.Counts[journal.Applied], sum.Counts[journal.AlreadyInEffect], sum.Counts[journal.SubjectUnknown], sum.Counts[journal.Skipped],
			sum.Counts[journal.Suspended], sum.Counts[journal.Revived], len(sum.Unreadable)))
		for _, r := range sum.Results {
			step("  " + r.Line())
		}
		for _, u := range sum.Unreadable {
			step("  unreadable " + u.Key + ": " + u.Reason)
		}
		for _, w := range sum.Warnings {
			step("  warning: " + w)
		}
		if sum.NoAdminBinding {
			step("no enabled administrator is left (a service account that holds the role counts as one, though nobody can sign in as it): sign in with the break-glass account (R-AUTH-014), then bind the admin role again or re-attach or fill the group that holds it")
		}
	}
	err = p.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("the transaction is not a PostgreSQL transaction")
		}
		qs := q.New(ptx)
		if p.o.Recorder != nil {
			if err := p.o.Recorder.RecordTx(ctx, tx, audit.Event{
				Type: audit.EventRestored, Outcome: audit.Success, Actor: audit.Actor{Kind: "system", Name: "post-restore"},
				Details: mustJSON(map[string]string{"source": "pitr", "journal": journalState, "snapshotAt": t}),
			}); err != nil {
				return fmt.Errorf("writing system.restored: %w", err)
			}
		}
		if err := qs.JournalSetState(ctx, q.JournalSetStateParams{Key: DoneKey, Value: string(mustJSON(doneValue{RestoredTo: t, Journal: journalState, At: p.o.Now().UTC().Format(time.RFC3339)}))}); err != nil {
			return err
		}
		_, err := qs.JournalDeleteState(ctx, MarkerKey)
		return err
	})
	if err != nil {
		return rep, backup.Fail(backup.ExitFailed, backup.CodeInternal, err)
	}
	step("system.restored written; the marker is gone and the replicas may become ready")
	step("reminder: access granted after the restore point is gone, and revocations newer than the last shipped journal entry are undone (H-37)")
	return rep, nil
}

type priorState struct {
	marker *markerValue
	done   *doneValue
}

func (p *PostRestorer) readState(ctx context.Context) (priorState, error) {
	var st priorState
	err := p.inTx(ctx, func(qs *q.Queries) error {
		for key, dst := range map[string]any{MarkerKey: &st.marker, DoneKey: &st.done} {
			rows, err := qs.JournalGetState(ctx, key)
			if err != nil {
				return err
			}
			if len(rows) > 0 {
				if err := json.Unmarshal([]byte(rows[0]), dst); err != nil {
					return fmt.Errorf("system_state %s is not readable: %w", key, err)
				}
			}
		}
		return nil
	})
	return st, err
}

func (p *PostRestorer) inTx(ctx context.Context, fn func(qs *q.Queries) error) error {
	return p.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("the transaction is not a PostgreSQL transaction")
		}
		return fn(q.New(ptx))
	})
}

func (p *PostRestorer) setMarker(ctx context.Context, qs *q.Queries, t string) error {
	return qs.JournalSetPostRestoreMarker(ctx, string(mustJSON(markerValue{RestoredTo: t, Phase: "cleaned", StartedAt: p.o.Now().UTC().Format(time.RFC3339)})))
}

// cleanUp is the first transaction: the marker first, then the clean-up of R-HA-025 step 4.
func (p *PostRestorer) cleanUp(ctx context.Context, fx []fixup.Fixup, t string, step func(string)) error {
	env := fixup.Env{Now: p.o.Now(), NewKeyspaceID: newUUID(), NewIncarnation: newUUID()}
	return p.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("the transaction is not a PostgreSQL transaction")
		}
		qs := q.New(ptx)
		if err := p.setMarker(ctx, qs, t); err != nil {
			return fmt.Errorf("setting the post-restore marker: %w", err)
		}
		replicas, err := qs.JournalDeleteReplicaRows(ctx)
		if err != nil {
			return fmt.Errorf("deleting the replica rows: %w", err)
		}
		step(fmt.Sprintf("deleted %d replica rows", replicas))
		if err := qs.JournalPauseDeletions(ctx, "post-restore"); err != nil {
			return fmt.Errorf("setting the deletion pause: %w", err)
		}
		step("deletion pause set: garbage collection, the orphan sweep, retention and cap enforcement delete nothing until `trove admin maintenance resume-deletions`")
		sessions := 0
		for {
			ended, err := qs.EndAllSessions(ctx)
			if err != nil {
				return fmt.Errorf("ending sessions: %w", err)
			}
			if len(ended) == 0 {
				break
			}
			sessions += len(ended)
		}
		// The sessions that had ended before are withdrawn as well, so that no delete job one of them queued goes on (Q-AUTHZ-262).
		for {
			n, err := qs.WithdrawEndedSessions(ctx)
			if err != nil {
				return fmt.Errorf("withdrawing ended sessions: %w", err)
			}
			if n == 0 {
				break
			}
		}
		step(fmt.Sprintf("ended %d sessions", sessions))
		for _, f := range fx {
			if err := f.Apply(ctx, tx, env); err != nil {
				return fmt.Errorf("fix-up %s: %w", f.Name, err)
			}
			step("fix-up " + f.Name)
		}
		if err := qs.JournalSetRestoredAt(ctx); err != nil {
			return fmt.Errorf("setting restored_at: %w", err)
		}
		step("restored_at set: single-use CI issuers refuse subject tokens issued before the restore")
		return nil
	})
}

// reportQuarantine says when the journal has rows that could never be shipped (audit finding L4): the revocations they
// recorded cannot be replayed.
func (p *PostRestorer) reportQuarantine(ctx context.Context, step func(string)) {
	var n int64
	if err := p.inTx(ctx, func(qs *q.Queries) (err error) { n, err = qs.JournalQuarantined(ctx); return err }); err == nil && n > 0 {
		step(fmt.Sprintf("warning: %d outbox rows could not be encoded and were set aside (system_state journal_quarantined): the changes they recorded are not in the journal", n))
	}
}

func (p *PostRestorer) replay(ctx context.Context, opts backup.PostRestoreOptions) (*journal.Summary, error) {
	r, err := p.o.OpenReplayer(ctx, opts.CredentialsFile)
	if err != nil {
		return nil, backup.Fail(backup.ExitPrecondition, backup.CodeJournalUnavailable, fmt.Errorf("the revocation journal cannot be opened: %w", err))
	}
	sum, err := r.Replay(ctx, journal.ReplayOptions{RestoredTo: opts.RestoredTo, Margin: p.o.Margin})
	if err != nil {
		return nil, backup.Fail(backup.ExitPrecondition, backup.CodeJournalUnavailable, fmt.Errorf("replaying the revocation journal: %w", err))
	}
	return sum, nil
}

// pitrFixups selects the fix-ups of [pitrOrders]. A platform restore with none registered for an order would leave a stale
// keyspace or stale revision counters, which is worse than refusing, so it refuses.
func (p *PostRestorer) pitrFixups() ([]fixup.Fixup, error) {
	var out []fixup.Fixup
	for _, order := range pitrOrders() {
		n := 0
		for _, f := range p.o.Fixups {
			if f.Order == order {
				out = append(out, f)
				n++
			}
		}
		if n == 0 {
			return nil, backup.Failf(backup.ExitFailed, backup.CodeInternal,
				"no restore fix-up is registered for order %d: this build cannot clean up a platform restore (R-BAK-022)", order)
		}
	}
	return out, nil
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
