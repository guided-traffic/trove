// Package shared is the UI's shared asset store on PostgreSQL and the blob store (R-HA-020, Q-HA-120 to Q-HA-123): every
// replica that serves the UI publishes the hashed files of its embedded build, and a replica asked for a hashed file its own
// build lacks reads it here. During a rolling upgrade that is how a page whose index.html came from a replica of one
// release loads its scripts from replicas of the other.
//
// The content is a blob like any other (INV-4): spooled, hashed and committed through the commit protocol of R-STOR-005,
// read back with the verification of R-STOR-013. A row of ui_assets names it by the locale directory and the path below
// it, holds the reference that keeps the collector away (R-STOR-024), and records when a replica whose build has the
// file last said so. Nothing here is reachable from a request but the read; the writes are the replica's own start-up and
// its hourly renewal (Keeper).
package shared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/webui"
)

const (
	// Retention is how long a file stays in the store after the last replica whose build has it renewed it (Q-HA-122): a
	// browser tab opened before an upgrade loads the lazy chunks of its release from the store for that long.
	Retention = 7 * 24 * time.Hour
	// RenewEvery is how often a running replica renews its files and publishes what is missing (Keeper), for instance
	// after a restore, which leaves the table empty.
	RenewEvery = time.Hour
	// MaxFileSize bounds a file of the store: a request holds it in memory. The production build's largest file is about
	// 200 KiB; a larger one is served by its own replicas only.
	MaxFileSize = 8 << 20
	// commitBatch is the number of files one commit transaction links.
	commitBatch = 32
)

// DB is the database the store reads and writes: the request pool with the retry of internal/meta/postgres.
type DB interface {
	Read(ctx context.Context, fn func(q.DBTX) error) error
	InTx(ctx context.Context, fn func(meta.Tx) error) error
}

// Blobs is the blob store the content lives in.
type Blobs interface {
	Open(ctx context.Context, d blob.Digest, size int64, o blob.OpenOptions) (io.ReadCloser, error)
	Ingest(ctx context.Context, body io.Reader, o blob.IngestOptions) (*blob.Spooled, error)
	CommitAll(ctx context.Context, sps []*blob.Spooled, link blob.LinkFunc) ([]blob.CommitResult, error)
}

// Store is the shared asset store of one replica. It implements webui.Shared.
type Store struct {
	db      DB
	blobs   Blobs
	release string
	log     *slog.Logger
}

var _ webui.Shared = (*Store)(nil)

// New returns the store. release names the replica's build in the rows it inserts (published_by).
func New(db DB, blobs Blobs, release string, log *slog.Logger) *Store {
	if log == nil {
		log = slog.Default()
	}
	return &Store{db: db, blobs: blobs, release: release, log: log}
}

// Asset is webui.Shared: the content of the hashed file at path under locale, read from the blob store and verified
// against its digest. A name that is not of the hashed pattern is never looked up.
func (s *Store) Asset(ctx context.Context, locale, path string) ([]byte, error) {
	if !webui.IsHashed(path) {
		return nil, webui.ErrNotShared
	}
	var rows []q.GetUIAssetRow
	err := s.db.Read(ctx, func(tx q.DBTX) (err error) {
		rows, err = q.New(tx).GetUIAsset(ctx, q.GetUIAssetParams{Locale: locale, Path: path})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("look up %s/%s: %w", locale, path, err)
	}
	if len(rows) == 0 {
		return nil, webui.ErrNotShared
	}
	row := rows[0]
	if row.Size > MaxFileSize || len(row.BlobSha256) != len(blob.Digest{}) {
		return nil, fmt.Errorf("%s/%s: the row names %d bytes of a %d-byte digest", locale, path, row.Size, len(row.BlobSha256))
	}
	body, err := s.blobs.Open(ctx, blob.Digest(row.BlobSha256), row.Size, blob.OpenOptions{})
	if err != nil {
		return nil, fmt.Errorf("open %s/%s: %w", locale, path, err)
	}
	defer func() { _ = body.Close() }()
	data := make([]byte, 0, row.Size)
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		data = append(data, buf[:n]...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read %s/%s: %w", locale, path, err)
		}
		if int64(len(data)) > row.Size {
			return nil, fmt.Errorf("read %s/%s: more than the %d bytes of its row", locale, path, row.Size)
		}
	}
	// With storage.download.verify off nothing else catches a short object; a short file must never be served as immutable.
	if int64(len(data)) != row.Size {
		return nil, fmt.Errorf("read %s/%s: %d bytes, its row says %d", locale, path, len(data), row.Size)
	}
	return data, nil
}

// PublishResult says what a Publish did.
type PublishResult struct {
	// Files is the number of hashed files of the build.
	Files int
	// Renewed is the number of rows whose seen_at this call renewed.
	Renewed int64
	// Inserted is the number of rows this call inserted; the files another replica inserted meanwhile are not counted.
	Inserted int64
	// Skipped is the number of files larger than MaxFileSize, which are not published.
	Skipped int
}

// Publish puts the hashed files of a build into the store and renews the rows of the ones that are there (Q-HA-121): it
// renews first, so that a row another replica's Prune is about to delete is kept or gone before the presence is read,
// then reads which files have a row, then commits the missing ones in batches. tree is the build, files its hashed files
// (webui.HashedFiles). It is safe to run on several replicas at once: the blobs are deduplicated by the commit protocol and
// the rows are write-once.
func (s *Store) Publish(ctx context.Context, tree fs.FS, files []webui.File) (PublishResult, error) {
	res := PublishResult{Files: len(files)}
	names := make([]string, 0, len(files))
	for _, f := range files {
		if f.Size > MaxFileSize {
			res.Skipped++
			s.log.Warn("a hashed file of the UI is larger than the shared asset store takes; only the replicas of this release serve it",
				"file", f.Name(), "size", f.Size, "max", MaxFileSize)
			continue
		}
		names = append(names, f.Name())
	}
	present := map[string]bool{}
	err := s.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		n, err := qs.TouchUIAssets(ctx, q.TouchUIAssetsParams{Names: names, FreshSecs: (RenewEvery / 2).Seconds()})
		if err != nil {
			return err
		}
		res.Renewed = n
		got, err := qs.UIAssetsPresent(ctx, names)
		if err != nil {
			return err
		}
		clear(present)
		for _, name := range got {
			present[name] = true
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("renew the shared UI assets: %w", err)
	}
	var missing []webui.File
	for _, f := range files {
		if f.Size <= MaxFileSize && !present[f.Name()] {
			missing = append(missing, f)
		}
	}
	for start := 0; start < len(missing); start += commitBatch {
		n, err := s.commit(ctx, tree, missing[start:min(start+commitBatch, len(missing))])
		res.Inserted += n
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

// commit spools a batch of files and links them in one transaction.
func (s *Store) commit(ctx context.Context, tree fs.FS, batch []webui.File) (int64, error) {
	sps := make([]*blob.Spooled, 0, len(batch))
	defer func() {
		for _, sp := range sps {
			_ = sp.Close()
		}
	}()
	for _, f := range batch {
		sp, err := s.ingest(ctx, tree, f)
		if err != nil {
			return 0, err
		}
		sps = append(sps, sp)
	}
	var inserted int64
	_, err := s.blobs.CommitAll(ctx, sps, func(ctx context.Context, tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		inserted = 0
		for i, f := range batch {
			n, err := qs.InsertUIAsset(ctx, q.InsertUIAssetParams{
				Locale: f.Locale, Path: f.Path, BlobSha256: sps[i].SHA256.Bytes(), Size: sps[i].Size, PublishedBy: s.release,
			})
			if err != nil {
				return err
			}
			if n == 0 {
				// Another replica's row names the file, maybe with other bytes (another translation): the blob this
				// commit wrote may have no reference, so the collector is told; it checks the references before it
				// deletes anything (R-STOR-024).
				if err := qs.EnqueueBlobForGC(ctx, sps[i].SHA256.Bytes()); err != nil {
					return err
				}
			}
			inserted += n
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("commit the shared UI assets %s to %s: %w", batch[0].Name(), batch[len(batch)-1].Name(), err)
	}
	return inserted, nil
}

// queries returns the sqlc queries on the transaction behind tx.
func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("shared: the transaction is not one of internal/meta/postgres")
	}
	return q.New(ptx), nil
}

func (s *Store) ingest(ctx context.Context, tree fs.FS, f webui.File) (*blob.Spooled, error) {
	file, err := tree.Open(f.Name())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", f.Name(), err)
	}
	defer func() { _ = file.Close() }()
	sp, err := s.blobs.Ingest(ctx, file, blob.IngestOptions{ContentLength: f.Size, MaxSize: MaxFileSize})
	if err != nil {
		return nil, fmt.Errorf("spool %s: %w", f.Name(), err)
	}
	return sp, nil
}

// Prune deletes the rows no replica renewed within the retention and returns how many; the delete trigger queues their
// blobs for the collector. Several replicas may prune at once: a row is deleted once.
func (s *Store) Prune(ctx context.Context, retention time.Duration) (int64, error) {
	var n int64
	err := s.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		n, err = qs.PruneUIAssets(ctx, retention.Seconds())
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("prune the shared UI assets: %w", err)
	}
	return n, nil
}
