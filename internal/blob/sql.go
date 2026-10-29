package blob

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
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

// DB is what the store needs of the database layer: a function run in one
// READ COMMITTED transaction on the main pool. *postgres.DB implements it.
type DB interface {
	InTx(ctx context.Context, fn func(meta.Tx) error) error
}

var errForeignTx = errors.New("blob: the transaction is not one of internal/meta/postgres")

// queries returns the sqlc queries on the transaction behind tx. The store does
// not write SQL: its statements live in internal/meta/postgres/queries/blob.sql.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// blobRows is the store's access to the blobs table for the commit protocol and the
// download path. pgRows runs the sqlc queries; the unit tests of the protocol's
// branches substitute a simulation (the integration tier runs the same code
// against PostgreSQL). The collector has no such seam: it is only tested
// against the database.
type blobRows interface {
	// LockShare is the first statement of the commit protocol: FOR SHARE.
	LockShare(ctx context.Context, tx meta.Tx, d Digest) (state, status string, found bool, err error)
	// InsertLive inserts the live row; inserted is false when a concurrent
	// uploader's row won.
	InsertLive(ctx context.Context, tx meta.Tx, sp *Spooled) (inserted bool, err error)
	// Exists reports whether a row exists, in any state.
	Exists(ctx context.Context, tx meta.Tx, d Digest) (bool, error)
	// MarkOK sets content_status ok on a live row.
	MarkOK(ctx context.Context, tx meta.Tx, d Digest) error
	// Enqueue puts the digest in blob_gc_queue.
	Enqueue(ctx context.Context, tx meta.Tx, d Digest) error
	// Row reads the row of a digest.
	Row(ctx context.Context, tx meta.Tx, d Digest) (Row, bool, error)
	// MarkDamaged moves a live, ok row to status and reports whether it changed.
	MarkDamaged(ctx context.Context, tx meta.Tx, d Digest, status string) (changed bool, err error)
}

type pgRows struct{}

var _ blobRows = pgRows{}

func (pgRows) LockShare(ctx context.Context, tx meta.Tx, d Digest) (state, status string, found bool, err error) {
	qs, err := queries(tx)
	if err != nil {
		return "", "", false, err
	}
	rows, err := qs.LockBlobShare(ctx, d.Bytes())
	if err != nil || len(rows) == 0 {
		return "", "", false, err
	}
	return rows[0].State, rows[0].ContentStatus, true, nil
}

func (pgRows) InsertLive(ctx context.Context, tx meta.Tx, sp *Spooled) (bool, error) {
	qs, err := queries(tx)
	if err != nil {
		return false, err
	}
	n, err := qs.InsertLiveBlob(ctx, q.InsertLiveBlobParams{
		Sha256: sp.SHA256.Bytes(), Sha1: sp.SHA1[:], Md5: sp.MD5[:], Sha512: sp.SHA512[:], Size: sp.Size,
	})
	return n > 0, err
}

func (pgRows) Exists(ctx context.Context, tx meta.Tx, d Digest) (bool, error) {
	qs, err := queries(tx)
	if err != nil {
		return false, err
	}
	rows, err := qs.BlobExists(ctx, d.Bytes())
	return len(rows) > 0, err
}

func (pgRows) MarkOK(ctx context.Context, tx meta.Tx, d Digest) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	return qs.MarkBlobOK(ctx, d.Bytes())
}

func (pgRows) Enqueue(ctx context.Context, tx meta.Tx, d Digest) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	return qs.EnqueueBlobForGC(ctx, d.Bytes())
}

func (pgRows) Row(ctx context.Context, tx meta.Tx, d Digest) (Row, bool, error) {
	qs, err := queries(tx)
	if err != nil {
		return Row{}, false, err
	}
	rows, err := qs.GetBlob(ctx, d.Bytes())
	if err != nil || len(rows) == 0 {
		return Row{}, false, err
	}
	g := rows[0]
	r := Row{State: g.State, ContentStatus: g.ContentStatus, CreatedAt: g.CreatedAt}
	copy(r.SHA256[:], g.Sha256)
	copy(r.SHA1[:], g.Sha1)
	copy(r.MD5[:], g.Md5)
	copy(r.SHA512[:], g.Sha512)
	r.Size = g.Size
	return r, true, nil
}

func (pgRows) MarkDamaged(ctx context.Context, tx meta.Tx, d Digest, status string) (bool, error) {
	qs, err := queries(tx)
	if err != nil {
		return false, err
	}
	n, err := qs.MarkBlobDamaged(ctx, q.MarkBlobDamagedParams{Sha256: d.Bytes(), ContentStatus: status})
	return n > 0, err
}
