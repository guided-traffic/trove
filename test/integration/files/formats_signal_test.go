//go:build integration

package files

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
)

// recordedSignals is the formathost.Signaler of the fixture: like cluster.Signals it
// attaches the signal to the transaction and publishes it after the commit, so a rollback
// records nothing.
type recordedSignals struct {
	mu  sync.Mutex
	got [][2]string
}

func (r *recordedSignals) Notify(tx meta.Tx, area, payload string) {
	tx.After(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.got = append(r.got, [2]string{area, payload})
	})
}

func (r *recordedSignals) take() [][2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.got)
	r.got = nil
	return out
}

// TestQ_ARCH_259_ThePackageWriterSignalsARaisedPackageOnce: the package writer attaches
// metadata_changed with the engine's payload for every package a transaction raised, once
// however many writes raise it and whether or not the write names the package, and only
// when the transaction commits. The format attaches nothing.
func TestQ_ARCH_259_ThePackageWriterSignalsARaisedPackageOnce(t *testing.T) {
	f := newFormatsFixture(t)
	repoID := f.e.number("SELECT id FROM repositories WHERE key = 'probe-local'")
	signalOf := func(name string) [2]string {
		return [2]string{catalog.MetadataChangedArea, catalog.PackageChanged(repoID, name)}
	}
	f.sigs.take()

	t.Run("a publish through a route signals its package once", func(t *testing.T) {
		if res := f.publish("writer", "alpha-sig", "1.0.0"); res.status != http.StatusCreated {
			t.Fatalf("publish: %d %s", res.status, res.body)
		}
		if got := f.sigs.take(); len(got) != 1 || got[0] != signalOf("alpha-sig") {
			t.Fatalf("signals = %v, want %v once", got, signalOf("alpha-sig"))
		}
		if res := f.publish("writer", "alpha-sig", "1.1.0"); res.status != http.StatusCreated {
			t.Fatalf("second publish: %d %s", res.status, res.body)
		}
		if got := f.sigs.take(); len(got) != 1 || got[0] != signalOf("alpha-sig") {
			t.Fatalf("signals of the second version = %v", got)
		}
	})

	t.Run("a commit that rolls back signals nothing", func(t *testing.T) {
		probeLog.mu.Lock()
		probeLog.failAfterRows = "doomed-sig"
		probeLog.mu.Unlock()
		if res := f.publish("writer", "doomed-sig", "1.0.0"); res.status != http.StatusInternalServerError {
			t.Fatalf("publish: %d %s", res.status, res.body)
		}
		if got := f.sigs.take(); len(got) != 0 {
			t.Fatalf("a rolled-back publish signalled %v", got)
		}
		if n := f.e.number("SELECT count(*) FROM packages WHERE name = 'doomed-sig'"); n != 0 {
			t.Fatalf("%d package rows of the rolled-back publish", n)
		}
	})

	t.Run("the package writer calls the host once per package, by name or by id", func(t *testing.T) {
		ctx := context.Background()
		var seen []packages.Ref
		raised := func(_ context.Context, _ meta.Tx, pkg packages.Ref) error {
			seen = append(seen, pkg)
			return nil
		}
		err := f.r.db.InTx(ctx, func(tx meta.Tx) error {
			seen = nil
			w, err := packages.NewWriter(tx, "probe.", packages.WithRaised(raised))
			if err != nil {
				return err
			}
			// A new package, raised at creation, then written three more times.
			pkg, created, err := w.EnsureLocked(ctx, repoID, "beta-sig", "")
			if err != nil || !created {
				t.Fatalf("EnsureLocked: %v created=%v", err, created)
			}
			vid, err := w.InsertVersion(ctx, pkg, packages.NewVersion{Version: "1.0.0"})
			if err != nil {
				return err
			}
			if err := w.SetVersionAttributes(ctx, vid, []byte(`{"k":"v"}`)); err != nil {
				return err
			}
			if _, _, err := w.BumpRevision(ctx, pkg); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0].Name != "beta-sig" || seen[0].RepoID != repoID {
			t.Fatalf("raised = %+v, want beta-sig once", seen)
		}
		// A write that names the version only, in a transaction that never locked the
		// package: the host learns which package it was from the row.
		err = f.r.db.InTx(ctx, func(tx meta.Tx) error {
			seen = nil
			w, err := packages.NewWriter(tx, "probe.", packages.WithRaised(raised))
			if err != nil {
				return err
			}
			id := f.e.number("SELECT id FROM package_versions WHERE version = '1.0.0' AND package_id = (SELECT id FROM packages WHERE name = 'beta-sig')")
			if err := w.SetVersionAttributes(ctx, id, []byte(`{"k":"w"}`)); err != nil {
				return err
			}
			return w.SetVersionAttributes(ctx, id, []byte(`{"k":"x"}`))
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0].Name != "beta-sig" || seen[0].RepoID != repoID {
			t.Fatalf("raised by id = %+v, want beta-sig once", seen)
		}
	})
}
