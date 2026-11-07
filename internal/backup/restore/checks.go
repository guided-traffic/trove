package restore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// state is what the checks learn and the later steps use.
type state struct {
	id       format.ID
	m        *format.Manifest
	inspect  postgres.RestoreInspection
	anchored bool
	journal  Journal
}

// checkFailure is a failed check of R-BAK-020: exit 6, naming the check.
func checkFailure(n int, what, code string, err error) error {
	return backup.Failf(backup.ExitPrecondition, code, "restore check %d (%s) failed: %w", n, what, err)
}

// loadManifest is the first half of check 1: the manifest exists and has a format this binary reads. A higher format is an
// *format.UnknownFormatError, which maps to exit 6 on its own and names the format.
func (r *Restorer) loadManifest(ctx context.Context, id format.ID) (*format.Manifest, error) {
	rc, _, err := r.d.Target.Get(ctx, r.d.Layout.Manifest(id))
	if err != nil {
		if errors.Is(err, target.ErrNotFound) {
			return nil, checkFailure(1, "manifest", backup.CodePrecondition, fmt.Errorf("backup %s has no manifest.json in the target: it does not exist or is incomplete", id))
		}
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	b, err := readAll(rc)
	if err != nil {
		return nil, err
	}
	m, err := format.DecodeManifest(b)
	if err != nil {
		var uf *format.UnknownFormatError
		if errors.As(err, &uf) {
			return nil, err
		}
		return nil, checkFailure(1, "manifest", backup.CodePrecondition, err)
	}
	if m.BackupID != id {
		return nil, checkFailure(1, "manifest", backup.CodePrecondition, fmt.Errorf("the manifest under %s names backup %s", id, m.BackupID))
	}
	if m.Encryption.ClientSide != nil {
		return nil, backup.Failf(backup.ExitUsage, backup.CodeConfig, "backup %s is client-side encrypted (%s); restoring one is not built yet (R-BAK-027)", id, *m.Encryption.ClientSide)
	}
	return m, nil
}

// runChecks runs checks 1 to 8 of R-BAK-020 in order and stops at the first that fails. It writes nothing but the probe
// object of check 7.
func (r *Restorer) runChecks(ctx context.Context, st *state, opts backup.RestoreOptions) error {
	if err := r.checkFiles(ctx, st); err != nil {
		return err
	}
	if err := r.checkSteps(st); err != nil {
		return err
	}
	if err := r.checkKeys(ctx, st); err != nil {
		return err
	}
	if err := r.checkDatabase(ctx, st); err != nil {
		return err
	}
	if err := r.checkBlobPrefix(ctx, st); err != nil {
		return err
	}
	if err := r.checkServer(ctx, st); err != nil {
		return err
	}
	if err := r.checkReachable(ctx); err != nil {
		return err
	}
	return r.checkJournal(ctx, st, opts)
}

// checkFiles is check 1: every file the manifest names exists with the recorded size.
func (r *Restorer) checkFiles(ctx context.Context, st *state) error {
	dir := r.d.Layout.Dir(st.id)
	for _, t := range st.m.Tables {
		o, err := r.d.Target.Head(ctx, dir+t.File)
		switch {
		case errors.Is(err, target.ErrNotFound):
			return checkFailure(1, "files", backup.CodePrecondition, fmt.Errorf("%s of the backup is missing from the target", t.File))
		case err != nil:
			return err
		case o.Size != t.CompressedBytes:
			return checkFailure(1, "files", backup.CodePrecondition, fmt.Errorf("%s is %d bytes in the target, the manifest says %d", t.File, o.Size, t.CompressedBytes))
		}
	}
	if _, err := r.d.Target.Head(ctx, dir+st.m.Blobs.File); err != nil {
		if errors.Is(err, target.ErrNotFound) {
			return checkFailure(1, "files", backup.CodePrecondition, fmt.Errorf("%s of the backup is missing from the target", st.m.Blobs.File))
		}
		return err
	}
	return nil
}

// checkSteps is check 2: the binary embeds every migration step of the backup with the same SHA-256 (R-BAK-023).
func (r *Restorer) checkSteps(st *state) error {
	have, err := r.d.Session.Steps()
	if err != nil {
		return err
	}
	byVersion := map[int]postgres.RestoreStep{}
	for _, s := range have {
		byVersion[s.Version] = s
	}
	release := st.m.Trove.Version
	for _, want := range st.m.Schema.Steps {
		got, ok := byVersion[want.Step]
		switch {
		case !ok:
			return checkFailure(2, "schema steps", backup.CodeSchemaStepMismatch, fmt.Errorf(
				"the backup needs migration step %d (%s), which this binary does not embed (it embeds %d steps); restore it with trove %s or a release that embeds the same steps",
				want.Step, want.Name, len(have), release))
		case got.Name != want.Name || got.SHA256 != want.SHA256:
			return checkFailure(2, "schema steps", backup.CodeSchemaStepMismatch, fmt.Errorf(
				"migration step %d (%s) differs from the backup's (%s, SHA-256 %s against %s); restore it with trove %s or a release that embeds the same steps",
				want.Step, got.Name, want.Name, short(got.SHA256), short(want.SHA256), release))
		}
	}
	if st.m.Schema.Step > len(have) {
		return checkFailure(2, "schema steps", backup.CodeSchemaStepMismatch, fmt.Errorf(
			"the backup is at schema step %d, this binary embeds %d; restore it with trove %s or newer", st.m.Schema.Step, len(have), release))
	}
	return nil
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// checkKeys is check 3: the key ring holds every key ID of the backup and opens every canary row of the backup.
func (r *Restorer) checkKeys(ctx context.Context, st *state) error {
	if err := r.d.Keys.RequireKeys("backup", st.m.Secrets.KeyIDs); err != nil {
		return checkFailure(3, "key ring", backup.CodeKeyRingMismatch, err)
	}
	canaries, err := readCanaries(ctx, r.d.Target, r.d.Layout, st.id, st.m)
	if err != nil {
		return err
	}
	for _, id := range sortedKeys(canaries) {
		if err := r.d.Keys.RequireKeys("backup", []string{id}); err != nil {
			return checkFailure(3, "key ring", backup.CodeKeyRingMismatch, err)
		}
		if err := r.d.Keys.CheckCanary(id, canaries[id]); err != nil {
			return checkFailure(3, "key ring", backup.CodeKeyRingMismatch, fmt.Errorf("key ring does not match backup: %w", err))
		}
	}
	return nil
}

// checkDatabase is check 4: the schema exists and holds no relation, or carries the anchor of this very restore.
func (r *Restorer) checkDatabase(ctx context.Context, st *state) error {
	in, err := r.d.Session.Inspect(ctx)
	if err != nil {
		return err
	}
	st.inspect = in
	schema := r.d.Session.Schema()
	switch {
	case !in.SchemaExists:
		return checkFailure(4, "database", backup.CodeDatabaseNotEmpty, fmt.Errorf("schema %q does not exist: create it as the migration role before the restore (R-HA-014)", schema))
	case in.Anchor != "" && in.Anchor != string(st.id):
		return checkFailure(4, "database", backup.CodeDatabaseNotEmpty, fmt.Errorf("schema %q is anchored by the restore of backup %s, not of backup %s: an interrupted restore of another backup is not overtaken; drop and recreate the schema to start over", schema, in.Anchor, st.id))
	case in.Anchor != "":
		st.anchored = true
	case in.RelationCount > 0:
		return checkFailure(4, "database", backup.CodeDatabaseNotEmpty, fmt.Errorf("schema %q is not empty: it holds %d relations (%s)", schema, in.RelationCount, strings.Join(in.Relations, ", ")))
	}
	return nil
}

// checkBlobPrefix is check 5: the blob prefix holds nothing but probe objects, and after an anchor the blobs of this restore.
func (r *Restorer) checkBlobPrefix(ctx context.Context, st *state) error {
	key, err := r.d.Blobs.FirstForeign(ctx, st.anchored)
	if err != nil {
		return err
	}
	if key != "" {
		return checkFailure(5, "blob prefix", backup.CodeBlobPrefixNotEmpty, fmt.Errorf("the blob prefix is not empty: found %q", key))
	}
	return nil
}

// checkServer is check 6: a supported PostgreSQL version and the owner credential.
func (r *Restorer) checkServer(ctx context.Context, st *state) error {
	if err := r.d.Session.ServerCheck(ctx); err != nil {
		return checkFailure(6, "database server", backup.CodePrecondition, err)
	}
	if !st.inspect.OwnsSchema {
		return checkFailure(6, "database server", backup.CodeOwnerPrivilege, fmt.Errorf("the database credential does not own schema %q: a restore creates tables and truncates them, run it with the owner credential (the migration role)", r.d.Session.Schema()))
	}
	return nil
}

// checkReachable is check 7: the target answers a HeadObject and the blob store takes and deletes a probe object. A restore
// writes nothing to the target, so the target gets no probe object.
func (r *Restorer) checkReachable(ctx context.Context) error {
	if _, err := r.d.Target.Head(ctx, r.d.Layout.Marker()); err != nil && !errors.Is(err, target.ErrNotFound) {
		return checkFailure(7, "reachability", backup.CodePrecondition, fmt.Errorf("the backup target is not reachable: %w", err))
	}
	if err := r.d.Blobs.Probe(ctx, r.d.Pod); err != nil {
		return checkFailure(7, "reachability", backup.CodePrecondition, fmt.Errorf("the blob store does not take a probe object: %w", err))
	}
	return nil
}

// checkJournal is check 8: the source's revocation journal can be listed, unless the restore skips it.
func (r *Restorer) checkJournal(ctx context.Context, st *state, opts backup.RestoreOptions) error {
	if !st.m.Source.RevocationJournal || opts.SkipRevocationJournal {
		return nil
	}
	if r.d.Journal == nil {
		return checkFailure(8, "revocation journal", backup.CodeJournalUnavailable, errors.New("this binary cannot read the revocation journal"))
	}
	rec, err := r.recorder()
	if err != nil {
		return err
	}
	j, err := r.d.Journal(ctx, r.journalEnv(st, opts, rec))
	if err == nil {
		err = j.Reachable(ctx)
	}
	if err != nil {
		return checkFailure(8, "revocation journal", backup.CodeJournalUnavailable, fmt.Errorf(
			"the revocation journal of the source (%s/%s/_journal/revocations/) cannot be listed: %w; give --journal-credentials-file, or --skip-revocation-journal if the source bucket is gone",
			st.m.Source.Storage.Bucket, st.m.Source.Storage.Prefix, err))
	}
	st.journal = j
	return nil
}
