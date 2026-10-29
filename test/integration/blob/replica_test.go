//go:build integration

package blob

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
)

// countingStore counts and can fail the requests of one replica.
type countingStore struct {
	blob.ObjectStore
	puts, parts, deletes, deleteMany atomic.Int64

	mu         sync.Mutex
	failPart   func(n int) error // called with the 1-based count of UploadPart calls
	blockMany  chan struct{}     // when set, DeleteMany waits for it or the context
	manyCalled chan struct{}
}

func (c *countingStore) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sum blob.Checksum) error {
	c.puts.Add(1)
	return c.ObjectStore.Put(ctx, key, body, size, sum)
}

func (c *countingStore) UploadPart(ctx context.Context, key, id string, n int, body io.ReadSeeker, size int64, sum blob.Checksum) (blob.Part, error) {
	count := int(c.parts.Add(1))
	c.mu.Lock()
	f := c.failPart
	c.mu.Unlock()
	if f != nil {
		if err := f(count); err != nil {
			return blob.Part{}, err
		}
	}
	return c.ObjectStore.UploadPart(ctx, key, id, n, body, size, sum)
}

func (c *countingStore) Delete(ctx context.Context, key string) error {
	c.deletes.Add(1)
	return c.ObjectStore.Delete(ctx, key)
}

func (c *countingStore) DeleteMany(ctx context.Context, keys []string) ([]blob.DeleteFailure, error) {
	c.deleteMany.Add(1)
	c.mu.Lock()
	block, called := c.blockMany, c.manyCalled
	c.mu.Unlock()
	if block != nil {
		if called != nil {
			select {
			case called <- struct{}{}:
			default:
			}
		}
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.ObjectStore.DeleteMany(ctx, keys)
}

// replica is one trove replica's storage: its own spool and object-store client
// on the shared bucket and database.
type replica struct {
	store *blob.Store
	coll  *blob.Collector
	obj   *countingStore
	cfg   config.Storage
	pg    *pgEnv
}

// gcFast is the collection timing of the tests: no grace period, short settle
// and hand-over.
func gcFast(c *config.Storage) {
	c.GC.GracePeriod = 0
	c.GC.DeleteSettle = 150 * time.Millisecond
	c.GC.HandoverDelay = 1200 * time.Millisecond
	c.GC.BatchSize = 25
}

func newReplica(tb testing.TB, e *s3Env, p *pgEnv, mutate func(*config.Storage)) *replica {
	tb.Helper()
	cfg := e.storage(tb)
	gcFast(&cfg)
	if mutate != nil {
		mutate(&cfg)
	}
	obj := &countingStore{ObjectStore: e.raw}
	s, err := blob.New(cfg, obj, p.db, blob.WithLogger(quiet), blob.WithWaitPoll(25*time.Millisecond))
	if err != nil {
		tb.Fatal(err)
	}
	c := blob.NewCollector(cfg.GC, cfg.S3.Prefix, obj, p.db, blob.WithCollectorLogger(quiet))
	return &replica{store: s, coll: c, obj: obj, cfg: cfg, pg: p}
}

// upload ingests, commits and closes: the pipeline of R-STOR-004 without HTTP.
func (r *replica) upload(ctx context.Context, path string, data []byte) (blob.CommitResult, blob.Digest, error) {
	sp, err := r.store.Ingest(ctx, bytes.NewReader(data), blob.IngestOptions{ContentLength: int64(len(data))})
	if err != nil {
		return blob.CommitResult{}, blob.Digest{}, err
	}
	defer sp.Close()
	res, err := r.store.Commit(ctx, sp, r.pg.link(path, sp))
	return res, sp.SHA256, err
}

func (r *replica) spoolEmpty(tb testing.TB) {
	tb.Helper()
	if r.store.Spool().Reserved() != 0 {
		tb.Errorf("spool still has %d bytes reserved", r.store.Spool().Reserved())
	}
	if f := dirEntries(tb, r.store.Spool().Dir()); len(f) != 0 {
		tb.Errorf("spool files left: %v", f)
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// busy reports the 503 answers a client retries.
func busy(err error) bool {
	return errors.Is(err, blob.ErrBlobBusy) || errors.Is(err, blob.ErrCommitAttempts)
}

// objectExists reports whether the blob's object is in the bucket.
func objectExists(tb testing.TB, e *s3Env, prefix string, d blob.Digest) bool {
	tb.Helper()
	_, err := e.raw.Head(context.Background(), blob.Key(prefix, d))
	if errors.Is(err, blob.ErrNotFound) {
		return false
	}
	if err != nil {
		tb.Fatalf("head %s: %v", d, err)
	}
	return true
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
