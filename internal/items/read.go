package items

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

var errForeignTx = errors.New("items: the transaction is not one of internal/meta/postgres")

// queries returns the sqlc queries on the transaction behind tx. The package
// writes no SQL: its statements live in internal/meta/postgres/queries/files.sql.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errForeignTx
	}
	return q.New(ptx), nil
}

// read runs fn on a transaction of the runner. Reads of the management API and
// of the transfer ask PostgreSQL every time (R-API-052).
func (s *Store) read(ctx context.Context, fn func(*q.Queries) error) error {
	return s.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		return fn(qs)
	})
}

// Live reads the live item at a path from PostgreSQL alone (R-STOR-012: HEAD and
// conditional requests never reach the object store); found is false when there
// is none. A file whose object is missing or damaged is returned as it is; see
// [ContentError].
func (s *Store) Live(ctx context.Context, repo RepoRef, path string) (it Item, found bool, err error) {
	err = s.read(ctx, func(qs *q.Queries) error {
		rows, err := qs.GetLiveItem(ctx, q.GetLiveItemParams{RepoID: repo.ID, Path: path})
		if err != nil || len(rows) == 0 {
			return err
		}
		it, found = itemFrom(repo.Key, rows[0]), true
		return nil
	})
	return it, found, err
}

// ContentError refuses a file whose blob is not ok: 503 with Retry-After for a
// missing object, which can come back through a restore, and 500 for a corrupt
// one (R-STOR-012). It is nil for a healthy file and for a folder.
func ContentError(it Item) error {
	switch it.ContentStatus {
	case "", "ok":
		return nil
	case "missing":
		return errs.E(errs.Unavailable, errs.CodeContentUnavailable, "The stored object of this file is missing; retry later.", errs.WithRetryAfter(30*time.Second))
	}
	return errs.E(errs.Internal, errs.CodeContentUnavailable, "The stored object of this file failed verification.")
}

// Range is a byte range of a download; Length < 0 reads to the end.
type Range struct{ Offset, Length int64 }

// Open opens the content of a file for streaming: the whole file through the
// verifying reader of R-STOR-013, or the byte range as stored (ranged reads
// cannot be verified, H-11). An object that is missing is 503 content_unavailable.
func (s *Store) Open(ctx context.Context, it Item, rng *Range) (io.ReadCloser, error) {
	var o blob.OpenOptions
	if rng != nil {
		o.Range = &blob.ByteRange{Offset: rng.Offset, Length: rng.Length}
	}
	body, err := s.blobs.Open(ctx, it.Digest, it.Size, o)
	switch {
	case err == nil:
		return body, nil
	case errors.Is(err, blob.ErrNotFound):
		return nil, errs.E(errs.Unavailable, errs.CodeContentUnavailable, "The stored object of this file is missing; retry later.",
			errs.WithRetryAfter(30*time.Second), errs.WithCause(err))
	case errors.Is(err, blob.ErrCorrupt):
		return nil, errs.E(errs.Internal, errs.CodeContentUnavailable, "The stored object of this file failed verification.", errs.WithCause(err))
	}
	return nil, err
}

// DownloadStats is the download statistics of a file (R-STOR-015).
type DownloadStats struct {
	Downloads      int64
	LastDownloadAt *time.Time
	LastDownloader string
}

// DownloadStats reads the counts of PostgreSQL, never the buffer of a replica
// (R-ARCH-032).
func (s *Store) DownloadStats(ctx context.Context, itemID int64) (DownloadStats, error) {
	var out DownloadStats
	err := s.read(ctx, func(qs *q.Queries) error {
		rows, err := qs.GetItemStats(ctx, itemID)
		if err != nil || len(rows) == 0 {
			return err
		}
		out.Downloads, out.LastDownloader = rows[0].Downloads, rows[0].LastDownloaderName
		if rows[0].LastDownloadAt.Valid {
			t := rows[0].LastDownloadAt.Time.UTC()
			out.LastDownloadAt = &t
		}
		return nil
	})
	return out, err
}
