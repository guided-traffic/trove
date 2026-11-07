package create

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
)

// tickWriter counts what passes and reports progress: every exported row batch is progress (R-BAK-013).
type tickWriter struct {
	w io.Writer
	n atomic.Int64
	h *backup.Handle
}

func (t *tickWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	t.n.Add(int64(n))
	t.h.Tick()
	return n, err
}

// exportAll exports every table of the snapshot with backup.database.parallel workers, largest first, then reads the
// sequences, lets go of the migration lock and closes the snapshot: it stays open until the last table is exported, and no
// longer (R-BAK-005).
func (r *runState) exportAll(ctx context.Context) error {
	tbls := r.snapInfo.Tables
	next := make(chan backup.TableInfo)
	g, _ := newGroup(ctx)
	var mu sync.Mutex
	files := make([]format.TableFile, 0, len(tbls))
	workers := min(max(r.d.Cfg.Database.Parallel, 1), max(len(tbls), 1))
	for range workers {
		g.Go(func(ctx context.Context) error {
			for t := range next {
				if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				f, err := r.exportTable(ctx, t)
				if err != nil {
					return err
				}
				mu.Lock()
				files = append(files, f)
				done := len(files)
				mu.Unlock()
				r.h.Update(func(p *backup.Progress) { p.TablesDone = done })
			}
			return nil
		})
	}
	g.Go(func(ctx context.Context) error {
		defer close(next)
		for _, t := range tbls {
			select {
			case next <- t:
			case <-ctx.Done():
				return nil
			}
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	seqs, err := r.snap.Sequences(ctx)
	if err != nil {
		return err
	}
	if err := r.snap.ReleaseMigrationLock(ctx); err != nil {
		return err
	}
	r.snapMu.Lock()
	snap := r.snap
	r.snap = nil
	r.snapMu.Unlock()
	if err := snap.Close(ctx); err != nil {
		r.d.Log.Warn("closing the backup snapshot failed", "err", err)
	}
	r.h.Update(func(p *backup.Progress) { p.SnapshotOpenSince = nil })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	r.tableFiles, r.sequences = files, seqs
	return nil
}

// exportTable streams one table through the zstd encoder into a multipart upload while SHA-256, byte and row counts are
// computed (R-BAK-005 step 5). Nothing touches the local disk.
func (r *runState) exportTable(ctx context.Context, t backup.TableInfo) (format.TableFile, error) {
	key := r.layout.Table(r.id, t.Name)
	w, err := r.newWriter(ctx, key)
	if err != nil {
		return format.TableFile{}, classify(fmt.Errorf("start the upload of %s: %w", t.Name, err))
	}
	fail := func(err error) (format.TableFile, error) {
		_ = w.Abort(context.WithoutCancel(ctx))
		r.forget(w)
		return format.TableFile{}, err
	}
	sum := format.NewSumWriter(w)
	enc, err := zstd.NewWriter(sum, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(r.d.Cfg.CompressionLevel)), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return fail(err)
	}
	tw := &tickWriter{w: enc, h: r.h}
	rows, err := r.snap.ExportTable(ctx, t, tw)
	if err != nil {
		_ = enc.Close()
		return fail(classify(err))
	}
	if err := enc.Close(); err != nil {
		return fail(err)
	}
	obj, err := w.Close(ctx)
	r.forget(w)
	if err != nil {
		return format.TableFile{}, classify(fmt.Errorf("finish the upload of %s: %w", t.Name, err))
	}
	if obj.Size != sum.Bytes() {
		return format.TableFile{}, fmt.Errorf("the target stored %d bytes of %s, %d were sent", obj.Size, t.Name, sum.Bytes())
	}
	r.d.Log.Debug("table exported", "table", t.Name, "rows", rows, "bytes", tw.n.Load(), "compressed", sum.Bytes(), "at", time.Now().UTC().Format(time.RFC3339))
	return format.TableFile{
		Name: t.Name, Class: t.Class.String(), File: strings.TrimPrefix(key, r.layout.Dir(r.id)), Columns: t.Columns,
		Rows: rows, Bytes: tw.n.Load(), CompressedBytes: sum.Bytes(), SHA256: sum.SHA256(),
	}, nil
}
