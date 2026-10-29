package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// lockMigration is the advisory-lock category `migration` (classid 3, objid 0) of
// the registry in R-ARCH-017. `trove migrate` and `trove backup restore` hold it
// exclusively, a backup's database phase holds it shared.
const lockMigration int32 = 3

// ExitRefused is the exit status of a refusal that retrying cannot change: the
// contract guard and a half-restored database (R-HA-018, R-HA-019).
const ExitRefused = 3

// MigrateOptions are what the runner needs besides the database section.
type MigrateOptions struct {
	// Instance is the Job's pod name, used in application_name (trove:<instance>:migrate).
	Instance string
	// Migrate are the lock wait and DDL retry settings.
	Migrate config.Migrate
	// Version is the running binary's release; it is recorded as the release of
	// every step this run applies. "dev" counts as newer than every release.
	Version string
	// ReplicaStaleAfter is how recent a replica's heartbeat must be for the
	// contract guard to count it live (system.heartbeat_stale_after).
	ReplicaStaleAfter time.Duration
	// Steps overrides the embedded migrations; tests inject failing or additional
	// steps through it. Nil means EmbeddedFS.
	Steps fs.FS
	// AuditRetentionDays is audit.retention_days. When set, every run writes it
	// into audit_policy and calls audit_maintain_partitions() once the steps are
	// applied (R-OPS-015); nil leaves the row alone (tests with their own steps).
	AuditRetentionDays *int
	// Log receives the progress lines; nil means slog.Default.
	Log *slog.Logger
}

func (o *MigrateOptions) fill() {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Steps == nil {
		o.Steps = EmbeddedFS()
	}
	if o.Instance == "" {
		o.Instance = "migrate"
	}
	if o.ReplicaStaleAfter <= 0 {
		o.ReplicaStaleAfter = 30 * time.Second
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.Migrate.AdvisoryLockWait <= 0 {
		o.Migrate.AdvisoryLockWait = 5 * time.Minute
	}
	if o.Migrate.DDLLockTimeout <= 0 {
		o.Migrate.DDLLockTimeout = 5 * time.Second
	}
	if o.Migrate.DDLLockRetries <= 0 {
		o.Migrate.DDLLockRetries = 20
	}
}

// StepResult describes one step this run applied.
type StepResult struct {
	Version    int    `json:"version"`
	Name       string `json:"name"`
	Phase      string `json:"class"`
	DurationMs int64  `json:"durationMs"`
	Attempts   int    `json:"attempts"`
}

// Result is the summary `trove migrate up` prints (R-HA-019).
type Result struct {
	Applied        []StepResult `json:"applied"`
	SchemaApplied  int          `json:"schemaApplied"`
	CompatFloor    string       `json:"compatFloor"`
	RequiredSchema int          `json:"requiredSchema"`
	// WaitedForLock is true when another migrator, a backup or a restore held
	// the lock when this run asked for it.
	WaitedForLock bool  `json:"waitedForLock"`
	LockWaitMs    int64 `json:"lockWaitMs"`
	// NothingToDo is true when no step was pending.
	NothingToDo bool `json:"nothingToDo"`
}

// Refusal is a refusal that exits with status 3 (ExitRefused).
type Refusal struct {
	Reason   string
	BackupID string       // a half-restored database: the backup being restored
	Replicas []ReplicaRef // the contract guard: the live replicas that are too old
	Step     string       // the contract guard: the step that was refused
}

// ReplicaRef names a live replica.
type ReplicaRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (r *Refusal) Error() string { return r.Reason }

// ExitCode is 3.
func (r *Refusal) ExitCode() int { return ExitRefused }

// StatusReport is what `trove migrate status` prints; it takes no lock.
type StatusReport struct {
	Schema struct {
		Applied     int    `json:"applied"`
		CompatFloor string `json:"compatFloor"`
	} `json:"schema"`
	Binary struct {
		Version        string `json:"version"`
		RequiredSchema int    `json:"requiredSchema"`
	} `json:"binary"`
	Pending []StepInfo    `json:"pending"`
	Applied []AppliedInfo `json:"applied"`
	// Servable is the verdict of the compatibility gate for this binary.
	Servable bool   `json:"servable"`
	Verdict  string `json:"verdict,omitempty"`
}

// StepInfo is a pending step.
type StepInfo struct {
	Version     int    `json:"version"`
	Name        string `json:"name"`
	Class       string `json:"class"`
	RequiresApp string `json:"requiresApp,omitempty"`
}

// AppliedInfo is an applied step (the shape of GET /api/v1/system/migrations).
type AppliedInfo struct {
	Version    int       `json:"version"`
	Name       string    `json:"name"`
	Class      string    `json:"class"`
	Release    string    `json:"release"`
	AppliedAt  time.Time `json:"appliedAt"`
	DurationMs int32     `json:"durationMs"`
}

// sessionConn opens the single session connection of the migration runner on
// the session host, retrying failover errors, and runs the session guard.
func sessionConn(ctx context.Context, cfg config.Database, opts MigrateOptions, class string) (*pgx.Conn, error) {
	cc, err := connectConfig(cfg, sessionEndpoint(cfg), opts.Instance, class, true)
	if err != nil {
		return nil, err
	}
	var conn *pgx.Conn
	err = newRetrier(cfg.Retry).do(ctx, func() error {
		var cerr error
		conn, cerr = pgx.ConnectConfig(ctx, cc)
		return cerr
	})
	if err != nil {
		if mapped := refusedStartupParameter(err); mapped != err { //nolint:errorlint // identity: the mapping returns its input unchanged
			return nil, mapped
		}
		return nil, fmt.Errorf("cannot connect to %s: %w", sessionEndpoint(cfg), err)
	}
	if err := sessionGuard(ctx, conn); err != nil {
		_ = conn.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return conn, nil
}

// Migrate applies the pending steps (R-ARCH-026, R-HA-019). It runs on one
// connection to the session host: preflight, the migration lock (waiting up to
// migrate.advisory_lock_wait), the half-restored refusal, the checksum check,
// the plan with the contract guard, then each step in its own transaction
// together with its schema_migration and schema_state rows.
//
// A *Refusal means nothing was applied and retrying will not help (exit status 3).
func Migrate(ctx context.Context, cfg config.Database, opts MigrateOptions) (Result, error) {
	opts.fill()
	log := opts.Log
	var res Result

	steps, err := ParseSteps(opts.Steps)
	if err != nil {
		return res, err
	}
	res.RequiredSchema = requiredSchema(steps)

	conn, err := sessionConn(ctx, cfg, opts, classMigrate)
	if err != nil {
		return res, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	// 1. Preflight (R-HA-013, R-HA-014, R-HA-016).
	info, err := serverChecks(ctx, conn, cfg)
	if err != nil {
		return res, err
	}
	if err := checkBudget(cfg, int(info.MaxConnections), int(info.SuperuserReserved), log); err != nil {
		return res, err
	}
	if err := ensureSchema(ctx, conn, cfg.Schema, info.RoleName, log); err != nil {
		return res, err
	}

	// 2. The migration lock; every later step runs on this connection.
	waited, took, err := acquireLock(ctx, conn, opts.Migrate.AdvisoryLockWait, log)
	if err != nil {
		return res, err
	}
	res.WaitedForLock, res.LockWaitMs = waited, took.Milliseconds()
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1::int, 0)", lockMigration)
	}()

	// 3. Refuse a half-restored database, then verify the applied steps.
	if err := refuseHalfRestored(ctx, conn, cfg.Schema); err != nil {
		return res, err
	}
	st, err := loadState(ctx, conn)
	if err != nil {
		return res, err
	}
	if err := verifyApplied(steps, st.applied); err != nil {
		return res, err
	}

	// 4. Plan, contract guard.
	pending, err := plan(steps, st.applied)
	if err != nil {
		return res, err
	}
	if err := contractGuard(ctx, conn, pending, opts.ReplicaStaleAfter); err != nil {
		return res, err
	}
	res.SchemaApplied, res.CompatFloor = st.schemaApplied, st.compatFloor
	if len(pending) == 0 {
		res.NothingToDo = true
		log.Info("nothing to do", "schema_applied", st.schemaApplied, "required_schema", res.RequiredSchema)
		return res, syncAudit(ctx, conn, opts, log)
	}

	// 5. Apply.
	for _, s := range pending {
		sr, err := applyStep(ctx, conn, s, &st, opts, log)
		if err != nil {
			return res, fmt.Errorf("migration %s.sql: %w", s.Name, err)
		}
		res.Applied = append(res.Applied, sr)
	}
	res.SchemaApplied, res.CompatFloor = st.schemaApplied, st.compatFloor
	return res, syncAudit(ctx, conn, opts, log)
}

// syncAudit is the audit part of every run, applied or not: the retention and
// the partitions (R-OPS-015). It runs under the migration lock, as the schema owner.
func syncAudit(ctx context.Context, conn *pgx.Conn, opts MigrateOptions, log *slog.Logger) error {
	if opts.AuditRetentionDays == nil {
		return nil
	}
	return syncAuditPolicy(ctx, conn, *opts.AuditRetentionDays, log)
}

// ensureSchema creates the schema if it is missing (a database owner may) and
// checks that the migration role can act as its owner (R-HA-014).
func ensureSchema(ctx context.Context, conn *pgx.Conn, schema, role string, log *slog.Logger) error {
	qs := q.New(conn)
	_, err := qs.SchemaOwnedBy(ctx, schema)
	if isNoRows(err) {
		if _, cerr := conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); cerr != nil {
			return fmt.Errorf("schema %q does not exist and %q cannot create it (%w); create it as the migration role: CREATE SCHEMA %s", schema, role, cerr, schema)
		}
		log.Info("created schema", "schema", schema, "owner", role)
	} else if err != nil {
		return err
	}
	ok, err := qs.SchemaOwnerAccessible(ctx, schema)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("schema %q is not owned by the migration role %q", schema, role)
	}
	return nil
}

// acquireLock takes pg_advisory_lock(3, 0) by polling pg_try_advisory_lock, so
// the wait is bounded and can name the holder.
func acquireLock(ctx context.Context, conn *pgx.Conn, wait time.Duration, log *slog.Logger) (waited bool, took time.Duration, err error) {
	start := time.Now()
	for {
		var got bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1::int, 0)", lockMigration).Scan(&got); err != nil {
			return waited, time.Since(start), err
		}
		if got {
			if waited {
				log.Info("migration lock acquired after waiting", "waited_ms", time.Since(start).Milliseconds())
			}
			return waited, time.Since(start), nil
		}
		holders := lockHolders(ctx, conn)
		if !waited {
			waited = true
			log.Info("waiting for the migration lock", "held_by", holders, "max_wait", wait.String())
		}
		if time.Since(start) >= wait {
			return waited, time.Since(start), fmt.Errorf("gave up after %s waiting for the migration lock (migrate.advisory_lock_wait), held by %s", wait, holders)
		}
		if err := sleepCtx(ctx, 200*time.Millisecond); err != nil {
			return waited, time.Since(start), err
		}
	}
}

func lockHolders(ctx context.Context, conn *pgx.Conn) string {
	rows, err := q.New(conn).MigrationLockHolders(ctx)
	if err != nil || len(rows) == 0 {
		return "another session"
	}
	var parts []string
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s (%s)", r.ApplicationName, r.Mode))
	}
	return strings.Join(parts, ", ")
}

// restoreAnchor is the prefix of the schema comment a restore sets (R-ARCH-026).
const restoreAnchor = "trove-restore "

// refuseHalfRestored implements step 2 of R-ARCH-026.
func refuseHalfRestored(ctx context.Context, conn *pgx.Conn, schema string) error {
	qs := q.New(conn)
	comment, err := qs.SchemaComment(ctx, schema)
	if err != nil {
		return err
	}
	if id, ok := strings.CutPrefix(strings.TrimSpace(comment), restoreAnchor); ok {
		return halfRestored(strings.TrimSpace(id))
	}
	found, err := qs.TableExists(ctx, "system_state")
	if err != nil || !found {
		return err
	}
	row, err := qs.GetSystemState(ctx, "restore_in_progress")
	if isNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return halfRestored(backupIDOf(row.Value))
}

func halfRestored(id string) *Refusal {
	return &Refusal{BackupID: id, Reason: fmt.Sprintf("the database is half restored from backup %s: resume the restore (trove backup restore) instead of migrating; nothing was applied", id)}
}

// backupIDOf extracts the backup ID from the value of restore_in_progress: a
// JSON string, or an object with a backupId or id member.
func backupIDOf(raw []byte) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o map[string]any
	if json.Unmarshal(raw, &o) == nil {
		for _, k := range []string{"backupId", "backup_id", "id"} {
			if v, ok := o[k].(string); ok {
				return v
			}
		}
	}
	return string(bytes.TrimSpace(raw))
}

// state is what the database says about the schema.
type state struct {
	applied       map[int]q.SchemaMigration
	schemaApplied int
	compatFloor   string
}

func loadState(ctx context.Context, db q.DBTX) (state, error) {
	st := state{applied: map[int]q.SchemaMigration{}, compatFloor: zeroVersion}
	qs := q.New(db)
	found, err := qs.TableExists(ctx, "schema_migration")
	if err != nil || !found {
		return st, err
	}
	rows, err := qs.ListAppliedSteps(ctx)
	if err != nil {
		return st, err
	}
	for _, r := range rows {
		st.applied[int(r.Version)] = r
		st.schemaApplied = max(st.schemaApplied, int(r.Version))
		if r.RequiresApp.Valid {
			a, _ := parseVersion(r.RequiresApp.String)
			b, _ := parseVersion(st.compatFloor)
			if a.compare(b) > 0 {
				st.compatFloor = r.RequiresApp.String
			}
		}
	}
	return st, nil
}

// verifyApplied compares the checksum of every applied step this binary knows
// with the embedded file (R-HA-018). Applied steps this binary does not know are
// newer than it and are not its business.
func verifyApplied(steps []Step, applied map[int]q.SchemaMigration) error {
	for _, s := range steps {
		a, ok := applied[s.Version]
		if !ok {
			continue
		}
		if !bytes.Equal(a.Checksum, s.Checksum[:]) {
			return fmt.Errorf("migration %s.sql was changed after it was applied (checksum mismatch); applied steps are never edited, add a new step instead", s.Name)
		}
		if a.Name != s.Name {
			return fmt.Errorf("migration %05d was applied as %s but this binary embeds it as %s", s.Version, a.Name, s.Name)
		}
	}
	return nil
}

// plan returns the steps that are not applied, in order. A step that is not
// applied while a later one is would be applied out of order: an error.
func plan(steps []Step, applied map[int]q.SchemaMigration) ([]Step, error) {
	maxApplied := 0
	for v := range applied {
		maxApplied = max(maxApplied, v)
	}
	var pending []Step
	for _, s := range steps {
		if _, ok := applied[s.Version]; ok {
			continue
		}
		if s.Version < maxApplied {
			return nil, fmt.Errorf("migration %s.sql is not applied but step %05d is: steps are applied in order", s.Name, maxApplied)
		}
		pending = append(pending, s)
	}
	return pending, nil
}

// contractGuard refuses to apply a contract step while a live replica runs a
// release below the step's requires-app (R-HA-018). No flag overrides it.
func contractGuard(ctx context.Context, db q.DBTX, pending []Step, stale time.Duration) error {
	var contracts []Step
	for _, s := range pending {
		if s.Phase == phaseContract {
			contracts = append(contracts, s)
		}
	}
	if len(contracts) == 0 {
		return nil
	}
	qs := q.New(db)
	if found, err := qs.TableExists(ctx, "replica"); err != nil || !found {
		return err
	}
	reps, err := qs.LiveReplicas(ctx, stale.Seconds())
	if err != nil {
		return err
	}
	for _, s := range contracts {
		floor, _ := parseVersion(s.RequiresApp)
		var old []ReplicaRef
		for _, r := range reps {
			v, err := parseVersion(r.Version)
			if err != nil || v.compare(floor) < 0 {
				old = append(old, ReplicaRef{Name: r.Name, Version: r.Version})
			}
		}
		if len(old) == 0 {
			continue
		}
		var names []string
		for _, r := range old {
			names = append(names, fmt.Sprintf("%s (%s)", r.Name, r.Version))
		}
		return &Refusal{Step: s.Name, Replicas: old, Reason: fmt.Sprintf(
			"contract step %s.sql requires trove >= %s, but live replicas run older releases: %s; nothing was applied. "+
				"Upgrade one minor release at a time, or stop all replicas and upgrade with downtime (R-HA-021)",
			s.Name, s.RequiresApp, strings.Join(names, ", "))}
	}
	return nil
}

var createIndexConcurrently = regexp.MustCompile(`(?i)create\s+(?:unique\s+)?index\s+concurrently\s+(?:if\s+not\s+exists\s+)?("?[a-z0-9_]+"?)`)

// applyStep applies one step, retrying lock timeouts, and updates st.
func applyStep(ctx context.Context, conn *pgx.Conn, s Step, st *state, opts MigrateOptions, log *slog.Logger) (StepResult, error) {
	log.Info("applying step", "version", s.Version, "name", s.Name, "class", s.Phase, "transactional", !s.NoTx)
	res := StepResult{Version: s.Version, Name: s.Name, Phase: s.Phase}
	backoff := 200 * time.Millisecond
	for attempt := 1; ; attempt++ {
		res.Attempts = attempt
		start := time.Now()
		var err error
		if s.NoTx {
			err = applyNoTx(ctx, conn, s, st, opts)
		} else {
			err = applyTx(ctx, conn, s, st, opts, start)
		}
		if err == nil {
			res.DurationMs = time.Since(start).Milliseconds()
			log.Info("applied step", "version", s.Version, "name", s.Name, "duration_ms", res.DurationMs, "attempts", attempt)
			return res, nil
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "55P03" && attempt <= opts.Migrate.DDLLockRetries {
			log.Warn("step waited too long for a lock, retrying", "version", s.Version, "attempt", attempt,
				"lock_timeout", opts.Migrate.DDLLockTimeout.String(), "retry_in", backoff.String())
			if serr := sleepCtx(ctx, jitter(backoff)); serr != nil {
				return res, serr
			}
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		return res, err
	}
}

// recordStep writes the step's schema_migration row and the new schema_state
// inside tx and, on success of the commit, the caller advances st.
func recordStep(ctx context.Context, tx pgx.Tx, s Step, st *state, release string, took time.Duration) (applied int, floor string, err error) {
	qs := q.New(tx)
	if err := qs.InsertAppliedStep(ctx, q.InsertAppliedStepParams{
		Version: int32(s.Version), Name: s.Name, Class: s.Phase, Release: release, //nolint:gosec // bounded by the file count
		RequiresApp:   textOf(s.RequiresApp),
		Transactional: !s.NoTx, Checksum: s.Checksum[:], DurationMs: int32(took.Milliseconds()), //nolint:gosec // a step does not run for 24 days
	}); err != nil {
		return 0, "", err
	}
	applied, floor = max(st.schemaApplied, s.Version), st.compatFloor
	if s.RequiresApp != "" {
		a, _ := parseVersion(s.RequiresApp)
		b, _ := parseVersion(floor)
		if a.compare(b) > 0 {
			floor = s.RequiresApp
		}
	}
	if err := qs.UpsertSchemaState(ctx, q.UpsertSchemaStateParams{Applied: int32(applied), CompatFloor: floor}); err != nil { //nolint:gosec // bounded by the file count
		return 0, "", err
	}
	return applied, floor, nil
}

// applyTx runs a transactional step: BEGIN, SET LOCAL lock_timeout, the whole
// file as one argument-less Exec (simple protocol, so the server splits the
// statements), the step's rows, COMMIT. A failure rolls everything back.
func applyTx(ctx context.Context, conn *pgx.Conn, s Step, st *state, opts MigrateOptions, start time.Time) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = %d", opts.Migrate.DDLLockTimeout.Milliseconds())); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, s.SQL); err != nil {
		return err
	}
	applied, floor, err := recordStep(ctx, tx, s, st, opts.Version, time.Since(start))
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	st.schemaApplied, st.compatFloor = applied, floor
	return nil
}

// applyNoTx runs a step that cannot run in a transaction block (CREATE INDEX
// CONCURRENTLY): its single statement under the session's lock_timeout, then its
// rows. An INVALID index that a failed earlier attempt left is dropped first.
func applyNoTx(ctx context.Context, conn *pgx.Conn, s Step, st *state, opts MigrateOptions) error {
	start := time.Now()
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = %d", opts.Migrate.DDLLockTimeout.Milliseconds())); err != nil {
		return err
	}
	if m := createIndexConcurrently.FindStringSubmatch(stripSQLNoise(s.SQL)); m != nil {
		name := strings.Trim(m[1], `"`)
		invalid, err := q.New(conn).InvalidIndexes(ctx, schemaOf(conn))
		if err != nil {
			return err
		}
		for _, n := range invalid {
			if strings.EqualFold(n, name) {
				if _, err := conn.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+pgx.Identifier{n}.Sanitize()); err != nil {
					return err
				}
			}
		}
	}
	if _, err := conn.Exec(ctx, s.SQL); err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	applied, floor, err := recordStep(ctx, tx, s, st, opts.Version, time.Since(start))
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	st.schemaApplied, st.compatFloor = applied, floor
	return nil
}

// schemaOf reads the schema a session connection was started with.
func schemaOf(conn *pgx.Conn) string { return conn.Config().RuntimeParams["search_path"] }

// Status lists applied and pending steps and the gate's verdict for this
// binary, without taking the lock (R-ARCH-026).
func Status(ctx context.Context, cfg config.Database, opts MigrateOptions) (StatusReport, error) {
	opts.fill()
	var rep StatusReport
	steps, err := ParseSteps(opts.Steps)
	if err != nil {
		return rep, err
	}
	conn, err := sessionConn(ctx, cfg, opts, classMigrate)
	if err != nil {
		return rep, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	qs := q.New(conn)
	_, err = qs.SchemaOwnedBy(ctx, cfg.Schema)
	var st state
	switch {
	case isNoRows(err):
		st = state{applied: map[int]q.SchemaMigration{}, compatFloor: zeroVersion}
	case err != nil:
		return rep, err
	default:
		if st, err = loadState(ctx, conn); err != nil {
			return rep, err
		}
	}
	if err := verifyApplied(steps, st.applied); err != nil {
		return rep, err
	}
	rep.Schema.Applied, rep.Schema.CompatFloor = st.schemaApplied, st.compatFloor
	rep.Binary.Version, rep.Binary.RequiredSchema = opts.Version, requiredSchema(steps)
	rep.Applied = []AppliedInfo{}
	versions := make([]int, 0, len(st.applied))
	for v := range st.applied {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	for _, v := range versions {
		a := st.applied[v]
		rep.Applied = append(rep.Applied, AppliedInfo{
			Version: v, Name: a.Name, Class: a.Class, Release: a.Release,
			AppliedAt: a.AppliedAt.Time, DurationMs: a.DurationMs,
		})
	}
	rep.Pending = []StepInfo{}
	for _, s := range steps {
		if _, done := st.applied[s.Version]; !done {
			rep.Pending = append(rep.Pending, StepInfo{Version: s.Version, Name: s.Name, Class: s.Phase, RequiresApp: s.RequiresApp})
		}
	}
	verdict := Gate(SchemaState{Applied: st.schemaApplied, CompatFloor: st.compatFloor}, rep.Binary.RequiredSchema, opts.Version)
	rep.Servable = verdict == nil
	if verdict != nil {
		rep.Verdict = verdict.Error()
	}
	return rep, nil
}
