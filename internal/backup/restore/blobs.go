package restore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// stepBlobs is step 3 (R-BAK-021): every digest of the blob list that is not damaged goes from the pool to the blob store,
// server-side when the credentials allow it, otherwise streamed and hashed. An object that exists with the right size is
// skipped, so a resumed restore copies only what is missing. The blobs come before the load so that the load commits
// references only to objects that exist (R-STOR-005). Once the marker's phase is loaded the step has completed.
func (r *Restorer) stepBlobs(ctx context.Context, st *state, marker *postgres.RestoreMarker) error {
	if marker.Phase != postgres.PhaseBlobs {
		return nil
	}
	rc, _, err := r.d.Target.Get(ctx, r.d.Layout.BlobList(st.id))
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	sum := sha256.New()
	tee := io.TeeReader(rc, sum)
	list, err := format.NewBlobListReader(tee)
	if err != nil {
		return err
	}
	defer list.Close()
	skip := map[string]bool{}
	for d := range st.m.DamagedSet() {
		skip[d] = true
	}
	it := format.Without(list, skip)

	conc := max(r.d.Config.Copy.Concurrency, 1)
	cp := &copier{r: r, st: st, mode: r.d.Config.Copy.ServerSide}
	if cp.mode == "never" {
		cp.streamOnly.Store(true)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	work := make(chan format.BlobEntry, conc*2)
	var wg sync.WaitGroup
	var firstErr error
	var once sync.Once
	fail := func(err error) { once.Do(func() { firstErr = err; cancel(err) }) }
	for range conc {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range work {
				if err := cp.one(ctx, e); err != nil {
					fail(err)
					return
				}
			}
		}()
	}
	var listErr error
produce:
	for {
		e, err := it.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			listErr = err
			break
		}
		select {
		case work <- e:
		case <-ctx.Done():
			break produce
		}
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if listErr != nil {
		return listErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, tee)
	if got := hex.EncodeToString(sum.Sum(nil)); got != st.m.Blobs.SHA256 {
		return backup.Failf(backup.ExitFailed, backup.CodeInternal, "the blob list has SHA-256 %s, the manifest says %s; run trove backup verify", got, st.m.Blobs.SHA256)
	}
	r.d.Log.Info("blobs copied", "copied", cp.copied.Load(), "skipped", cp.skipped.Load(), "server_side", cp.server.Load(), "streamed", cp.streamed.Load())
	return nil
}

// copier copies one digest at a time for the workers of stepBlobs.
type copier struct {
	r          *Restorer
	st         *state
	mode       string
	streamOnly atomic.Bool
	copied     atomic.Int64
	skipped    atomic.Int64
	server     atomic.Int64
	streamed   atomic.Int64
}

func (c *copier) one(ctx context.Context, e format.BlobEntry) error {
	sink := c.r.d.Blobs
	has, err := sink.Has(ctx, e.Digest, e.Size)
	if err != nil {
		return err
	}
	if has {
		c.skipped.Add(1)
		c.r.progress()
		return nil
	}
	key := c.r.d.Layout.Pool(e.Digest)
	if !c.streamOnly.Load() {
		src := target.ObjectRef{Endpoint: c.r.d.TargetEndpoint, Bucket: c.r.d.TargetBucket, Key: key, Size: e.Size, Progress: func(int64) { c.r.progress() }}
		err := sink.CopyFrom(ctx, src, e.Digest, e.Size)
		switch {
		case err == nil:
			c.done(true)
			return nil
		case errors.Is(err, target.ErrServerCopyUnavailable):
			if c.mode == "always" {
				return backup.Failf(backup.ExitFailed, backup.CodeInternal, "backup.copy.server_side is always, but the blob store cannot copy from the target: %w", err)
			}
			if c.streamOnly.CompareAndSwap(false, true) {
				c.r.d.Log.Info("server-side copy is not available, streaming from now on")
			}
		case errors.Is(err, target.ErrNotFound), errors.Is(err, backup.ErrBlobNotFound):
			return c.missing(e)
		default:
			return fmt.Errorf("copy %s: %w", e.Digest.Hex(), err)
		}
	}
	body, _, err := c.r.d.Target.Get(ctx, key)
	if err != nil {
		if errors.Is(err, target.ErrNotFound) {
			return c.missing(e)
		}
		return err
	}
	defer func() { _ = body.Close() }()
	if err := sink.Put(ctx, e.Digest, e.Size, &progressReader{r: body, f: c.r.progress}); err != nil {
		return fmt.Errorf("copy %s: %w", e.Digest.Hex(), err)
	}
	c.done(false)
	return nil
}

func (c *copier) done(server bool) {
	if server {
		c.server.Add(1)
	} else {
		c.streamed.Add(1)
	}
	n := c.copied.Add(1)
	c.r.progress()
	c.r.hook(fmt.Sprintf("step3:copied:%d", n))
}

func (c *copier) missing(e format.BlobEntry) error {
	return backup.Failf(backup.ExitFailed, backup.CodeMissingPoolObject,
		"pool object %s of backup %s is missing from the target; trove backup verify %s reports it", e.Digest.Hex(), c.st.id, c.st.id)
}

// progressReader reports every read as progress.
type progressReader struct {
	r io.Reader
	f func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.f()
	}
	return n, err
}
