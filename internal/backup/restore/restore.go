package restore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/fixup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/listq"
	"github.com/guided-traffic/trove/internal/secrets"
)

// Deps is what a restore is built from.
type Deps struct {
	Session Session
	Target  target.Store
	Layout  format.Layout
	// TargetEndpoint and TargetBucket name the target for a server-side copy into the blob store.
	TargetEndpoint, TargetBucket string
	Blobs                        BlobStore
	Keys                         *secrets.KeyRing
	// Items builds the storage delete path the remote-cache purgers empty caches through (fixup.Env.Items, R-BAK-008) on the
	// restore's session; nil means this binary cannot, and a restore that must purge fails at step 7.
	Items func(ctx context.Context, db meta.Runner) (fixup.CacheEraser, error)
	// Journal builds the revocation journal of a source; nil means this binary cannot read one.
	Journal func(ctx context.Context, env JournalEnv) (Journal, error)
	Config  config.Backup
	// Registry classifies the tables; nil means tables.Default().
	Registry *tables.Registry
	Log      *slog.Logger
	// Out receives the warnings and the next steps of the summary.
	Out io.Writer
	// Pod names the Job's pod, for the probe object and the audit event.
	Pod string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Hook is called at named points of the phases ("step2:anchored", "step2:migrated:<n>", "step3:copied:<n>",
	// "load:<table>", "step5", "step6", "step7", "step8", "step9"). The kill tests pause there; production leaves it nil.
	Hook func(event string)
}

// Restorer implements [backup.Restorer].
type Restorer struct {
	d    Deps
	last atomic.Int64
	rec  audit.Recorder
}

var _ backup.Restorer = (*Restorer)(nil)

// New returns a Restorer. It closes the session when a restore ends.
func New(d Deps) *Restorer {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Out == nil {
		d.Out = io.Discard
	}
	if d.Registry == nil {
		d.Registry = tables.Default()
	}
	return &Restorer{d: d}
}

func (r *Restorer) hook(event string) {
	if r.d.Hook != nil {
		r.d.Hook(event)
	}
}

// progress records that the restore made progress (R-BAK-013 stall detection).
func (r *Restorer) progress() { r.last.Store(time.Now().UnixNano()) }

// Restore runs the restore, or with opts.Check only its checks.
func (r *Restorer) Restore(parent context.Context, opts backup.RestoreOptions) (*backup.RestoreReport, error) {
	if opts.IdentityFile != "" {
		return nil, backup.Failf(backup.ExitUsage, backup.CodeConfig, "--identity-file: client-side encrypted backups are not built yet (R-BAK-027)")
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	r.progress()
	go r.watch(ctx, cancel)
	defer r.d.Session.Close(context.WithoutCancel(parent))
	rep, err := r.run(ctx, opts)
	return rep, r.classify(ctx, err)
}

// watch cancels the restore when it made no progress for backup.stall_timeout (R-BAK-013).
func (r *Restorer) watch(ctx context.Context, cancel context.CancelCauseFunc) {
	limit := r.d.Config.StallTimeout
	if limit <= 0 {
		return
	}
	t := time.NewTicker(max(limit/10, 10*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if time.Since(time.Unix(0, r.last.Load())) > limit {
				cancel(backup.ErrStalled)
				return
			}
		}
	}
}

// classify maps an error of the run to the exit code of R-BAK-013.
func (r *Restorer) classify(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(context.Cause(ctx), backup.ErrStalled) {
		return backup.Failf(backup.ExitTemporary, backup.CodeStalled, "the restore made no progress for backup.stall_timeout (%s); the next pod resumes it: %w", r.d.Config.StallTimeout, err)
	}
	if ctx.Err() != nil {
		return backup.Failf(backup.ExitTemporary, backup.CodeInterrupted, "the restore was interrupted; the next run resumes it: %w", err)
	}
	var ee *backup.ExitError
	var lw *postgres.LockWaitError
	var se *postgres.RestoreSchemaError
	var mm *secrets.MismatchError
	switch {
	case errors.As(err, &ee):
		return err
	case errors.As(err, &lw):
		return backup.Failf(backup.ExitTemporary, backup.CodeMigrationWait, "gave up after %s waiting for the migration lock (backup.database.migration_wait), held by %s: another restore or migration runs; retry later", lw.Wait, lw.Holders)
	case errors.As(err, &se):
		return backup.Fail(backup.ExitPrecondition, backup.CodeSchemaStepMismatch, err)
	case errors.As(err, &mm):
		return backup.Fail(backup.ExitPrecondition, backup.CodeKeyRingMismatch, err)
	case postgres.IsUnavailable(err):
		return backup.Fail(backup.ExitTemporary, backup.CodeTemporary, err)
	}
	return err
}

func (r *Restorer) run(ctx context.Context, opts backup.RestoreOptions) (*backup.RestoreReport, error) {
	s := r.d.Session
	if err := s.Lock(ctx, r.d.Config.Database.MigrationWait); err != nil {
		return nil, err
	}
	r.progress()
	m, err := r.loadManifest(ctx, opts.ID)
	if err != nil {
		return nil, err
	}
	st := &state{id: opts.ID, m: m}
	if err := r.runChecks(ctx, st, opts); err != nil {
		return nil, err
	}
	r.progress()
	rep := &backup.RestoreReport{BackupID: opts.ID, SnapshotAt: m.SnapshotAt, Journal: "replayed", Phases: map[string]float64{}}
	if opts.Check {
		rep.Checked = true
		rep.Journal = ""
		return rep, nil
	}
	if opts.SkipRevocationJournal || !m.Source.RevocationJournal {
		rep.Journal = "skipped"
	}
	r.d.Log.Info("restore checks passed", "backup", opts.ID, "anchored", st.anchored, "snapshot_at", m.SnapshotAt, "schema_step", m.Schema.Step)

	phase := func(name string, fn func() error) error {
		start := time.Now()
		err := fn()
		rep.Phases[name] = time.Since(start).Seconds()
		r.progress()
		return err
	}
	var marker *postgres.RestoreMarker
	steps := []struct {
		name string
		fn   func() error
	}{
		{"schema", func() (err error) { marker, err = r.stepSchema(ctx, st); return err }},
		{"blobs", func() error { return r.stepBlobs(ctx, st, marker) }},
		{"load", func() error { return r.stepLoad(ctx, st, marker, rep) }},
		{"forward", func() error { return r.stepForward(ctx) }},
		{"fixups", func() error { return r.stepFixups(ctx, st, marker) }},
		{"caches", func() error { return r.stepCaches(ctx, st) }},
		{"journal", func() error { return r.stepJournal(ctx, st, opts, rep) }},
		{"verify", func() error { return r.stepVerify(ctx, st) }},
		{"finish", func() error { return r.stepFinish(ctx, st, rep) }},
	}
	for _, s := range steps {
		if err := phase(s.name, s.fn); err != nil {
			return nil, err
		}
	}
	r.summary(st)
	return rep, nil
}

// stepSchema is step 2: the anchor, then the migrations up to the backup's step with the marker committed in the last.
func (r *Restorer) stepSchema(ctx context.Context, st *state) (*postgres.RestoreMarker, error) {
	s := r.d.Session
	if st.inspect.Anchor == "" {
		if err := s.SetAnchor(ctx, string(st.id)); err != nil {
			return nil, err
		}
	}
	r.hook("step2:anchored")
	fresh := postgres.RestoreMarker{BackupID: string(st.id), Phase: postgres.PhaseBlobs, StartedAt: r.d.Now().UTC()}
	if _, err := s.MigrateTo(ctx, postgres.RestoreMigration{UpTo: st.m.Schema.Step, Marker: &fresh, After: func(sr postgres.StepResult) {
		r.progress()
		r.hook(fmt.Sprintf("step2:migrated:%d", sr.Version))
	}}); err != nil {
		return nil, err
	}
	in, err := s.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	if in.Marker == nil {
		return nil, errors.New("restore_in_progress is missing after the schema step")
	}
	if in.Marker.BackupID != string(st.id) {
		return nil, backup.Failf(backup.ExitPrecondition, backup.CodeDatabaseNotEmpty, "restore_in_progress names backup %s, not %s", in.Marker.BackupID, st.id)
	}
	r.d.Log.Info("schema at the backup's step", "step", st.m.Schema.Step, "phase", in.Marker.Phase)
	return in.Marker, nil
}

// stepForward is step 5: the binary's remaining migrations.
func (r *Restorer) stepForward(ctx context.Context) error {
	r.hook("step5")
	res, err := r.d.Session.MigrateTo(ctx, postgres.RestoreMigration{After: func(postgres.StepResult) { r.progress() }})
	if err != nil {
		return err
	}
	r.d.Log.Info("schema brought forward", "applied", len(res.Applied), "schema", res.SchemaApplied)
	return nil
}

// stepLoad is step 4, skipped once the marker's phase is loaded or later.
func (r *Restorer) stepLoad(ctx context.Context, st *state, marker *postgres.RestoreMarker, rep *backup.RestoreReport) error {
	if marker.Phase != postgres.PhaseBlobs {
		r.d.Log.Info("load already committed", "phase", marker.Phase)
		return nil
	}
	m := st.m
	plan := postgres.LoadPlan{
		Damaged: m.DamagedSet(), Marker: *marker, Registry: r.d.Registry,
		LockTimeout: r.d.Config.Database.LockTimeout, OnTable: func(string) { r.progress() },
	}
	for _, t := range m.Tables {
		lt := postgres.LoadTable{Name: t.Name, Columns: t.Columns, Rows: t.Rows}
		if reg, ok := r.d.Registry.Lookup(t.Name); ok {
			lt.CycleBreak = reg.CycleBreak
		}
		plan.Tables = append(plan.Tables, lt)
	}
	for _, sq := range m.Sequences {
		plan.Sequences = append(plan.Sequences, postgres.LoadSequence{Name: sq.Name, LastValue: sq.LastValue, IsCalled: sq.IsCalled})
	}
	res, err := r.d.Session.Load(ctx, plan, &tableSource{st: r.d.Target, layout: r.d.Layout, id: st.id, m: m, progress: r.progress, hook: r.hook})
	if err != nil {
		return err
	}
	rep.Tables = res.Rows
	marker.Phase = postgres.PhaseLoaded
	return nil
}

// stepFixups is step 6: the registered fix-ups in one transaction with the phase change to fixed.
func (r *Restorer) stepFixups(ctx context.Context, st *state, marker *postgres.RestoreMarker) error {
	if marker.Phase == postgres.PhaseFixed {
		r.d.Log.Info("fix-ups already committed")
		return nil
	}
	r.hook("step6")
	if err := r.d.Session.EnsureAuditPartitions(ctx); err != nil {
		return err
	}
	env := r.fixupEnv(st)
	err := r.d.Session.Fix(ctx, func(tx meta.Tx) error {
		for _, f := range fixup.Fixups() {
			if err := f.Apply(ctx, tx, env); err != nil {
				return fmt.Errorf("fix-up %s: %w", f.Name, err)
			}
			r.d.Log.Info("fix-up applied", "fixup", f.Name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	marker.Phase = postgres.PhaseFixed
	return nil
}

func (r *Restorer) fixupEnv(st *state) fixup.Env {
	return fixup.Env{
		Manifest: st.m, Now: r.d.Now().UTC(), NewKeyspaceID: randomUUID(), NewIncarnation: randomUUID(),
		ExcludeRemoteCaches: st.m.Options.ExcludeRemoteCaches,
	}
}

// recorder is the audit writer over the restore's session, shared by the replay and the finish.
func (r *Restorer) recorder() (audit.Recorder, error) {
	if r.rec != nil {
		return r.rec, nil
	}
	w, err := audit.New(audit.Options{DB: r.d.Session, Replica: r.d.Pod})
	if err != nil {
		return nil, err
	}
	r.rec = w
	return w, nil
}

// journalEnv is what the journal of the backup's source is built from.
func (r *Restorer) journalEnv(st *state, opts backup.RestoreOptions, rec audit.Recorder) JournalEnv {
	return JournalEnv{
		Runner: r.d.Session, SourcePrefix: st.m.Source.Storage.Prefix, SourceBucket: st.m.Source.Storage.Bucket,
		SourceEndpoint: st.m.Source.Storage.Endpoint, CredentialsFile: opts.JournalCredentialsFile, Recorder: rec,
	}
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// stepCaches is step 7: with the remote caches left out of the backup, every format empties them.
func (r *Restorer) stepCaches(ctx context.Context, st *state) error {
	if !st.m.Options.ExcludeRemoteCaches {
		return nil
	}
	r.hook("step7")
	env := r.fixupEnv(st)
	if purgers := fixup.Purgers(); len(purgers) > 0 {
		if r.d.Items == nil {
			return backup.Failf(backup.ExitFailed, backup.CodeInternal, "the backup left out the remote caches, and this binary cannot empty them: no storage delete path is wired into the restore")
		}
		items, err := r.d.Items(ctx, r.d.Session)
		if err != nil {
			return fmt.Errorf("storage delete path: %w", err)
		}
		env.Items = items
	}
	for _, p := range fixup.Purgers() {
		if err := p.Purge(ctx, r.d.Session, env); err != nil {
			return fmt.Errorf("purging the %s remote caches: %w", p.Name, err)
		}
		r.d.Log.Info("remote caches purged", "format", p.Name)
		r.progress()
	}
	var n int64
	err := r.d.Session.InTx(ctx, func(tx meta.Tx) (err error) {
		n, err = postgres.DeleteUnreferencedBlobs(ctx, tx)
		return err
	})
	if err != nil {
		return err
	}
	r.d.Log.Info("blob rows without a reference deleted", "rows", n)
	return nil
}

// stepJournal is the first half of step 8: the revocation journal replay (R-BAK-024).
func (r *Restorer) stepJournal(ctx context.Context, st *state, opts backup.RestoreOptions, rep *backup.RestoreReport) error {
	r.hook("step8")
	if rep.Journal == "skipped" {
		r.warnSkipped(opts, st)
		return nil
	}
	if st.journal == nil {
		if r.d.Journal == nil {
			return backup.Failf(backup.ExitFailed, backup.CodeJournalUnavailable, "this binary cannot read the revocation journal")
		}
		rec, err := r.recorder()
		if err != nil {
			return err
		}
		j, err := r.d.Journal(ctx, r.journalEnv(st, opts, rec))
		if err != nil {
			return err
		}
		st.journal = j
	}
	sum, err := st.journal.Replay(ctx, journal.ReplayOptions{
		RestoredTo: st.m.SnapshotAt, Margin: r.d.Config.RevocationJournal.ReplayMargin, Out: r.d.Out,
	})
	if err != nil {
		return fmt.Errorf("revocation journal replay: %w", err)
	}
	if sum != nil {
		if rep.Replay, err = json.Marshal(sum); err != nil {
			return err
		}
	}
	return nil
}

// warnSkipped prints the warning of H-37 and the manual procedure (R-BAK-024).
func (r *Restorer) warnSkipped(opts backup.RestoreOptions, st *state) {
	why := "the source did not write a revocation journal"
	if opts.SkipRevocationJournal {
		why = "--skip-revocation-journal was given"
	}
	_, _ = fmt.Fprintf(r.d.Out, `WARNING: the revocation journal was not replayed (%s).
Access removals made after the snapshot (%s) are NOT in effect in this installation: revoked tokens, removed
memberships and tightened policies come back as they were at the snapshot (H-37). Review the audit trail of the
old installation for access changes after the snapshot and repeat them by hand before clients are switched over.
NuGet version numbers published after the snapshot are not reserved: a lost version can be published again with
different bytes (R-NUGET-033, H-50).
`, why, st.m.SnapshotAt.UTC().Format(time.RFC3339))
}

// stepVerify is the second half of step 8: every live blob row has a copied object, the key ring opens the canaries and the
// sealed values of the restored database, and no migration step is pending.
func (r *Restorer) stepVerify(ctx context.Context, st *state) error {
	if err := r.verifyBlobs(ctx, st); err != nil {
		return err
	}
	if err := r.verifyKeys(ctx); err != nil {
		return err
	}
	pending, err := r.d.Session.PendingSteps(ctx)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("trove migrate status shows %d pending steps after the restore (first: %s)", len(pending), pending[0].Name)
	}
	return nil
}

// verifyBlobs merges the live blob rows (digest order) with the blob list without the damaged digests.
func (r *Restorer) verifyBlobs(ctx context.Context, st *state) error {
	rc, _, err := r.d.Target.Get(ctx, r.d.Layout.BlobList(st.id))
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	list, err := format.NewBlobListReader(rc)
	if err != nil {
		return err
	}
	defer list.Close()
	skip := map[string]bool{}
	for d := range st.m.DamagedSet() {
		skip[d] = true
	}
	it := format.Without(list, skip)
	var cur *format.BlobEntry
	done := false
	var missing []string
	var count int64
	err = r.d.Session.LiveBlobs(ctx, func(raw []byte) error {
		r.progress()
		d, err := format.DigestOf(raw)
		if err != nil {
			return err
		}
		for !done && (cur == nil || cur.Digest.Less(d)) {
			e, err := it.Next()
			if errors.Is(err, io.EOF) {
				done = true
				break
			}
			if err != nil {
				return err
			}
			cur = &e
		}
		if !done && cur.Digest == d {
			return nil
		}
		count++
		if len(missing) < 10 {
			missing = append(missing, d.Hex())
		}
		return nil
	})
	if err != nil {
		return err
	}
	if count > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%d live blob rows of the restored database have no copied object (first: %v)", count, missing)
	}
	return nil
}

// verifyKeys is the canary check of R-ARCH-021 against the restored database.
func (r *Restorer) verifyKeys(ctx context.Context) error {
	status, err := r.d.Keys.StatusWithCounts(ctx, listq.SealedCounter{DB: r.d.Session.DBTX()})
	if err != nil {
		return fmt.Errorf("key ring: %w", err)
	}
	var referenced []string
	for _, k := range status.Keys {
		if k.SealedValues != nil && *k.SealedValues > 0 {
			referenced = append(referenced, k.KeyID)
		}
	}
	if err := r.d.Keys.RequireKeys("database", referenced); err != nil {
		return err
	}
	canaries, err := r.d.Session.SecretCanaries(ctx)
	if err != nil {
		return err
	}
	for _, id := range sortedKeys(canaries) {
		if !r.d.Keys.Has(id) {
			continue
		}
		if err := r.d.Keys.CheckCanary(id, canaries[id]); err != nil {
			return fmt.Errorf("key ring does not match database: %w", err)
		}
	}
	return nil
}

// stepFinish is step 9: the finishing transaction.
func (r *Restorer) stepFinish(ctx context.Context, st *state, rep *backup.RestoreReport) error {
	r.hook("step9")
	rec, err := r.recorder()
	if err != nil {
		return err
	}
	details, err := json.Marshal(map[string]any{
		"source": "backup", "backupId": string(st.id), "snapshotAt": st.m.SnapshotAt.UTC().Format(time.RFC3339Nano), "journal": rep.Journal,
	})
	if err != nil {
		return err
	}
	return r.d.Session.Finish(ctx, func(tx meta.Tx) error {
		return rec.RecordTx(ctx, tx, audit.Event{
			Type: audit.EventRestored, Actor: audit.Actor{Kind: "system", Name: "backup"}, Outcome: audit.Success, Details: details,
		})
	})
}

// summary prints the next steps of R-BAK-021 step 9.
func (r *Restorer) summary(st *state) {
	_, _ = fmt.Fprintf(r.d.Out, `restored backup %s (snapshot %s)
next steps:
  - if this installation shall back up into the old target, set backup.adoptTarget: true for its CronJob (R-BAK-002)
  - review the access changes made after the snapshot (H-37) and the versions the journal replay reports as revived (R-BAK-024)
  - run the final helm upgrade with the normal values (restore.enabled off) to start web and worker
`, st.id, st.m.SnapshotAt.UTC().Format(time.RFC3339))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readAll(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, 64<<20))
	return b, err
}
