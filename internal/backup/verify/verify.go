// Package verify checks backups of a target without changing them (R-BAK-019): the manifest, every file the manifest lists,
// and the pool objects the blob list names, each once even when several backups share it.
package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/listing"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// DefaultConcurrency is the number of pool objects checked at once.
const DefaultConcurrency = 16

// Verifier implements [backup.Verifier].
type Verifier struct {
	Store  target.Store
	Layout format.Layout
	// Runs records the run (operation verify) when the source database is reachable; nil records nothing. A failure to
	// begin the row is logged and does not stop the verification (R-BAK-019).
	Runs   backup.RunStore
	Timing backup.Timing
	// Target, Pod and TroveVersion complete the run row; NewRunID returns a UUIDv7.
	Target, Pod, TroveVersion string
	NewRunID                  func() string
	// Concurrency is the number of pool objects checked at once; the default is [DefaultConcurrency].
	Concurrency int
	Log         *slog.Logger
}

var _ backup.Verifier = (*Verifier)(nil)

// Verify implements [backup.Verifier]. A report with findings comes with [backup.ErrFindings] (exit 5); a manifest of a
// higher format is exit 6; a backup that was asked for by ID and does not exist is exit 2. The run row of a verification
// stays in state starting: it has no phases, and the lease is renewed by the progress of the checks.
func (v *Verifier) Verify(ctx context.Context, opts backup.VerifyOptions) (*backup.VerifyReport, error) {
	log := v.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	var (
		h    *backup.Handle
		run  backup.Run
		hctx = ctx
	)
	if v.Runs != nil {
		var err error
		run, err = v.Runs.Begin(ctx, backup.NewRun{
			ID: v.NewRunID(), Operation: backup.OpVerify, BackupID: string(opts.ID), Trigger: backup.TriggerManual,
			Target: v.Target, Pod: v.Pod, TroveVersion: v.TroveVersion, Lease: v.Timing.Lease,
		})
		if err != nil {
			log.Warn("the verification is not recorded: the run row could not be written", "err", err)
		} else {
			h, hctx = backup.StartHandle(ctx, v.Runs, run, v.Timing, log)
			defer h.Stop()
		}
	}
	rep, err := v.verify(hctx, opts, h)
	if h != nil {
		h.Stop()
		v.finish(ctx, hctx, run.ID, rep, err, log)
	}
	if err != nil {
		return rep, err
	}
	if n := len(rep.Findings); n > 0 {
		return rep, fmt.Errorf("%w: %d findings", backup.ErrFindings, n)
	}
	return rep, nil
}

func (v *Verifier) finish(ctx, hctx context.Context, id string, rep *backup.VerifyReport, runErr error, log *slog.Logger) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	state := backup.StateCompleted
	var errJSON json.RawMessage
	if runErr != nil {
		state = backup.StateFailed
		code := backup.ErrorCode(runErr)
		switch {
		case errors.Is(context.Cause(hctx), backup.ErrSuperseded):
			return
		case ctx.Err() != nil:
			state, code = backup.StateInterrupted, backup.CodeInterrupted
		}
		errJSON, _ = json.Marshal(map[string]string{"code": code, "message": runErr.Error()})
	}
	var result json.RawMessage
	if rep != nil {
		result, _ = json.Marshal(rep)
	}
	if err := v.Runs.Finish(fctx, id, state, result, errJSON); err != nil {
		log.Error("recording the end of the verification failed", "err", err)
	}
}

func (v *Verifier) tick(h *backup.Handle) {
	if h != nil {
		h.Tick()
	}
}

func (v *Verifier) verify(ctx context.Context, opts backup.VerifyOptions, h *backup.Handle) (*backup.VerifyReport, error) {
	rep := &backup.VerifyReport{Verified: []format.ID{}, Findings: []backup.Finding{}}
	ids, err := v.selectBackups(ctx, opts, h)
	if err != nil {
		return rep, err
	}
	marker, haveMarker, err := v.marker(ctx)
	if err != nil {
		return rep, err
	}
	if !haveMarker {
		rep.Findings = append(rep.Findings, backup.Finding{
			Kind: backup.FindingMissing, Object: v.Layout.Marker(), Detail: "the target marker does not exist",
		})
	}

	var usable []*format.Manifest
	for _, id := range ids {
		m, findings, err := v.checkFiles(ctx, id, marker, haveMarker, h)
		if err != nil {
			return rep, err
		}
		rep.Findings = append(rep.Findings, findings...)
		rep.Verified = append(rep.Verified, id)
		if m != nil {
			usable = append(usable, m)
		}
	}
	pool, err := v.checkPool(ctx, usable, opts, h)
	rep.Findings = append(rep.Findings, pool...)
	return rep, err
}

func (v *Verifier) selectBackups(ctx context.Context, opts backup.VerifyOptions, h *backup.Handle) ([]format.ID, error) {
	if opts.ID != "" {
		_, err := v.Store.Head(ctx, v.Layout.Manifest(opts.ID))
		if errors.Is(err, target.ErrNotFound) {
			return nil, backup.Failf(backup.ExitUsage, backup.CodeConfig, "backup %s does not exist in the target (no manifest)", opts.ID)
		}
		if err != nil {
			return nil, fmt.Errorf("looking for backup %s: %w", opts.ID, err)
		}
		return []format.ID{opts.ID}, nil
	}
	var tick func()
	if h != nil {
		tick = h.Tick
	}
	dirs, err := listing.DirsTicking(ctx, v.Store, v.Layout, tick)
	if err != nil {
		return nil, err
	}
	var ids []format.ID
	for _, d := range dirs {
		if d.HasManifest {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return nil, backup.Failf(backup.ExitUsage, backup.CodeConfig, "the target holds no complete backup")
	}
	if opts.Latest {
		ids = ids[:1]
	}
	return ids, nil
}

func (v *Verifier) marker(ctx context.Context) (format.Marker, bool, error) {
	rc, _, err := v.Store.Get(ctx, v.Layout.Marker())
	if errors.Is(err, target.ErrNotFound) {
		return format.Marker{}, false, nil
	}
	if err != nil {
		return format.Marker{}, false, fmt.Errorf("reading the target marker: %w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return format.Marker{}, false, fmt.Errorf("reading the target marker: %w", err)
	}
	m, err := format.DecodeMarker(raw)
	return m, err == nil, err
}

// checkFiles verifies the manifest and the files it lists. It returns the manifest when the blob list is intact, so that the
// pool check reads it, and a nil manifest when it is not (its entries are not trusted).
func (v *Verifier) checkFiles(ctx context.Context, id format.ID, marker format.Marker, haveMarker bool, h *backup.Handle) (*format.Manifest, []backup.Finding, error) {
	var out []backup.Finding
	add := func(kind backup.FindingKind, object, detail string) {
		out = append(out, backup.Finding{Kind: kind, BackupID: string(id), Object: object, Detail: detail})
	}
	m, err := listing.ReadManifest(ctx, v.Store, v.Layout, id)
	if err != nil {
		var uf *format.UnknownFormatError
		if errors.As(err, &uf) || ctx.Err() != nil {
			return nil, nil, err
		}
		kind := backup.FindingFileChecksumMismatch
		if errors.Is(err, target.ErrNotFound) {
			kind = backup.FindingMissing
		}
		add(kind, v.Layout.Manifest(id), err.Error())
		return nil, out, nil
	}
	v.tick(h)
	if haveMarker && marker.AdoptedAt == nil && m.InstallationID != marker.InstallationID {
		add(backup.FindingFileChecksumMismatch, v.Layout.Manifest(id),
			fmt.Sprintf("the manifest names installation %s, the target marker names %s", m.InstallationID, marker.InstallationID))
	}
	for _, t := range m.Tables {
		key := v.Layout.Dir(id) + t.File
		sum, size, err := listing.HashObject(ctx, v.Store, key, nil)
		switch {
		case errors.Is(err, target.ErrNotFound):
			add(backup.FindingMissing, key, "")
		case err != nil:
			return nil, nil, fmt.Errorf("reading %s: %w", key, err)
		case size != t.CompressedBytes:
			add(backup.FindingSizeMismatch, key, fmt.Sprintf("%d bytes, the manifest says %d", size, t.CompressedBytes))
		case sum != t.SHA256:
			add(backup.FindingFileChecksumMismatch, key, "")
		}
		v.tick(h)
	}
	listKey := v.Layout.BlobList(id)
	switch err := listing.CheckBlobList(ctx, v.Store, v.Layout, m); {
	case errors.Is(err, target.ErrNotFound):
		add(backup.FindingMissing, listKey, "")
		return nil, out, nil
	case errors.Is(err, listing.ErrBlobList):
		add(backup.FindingFileChecksumMismatch, listKey, "")
		return nil, out, nil
	case err != nil:
		return nil, nil, fmt.Errorf("reading %s: %w", listKey, err)
	}
	v.tick(h)
	return m, out, nil
}

// source is one backup's blob list in the merge of the pool check.
type source struct {
	m    *format.Manifest
	it   format.BlobIterator
	list *listing.BlobList
	head format.BlobEntry
	ok   bool
}

// poolItem is one distinct digest with the sizes the backups that list it expect.
type poolItem struct {
	digest  format.Digest
	backups []format.ID
	sizes   []int64
}

// checkPool merges the blob lists of the given backups in digest order and checks every distinct digest once: HeadObject for
// the object and its size, and with Deep a download that recomputes the SHA-256. A finding is reported for every backup that
// lists the digest, minus the backups that list it as damaged.
func (v *Verifier) checkPool(ctx context.Context, manifests []*format.Manifest, opts backup.VerifyOptions, h *backup.Handle) ([]backup.Finding, error) {
	var findings []backup.Finding
	srcs := make([]*source, 0, len(manifests))
	defer func() {
		for _, s := range srcs {
			s.list.Close()
		}
	}()
	for _, m := range manifests {
		bl, err := listing.OpenBlobList(ctx, v.Store, v.Layout, m)
		if err != nil {
			return findings, fmt.Errorf("reading %s: %w", v.Layout.BlobList(m.BackupID), err)
		}
		srcs = append(srcs, &source{m: m, list: bl, it: bl.Without(m)})
	}
	advance := func(s *source) {
		if !s.ok && s.it == nil {
			return
		}
		e, err := s.it.Next()
		switch {
		case err == nil:
			s.head, s.ok = e, true
		case errors.Is(err, io.EOF):
			s.ok, s.it = false, nil
		default:
			findings = append(findings, backup.Finding{
				Kind: backup.FindingFileChecksumMismatch, BackupID: string(s.m.BackupID), Object: v.Layout.BlobList(s.m.BackupID), Detail: err.Error(),
			})
			s.ok, s.it = false, nil
		}
	}
	for _, s := range srcs {
		advance(s)
	}

	conc := v.Concurrency
	if conc <= 0 {
		conc = DefaultConcurrency
	}
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	wctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	items := make(chan poolItem, conc)
	lim := newLimiter(opts.BytesPerSecond)
	for range conc {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range items {
				fs, err := v.checkObject(wctx, it, opts.Deep, lim)
				mu.Lock()
				findings = append(findings, fs...)
				if err != nil && firstErr == nil {
					firstErr = err
					cancel(err)
				}
				mu.Unlock()
				v.tick(h)
			}
		}()
	}
produce:
	for {
		var lowest *source
		for _, s := range srcs {
			if s.ok && (lowest == nil || s.head.Digest.Less(lowest.head.Digest)) {
				lowest = s
			}
		}
		if lowest == nil {
			break
		}
		it := poolItem{digest: lowest.head.Digest}
		// advance mutates findings, which the workers append to: hold the lock around it.
		mu.Lock()
		for _, s := range srcs {
			if s.ok && s.head.Digest == it.digest {
				it.backups = append(it.backups, s.m.BackupID)
				it.sizes = append(it.sizes, s.head.Size)
				advance(s)
			}
		}
		mu.Unlock()
		select {
		case items <- it:
		case <-wctx.Done():
			break produce
		}
	}
	close(items)
	wg.Wait()
	if firstErr != nil {
		return findings, firstErr
	}
	return findings, ctx.Err()
}

// checkObject checks one pool object for every backup that lists it.
func (v *Verifier) checkObject(ctx context.Context, it poolItem, deep bool, lim *limiter) ([]backup.Finding, error) {
	key := v.Layout.Pool(it.digest)
	finding := func(i int, kind backup.FindingKind, detail string) backup.Finding {
		return backup.Finding{Kind: kind, BackupID: string(it.backups[i]), Object: it.digest.Hex(), Detail: detail}
	}
	var out []backup.Finding
	obj, err := v.Store.Head(ctx, key)
	if errors.Is(err, target.ErrNotFound) {
		for i := range it.backups {
			out = append(out, finding(i, backup.FindingMissing, ""))
		}
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checking pool object %s: %w", it.digest.Hex(), err)
	}
	sizeOK := false
	for i, want := range it.sizes {
		if obj.Size != want {
			out = append(out, finding(i, backup.FindingSizeMismatch, fmt.Sprintf("%d bytes, the blob list says %d", obj.Size, want)))
			continue
		}
		sizeOK = true
	}
	if !deep || !sizeOK {
		return out, nil
	}
	sum, _, err := listing.HashObject(ctx, v.Store, key, lim.wait)
	switch {
	case errors.Is(err, target.ErrNotFound):
		for i, want := range it.sizes {
			if obj.Size == want {
				out = append(out, finding(i, backup.FindingMissing, "the object vanished during the download"))
			}
		}
	case err != nil:
		return out, fmt.Errorf("downloading pool object %s: %w", it.digest.Hex(), err)
	case sum != it.digest.Hex():
		for i, want := range it.sizes {
			if obj.Size == want {
				out = append(out, finding(i, backup.FindingDigestMismatch, "the content hashes to "+sum))
			}
		}
	}
	return out, nil
}
