package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/listq"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/secrets"
)

// PGSnapshotter is the Snapshotter over PostgreSQL (R-BAK-005). The pgx half is internal/meta/postgres/backup_snapshot.go; this
// adapter gives it the shapes of the ports and the exit codes of R-BAK-013, which a package of the infrastructure layer cannot
// import.
type PGSnapshotter struct{ db *postgres.DB }

// NewPGSnapshotter builds the Snapshotter over the backup role's database.
func NewPGSnapshotter(db *postgres.DB) *PGSnapshotter { return &PGSnapshotter{db: db} }

var _ Snapshotter = (*PGSnapshotter)(nil)

// mapSnapshotError turns the errors of the pgx half into the exit codes of R-BAK-013: a standby is a usage error (2), a
// migration wait, a table-lock wait and a table list that keeps changing are temporary (75), a referenced row in state
// deleting is an invariant violation (1). Anything else passes through.
func mapSnapshotError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, postgres.ErrBackupStandby):
		return Fail(ExitUsage, CodeStandby, err)
	case errors.Is(err, postgres.ErrBackupMigrationWait):
		return Fail(ExitTemporary, CodeMigrationWait, err)
	case errors.Is(err, postgres.ErrBackupTableLock):
		return Fail(ExitTemporary, CodeTableLockTimeout, err)
	case errors.Is(err, postgres.ErrBackupCatalogChanged):
		return Fail(ExitTemporary, CodeCatalogChanged, err)
	case errors.Is(err, postgres.ErrBackupInvariant):
		return Fail(ExitFailed, CodeInvariantViolation, errors.Join(ErrInvariantViolation, err))
	case postgres.IsUnavailable(err):
		return Fail(ExitTemporary, CodeTemporary, err)
	}
	return err
}

// Open implements Snapshotter.
func (s *PGSnapshotter) Open(ctx context.Context, opts SnapshotOptions, onLocked func(context.Context) error) (Snapshot, error) {
	snap, err := s.db.OpenBackupSnapshot(ctx, postgres.BackupSnapshotParams{
		Registry: opts.Registry, ExcludeRemoteCaches: opts.ExcludeRemoteCaches, Parallel: opts.Parallel,
		LockTimeout: opts.LockTimeout, MigrationWait: opts.MigrationWait, KeyIDs: sealedKeyIDs,
	}, onLocked)
	if err != nil {
		return nil, mapSnapshotError(err)
	}
	pi := snap.Info()
	info := SnapshotInfo{At: pi.At, ServerVersion: pi.ServerVersion, KeyIDs: pi.KeyIDs}
	for _, st := range pi.Steps {
		info.Schema.Steps = append(info.Schema.Steps, format.SchemaStep{Step: st.Step, Name: st.Name, SHA256: st.SHA256})
		info.Schema.Step = max(info.Schema.Step, st.Step)
	}
	for _, t := range pi.Tables {
		info.Tables = append(info.Tables, TableInfo{Name: t.Name, Class: t.Class, Columns: t.Columns, Registered: t.Registered, EstimatedBytes: t.EstimatedBytes})
	}
	return &pgSnapshot{snap: snap, info: info}, nil
}

type pgSnapshot struct {
	snap *postgres.BackupSnapshot
	info SnapshotInfo
}

func (p *pgSnapshot) Info() SnapshotInfo { return p.info }

// blobStream adapts the digest stream to format.BlobIterator.
type blobStream struct{ s *postgres.BlobStream }

func (b blobStream) Next() (format.BlobEntry, error) {
	d, size, err := b.s.Next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return format.BlobEntry{}, io.EOF
		}
		return format.BlobEntry{}, mapSnapshotError(err)
	}
	return format.BlobEntry{Digest: d, Size: size}, nil
}

func (p *pgSnapshot) Referenced(ctx context.Context) (format.BlobIterator, error) {
	s, err := p.snap.Referenced(ctx)
	if err != nil {
		return nil, mapSnapshotError(err)
	}
	return blobStream{s: s}, nil
}

func (p *pgSnapshot) ExportTable(ctx context.Context, t TableInfo, w io.Writer) (int64, error) {
	n, err := p.snap.ExportTable(ctx, postgres.SnapshotTable{
		Name: t.Name, Class: t.Class, Columns: t.Columns, Registered: t.Registered, EstimatedBytes: t.EstimatedBytes,
	}, w)
	return n, mapSnapshotError(err)
}

func (p *pgSnapshot) Sequences(ctx context.Context) ([]format.Sequence, error) {
	seqs, err := p.snap.Sequences(ctx)
	if err != nil {
		return nil, mapSnapshotError(err)
	}
	out := make([]format.Sequence, len(seqs))
	for i, s := range seqs {
		out[i] = format.Sequence{Name: s.Name, LastValue: s.LastValue, IsCalled: s.IsCalled}
	}
	return out, nil
}

func (p *pgSnapshot) ReleaseMigrationLock(ctx context.Context) error {
	return mapSnapshotError(p.snap.ReleaseMigrationLock(ctx))
}

func (p *pgSnapshot) Close(ctx context.Context) error { return p.snap.Close(ctx) }

// sealedKeyIDs is the set of key IDs referenced by the sealed values and the canary rows (R-BAK-003), found through the
// sealed-column registry of R-ARCH-021.
func sealedKeyIDs(ctx context.Context, db q.DBTX) ([]string, error) {
	counter := listq.SealedCounter{DB: db}
	set := map[string]struct{}{}
	for _, c := range append(secrets.Columns(), secrets.CanaryColumn()) {
		per, err := counter.CountSealed(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("key IDs of %s: %w", c, err)
		}
		for id := range per {
			if id != "" {
				set[id] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}
