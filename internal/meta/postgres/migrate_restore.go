package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// RestoreSession is the one connection of `trove backup restore` (R-BAK-021): it takes the migration lock exclusively
// (R-ARCH-017) and holds it until Close, and everything the restore does to the database runs on it: the checks, the
// runner's restore mode (R-BAK-023, R-ARCH-026), the load transaction, the fix-ups and the finishing transaction. Closing
// the connection releases the lock, which is also what a killed process leaves behind.
//
// It is not safe for concurrent use. Its transactions carry the hooks of [meta.Tx], so the domain writers a fix-up or the
// journal replay needs (the audit writer, the stores of a format) run on it as on a pool.
type RestoreSession struct {
	conn *pgx.Conn
	cfg  config.Database
	opts MigrateOptions
	log  *slog.Logger
}

var _ meta.Runner = (*RestoreSession)(nil)

// OpenRestoreSession connects to the session host (a transaction-mode pooler cannot carry a session lock and the load
// transaction, R-BAK-005) and runs the session guard. The lock is taken by [RestoreSession.Lock].
//
// opts.Steps selects the migrations the restore applies and compares with the backup (nil: the embedded ones); opts.Version
// is recorded as the release of every step it applies.
func OpenRestoreSession(ctx context.Context, cfg config.Database, opts MigrateOptions) (*RestoreSession, error) {
	opts.fill()
	conn, err := sessionConn(ctx, cfg, opts, classMigrate)
	if err != nil {
		return nil, err
	}
	return &RestoreSession{conn: conn, cfg: cfg, opts: opts, log: opts.Log}, nil
}

// Close releases the migration lock by ending the session.
func (s *RestoreSession) Close(ctx context.Context) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = s.conn.Exec(rctx, "SELECT pg_advisory_unlock($1::int, 0)", lockMigration)
	_ = s.conn.Close(rctx)
}

// Lock takes pg_advisory_lock(3, 0) on the session connection, waiting up to wait (backup.database.migration_wait). A wait
// that ran out is a *LockWaitError.
func (s *RestoreSession) Lock(ctx context.Context, wait time.Duration) error {
	_, _, err := acquireLock(ctx, s.conn, wait, s.log)
	return err
}

// DBTX is the connection itself, for a reader that takes a q.DBTX (the sealed-value counter of the key ring check).
func (s *RestoreSession) DBTX() q.DBTX { return s.conn }

// InTx runs fn in one READ COMMITTED transaction on the session, as [MigrationSession.InTx] does. There is no retry: a
// failed phase of the restore is repeated by the Job's next pod, which resumes from the committed state.
func (s *RestoreSession) InTx(ctx context.Context, fn func(meta.Tx) error) error {
	return (&MigrationSession{conn: s.conn}).InTx(ctx, fn)
}

// Read runs fn on the session outside a transaction (the read side of the audit writer's store).
func (s *RestoreSession) Read(ctx context.Context, fn func(q.DBTX) error) error {
	return (&MigrationSession{conn: s.conn}).Read(ctx, fn)
}

// RestoreStep is one migration step of the binary: the number, the file name without .sql and the SHA-256 of the file as
// lowercase hex, the form of the manifest's schema.steps (R-BAK-003).
type RestoreStep struct {
	Version int
	Name    string
	SHA256  string
}

// Steps lists the steps this binary embeds, in order (R-BAK-020 check 2).
func (s *RestoreSession) Steps() ([]RestoreStep, error) {
	steps, err := ParseSteps(s.opts.Steps)
	if err != nil {
		return nil, err
	}
	out := make([]RestoreStep, len(steps))
	for i, st := range steps {
		out[i] = RestoreStep{Version: st.Version, Name: st.Name, SHA256: hex.EncodeToString(st.Checksum[:])}
	}
	return out, nil
}

// Restore phases, the value of RestoreMarker.Phase (R-BAK-021): the blobs are being copied, the load is committed, and the
// fix-ups are committed.
const (
	PhaseBlobs  = "blobs"
	PhaseLoaded = "loaded"
	PhaseFixed  = "fixed"
)

// RestoreMarker is the value of system_state key restore_in_progress (R-BAK-021 step 2): {"backupId", "phase", "startedAt"}.
type RestoreMarker struct {
	BackupID  string    `json:"backupId"`
	Phase     string    `json:"phase"`
	StartedAt time.Time `json:"startedAt"`
}

// RestoreMigration is the restore mode of the migration runner (R-BAK-023, R-ARCH-026 "Restore mode").
type RestoreMigration struct {
	// UpTo is the last step to apply; 0 applies every step the binary embeds. A step above UpTo is left alone.
	UpTo int
	// Marker, when set, is committed in the transaction of the last step this call applies (and in a transaction of its
	// own when nothing is pending), unless the row exists: restore_in_progress is there from the moment the schema is
	// servable, so a replica's schema check never finds a servable schema without it (R-BAK-021 step 2).
	Marker *RestoreMarker
	// After is called after each step committed, with its result, for the progress log.
	After func(StepResult)
}

// MigrateTo applies the embedded steps up to the step of the backup, or all of them, on the session connection, which
// already holds the migration lock. It uses the checksums, the lock timeouts and the retries of `trove migrate up`, and
// skips what that command does around the steps: the half-restored refusal (the restore set the anchor), the contract
// guard (no replica runs during a restore), the audit policy, the backup role and the declared state (the migration Job of
// the final upgrade applies those). It is not reachable from the command line of `trove migrate`.
func (s *RestoreSession) MigrateTo(ctx context.Context, m RestoreMigration) (Result, error) {
	var res Result
	steps, err := ParseSteps(s.opts.Steps)
	if err != nil {
		return res, err
	}
	if m.UpTo < 0 || m.UpTo > len(steps) {
		return res, fmt.Errorf("the restore asks for schema step %d, this binary embeds %d", m.UpTo, len(steps))
	}
	res.RequiredSchema = requiredSchema(steps, EnforceMigrationPhases)
	st, err := loadState(ctx, s.conn)
	if err != nil {
		return res, err
	}
	if err := verifyApplied(steps, st.applied); err != nil {
		return res, err
	}
	pending, err := plan(steps, st.applied)
	if err != nil {
		return res, err
	}
	if m.UpTo > 0 {
		kept := pending[:0]
		for _, p := range pending {
			if p.Version <= m.UpTo {
				kept = append(kept, p)
			}
		}
		pending = kept
	}
	res.SchemaApplied, res.CompatFloor = st.schemaApplied, st.compatFloor
	if len(pending) == 0 {
		res.NothingToDo = true
		if m.Marker != nil {
			if err := s.InsertMarker(ctx, *m.Marker); err != nil {
				return res, err
			}
		}
		return res, nil
	}
	for i, step := range pending {
		var final stepHook
		if m.Marker != nil && i == len(pending)-1 {
			marker := *m.Marker
			final = func(ctx context.Context, tx pgx.Tx) error { return insertMarker(ctx, tx, marker) }
		}
		sr, err := applyStep(ctx, s.conn, step, &st, s.opts, s.log, final)
		if err != nil {
			return res, fmt.Errorf("migration %s.sql: %w", step.Name, err)
		}
		res.Applied = append(res.Applied, sr)
		if m.After != nil {
			m.After(sr)
		}
	}
	res.SchemaApplied, res.CompatFloor = st.schemaApplied, st.compatFloor
	return res, nil
}

// ErrMarkerMissing is a phase change that found no restore_in_progress row to change.
var ErrMarkerMissing = errors.New("system_state has no restore_in_progress marker in the expected phase")

// PendingSteps lists the embedded steps that are not applied, for the check of R-BAK-021 step 8 (`trove migrate status`
// shows no pending step).
func (s *RestoreSession) PendingSteps(ctx context.Context) ([]StepInfo, error) {
	rep, err := Report(ctx, s.conn, s.opts)
	if err != nil {
		return nil, err
	}
	return rep.Pending, nil
}

// AppliedSteps lists the applied steps with their checksums as hex, to compare an existing schema with the manifest on a
// resumed restore.
func (s *RestoreSession) AppliedSteps(ctx context.Context) ([]RestoreStep, error) {
	st, err := loadState(ctx, s.conn)
	if err != nil {
		return nil, err
	}
	out := make([]RestoreStep, 0, len(st.applied))
	for _, a := range appliedInfos(st.applied) {
		out = append(out, RestoreStep{Version: a.Version, Name: a.Name, SHA256: hex.EncodeToString(st.applied[a.Version].Checksum)})
	}
	return out, nil
}
