package cache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

func spoolBytes(t *testing.T, r *rig) int64 {
	t.Helper()
	var n int64
	_ = filepath.WalkDir(r.dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(filepath.Dir(p)) == "tmp" {
			if fi, e := d.Info(); e == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// The fills in progress together hold at most the spool budget: the write that would pass it is refused, the
// fill's bytes are given back at once, and the other fills go on (Q-BAS-131, R-BAS-021).
func TestR_BAS_021_R_BAS_030_TheFillsInProgressNeverHoldMoreThanTheSpoolBudget(t *testing.T) {
	r := newRig(t, func(c *config.BastionCache) { c.SpoolMaxBytes = 500; c.MaxObjectBytes = 500; c.MaxBytes = 1200 })
	a, err := r.NewFill(EntryArtifact)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Abort()
	b, err := r.NewFill(EntryArtifact)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort()
	if _, err := a.Write(bytes.Repeat([]byte{1}, 300)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write(bytes.Repeat([]byte{2}, 150)); err != nil {
		t.Fatal(err)
	}
	if got := r.spool.Held(); got != 450 {
		t.Fatalf("held = %d, want 450", got)
	}
	// 300 + 150 + 100 would pass 500: the third write is refused, counted, and the fill gives its 300 back.
	if _, err := a.Write(bytes.Repeat([]byte{1}, 100)); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("write past the budget = %v, want ErrSpoolFull", err)
	}
	if got := r.spool.Held(); got != 150 {
		t.Fatalf("held = %d after the refused fill gave its bytes back, want 150", got)
	}
	if got := val(t, r.m.CacheErrors.WithLabelValues("spool")); got != 1 {
		t.Fatalf("trove_bastion_cache_errors_total{op=spool} = %v, want 1", got)
	}
	if n := spoolBytes(t, r); n > 500 {
		t.Fatalf("%d bytes on disk in the spool, the budget is 500", n)
	}
	// The refused fill stays refused and cannot be committed; the other one can.
	if _, err := a.Write([]byte("x")); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("write after the refusal = %v", err)
	}
	if err := a.Commit(context.Background(), digest(nil)); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("commit of a refused fill = %v", err)
	}
	if _, err := b.Write(bytes.Repeat([]byte{2}, 300)); err != nil {
		t.Fatalf("a fill that still fits was held back: %v", err)
	}
	data := append(bytes.Repeat([]byte{2}, 150), bytes.Repeat([]byte{2}, 300)...)
	if err := b.Commit(context.Background(), digest(data)); err != nil {
		t.Fatal(err)
	}
	if got := r.spool.Held(); got != 0 {
		t.Fatalf("held = %d after the commit, want 0", got)
	}
}

// A fill that ends, committed or aborted, gives its bytes back, also when it was kept.
func TestR_BAS_021_AFillGivesItsSpoolBytesBackWhenItEnds(t *testing.T) {
	r := newRig(t, func(c *config.BastionCache) { c.SpoolMaxBytes = 1000 })
	for _, keep := range []bool{false, true} {
		data := bytes.Repeat([]byte{7}, 100)
		f, err := r.NewFill(EntryArtifact)
		if err != nil {
			t.Fatal(err)
		}
		if keep {
			f.Keep()
		}
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := f.Commit(context.Background(), digest(data)); err != nil {
			t.Fatal(err)
		}
		if keep && r.spool.Held() != 100 {
			t.Fatalf("a kept fill holds %d after its commit, want 100", r.spool.Held())
		}
		f.Abort()
		if got := r.spool.Held(); got != 0 {
			t.Fatalf("keep=%v: held = %d after the fill ended, want 0", keep, got)
		}
	}
	g, _ := r.NewFill(EntryArtifact)
	_, _ = g.Write(bytes.Repeat([]byte{1}, 50))
	g.Abort()
	if got := r.spool.Held(); got != 0 {
		t.Fatalf("held = %d after an abort", got)
	}
}

// No budget configured (zero) is no bound: the tests and the configuration of a volume that bounds itself.
func TestR_BAS_021_ASpoolBudgetOfZeroIsNoBound(t *testing.T) {
	r := newRig(t)
	f, err := r.NewFill(EntryArtifact)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Abort()
	if _, err := f.Write(bytes.Repeat([]byte{1}, 500)); err != nil {
		t.Fatal(err)
	}
}
