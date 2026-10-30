//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestSnapshotAcrossReplicas covers the repository state on n replicas without
// Valkey (R-REPO-049) and the lookup of a key the snapshot does not hold
// (R-ARCH-011 step 2). Neither replica has a Valkey client, so only the
// PostgreSQL change log carries a change.
func TestSnapshotAcrossReplicas(t *testing.T) {
	e := newEnv(t)
	poll := 300 * time.Millisecond
	a := e.newReplica("snap-a", replicaOpts{poll: poll})
	b := e.newReplica("snap-b", replicaOpts{poll: poll})
	ctx := context.Background()
	within := 3 * poll

	t.Run("R-ARCH-011 a key that cannot be a key is not found without a query", func(t *testing.T) {
		if _, err := b.reader.List(ctx); err != nil { // build the snapshot
			t.Fatal(err)
		}
		before := b.runner.n.Load()
		for _, key := range []string{"Upper", "a", "-x", "a..b", "x.", "npm~cache", "with space", ""} {
			_, err := b.reader.ByKey(ctx, key)
			wantCode(t, err, 404, errs.CodeRepositoryNotFound)
		}
		if b.runner.n.Load() != before {
			t.Fatal("an impossible key caused a query")
		}
	})

	t.Run("R-ARCH-011 1000 requests for one unknown key cost one lookup", func(t *testing.T) {
		before := b.runner.n.Load()
		var wg sync.WaitGroup
		for range 1000 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := b.reader.ByKey(ctx, "unknown-key"); err == nil {
					t.Error("unknown key found")
				}
			}()
		}
		wg.Wait()
		for range 1000 {
			if _, err := b.reader.ByKey(ctx, "unknown-key"); err == nil {
				t.Fatal("unknown key found")
			}
		}
		if n := b.runner.n.Load() - before; n > 3 {
			t.Fatalf("%d transactions for one unknown key", n)
		}
	})

	t.Run("R-ARCH-011 R-API-052 R-REPO-049 a repository created on replica A is usable on replica B at once, without Valkey", func(t *testing.T) {
		// B has never heard of the key, and its poll interval is far longer than this
		// test waits: the lookup finds the row in PostgreSQL and polls the change log
		// itself.
		if _, err := a.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "fresh-key"))); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		r, err := b.reader.ByKey(ctx, "fresh-key")
		if err != nil || r.Key != "fresh-key" || r.Format != "generic" || r.Lifecycle != repository.Active {
			t.Fatalf("ByKey = %+v, %v", r, err)
		}
		if d := time.Since(start); d > poll {
			t.Fatalf("the lookup took %s with a poll interval of %s", d, poll)
		}
		// And it is in the snapshot now: no further query.
		before := b.runner.n.Load()
		if _, err := b.reader.ByKey(ctx, "fresh-key"); err != nil || b.runner.n.Load() != before {
			t.Fatalf("second lookup: %v, %d queries", err, b.runner.n.Load()-before)
		}
	})

	t.Run("R-API-052 a key that missed shortly before its creation appears within the poll bound", func(t *testing.T) {
		// The documented exception: B answers 404 from the negative entry until the
		// creation's change evicts it.
		if _, err := a.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "unknown-key"))); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "B to find the repository", within, func() bool {
			r, err := b.reader.ByKey(ctx, "unknown-key")
			return err == nil && r.Key == "unknown-key"
		})
	})

	t.Run("R-REPO-049 R-ARCH-018 a pattern change on A reaches B within the poll interval", func(t *testing.T) {
		if _, err := a.svc.Patch(ctx, caller, "unknown-key", repository.MatchAny(), mergePatch(`{"includePatterns":["releases/**"]}`)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "B to honour the include pattern", within, func() bool {
			r, err := b.reader.ByKey(ctx, "unknown-key")
			return err == nil && !r.Admits("tmp/x.bin", false) && r.Admits("releases/1.0/x.bin", false)
		})
		waitFor(t, "A to honour the include pattern", within, func() bool {
			r, err := a.reader.ByKey(ctx, "unknown-key")
			return err == nil && !r.Admits("tmp/x.bin", false)
		})
	})

	t.Run("R-REPO-004 disabled and requireAuthentication reach the read model of the other replica", func(t *testing.T) {
		if _, err := a.svc.Patch(ctx, caller, "unknown-key", repository.MatchAny(), mergePatch(`{"disabled":true,"requireAuthentication":true,"hideExistence":"false"}`)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "B to see the settings", within, func() bool {
			r, err := b.reader.ByKey(ctx, "unknown-key")
			return err == nil && r.Disabled && r.RequireAuthentication && r.HideExistence == "false"
		})
	})

	t.Run("R-REPO-049 the list is the same on both replicas", func(t *testing.T) {
		if _, err := a.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "second-one"))); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "B to list both repositories", within, func() bool {
			l, err := b.reader.List(ctx)
			return err == nil && len(l) == 3 && l[0].Key == "fresh-key" && l[1].Key == "second-one" && l[2].Key == "unknown-key"
		})
	})

	t.Run("R-ARCH-018 a change committed after the snapshot was loaded is not lost to a late fill", func(t *testing.T) {
		// Build B's snapshot, then change through A and read through B's snapshot
		// path repeatedly: every read after the poll applied the change sees it.
		if _, err := b.reader.List(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := a.svc.Patch(ctx, caller, "second-one", repository.MatchAny(), mergePatch(`{"disabled":true}`)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "B to see the change", within, func() bool {
			r, err := b.reader.ByKey(ctx, "second-one")
			return err == nil && r.Disabled
		})
		for range 50 {
			if r, _ := b.reader.ByKey(ctx, "second-one"); !r.Disabled {
				t.Fatal("B served the old snapshot after it applied the change")
			}
		}
	})
}
