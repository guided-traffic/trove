//go:build integration

package proxy

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
)

// lateItems answers the first lookup of an item with a miss, as a statement that ran before a concurrent fill committed does.
type lateItems struct {
	proxy.ItemStore
	missed atomic.Bool
}

func (l *lateItems) Live(ctx context.Context, repo items.RepoRef, path string) (items.Item, bool, error) {
	if l.missed.CompareAndSwap(false, true) {
		return items.Item{}, false, nil
	}
	return l.ItemStore.Live(ctx, repo, path)
}

// R-REPO-026, R-ARCH-027: a request whose item lookup ran just before a fill on another replica committed finds the cache entry
// afterwards. The item and the entry of a fill become visible together, so the request reads the item again and serves it; it is
// never answered with the 404 of an entry without its item, which would fail every client that raced a fill.
func TestHitRace_ALookupBeforeACommitAndAnEntryAfterItIsServed(t *testing.T) {
	e := newEnv(t)
	const path = "pkg/-/pkg-1.0.0.tgz"
	tarball := bytes.Repeat([]byte("x"), 4096)
	up := e.upstream()
	up.Set("/"+path, proxytest.Route{Body: tarball, Type: "application/octet-stream"})
	rem := e.remote("hit-race", up)
	a := e.newReplica("a", replicaOpts{})
	if s := a.artifact(rem, path); s.status != 200 {
		t.Fatalf("fill: %d (%v)", s.status, s.err)
	}
	late := &lateItems{}
	b := e.newReplica("b", replicaOpts{items: func(s proxy.ItemStore) proxy.ItemStore { late.ItemStore = s; return late }})
	s := b.artifact(rem, path)
	if !late.missed.Load() {
		t.Fatal("the late lookup was not exercised")
	}
	if s.status != 200 || !bytes.Equal(s.body, tarball) {
		t.Fatalf("the request that read the item before the commit: %d (%v)", s.status, s.err)
	}
	if n := up.Count("GET", "/"+path); n != 1 {
		t.Errorf("the upstream saw %d GETs, want 1", n)
	}
}
