package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// PGFacts is the small reads of the create pipeline over PostgreSQL at READ COMMITTED: the system facts of R-BAK-009, the
// grant check of R-BAK-015, the blob row of the damage check of R-BAK-006 and the page of pins the copy phase works through.
// It implements [Facts] and [BlobStateReader] besides the pipeline's pin and grant ports.
type PGFacts struct {
	db     DB
	schema string
}

// NewPGFacts builds the reads over the backup role's database; schema is database.schema.
func NewPGFacts(db DB, schema string) *PGFacts { return &PGFacts{db: db, schema: schema} }

var (
	_ Facts           = (*PGFacts)(nil)
	_ BlobStateReader = (*PGFacts)(nil)
)

func (f *PGFacts) read(ctx context.Context, fn func(qs *q.Queries) error) error {
	return f.db.Read(ctx, func(c q.DBTX) error { return fn(q.New(c)) })
}

// stateString reads a system_state value that is a JSON string.
func stateString(ctx context.Context, qs *q.Queries, key string) (string, error) {
	row, err := qs.GetSystemState(ctx, key)
	if err != nil {
		return "", fmt.Errorf("system_state %s: %w", key, err)
	}
	var s string
	if err := json.Unmarshal(row.Value, &s); err != nil {
		return "", fmt.Errorf("system_state %s is not a JSON string: %w", key, err)
	}
	return s, nil
}

// SystemFacts implements Facts.
func (f *PGFacts) SystemFacts(ctx context.Context) (SystemFacts, error) {
	var out SystemFacts
	err := f.read(ctx, func(qs *q.Queries) error {
		out = SystemFacts{}
		var err error
		if out.InstallationID, err = stateString(ctx, qs, "installation_id"); err != nil {
			return err
		}
		if out.Incarnation, err = stateString(ctx, qs, "incarnation"); err != nil {
			return err
		}
		if _, err := qs.GetSystemState(ctx, "restore_in_progress"); err == nil {
			out.RestoreInProgress = true
		} else if !postgres.IsNoRows(err) {
			return err
		}
		switch row, err := qs.GetSystemState(ctx, "restored_from"); {
		case err == nil:
			var rf RestoredFrom
			if err := json.Unmarshal(row.Value, &rf); err != nil {
				return fmt.Errorf("system_state restored_from: %w", err)
			}
			out.RestoredFrom = &rf
		case !postgres.IsNoRows(err):
			return err
		}
		applied, err := qs.BackupSchemaApplied(ctx)
		if err != nil {
			return err
		}
		out.SchemaBehind = int(applied) < postgres.RequiredSchema()
		return nil
	})
	return out, err
}

// BlobState implements BlobStateReader.
func (f *PGFacts) BlobState(ctx context.Context, d format.Digest) (state, contentStatus string, found bool, err error) {
	err = f.read(ctx, func(qs *q.Queries) error {
		rows, err := qs.BackupBlobRow(ctx, d[:])
		if err != nil || len(rows) == 0 {
			return err
		}
		state, contentStatus, found = rows[0].State, rows[0].ContentStatus, true
		return nil
	})
	return state, contentStatus, found, err
}

// Pins returns up to limit pins of a run after a digest, in digest order (R-BAK-006): the copy phase pages through its to-copy
// set so that a first backup of millions of blobs stays in bounded memory.
func (f *PGFacts) Pins(ctx context.Context, runID string, after format.Digest, limit int) ([]format.BlobEntry, error) {
	var out []format.BlobEntry
	err := f.read(ctx, func(qs *q.Queries) error {
		rows, err := qs.ListBackupPins(ctx, q.ListBackupPinsParams{RunID: runID, After: after[:], RowLimit: int32(max(limit, 1))}) //nolint:gosec // a page size, bounded by the caller
		if err != nil {
			return err
		}
		out = make([]format.BlobEntry, 0, len(rows))
		for _, r := range rows {
			if len(r.Sha256) != len(after) {
				return fmt.Errorf("a pin has a digest of %d bytes", len(r.Sha256))
			}
			e := format.BlobEntry{Size: r.Size}
			copy(e.Digest[:], r.Sha256)
			out = append(out, e)
		}
		return nil
	})
	return out, err
}

// requiredWrites are the privileges the run needs besides SELECT (R-BAK-015): its own record, and the audit trail.
func requiredWrites() []string {
	return []string{
		"backup_run:INSERT", "backup_run:UPDATE", "backup_run:DELETE",
		"backup_blob_pin:INSERT", "backup_blob_pin:UPDATE", "backup_blob_pin:DELETE",
		"audit_event:INSERT",
	}
}

// CheckGrants is the check of R-BAK-015 at the start of a create: SELECT on every table and sequence of the schema, the writes
// of the run record, INSERT on the audit trail. The error names every privilege that is missing and is a usage error (exit 2).
func (f *PGFacts) CheckGrants(ctx context.Context) error {
	var missing []string
	err := f.read(ctx, func(qs *q.Queries) error {
		missing = missing[:0]
		tbls, err := qs.BackupTablesWithoutSelect(ctx, f.schema)
		if err != nil {
			return err
		}
		for _, t := range tbls {
			missing = append(missing, "SELECT on "+t)
		}
		seqs, err := qs.BackupSequencesWithoutSelect(ctx, f.schema)
		if err != nil {
			return err
		}
		for _, s := range seqs {
			missing = append(missing, "SELECT on sequence "+s)
		}
		writes, err := qs.BackupMissingPrivileges(ctx, requiredWrites())
		if err != nil {
			return err
		}
		for _, w := range writes {
			tbl, priv, _ := strings.Cut(w, ":")
			missing = append(missing, priv+" on "+tbl)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return Failf(ExitUsage, CodeBackupRoleGrant,
			"the database role of the backup lacks privileges (%s); trove migrate grants them to database.backup_role (R-BAK-015)", strings.Join(missing, ", "))
	}
	return nil
}
