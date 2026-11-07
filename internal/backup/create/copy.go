package create

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// copyStats is the outcome of the copy phase.
type copyStats struct {
	copied  int64 // pins confirmed in the pool, damaged ones excluded
	bytes   int64
	damaged []format.DamagedBlob
}

// errSourceMissing and errSourceCorrupt are the two ways a source object is damaged (R-BAK-006 "Source damage"): not there or
// of another size, and of the right size with other content.
var (
	errSourceMissing = errors.New("the source object is missing or has another size")
	errSourceCorrupt = errors.New("the source object does not hash to its digest")
)

const unpinBatch = 500

// copier is the state of the copy phase.
type copier struct {
	r     *runState
	lim   *limiter
	mu    sync.Mutex
	stats copyStats
	done  []format.Digest // confirmed, pin not yet deleted

	serverMu  sync.Mutex
	serverOff bool
}

// copyBlobs is step 5 of the protocol of R-BAK-007 and the copy of R-BAK-006: every pin is copied into the pool and deleted once
// the object is confirmed; with the last pin gone the hold becomes 'none'.
func (r *runState) copyBlobs(ctx context.Context) error {
	if r.list.toCopy == 0 {
		return r.fault(ctx, PointCopied)
	}
	c := &copier{r: r, lim: newLimiter(int64(r.d.Cfg.Copy.BytesPerSecond))}
	c.lim.tick, c.lim.slice = r.h.Tick, max(r.d.Cfg.HeartbeatInterval/2, 10*time.Millisecond)
	g, _ := newGroup(ctx)
	work := make(chan format.BlobEntry, 2*r.d.Cfg.Copy.Concurrency)
	g.Go(func(ctx context.Context) error {
		defer close(work)
		var after format.Digest
		for {
			page, err := r.d.Pins.Pins(ctx, r.runID, after, 1000)
			if err != nil {
				return classify(fmt.Errorf("read the pins: %w", err))
			}
			if len(page) == 0 {
				return nil
			}
			for _, e := range page {
				select {
				case work <- e:
				case <-ctx.Done():
					return nil
				}
			}
			after = page[len(page)-1].Digest
		}
	})
	for range max(r.d.Cfg.Copy.Concurrency, 1) {
		g.Go(func(ctx context.Context) error {
			for e := range work {
				if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				if err := c.one(ctx, e); err != nil {
					return err
				}
			}
			return nil
		})
	}
	tctx, stopTicker := context.WithCancel(ctx)
	var tw sync.WaitGroup
	tw.Add(1)
	go func() {
		defer tw.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := c.unpin(tctx, false); err != nil && tctx.Err() == nil {
					r.d.Log.Warn("could not delete confirmed pins; the next batch tries again", "err", err)
				}
			case <-tctx.Done():
				return
			}
		}
	}()
	err := g.Wait()
	stopTicker()
	tw.Wait()
	if err != nil {
		return err
	}
	if err := c.unpin(ctx, true); err != nil {
		return err
	}
	c.mu.Lock()
	r.copied = c.stats
	c.mu.Unlock()
	sort.Slice(r.copied.damaged, func(i, j int) bool { return r.copied.damaged[i].SHA256 < r.copied.damaged[j].SHA256 })
	if err := r.h.SetHold(ctx, backup.HoldPinned, backup.HoldNone); err != nil {
		return err
	}
	return r.fault(ctx, PointCopied)
}

// unpin deletes the pins of confirmed digests in batches of at most 1,000; with force it deletes every one that is left.
func (c *copier) unpin(ctx context.Context, force bool) error {
	for {
		c.mu.Lock()
		n := len(c.done)
		if n == 0 || (!force && n < unpinBatch) {
			c.mu.Unlock()
			return nil
		}
		n = min(n, 1000)
		batch := append([]format.Digest(nil), c.done[:n]...)
		c.mu.Unlock()
		if err := c.r.d.Store.DeletePins(ctx, c.r.runID, batch); err != nil {
			return classify(fmt.Errorf("delete the pins: %w", err))
		}
		c.mu.Lock()
		c.done = c.done[n:]
		c.mu.Unlock()
		c.r.h.Tick()
		if !force {
			return nil
		}
	}
}

// confirm records a digest whose pin may go and updates the progress.
func (c *copier) confirm(e format.BlobEntry, damaged *format.DamagedBlob) {
	c.mu.Lock()
	c.done = append(c.done, e.Digest)
	if damaged != nil {
		c.stats.damaged = append(c.stats.damaged, *damaged)
	} else {
		c.stats.copied++
		c.stats.bytes += e.Size
	}
	copied, bytes, dmg := c.stats.copied, c.stats.bytes, int64(len(c.stats.damaged))
	c.mu.Unlock()
	total := c.r.list.toCopy
	c.r.h.Update(func(p *backup.Progress) {
		p.BlobsCopied, p.BytesCopied = copied, bytes
		p.PinsLeft = max(total-copied-dmg, 0)
	})
}

// one copies a single pinned digest into the pool and confirms it.
func (c *copier) one(ctx context.Context, e format.BlobEntry) error {
	r := c.r
	key := r.layout.Pool(e.Digest)
	if r.checkExisting {
		if o, err := r.d.Target.Head(ctx, key); err == nil && o.Size == e.Size {
			c.confirm(e, nil)
			return nil
		} else if err != nil && !errors.Is(err, target.ErrNotFound) {
			return classify(fmt.Errorf("check the pool object %s: %w", e.Digest.Hex(), err))
		}
	}
	size, err := r.d.Source.Size(ctx, e.Digest)
	switch {
	case errors.Is(err, backup.ErrBlobNotFound):
		return c.damaged(ctx, e, "missing")
	case err != nil:
		return classify(fmt.Errorf("size of the source object %s: %w", e.Digest.Hex(), err))
	case size != e.Size:
		return c.damaged(ctx, e, "missing")
	}
	mode, err := c.transfer(ctx, e, key)
	switch {
	case errors.Is(err, errSourceMissing), errors.Is(err, target.ErrNotFound), errors.Is(err, backup.ErrBlobNotFound):
		return c.damaged(ctx, e, "missing")
	case errors.Is(err, errSourceCorrupt):
		return c.damaged(ctx, e, "corrupt")
	case err != nil:
		return classify(fmt.Errorf("copy %s: %w", e.Digest.Hex(), err))
	}
	o, err := r.d.Target.Head(ctx, key)
	if err != nil {
		return classify(fmt.Errorf("confirm the pool object %s: %w", e.Digest.Hex(), err))
	}
	if o.Size != e.Size {
		return fmt.Errorf("the pool object %s has %d bytes, %d were copied", e.Digest.Hex(), o.Size, e.Size)
	}
	if o.SHA256 != "" && o.SHA256 != e.Digest.Hex() {
		return fmt.Errorf("%w: the pool object %s reports the checksum %s", backup.ErrPoolDamaged, e.Digest.Hex(), o.SHA256)
	}
	r.d.Metrics.Copied(string(mode), e.Size)
	c.confirm(e, nil)
	return nil
}

// damaged tells source damage from a lost hold (R-BAK-006 "Source damage"): the blob row is read at READ COMMITTED. A row that is
// live means the object is damaged in the source (R-STOR-027), recorded and the run goes on; a row that is deleting or gone means
// the hold was lost, and the run fails without a manifest.
func (c *copier) damaged(ctx context.Context, e format.BlobEntry, status string) error {
	state, _, found, err := c.r.d.BlobState.BlobState(ctx, e.Digest)
	if err != nil {
		return classify(fmt.Errorf("read the blob row %s: %w", e.Digest.Hex(), err))
	}
	if !found || state != "live" {
		return fmt.Errorf("%w: %s is %s", backup.ErrHoldLost, e.Digest.Hex(), map[bool]string{true: "in state " + state, false: "gone"}[found])
	}
	c.r.d.Log.Warn("a referenced blob is damaged in the source", "sha256", e.Digest.Hex(), "status", status)
	c.confirm(e, &format.DamagedBlob{SHA256: e.Digest.Hex(), Status: status})
	return nil
}

// transfer writes the pool object, server-side where the store allows, otherwise through the pod (R-BAK-006 "Copy").
func (c *copier) transfer(ctx context.Context, e format.BlobEntry, key string) (target.CopyMode, error) {
	r := c.r
	mode := r.d.Cfg.Copy.ServerSide
	if mode != "never" && !c.serverDisabled() {
		if err := c.lim.wait(ctx, e.Size); err != nil {
			return "", err
		}
		ref := r.d.Source.Ref(e.Digest)
		ref.Size = e.Size
		ref.Progress = func(int64) { r.h.Tick() } // a long copy inside the store is progress (R-BAK-013)
		err := r.d.Target.Copy(ctx, ref, key)
		switch {
		case err == nil:
			r.h.Tick()
			return target.CopyServer, nil
		case errors.Is(err, target.ErrServerCopyUnavailable):
			if mode == "always" {
				return "", backup.Fail(backup.ExitFailed, backup.CodeInternal, fmt.Errorf("backup.copy.server_side is always but the store cannot copy server-side: %w", err))
			}
			c.disableServer()
		default:
			return "", err
		}
	}
	return target.CopyStream, c.stream(ctx, e, key)
}

func (c *copier) serverDisabled() bool {
	c.serverMu.Lock()
	defer c.serverMu.Unlock()
	return c.serverOff
}

// disableServer remembers that the store rejected a server-side copy: with 'auto' the run streams from then on.
func (c *copier) disableServer() {
	c.serverMu.Lock()
	if !c.serverOff {
		c.r.d.Log.Info("the target cannot copy server-side; copying through the pod")
	}
	c.serverOff = true
	c.serverMu.Unlock()
}

// tickReader reports progress for every read and applies the rate limit.
type tickReader struct {
	ctx context.Context
	rc  io.Reader
	c   *copier
}

func (t *tickReader) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 {
		t.c.r.h.Tick()
		if lerr := t.c.lim.wait(t.ctx, int64(n)); lerr != nil {
			return n, lerr
		}
	}
	return n, err
}

// stream copies through the pod: ranged reads of the source in parts of backup.copy.part_size, piped into the upload while the
// digest is computed; a stream that does not hash to the digest aborts the upload.
func (c *copier) stream(ctx context.Context, e format.BlobEntry, key string) error {
	r := c.r
	w, err := r.newWriterHint(ctx, key, e.Size)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		r.abort(ctx, w)
		r.forget(w)
		return err
	}
	h := sha256.New()
	part := int64(r.d.Cfg.Copy.PartSize)
	for off := int64(0); off < e.Size; off += part {
		length := min(part, e.Size-off)
		rc, err := r.d.Source.Open(ctx, e.Digest, off, length)
		if err != nil {
			if errors.Is(err, backup.ErrBlobNotFound) {
				err = errSourceMissing
			}
			return fail(err)
		}
		n, err := io.Copy(io.MultiWriter(w, h), &tickReader{ctx: ctx, rc: rc, c: c})
		_ = rc.Close()
		if err != nil {
			return fail(err)
		}
		if n != length {
			return fail(fmt.Errorf("%w: a read of %d bytes returned %d", errSourceMissing, length, n))
		}
	}
	var sum format.Digest
	copy(sum[:], h.Sum(nil))
	if sum != e.Digest {
		return fail(errSourceCorrupt)
	}
	if _, err := w.Close(ctx); err != nil {
		r.forget(w)
		return err
	}
	r.forget(w)
	return nil
}
