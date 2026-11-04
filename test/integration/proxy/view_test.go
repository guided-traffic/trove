//go:build integration

package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestViewReadsWhatTheEngineStored is Q-ARCH-294 at the engine: the read-only view of the role api, built without upstream
// clients and without a blob store, gives the answers of the engine for the stored artifacts, the entries, the negative
// entries, the circuit and the offline mode, and sends no request to the upstream while it does.
func TestViewReadsWhatTheEngineStored(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	up.Set("/pkg/there", proxytest.Route{Body: []byte("{}"), Type: "application/json"})
	up.Set("/pkg/there/-/there-1.tgz", proxytest.Route{Body: proxytest.Body("there", 4096), Type: "application/octet-stream"})
	up.Set("/pkg/busy", proxytest.Route{Status: 500})
	rem := e.remote("view", up, func(s *repository.RemoteSettings) { s.Availability.FailureThreshold = 2 })
	r := e.newReplica("a", replicaOpts{})

	reg, err := cache.NewRegistry(r.changes, prometheus.NewRegistry()) // the change log invalidates the state like on a replica
	if err != nil {
		t.Fatal(err)
	}
	r.changes.AddListener(reg)
	view, err := proxy.NewView(proxy.Options{
		Runner: r.db, Items: r.items, Negatives: cluster.NewNegativeStore(r.vk), Lookups: r.vk, Caches: reg,
		Config: proxyConfig(t), SafetyTTL: time.Minute, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = view.Stop(context.Background()) })
	ctx := context.Background()
	rq := func(path string, kind proxy.Kind) proxy.Request { return r.request(rem, path, kind) }

	t.Run("Q-ARCH-294 before any fill the view finds nothing and asks nobody", func(t *testing.T) {
		if _, ok, err := view.Cached(ctx, rq("pkg/there/-/there-1.tgz", proxy.Artifact)); err != nil || ok {
			t.Fatalf("Cached = %v, %v", ok, err)
		}
		if st, err := view.EntryState(ctx, rem.ID(), "pkg/there"); err != nil || st.Stored {
			t.Fatalf("EntryState = %+v, %v", st, err)
		}
		if up.Total() != 0 {
			t.Fatalf("the view sent %d requests", up.Total())
		}
	})

	t.Run("Q-ARCH-294 after the engine filled, the view reads the artifact and the entry, like the engine", func(t *testing.T) {
		if s := r.metadata(rem, "pkg/there"); s.status != 200 {
			t.Fatalf("metadata %d", s.status)
		}
		if s := r.artifact(rem, "pkg/there/-/there-1.tgz"); s.status != 200 {
			t.Fatalf("artifact %d", s.status)
		}
		before := up.Total()
		it, ok, err := view.Cached(ctx, rq("pkg/there/-/there-1.tgz", proxy.Artifact))
		want, wantOK, _ := r.engine.Cached(ctx, rq("pkg/there/-/there-1.tgz", proxy.Artifact))
		if err != nil || !ok || !wantOK || it.ID != want.ID || it.Size != want.Size {
			t.Fatalf("Cached = %+v %v %v; the engine %+v %v", it, ok, err, want, wantOK)
		}
		got, err := view.EntryState(ctx, rem.ID(), "pkg/there")
		wantState, _ := r.engine.EntryState(ctx, rem.ID(), "pkg/there")
		if err != nil || got != wantState || !got.Stored {
			t.Fatalf("EntryState = %+v, %v; the engine %+v", got, err, wantState)
		}
		paths, err := view.CachedArtifacts(ctx, rem.ID(), "pkg/there/")
		if err != nil || len(paths) != 1 || paths[0] != "pkg/there/-/there-1.tgz" {
			t.Fatalf("CachedArtifacts = %v, %v", paths, err)
		}
		if up.Total() != before {
			t.Fatalf("the view sent %d requests", up.Total()-before)
		}
	})

	t.Run("Q-ARCH-294 the view sees the circuit the engine opened and the offline mode of the settings", func(t *testing.T) {
		_ = r.metadata(rem, "pkg/busy")
		_ = r.metadata(rem, "pkg/busy")
		waitFor(t, "the circuit to open", 2*time.Second, func() bool { return r.metric("trove_remote_upstream_up", "repo", "view") == 0 })
		var peek proxy.Peek
		waitFor(t, "the view to see the open circuit", 5*time.Second, func() bool {
			var err error
			if peek, err = view.Peek(ctx, rem, "pkg/other", ""); err != nil {
				t.Fatal(err)
			}
			return peek.CircuitOpenFor > 0
		})
		if peek.Offline {
			t.Error("the repository is online")
		}
		offline := rem
		offline.Settings.Availability.Mode = repository.AvailabilityOffline
		if p, err := view.Peek(ctx, offline, "pkg/other", ""); err != nil || !p.Offline {
			t.Fatalf("Peek of an offline repository = %+v, %v", p, err)
		}
	})
}
