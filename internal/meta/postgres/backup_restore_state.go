package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The database side of `trove backup restore` that is not the load: the inspection of check 4 and check 6, the anchor, the
// marker restore_in_progress and its phases, the statements of the fix-ups that name a table by a value taken from the
// registry, and the finishing transaction (R-BAK-020, R-BAK-021, R-BAK-022). This file and backup_load.go assemble SQL from
// catalog and registry names, every identifier quoted with pgx.Identifier and no value in the text; the architecture test
// lists both next to blobrefs.go (R-ARCH-019).

// RestoreInspection is what checks 4 and 6 of R-BAK-020 and the resumption read from the database.
type RestoreInspection struct {
	// SchemaExists is false when database.schema does not exist (R-HA-014 creates it).
	SchemaExists bool
	// OwnsSchema is true when the credential is the schema's owner or a member of its owner role.
	OwnsSchema bool
	// Anchor is the backup ID in the schema comment 'trove-restore <id>', empty without an anchor (R-BAK-021 step 2).
	Anchor string
	// Relations are the first names of the relations the schema contains, and RelationCount is how many there are.
	Relations     []string
	RelationCount int
	// Marker is system_state restore_in_progress, nil when absent.
	Marker *RestoreMarker
	// ServerVersion is the server_version string, for the log.
	ServerVersion string
}

// Inspect reads the database state the restore decides on. It writes nothing.
func (s *RestoreSession) Inspect(ctx context.Context) (RestoreInspection, error) {
	var in RestoreInspection
	qs := q.New(s.conn)
	if _, err := qs.SchemaOwnedBy(ctx, s.cfg.Schema); err != nil {
		if isNoRows(err) {
			return in, nil
		}
		return in, err
	}
	in.SchemaExists = true
	info, err := qs.ServerInfo(ctx)
	if err != nil {
		return in, err
	}
	in.ServerVersion = info.Version
	if in.OwnsSchema, err = qs.SchemaOwnerAccessible(ctx, s.cfg.Schema); err != nil {
		return in, err
	}
	comment, err := qs.SchemaComment(ctx, s.cfg.Schema)
	if err != nil {
		return in, err
	}
	if id, ok := strings.CutPrefix(strings.TrimSpace(comment), restoreAnchor); ok {
		in.Anchor = strings.TrimSpace(id)
	}
	rels, err := qs.RestoreSchemaRelations(ctx, s.cfg.Schema)
	if err != nil {
		return in, err
	}
	for _, r := range rels {
		in.Relations = append(in.Relations, r.Name)
		in.RelationCount = int(r.Total)
	}
	in.Marker, err = s.readMarker(ctx)
	return in, err
}

// ServerCheck is the server half of check 6: a supported PostgreSQL major version (R-HA-013), UTF8, and a role that is not a
// superuser unless database.preflight.allow_superuser allows it.
func (s *RestoreSession) ServerCheck(ctx context.Context) error {
	_, err := serverChecks(ctx, s.conn, s.cfg)
	return err
}

// Schema is database.schema.
func (s *RestoreSession) Schema() string { return s.cfg.Schema }

// readMarker reads restore_in_progress; nil when the table or the row is missing.
func (s *RestoreSession) readMarker(ctx context.Context) (*RestoreMarker, error) {
	qs := q.New(s.conn)
	found, err := qs.TableExists(ctx, "system_state")
	if err != nil || !found {
		return nil, err
	}
	row, err := qs.GetSystemState(ctx, "restore_in_progress")
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m RestoreMarker
	if err := json.Unmarshal(row.Value, &m); err != nil {
		return nil, fmt.Errorf("system_state restore_in_progress: %w", err)
	}
	if m.BackupID == "" {
		m.BackupID = backupIDOf(row.Value)
	}
	return &m, nil
}

// Marker reads the restore marker, nil when there is none.
func (s *RestoreSession) Marker(ctx context.Context) (*RestoreMarker, error) {
	return s.readMarker(ctx)
}

// SetAnchor sets COMMENT ON SCHEMA <database.schema> IS 'trove-restore <id>', the anchor that makes the database this
// restore's from its first write on (R-BAK-021 step 2). Setting the same anchor again changes nothing.
func (s *RestoreSession) SetAnchor(ctx context.Context, backupID string) error {
	_, err := s.conn.Exec(ctx, "COMMENT ON SCHEMA "+pgx.Identifier{s.cfg.Schema}.Sanitize()+" IS "+quoteLiteral(restoreAnchor+backupID))
	return err
}

// quoteLiteral quotes a string as an SQL literal. Only fixed texts and validated identifiers go through it.
func quoteLiteral(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

// InsertMarker commits restore_in_progress unless it exists.
func (s *RestoreSession) InsertMarker(ctx context.Context, m RestoreMarker) error {
	return insertMarker(ctx, s.conn, m)
}

func insertMarker(ctx context.Context, db q.DBTX, m RestoreMarker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return q.New(db).InsertRestoreMarker(ctx, string(b))
}

// SetPhase moves the marker from one phase to the next in a transaction of its own; the fix-ups use [RestoreSession.Fix]
// instead, which changes the phase in their transaction.
func (s *RestoreSession) SetPhase(ctx context.Context, from, to string) error {
	n, err := q.New(s.conn).SetRestorePhase(ctx, q.SetRestorePhaseParams{Phase: to, FromPhase: from})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w (%s)", ErrMarkerMissing, from)
	}
	return nil
}

// Fix runs fn, the fix-ups of R-BAK-022, in one transaction that also moves the marker from phase "loaded" to "fixed", so a
// resumed restore never applies them twice (R-BAK-021 step 6). A marker in another phase fails the transaction.
func (s *RestoreSession) Fix(ctx context.Context, fn func(meta.Tx) error) error {
	return s.InTx(ctx, func(tx meta.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		ptx, ok := Unwrap(tx)
		if !ok {
			return errors.New("restore: not a PostgreSQL transaction")
		}
		n, err := q.New(ptx).SetRestorePhase(ctx, q.SetRestorePhaseParams{Phase: PhaseFixed, FromPhase: PhaseLoaded})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w (%s)", ErrMarkerMissing, PhaseLoaded)
		}
		return nil
	})
}

// Finish is the finishing transaction of R-BAK-021 step 9: it deletes restore_in_progress, sets restored_at to the
// transaction's time, removes the anchor and runs fn, which writes system.restored, so none of the four is visible without
// the others. It makes sure the audit partition of the current month exists first, because a backup of an earlier month
// brought none.
func (s *RestoreSession) Finish(ctx context.Context, fn func(meta.Tx) error) error {
	return s.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := Unwrap(tx)
		if !ok {
			return errors.New("restore: not a PostgreSQL transaction")
		}
		qs := q.New(ptx)
		if err := qs.RestoreEnsureAuditPartitions(ctx); err != nil {
			return fmt.Errorf("audit partitions: %w", err)
		}
		if err := fn(tx); err != nil {
			return err
		}
		if _, err := qs.DeleteRestoreMarker(ctx); err != nil {
			return err
		}
		if err := qs.RestoreSetRestoredAt(ctx); err != nil {
			return err
		}
		_, err := ptx.Exec(ctx, "COMMENT ON SCHEMA "+pgx.Identifier{s.cfg.Schema}.Sanitize()+" IS NULL")
		return err
	})
}

// EnsureAuditPartitions creates the monthly audit partitions that cover the next three months from now, so that the audit
// events of the replay and of the fix-ups have a partition after a backup of an earlier month was loaded.
func (s *RestoreSession) EnsureAuditPartitions(ctx context.Context) error {
	return q.New(s.conn).RestoreEnsureAuditPartitions(ctx)
}

// RaiseRevisionColumns adds by to every listed column (R-BAK-022 "Revision columns") and returns the rows it changed. The
// statement per column is assembled from the registry's names with the identifiers quoted.
func RaiseRevisionColumns(ctx context.Context, tx meta.Tx, cols []meta.RevisionColumn, by int64) (int64, error) {
	ptx, ok := Unwrap(tx)
	if !ok {
		return 0, errors.New("postgres: RaiseRevisionColumns needs a transaction of internal/meta/postgres")
	}
	var total int64
	for _, c := range cols {
		col := pgx.Identifier{c.Column}.Sanitize()
		tag, err := ptx.Exec(ctx, "UPDATE "+pgx.Identifier{c.Table}.Sanitize()+" SET "+col+" = "+col+" + $1", by)
		if err != nil {
			return total, fmt.Errorf("revision column %s.%s: %w", c.Table, c.Column, err)
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

// DeleteUnreferencedBlobs deletes the blob rows no column references, read from the catalog the way garbage collection reads
// it (R-STOR-024 rule 3). After a restore that left out the remote caches' blobs and emptied the caches, these are the rows
// that are neither referenced nor in the pool (R-BAK-008): a digest of the pool is referenced by a row that is not a cache
// row. It returns the number deleted.
func DeleteUnreferencedBlobs(ctx context.Context, tx meta.Tx) (int64, error) {
	ptx, ok := Unwrap(tx)
	if !ok {
		return 0, errors.New("postgres: DeleteUnreferencedBlobs needs a transaction of internal/meta/postgres")
	}
	cols, err := q.New(ptx).BlobReferencingColumns(ctx)
	if err != nil {
		return 0, err
	}
	if len(cols) == 0 {
		return 0, errors.New("no column references blobs: refusing to delete every blob row")
	}
	var sb strings.Builder
	sb.WriteString("DELETE FROM blobs b WHERE true")
	for _, c := range cols {
		sb.WriteString(" AND NOT EXISTS (SELECT 1 FROM " + pgx.Identifier{c.SchemaName, c.TableName}.Sanitize() +
			" r WHERE r." + pgx.Identifier{c.ColumnName}.Sanitize() + " = b.sha256)")
	}
	tag, err := ptx.Exec(ctx, sb.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// LiveBlobs calls fn for the digest of every blob row that is live with content_status ok, in digest order (R-BAK-021 step
// 8: each of them must have a copied object). fn runs while the cursor is open; it must not use the session.
func (s *RestoreSession) LiveBlobs(ctx context.Context, fn func(digest []byte) error) error {
	rows, err := s.conn.Query(ctx, "SELECT sha256 FROM blobs WHERE state = 'live' AND content_status = 'ok' ORDER BY sha256")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d []byte
		if err := rows.Scan(&d); err != nil {
			return err
		}
		if err := fn(d); err != nil {
			return err
		}
	}
	return rows.Err()
}

// SecretCanaries returns the canary rows of the restored database: key ID to sealed canary (R-ARCH-021).
func (s *RestoreSession) SecretCanaries(ctx context.Context) (map[string]string, error) {
	rows, err := q.New(s.conn).RestoreSecretCanaries(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.KeyID] = r.Ciphertext
	}
	return out, nil
}
