package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// SchemaState is schema_state: the newest applied step and the compatibility
// floor (R-HA-018).
type SchemaState struct {
	Applied     int
	CompatFloor string
}

// ErrSchemaBehind means the database has not yet reached this binary's
// requiredSchema. The replica waits not ready; the pre-upgrade Job normally
// migrates first (R-HA-008).
var ErrSchemaBehind = errors.New("schema is behind this binary")

// FloorError means the schema's compat_floor is above this binary's version: the
// process exits with its message (R-ARCH-026).
type FloorError struct{ Floor, Version string }

func (e *FloorError) Error() string { return "schema requires trove >= " + e.Floor }

// Gate is the compatibility gate of R-HA-018: a binary of version V serves iff
// applied >= requiredSchema and compat_floor <= V. There is no upper bound on
// applied: under the expand/contract rules newer expand steps do not make an
// older binary incompatible; while they are switched off (EnforceMigrationPhases,
// Q-ARCH-340) nothing promises that an older binary works on a newer schema.
// Below requiredSchema it returns an error wrapping ErrSchemaBehind; above the
// floor a *FloorError. The floor is checked first, because a binary that is too
// old will never become servable by waiting.
func Gate(st SchemaState, required int, version string) error {
	floor, err := parseVersion(st.CompatFloor)
	if err != nil {
		return fmt.Errorf("schema_state.compat_floor %q: %w", st.CompatFloor, err)
	}
	v, err := parseVersion(version)
	if err != nil {
		return fmt.Errorf("binary version: %w", err)
	}
	if floor.compare(v) > 0 {
		return &FloorError{Floor: st.CompatFloor, Version: version}
	}
	if st.Applied < required {
		return fmt.Errorf("%w: applied %d, this binary needs %d", ErrSchemaBehind, st.Applied, required)
	}
	return nil
}

// SchemaState reads schema_state on the request pool, for callers on a request
// path such as GET /system/info: a request must never queue behind the loops
// that hold the control connection, and the control connection must not be
// spent on a client's rate. A database without the table or the row counts as
// applied 0. It reads through Read, so it rides out a failover.
func (db *DB) SchemaState(ctx context.Context) (SchemaState, error) {
	var st SchemaState
	err := db.Read(ctx, func(c q.DBTX) (err error) {
		st, err = readSchemaState(ctx, c)
		return err
	})
	return st, err
}

// readSchemaState reads schema_state through any pool or connection.
func readSchemaState(ctx context.Context, conn q.DBTX) (SchemaState, error) {
	qs := q.New(conn)
	found, err := qs.TableExists(ctx, "schema_state")
	if err != nil || !found {
		return SchemaState{CompatFloor: zeroVersion}, err
	}
	row, err := qs.GetSchemaState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return SchemaState{CompatFloor: zeroVersion}, nil
	}
	if err != nil {
		return SchemaState{}, err
	}
	return SchemaState{Applied: int(row.Applied), CompatFloor: row.CompatFloor}, nil
}

// CheckSchema is the `schema` readiness check (R-OPS-001): Gate applied to the
// database and this binary's embedded requiredSchema. It reads on the control
// connection, like the other checks readiness depends on, on one connection for
// all its statements; ctx bounds the wait for it, the statements run under
// probeStatementBound (Q-HA-284).
func (db *DB) CheckSchema(ctx context.Context, version string) error {
	conn, err := db.control.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	sctx, cancel := db.probeStatementContext(ctx)
	defer cancel()
	st, err := readSchemaState(sctx, conn)
	if err != nil {
		return err
	}
	if err := Gate(st, RequiredSchema(), version); err != nil {
		return err
	}
	// After the schema state, never before: a restore commits the marker with the step that makes the schema servable, so a
	// state read first cannot be followed by a missing marker (R-BAK-021 step 2).
	return checkRestoreMarker(sctx, conn)
}

// ErrRestoreInProgress means system_state holds restore_in_progress: a restore has not finished, and the replica waits, not
// ready and claiming no job (R-ARCH-024 start-up step 3, R-OPS-001).
var ErrRestoreInProgress = errors.New("a restore is in progress")

// checkRestoreMarker returns ErrRestoreInProgress, naming the backup, while the marker exists.
func checkRestoreMarker(ctx context.Context, conn q.DBTX) error {
	qs := q.New(conn)
	found, err := qs.TableExists(ctx, "system_state")
	if err != nil || !found {
		return err
	}
	row, err := qs.GetSystemState(ctx, "restore_in_progress")
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		return fmt.Errorf("%w: backup %s", ErrRestoreInProgress, backupIDOf(row.Value))
	}
	// `trove admin post-restore` of a platform restore holds the replicas the same way until its replay has finished.
	post, err := qs.GetSystemState(ctx, PostRestoreMarkerKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: post-restore %s", ErrRestoreInProgress, restoredToOf(post.Value))
}

// PostRestoreMarkerKey is the system_state key `trove admin post-restore` commits in its first transaction and deletes in its
// last (R-HA-025 step 4): the schema check fails while it exists.
const PostRestoreMarkerKey = "post_restore_in_progress"

// restoredToOf extracts the restore point from the value of the post-restore marker.
func restoredToOf(raw []byte) string {
	var o struct {
		RestoredTo string `json:"restoredTo"`
	}
	if json.Unmarshal(raw, &o) == nil && o.RestoredTo != "" {
		return o.RestoredTo
	}
	return "(restore point unknown)"
}

// ReplicasUpTo lists the live replicas that run release or an older one, and those whose
// version is no release number: the replicas that may still run the code of release. A
// replica is live while its heartbeat is newer than staleAfter on PostgreSQL's clock
// (R-HA-003); "dev" is newer than every release. A background data migration that a later
// release introduced waits while one is listed, because such a replica can still write rows
// the old way behind it (R-HA-022).
func ReplicasUpTo(ctx context.Context, db q.DBTX, release string, staleAfter time.Duration) ([]ReplicaRef, error) {
	reps, err := q.New(db).LiveReplicas(ctx, staleAfter.Seconds())
	if err != nil {
		return nil, fmt.Errorf("reading the live replicas: %w", err)
	}
	return replicasUpTo(reps, release)
}

// replicasUpTo is the selection of [ReplicasUpTo] over rows already read.
func replicasUpTo(reps []q.LiveReplicasRow, release string) ([]ReplicaRef, error) {
	limit, err := parseVersion(release)
	if err != nil {
		return nil, fmt.Errorf("release %q: %w", release, err)
	}
	out := []ReplicaRef{}
	for _, r := range reps {
		if v, err := parseVersion(r.Version); err != nil || v.compare(limit) <= 0 {
			out = append(out, ReplicaRef{Name: r.Name, Version: r.Version})
		}
	}
	return out, nil
}
