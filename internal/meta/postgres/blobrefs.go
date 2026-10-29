package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// BlobRefs is the set of columns that reference blobs, read from the catalog.
// The collector asks it whether a blob is referenced (R-STOR-024 step 2) and for
// the candidates of a full sweep (rule 3).
//
// Its two statements are built from the names in the catalog, which makes this
// file the one place of the module where SQL text is assembled: every
// identifier is quoted with pgx.Identifier, and no value is ever put into the
// text. The architecture test lists this file as the exception.
type BlobRefs struct {
	cols       []pgx.Identifier // {schema, table, column}
	referenced string           // SELECT EXISTS (...) OR EXISTS (...), $1 the digest; empty without columns
	sweep      string           // the full-sweep anti-join; $1 grace seconds, $2 after, $3 limit
}

// LoadBlobRefs reads the referencing columns in the caller's transaction. A
// collector reads them once per batch, not once per candidate.
func LoadBlobRefs(ctx context.Context, tx meta.Tx) (*BlobRefs, error) {
	ptx, ok := Unwrap(tx)
	if !ok {
		return nil, errors.New("postgres: LoadBlobRefs needs a transaction of internal/meta/postgres")
	}
	rows, err := q.New(ptx).BlobReferencingColumns(ctx)
	if err != nil {
		return nil, err
	}
	cols := make([]pgx.Identifier, len(rows))
	for i, r := range rows {
		cols[i] = pgx.Identifier{r.SchemaName, r.TableName, r.ColumnName}
	}
	return newBlobRefs(cols), nil
}

// newBlobRefs builds the statements for the columns.
func newBlobRefs(cols []pgx.Identifier) *BlobRefs {
	r := &BlobRefs{cols: cols}
	var ref strings.Builder
	var sweep strings.Builder
	sweep.WriteString(`SELECT b.sha256, q.enqueued_at FROM blobs b
LEFT JOIN blob_gc_queue q ON q.sha256 = b.sha256
WHERE b.state = 'live'
  AND b.created_at < now() - make_interval(secs => $1::float8)
  AND b.sha256 > $2
  AND (q.enqueued_at IS NULL OR q.enqueued_at < now() - make_interval(secs => $1::float8))`)
	for i, c := range cols {
		table := c[:2].Sanitize()
		column := c[2:].Sanitize()
		if i == 0 {
			ref.WriteString("SELECT ")
		} else {
			ref.WriteString(" OR ")
		}
		fmt.Fprintf(&ref, "EXISTS (SELECT 1 FROM %s WHERE %s = $1)", table, column)
		fmt.Fprintf(&sweep, "\n  AND NOT EXISTS (SELECT 1 FROM %s r WHERE r.%s = b.sha256)", table, column)
	}
	sweep.WriteString("\nORDER BY b.sha256 LIMIT $3")
	r.referenced, r.sweep = ref.String(), sweep.String()
	return r
}

// Len is the number of referencing columns.
func (r *BlobRefs) Len() int { return len(r.cols) }

// Referenced reports whether any referencing column holds the digest. It is a
// statement of its own, issued after the caller took the row lock, so that under
// READ COMMITTED it sees every reference committed before the lock was granted.
func (r *BlobRefs) Referenced(ctx context.Context, tx meta.Tx, sha256 []byte) (bool, error) {
	if r.referenced == "" {
		return false, nil
	}
	ptx, ok := Unwrap(tx)
	if !ok {
		return false, errors.New("postgres: BlobRefs needs a transaction of internal/meta/postgres")
	}
	var found bool
	if err := ptx.QueryRow(ctx, r.referenced, sha256).Scan(&found); err != nil {
		return false, err
	}
	return found, nil
}

// SweepCandidate is a live blob older than the grace period that no table
// references; EnqueuedAt is set when it is also in blob_gc_queue.
type SweepCandidate struct {
	SHA256     []byte
	EnqueuedAt *time.Time
}

// FullSweep returns up to limit candidates of the full sweep after a digest, in
// digest order.
func (r *BlobRefs) FullSweep(ctx context.Context, tx meta.Tx, graceSecs float64, after []byte, limit int32) ([]SweepCandidate, error) {
	ptx, ok := Unwrap(tx)
	if !ok {
		return nil, errors.New("postgres: BlobRefs needs a transaction of internal/meta/postgres")
	}
	rows, err := ptx.Query(ctx, r.sweep, graceSecs, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SweepCandidate
	for rows.Next() {
		var c SweepCandidate
		if err := rows.Scan(&c.SHA256, &c.EnqueuedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
