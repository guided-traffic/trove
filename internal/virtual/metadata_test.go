package virtual_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

// entries is the number of entries in the merged-metadata cache of the world.
func entries(w *world) int {
	for _, s := range w.caches.Stats() {
		if s.Name == virtual.CacheMerged {
			return s.Entries
		}
	}
	return -1
}

func versionsOf(t *testing.T, m *virtual.Merged, err error) map[string]string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return vt.Versions(m.Bytes)
}

func upstream(w *world, pkg string, expires time.Duration, versions ...string) {
	w.remote.SetDoc("npm-remote", pkg, versions, future(w, expires), true)
}

func TestR_REPO_020_MergedMetadata(t *testing.T) {
	t.Run("R-REPO-020 a document is merged from the members and the version comes from the member the tiers would pick", func(t *testing.T) {
		w := newWorld(t)
		w.rows.Publish(idLocal, "lodash", "1.0.0")
		upstream(w, "lodash", time.Hour, "1.0.0", "4.17.21")
		m, err := w.metadata(alice, "lodash")
		v := versionsOf(t, m, err)
		if v["1.0.0"] != "npm-local" || v["4.17.21"] != "npm-remote" {
			t.Fatalf("merged = %v", v)
		}
	})

	t.Run("R-REPO-020 a version whose artifact is cached comes from the remote member that caches it", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.members = []repository.Repository{remote2, remote} })
		w.remote.SetDoc("npm-remote", "p", []string{"1.0.0"}, future(w, time.Hour), true)
		w.remote.SetDoc("npm-remote2", "p", []string{"1.0.0"}, future(w, time.Hour), true)
		w.remote.CacheArtifact("npm-remote", "p/-/1.0.0.tgz")
		m, err := w.metadata(alice, "p")
		if v := versionsOf(t, m, err); v["1.0.0"] != "npm-remote" {
			t.Fatalf("merged = %v, want the cached member although npm-remote2 comes first", v)
		}
	})

	t.Run("R-REPO-020 two callers with different readable members get different merges, in either order", func(t *testing.T) {
		for name, order := range map[string][]string{"alice first": {"alice", "bob"}, "bob first": {"bob", "alice"}} {
			t.Run(name, func(t *testing.T) {
				w := newWorld(t)
				w.rows.Publish(idLocal, "lodash", "1.0.0")
				upstream(w, "lodash", time.Hour, "4.17.21")
				got := map[string]map[string]string{}
				for _, who := range order {
					sub := map[string]authz.Subject{"alice": alice, "bob": bob}[who]
					m, err := w.metadata(sub, "lodash")
					got[who] = versionsOf(t, m, err)
				}
				if len(got["alice"]) != 2 || got["alice"]["4.17.21"] != "npm-remote" {
					t.Fatalf("alice = %v", got["alice"])
				}
				if len(got["bob"]) != 1 || got["bob"]["1.0.0"] != "npm-local" {
					t.Fatalf("bob = %v: a version that exists only upstream leaked", got["bob"])
				}
			})
		}
	})

	t.Run("R-REPO-020 callers who see the same members share an entry", func(t *testing.T) {
		w := newWorld(t)
		w.rows.Publish(idLocal, "lodash", "1.0.0")
		upstream(w, "lodash", time.Hour, "4.17.21")
		first, err := w.metadata(alice, "lodash")
		if err != nil {
			t.Fatal(err)
		}
		second, err := w.metadata(carol, "lodash") // same members, same fill right (read)
		if err != nil {
			t.Fatal(err)
		}
		if n := w.merger.Merges.Load(); n != 1 {
			t.Fatalf("Merge ran %d times", n)
		}
		if !strings.HasPrefix(second.CacheStatus, "trove; hit; ttl=") || first.CacheStatus != "trove; fwd=miss" || first.MergeFingerprint != second.MergeFingerprint {
			t.Fatalf("status %q / %q", first.CacheStatus, second.CacheStatus)
		}
		if entries(w) != 1 {
			t.Fatalf("entries = %d", entries(w))
		}
	})

	t.Run("R-REPO-020 a merged entry of one virtual repository, path, variant or endpoint is not another's", func(t *testing.T) {
		w := newWorld(t)
		w.rows.Publish(idLocal, "lodash", "1.0.0")
		w.rows.Publish(idLocal, "react", "1.0.0")
		c, p := w.plan(alice, metadataOf("lodash"))
		if _, err := w.r.Metadata(t.Context(), c, p, w.merger); err != nil {
			t.Fatal(err)
		}
		d := metadataOf("lodash")
		d.Variant = "abbreviated"
		_, p2 := w.plan(alice, d)
		if _, err := w.r.Metadata(t.Context(), c, p2, w.merger); err != nil {
			t.Fatal(err)
		}
		if _, err := w.metadata(alice, "react"); err != nil {
			t.Fatal(err)
		}
		if entries(w) != 3 {
			t.Fatalf("entries = %d, want one per path and variant", entries(w))
		}
	})
}

func TestR_REPO_020_RevSeqCheck(t *testing.T) {
	t.Run("R-REPO-020 a local publish is visible on the next request without any notification", func(t *testing.T) {
		// No signals are wired at all: the same as a replica whose Valkey client is unavailable.
		w := newWorld(t)
		w.rows.Publish(idLocal, "@acme/util", "1.0.0")
		if _, err := w.metadata(alice, "@acme/util"); err != nil {
			t.Fatal(err)
		}
		before := w.rows.RevQueries.Load()
		m, err := w.metadata(alice, "@acme/util")
		if v := versionsOf(t, m, err); len(v) != 1 || w.rows.RevQueries.Load() != before+1 || w.merger.Merges.Load() != 1 {
			t.Fatalf("hit: versions %v, rev queries %d, merges %d", v, w.rows.RevQueries.Load()-before, w.merger.Merges.Load())
		}
		w.rows.Publish(idLocal, "@acme/util", "1.5.0")
		m, err = w.metadata(alice, "@acme/util")
		if v := versionsOf(t, m, err); v["1.5.0"] != "npm-local" || w.merger.Merges.Load() != 2 {
			t.Fatalf("after the publish: %v, merges %d", v, w.merger.Merges.Load())
		}
	})

	t.Run("R-REPO-020 one RevSeqs query per request that finds an entry", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.members = []repository.Repository{local, local2, remote} })
		w.v = vt.VirtualRepo(5, "npm-all", 7200, nil, local, local2, remote)
		w.rows.Publish(idLocal, "p", "1.0.0")
		if _, err := w.metadata(alice, "p"); err != nil {
			t.Fatal(err)
		}
		before := w.rows.RevQueries.Load()
		if _, err := w.metadata(alice, "p"); err != nil {
			t.Fatal(err)
		}
		if got := w.rows.RevQueries.Load() - before; got != 1 {
			t.Fatalf("%d queries for two local members", got)
		}
	})

	t.Run("R-REPO-020 the first local publish of a name that so far came only from a remote member is visible", func(t *testing.T) {
		w := newWorld(t)
		upstream(w, "lodash", time.Hour, "4.17.21")
		m, err := w.metadata(alice, "lodash")
		if v := versionsOf(t, m, err); len(v) != 1 {
			t.Fatalf("merged = %v", v)
		}
		w.rows.Publish(idLocal, "lodash", "99.0.0") // rev_seq 0 -> 1
		m, err = w.metadata(alice, "lodash")
		if v := versionsOf(t, m, err); v["99.0.0"] != "npm-local" || v["4.17.21"] != "npm-remote" {
			t.Fatalf("after the first publish: %v", v)
		}
	})

	t.Run("R-REPO-020 a version removed from a local member leaves the next merge", func(t *testing.T) {
		w := newWorld(t)
		w.rows.Publish(idLocal, "p", "1.0.0")
		w.rows.Publish(idLocal, "p", "2.0.0")
		if _, err := w.metadata(alice, "p"); err != nil {
			t.Fatal(err)
		}
		w.rows.Unpublish(idLocal, "p", "2.0.0")
		m, err := w.metadata(alice, "p")
		if v := versionsOf(t, m, err); len(v) != 1 || v["1.0.0"] == "" {
			t.Fatalf("merged = %v", v)
		}
	})

	t.Run("R-ARCH-015 a publish on one replica is listed on the other at once, 50 times, without a signal", func(t *testing.T) {
		rows, remoteEngine := vt.NewRows(), vt.NewRemote()
		share := func(o *worldOpts) { o.rows, o.remote = rows, remoteEngine }
		a, b := newWorld(t, share), newWorld(t, share)
		rows.Publish(idLocal, "@acme/lib", "0.0.0")
		for _, w := range []*world{a, b} {
			if _, err := w.metadata(alice, "@acme/lib"); err != nil {
				t.Fatal(err)
			}
		}
		for i := 1; i <= 50; i++ {
			v := fmt.Sprintf("1.0.%d", i)
			rows.Publish(idLocal, "@acme/lib", v) // the publish "on replica A"
			m, err := b.metadata(alice, "@acme/lib")
			if got := versionsOf(t, m, err); got[v] != "npm-local" {
				t.Fatalf("iteration %d: replica B lists %v", i, got)
			}
		}
	})
}

func TestR_REPO_020_PartialMerge(t *testing.T) {
	t.Run("R-REPO-020 a remote member that fails and has no copy is left out, the result is partial and not cached", func(t *testing.T) {
		w := newWorld(t)
		w.rows.Publish(idLocal, "lodash", "1.0.0")
		w.remote.MetadataErr["npm-remote"] = errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "down")
		m, err := w.metadata(alice, "lodash")
		if v := versionsOf(t, m, err); len(v) != 1 || !m.Partial || m.CacheStatus != "trove; detail=partial" {
			t.Fatalf("merged %v partial %v status %q", v, m.Partial, m.CacheStatus)
		}
		if entries(w) != 0 {
			t.Fatalf("a partial merge was cached (%d entries)", entries(w))
		}
		if _, err := w.metadata(alice, "lodash"); err != nil || w.merger.Merges.Load() != 2 {
			t.Fatalf("the second request did not merge again: %d merges, err %v", w.merger.Merges.Load(), err)
		}

		delete(w.remote.MetadataErr, "npm-remote")
		upstream(w, "lodash", time.Hour, "4.17.21")
		m, err = w.metadata(alice, "lodash")
		if v := versionsOf(t, m, err); len(v) != 2 || m.Partial || entries(w) != 1 {
			t.Fatalf("after the upstream came back: %v partial %v entries %d", v, m.Partial, entries(w))
		}
	})

	t.Run("R-REPO-019 step 7 only the failing member knows the package: 502, not 404", func(t *testing.T) {
		w := newWorld(t)
		w.remote.MetadataErr["npm-remote"] = errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "down")
		_, err := w.metadata(alice, "lodash")
		if k, _ := kind(err); k != errs.UpstreamFailed {
			t.Fatalf("err = %v", err)
		}
		w.remote.MetadataErr["npm-remote"] = errs.E(errs.UpstreamTimeout, errs.CodeUpstreamTimeout, "slow")
		if _, err = w.metadata(alice, "lodash"); err == nil {
			t.Fatal("answered")
		}
		if k, _ := kind(err); k != errs.UpstreamTimeout {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-REPO-019 step 7 every member said not found: 404", func(t *testing.T) {
		w := newWorld(t)
		_, err := w.metadata(alice, "lodash")
		if k, code := kind(err); k != errs.NotFound || code != errs.CodeNotFound {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-REPO-020 a member that does not know the package is no reason for a partial merge", func(t *testing.T) {
		w := newWorld(t)
		w.rows.Publish(idLocal, "internal-only", "1.0.0")
		m, err := w.metadata(alice, "internal-only")
		if versionsOf(t, m, err); m.Partial || entries(w) != 1 {
			t.Fatalf("partial %v entries %d", m.Partial, entries(w))
		}
	})

	t.Run("R-REPO-020 a replica without the remote engine merges what it has and says partial", func(t *testing.T) {
		w := newWorld(t)
		w.rows.Publish(idLocal, "p", "1.0.0")
		caches, err := cache.NewRegistry(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		r, err := virtual.New(virtual.Options{Router: w.router, Decider: w.authz, Locals: w.rows, Caches: caches, CacheBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		c, p := w.plan(alice, metadataOf("p"))
		m, err := r.Metadata(t.Context(), c, p, w.merger)
		if v := versionsOf(t, m, err); len(v) != 1 || !m.Partial {
			t.Fatalf("merged %v partial %v", v, m.Partial)
		}
	})
}

func TestR_REPO_020_Fingerprint(t *testing.T) {
	populate := func(w *world) {
		w.rows.Publish(idLocal, "lodash", "1.0.0")
		upstream(w, "lodash", time.Hour, "1.0.0", "2.0.0")
		w.remote.CacheArtifact("npm-remote", "lodash/-/2.0.0.tgz")
	}

	t.Run("D-7 two callers of equal read and different cache-fill right get different merges, in either order", func(t *testing.T) {
		// npm-remote is a frozen mirror (cacheFillAction deploy): carol may fill it, alice may not.
		for name, order := range map[string][]string{"alice first": {"alice", "carol"}, "carol first": {"carol", "alice"}} {
			t.Run(name, func(t *testing.T) {
				w := newWorld(t, func(o *worldOpts) { o.fill = "deploy" })
				populate(w)
				// 3.0.0 exists upstream and is not cached.
				w.remote.SetDoc("npm-remote", "lodash", []string{"2.0.0", "3.0.0"}, future(w, time.Hour), true)
				got := map[string]map[string]string{}
				for _, who := range order {
					sub := map[string]authz.Subject{"alice": alice, "carol": carol}[who]
					m, err := w.metadata(sub, "lodash")
					got[who] = versionsOf(t, m, err)
				}
				if _, ok := got["alice"]["3.0.0"]; ok {
					t.Fatalf("alice may not fill the mirror and was offered an uncached version: %v", got["alice"])
				}
				if got["alice"]["2.0.0"] != "npm-remote" || got["alice"]["1.0.0"] != "npm-local" {
					t.Fatalf("alice = %v", got["alice"])
				}
				if got["carol"]["3.0.0"] != "npm-remote" {
					t.Fatalf("carol = %v", got["carol"])
				}
				ca, pa := w.plan(alice, metadataOf("lodash"))
				cc, pc := w.plan(carol, metadataOf("lodash"))
				_, _ = ca, cc
				if pa.Fingerprint == pc.Fingerprint {
					t.Fatal("the plan fingerprint ignores the cache-fill right")
				}
				if entries(w) != 2 {
					t.Fatalf("entries = %d, want one per fingerprint", entries(w))
				}
			})
		}
	})

	t.Run("D-7 the endpoint is part of the fingerprint: an endpoint that does not fill caches restricts the merge", func(t *testing.T) {
		w := newWorld(t)
		populate(w)
		w.remote.SetDoc("npm-remote", "lodash", []string{"2.0.0", "3.0.0"}, future(w, time.Hour), true)
		fills, closed := alice, alice
		fills.Endpoint, fills.CacheFill = "default", true
		closed.Endpoint, closed.CacheFill = "customers", false
		m1, err := w.metadata(fills, "lodash")
		v1 := versionsOf(t, m1, err)
		m2, err := w.metadata(closed, "lodash")
		v2 := versionsOf(t, m2, err)
		if v1["3.0.0"] != "npm-remote" {
			t.Fatalf("default endpoint = %v", v1)
		}
		if _, ok := v2["3.0.0"]; ok {
			t.Fatalf("an endpoint with cache_fill false lists an uncached version: %v", v2)
		}
		_, p1 := w.plan(fills, metadataOf("lodash"))
		_, p2 := w.plan(closed, metadataOf("lodash"))
		if p1.Fingerprint == p2.Fingerprint || m1.MergeFingerprint == m2.MergeFingerprint {
			t.Fatal("the endpoint does not change the fingerprint")
		}
		// And the same endpoint shares.
		if _, err := w.metadata(fills, "lodash"); err != nil || w.merger.Merges.Load() != 2 {
			t.Fatalf("merges = %d", w.merger.Merges.Load())
		}
	})

	t.Run("D-7 the fingerprint is stable for the same members and rights, and changes with the order", func(t *testing.T) {
		w := newWorld(t)
		_, a := w.plan(alice, metadataOf("p"))
		_, b := w.plan(carol, metadataOf("p"))
		if a.Fingerprint != b.Fingerprint {
			t.Fatal("equal members and rights, different fingerprint")
		}
		w2 := newWorld(t, func(o *worldOpts) { o.members = []repository.Repository{remote, local} })
		_, c := w2.plan(alice, metadataOf("p"))
		if a.Fingerprint == c.Fingerprint {
			t.Fatal("a different order has the same fingerprint")
		}
	})

	t.Run("D-7 the merge fingerprint changes with the cached versions the merge may take", func(t *testing.T) {
		w := newWorld(t)
		upstream(w, "p", time.Hour, "1.0.0")
		m1, err := w.metadata(alice, "p")
		if err != nil {
			t.Fatal(err)
		}
		w.caches.FlushAll("admin")
		w.remote.CacheArtifact("npm-remote", "p/-/1.0.0.tgz")
		m2, err := w.metadata(alice, "p")
		if err != nil || m1.MergeFingerprint == m2.MergeFingerprint {
			t.Fatalf("fingerprints equal for different sources (%v)", err)
		}
	})
}

func TestR_REPO_020_Expiry(t *testing.T) {
	t.Run("R-REPO-020 an entry expires at the earliest expiry of the remote documents it was built from", func(t *testing.T) {
		w := newWorld(t)
		upstream(w, "p", 10*time.Second, "1.0.0")
		for _, step := range []struct {
			advance time.Duration
			merges  int64
		}{{0, 1}, {5 * time.Second, 1}, {6 * time.Second, 2}} {
			w.advance(step.advance)
			if _, err := w.metadata(alice, "p"); err != nil || w.merger.Merges.Load() != step.merges {
				t.Fatalf("after %v: merges %d err %v, want %d", step.advance, w.merger.Merges.Load(), err, step.merges)
			}
		}
	})

	t.Run("R-REPO-020 an entry expires mergedMetadataTtlSeconds after the merge", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.ttl = 60 })
		w.rows.Publish(idLocal, "p", "1.0.0")
		upstream(w, "p", time.Hour, "2.0.0")
		_, _ = w.metadata(alice, "p")
		w.advance(59 * time.Second)
		_, _ = w.metadata(alice, "p")
		if w.merger.Merges.Load() != 1 {
			t.Fatal("expired early")
		}
		w.advance(2 * time.Second)
		_, _ = w.metadata(alice, "p")
		if w.merger.Merges.Load() != 2 {
			t.Fatal("did not expire at the period")
		}
	})

	t.Run("R-REPO-020 a period of 0 keeps nothing", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.ttl = 0 })
		w.rows.Publish(idLocal, "p", "1.0.0")
		_, _ = w.metadata(alice, "p")
		_, _ = w.metadata(alice, "p")
		if w.merger.Merges.Load() != 2 || entries(w) != 0 {
			t.Fatalf("merges %d entries %d", w.merger.Merges.Load(), entries(w))
		}
	})

	t.Run("R-REPO-020 a stale remote document is served and never kept", func(t *testing.T) {
		w := newWorld(t)
		upstream(w, "p", -time.Minute, "1.0.0")
		m, err := w.metadata(alice, "p")
		if v := versionsOf(t, m, err); len(v) != 1 || entries(w) != 0 {
			t.Fatalf("merged %v entries %d", v, entries(w))
		}
	})

	t.Run("R-REPO-020 a path that belongs to no package is merged on every request", func(t *testing.T) {
		w := newWorld(t)
		d := virtual.Descriptor{Format: vt.Format, Path: "-/ping", Kind: "metadata"}
		w.rows.Publish(idLocal, "", "1.0.0")
		for range 2 {
			c, p := w.plan(alice, d)
			_, _ = w.r.Metadata(t.Context(), c, p, w.merger)
		}
		if entries(w) != 0 {
			t.Fatalf("entries = %d", entries(w))
		}
	})

	t.Run("R-REPO-020 proxy.merged_metadata_cache_bytes of 0 turns the cache off", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.cacheBytes = 0 })
		w.rows.Publish(idLocal, "p", "1.0.0")
		_, _ = w.metadata(alice, "p")
		_, _ = w.metadata(alice, "p")
		if w.merger.Merges.Load() != 2 {
			t.Fatalf("merges = %d", w.merger.Merges.Load())
		}
	})
}

func TestR_REPO_020_EarlyEviction(t *testing.T) {
	prime := func(t *testing.T) *world {
		w := newWorld(t, func(o *worldOpts) { o.signals = true })
		w.rows.Publish(idLocal, "lodash", "1.0.0")
		w.rows.Publish(idLocal, "react", "1.0.0")
		upstream(w, "lodash", time.Hour, "4.17.21")
		for _, p := range []string{"lodash", "react"} {
			if _, err := w.metadata(alice, p); err != nil {
				t.Fatal(err)
			}
		}
		if entries(w) != 2 {
			t.Fatalf("entries = %d", entries(w))
		}
		return w
	}

	t.Run("R-REPO-020 metadata_changed drops the entries of the changed member and package", func(t *testing.T) {
		w := prime(t)
		w.sig.deliver([]string{virtual.PackageChanged(idRemote, "lodash")}, false)
		if entries(w) != 1 {
			t.Fatalf("entries = %d, want only react", entries(w))
		}
		// A change of a member that does not take part, or of another package, evicts nothing.
		w.sig.deliver([]string{virtual.PackageChanged(idRemote2, "react"), virtual.PackageChanged(idRemote, "left-pad")}, false)
		if entries(w) != 1 {
			t.Fatalf("entries = %d", entries(w))
		}
	})

	t.Run("R-REPO-020 a coarse signal and a payload this build cannot read drop everything", func(t *testing.T) {
		w := prime(t)
		w.sig.deliver(nil, true)
		if entries(w) != 0 {
			t.Fatalf("coarse: entries = %d", entries(w))
		}
		w = prime(t)
		w.sig.deliver([]string{"not a payload"}, false)
		if entries(w) != 0 {
			t.Fatalf("garbled: entries = %d", entries(w))
		}
	})

	t.Run("R-REPO-020 a virtual invalidation drops the entries of that repository under the prefix, whole segments only", func(t *testing.T) {
		w := prime(t)
		payload := func(p invalidation.Payload) {
			raw, _ := json.Marshal(p)
			w.caches.ApplyChange(cluster.AreaCacheInvalidation, invalidation.PayloadVersion, raw)
		}
		payload(invalidation.Payload{Kind: invalidation.KindVirtual, Repo: 5, Prefix: invalidation.EncodePrefix("lo")})
		if entries(w) != 2 {
			t.Fatalf("a prefix that is no segment boundary dropped entries: %d", entries(w))
		}
		payload(invalidation.Payload{Kind: invalidation.KindVirtual, Repo: 99, Prefix: invalidation.EncodePrefix("lodash")})
		if entries(w) != 2 {
			t.Fatalf("another repository's invalidation dropped entries: %d", entries(w))
		}
		payload(invalidation.Payload{Kind: invalidation.KindVirtual, Repo: 5, Prefix: invalidation.EncodePrefix("lodash")})
		if entries(w) != 1 {
			t.Fatalf("entries = %d", entries(w))
		}
		payload(invalidation.Payload{Kind: invalidation.KindVirtual, Repo: 5})
		if entries(w) != 0 {
			t.Fatalf("the whole repository: entries = %d", entries(w))
		}
	})

	t.Run("R-REPO-045 an invalidation of a remote repository drops the merges built from it", func(t *testing.T) {
		w := prime(t)
		raw, _ := json.Marshal(invalidation.Payload{Kind: invalidation.KindAdd, Repo: idRemote, ID: 1, Prefix: invalidation.EncodePrefix("lodash"), Scope: "metadata"})
		w.caches.ApplyChange(cluster.AreaCacheInvalidation, invalidation.PayloadVersion, raw)
		if entries(w) != 1 {
			t.Fatalf("entries = %d", entries(w))
		}
	})

	t.Run("R-REPO-023 a change of claims or of the repository configuration drops every entry", func(t *testing.T) {
		for _, area := range []string{cluster.AreaClaims, cluster.AreaRepoConfig} {
			w := prime(t)
			w.caches.ApplyChange(area, 1, json.RawMessage(`{}`))
			if entries(w) != 0 {
				t.Fatalf("%s: entries = %d", area, entries(w))
			}
		}
	})

	t.Run("R-ARCH-018 the cache is the registered merged_metadata cache with a safety TTL", func(t *testing.T) {
		w := prime(t)
		found := false
		for _, s := range w.caches.Stats() {
			found = found || s.Name == virtual.CacheMerged
		}
		if !found {
			t.Fatal("not registered")
		}
	})
}

func TestMetadataFirstMatch(t *testing.T) {
	t.Run("R-REPO-019 a path resolved by FirstMatch is served from the first member that has it", func(t *testing.T) {
		w := newWorld(t)
		w.merger.Mode = virtual.FirstMatch
		w.rows.Publish(idLocal, "p", "1.0.0")
		upstream(w, "p", time.Hour, "9.9.9")
		m, err := w.metadata(alice, "p")
		if err != nil || !strings.Contains(string(m.Bytes), "1.0.0") || strings.Contains(string(m.Bytes), "9.9.9") {
			t.Fatalf("%s %v", m.Bytes, err)
		}
		upstream(w, "q", time.Hour, "9.9.9")
		m, err = w.metadata(alice, "q")
		if err != nil || !strings.Contains(string(m.Bytes), "9.9.9") {
			t.Fatalf("%v %v", m, err)
		}
		if w.merger.Merges.Load() != 0 || entries(w) != 0 {
			t.Fatal("FirstMatch merged or cached")
		}
		w.remote.MetadataErr["npm-remote"] = errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "down")
		if _, err := w.metadata(alice, "nothing"); err == nil {
			t.Fatal("answered")
		} else if k, _ := kind(err); k != errs.UpstreamFailed {
			t.Fatalf("err = %v", err)
		}
	})
}
