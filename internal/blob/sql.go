package blob

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/meta"
)

// The statements of the blob store. They are plain strings run through
// meta.Tx because internal/blob may not import pgx, which a sqlc query
// (q.New(pgx.Tx)) would need. Every statement uses unqualified names: the
// connection's search_path is the trove schema.
const (
	// sqlLockShare is the first statement of the commit protocol (R-STOR-005).
	sqlLockShare = `SELECT state, content_status FROM blobs WHERE sha256 = $1 FOR SHARE`

	sqlInsertBlob = `INSERT INTO blobs (sha256, sha1, md5, sha512, size, state)
		VALUES ($1, $2, $3, $4, $5, 'live') ON CONFLICT DO NOTHING`

	sqlBlobExists = `SELECT 1 FROM blobs WHERE sha256 = $1`

	sqlMarkOK = `UPDATE blobs SET content_status = 'ok' WHERE sha256 = $1 AND state = 'live'`

	sqlEnqueue = `INSERT INTO blob_gc_queue (sha256, enqueued_at) VALUES ($1, now())
		ON CONFLICT (sha256) DO UPDATE SET enqueued_at = EXCLUDED.enqueued_at`

	sqlRow = `SELECT sha256, sha1, md5, sha512, size, state, content_status, created_at FROM blobs WHERE sha256 = $1`

	sqlMarkDamaged = `UPDATE blobs SET content_status = $2 WHERE sha256 = $1 AND state = 'live' AND content_status = 'ok'`
)

// sqlStateAS001 is raised by the blob reference guard when a reference points
// at a blob that is not live (migration 00002).
const sqlStateAS001 = "AS001"

// sqlState returns the SQLSTATE of a database error, empty for other errors.
func sqlState(err error) string {
	var s interface{ SQLState() string }
	if errors.As(err, &s) {
		return s.SQLState()
	}
	return ""
}

// queryOne runs a query and scans its first row. It reports found=false for an
// empty result, so that no package-level driver error is needed.
func queryOne(ctx context.Context, tx meta.Tx, sql string, args []any, dest ...any) (found bool, err error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	if err := rows.Scan(dest...); err != nil {
		return false, err
	}
	rows.Close()
	return true, rows.Err()
}

// DB is what the store needs of the database layer: a function run in one
// READ COMMITTED transaction on the main pool. *postgres.DB implements it.
type DB interface {
	Tx(ctx context.Context, fn func(meta.Tx) error) error
}
