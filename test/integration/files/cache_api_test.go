//go:build integration

package files

import (
	"net/url"
	"regexp"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/authz"
)

// seedRemote inserts a repository of a class and, for a remote one, n metadata
// entries pkg-00 … that expire in an hour.
func (e *env) seedRemote(key, class string, n int) int64 {
	e.t.Helper()
	id := e.number(`INSERT INTO repositories (key, format, class) VALUES ($1, 'npm', $2) RETURNING id`, key, class)
	if n > 0 {
		e.exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size)
		        VALUES (decode(repeat('ab', 32), 'hex'), decode(repeat('cd', 20), 'hex'), decode(repeat('ef', 16), 'hex'), decode(repeat('01', 64), 'hex'), 10)
		        ON CONFLICT DO NOTHING`)
		e.exec(`INSERT INTO remote_cache_entries (repo_id, path, variant, kind, blob_sha256, upstream_url, fetched_at, validated_at, expires_at, last_used_at)
		        SELECT $1, 'pkg-' || lpad(g::text, 2, '0'), 'full', 'metadata', decode(repeat('ab', 32), 'hex'), 'https://registry.example/pkg',
		               now() - interval '1 hour', now() - interval '1 hour', now() + interval '1 hour', now() - interval '1 hour'
		          FROM generate_series(1, $2::int) g`, id, n)
	}
	return id
}

// TestR_API_024_CacheOperations drives the cache operations through the whole chain
// of a web replica with the real decision point: who may invalidate and list, and
// what the answers are for each class of repository.
func TestR_API_024_CacheOperations(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	e.user("manager")
	e.user("reader")
	val := apitest.New(t, v1.EmbeddedYAML())
	a := e.newReplica("a", replicaOpts{validate: val})
	b := e.newReplica("b", replicaOpts{validate: val})
	a.createRepository("generic-local", nil)
	e.seedRemote("npm-remote", "remote", 25)
	e.seedRemote("npm-virtual", "virtual", 0)
	e.grant(a, "cache-manage", []string{"npm-remote", "npm-virtual", "generic-local"}, authz.PatternLists{},
		map[string][]string{"user:manager": {"read", "manage"}, "user:reader": {"read"}})
	for _, r := range []*replica{a, b} {
		waitFor(t, "the policy on "+r.name, 10*time.Second, func() bool {
			return r.get("manager", "/api/v1/repositories/npm-remote").status == 200 && r.get("reader", "/api/v1/repositories/npm-virtual").status == 200
		})
	}
	const invalidate, entries = "/api/v1/repositories/npm-remote/cache/invalidate", "/api/v1/repositories/npm-remote/cache/entries"

	t.Run("R-API-024 a caller with read only is answered 403 on both operations", func(t *testing.T) {
		problemOf(t, a.postJSON("reader", invalidate, map[string]any{"path": "pkg"}), 403, "forbidden")
		problemOf(t, b.get("reader", entries), 403, "forbidden")
		if got := e.number(`SELECT count(*) FROM remote_cache_invalidations`); got != 0 {
			t.Errorf("%d rows after a refused call", got)
		}
	})
	t.Run("R-API-024 an anonymous caller is answered 401", func(t *testing.T) {
		problemOf(t, a.postJSON("", invalidate, map[string]any{}), 401, "unauthenticated")
		problemOf(t, a.get("", entries), 401, "unauthenticated")
	})
	t.Run("R-API-024 a caller without any grant is answered 403, and a missing repository is decided like a missing grant", func(t *testing.T) {
		e.user("stranger")
		problemOf(t, a.postJSON("stranger", invalidate, map[string]any{}), 403, "forbidden")
		problemOf(t, a.postJSON("manager", "/api/v1/repositories/no-such-repo/cache/invalidate", map[string]any{}), 403, "forbidden")
	})
	t.Run("R-API-024 a local repository answers 400 not_applicable, for the invalidation and for the list", func(t *testing.T) {
		problemOf(t, a.postJSON("manager", "/api/v1/repositories/generic-local/cache/invalidate", map[string]any{}), 400, "not_applicable")
		problemOf(t, a.get("manager", "/api/v1/repositories/generic-local/cache/entries"), 400, "not_applicable")
		problemOf(t, a.get("manager", "/api/v1/repositories/npm-virtual/cache/entries"), 400, "not_applicable")
	})
	t.Run("R-API-024 R-OPS-021 a holder of manage invalidates a remote repository and learns the covered entries", func(t *testing.T) {
		res := a.postJSON("manager", invalidate, map[string]any{"path": "pkg-0", "scope": "metadata"})
		if res.status != 200 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		// pkg-01 … pkg-09 share the string prefix but not the segment: the prefix pkg-0
		// covers a path only when it is the path or its leading segments.
		if got := res.json(t)["invalidated"]; got != float64(0) {
			t.Errorf("invalidated = %v; pkg-0 is a segment that no path has", got)
		}
		res = b.postJSON("manager", invalidate, map[string]any{"path": "pkg-07"})
		if res.status != 200 || res.json(t)["invalidated"] != float64(1) {
			t.Fatalf("%d %s", res.status, res.body)
		}
		res = a.postJSON("manager", invalidate, map[string]any{})
		if res.status != 200 || res.json(t)["invalidated"] != float64(25) {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if got := e.number(`SELECT count(*) FROM remote_cache_invalidations`); got != 3 {
			t.Errorf("%d rows", got)
		}
		if got := len(a.rec.ofType("repository.cache_invalidated")) + len(b.rec.ofType("repository.cache_invalidated")); got != 3 {
			t.Errorf("%d audit events", got)
		}
	})
	t.Run("R-API-024 the system administrator invalidates without a grant, and a virtual repository answers 0", func(t *testing.T) {
		res := a.postJSON("admin", invalidate, map[string]any{"scope": "negative"})
		if res.status != 200 || res.json(t)["invalidated"] != float64(0) {
			t.Fatalf("%d %s", res.status, res.body)
		}
		res = b.postJSON("manager", "/api/v1/repositories/npm-virtual/cache/invalidate", map[string]any{"path": "@acme", "scope": "all"})
		if res.status != 200 || res.json(t)["invalidated"] != float64(0) {
			t.Fatalf("%d %s", res.status, res.body)
		}
	})
	t.Run("R-REPO-045 a bad prefix or scope is 422 with the field, a foreign body 400", func(t *testing.T) {
		violation(t, a.postJSON("manager", invalidate, map[string]any{"path": "a//b"}), "invalid_value")
		violation(t, a.postJSON("manager", invalidate, map[string]any{"scope": "some"}), "invalid_value")
		problemOf(t, a.postJSON("manager", invalidate, map[string]any{"colour": "red"}), 400, "malformed_request")
	})
	t.Run("R-REPO-032 the entry list pages over two replicas without a gap or a repeat, and the administrator may read it", func(t *testing.T) {
		seen := map[string]bool{}
		next := entries + "?limit=10"
		pages := 0
		for next != "" {
			rep := a
			if pages%2 == 1 {
				rep = b
			}
			res := rep.get("manager", next)
			if res.status != 200 {
				t.Fatalf("%d %s", res.status, res.body)
			}
			pages++
			var items []any
			m := res.json(t)
			items, _ = m["items"].([]any)
			for _, it := range items {
				p := it.(map[string]any)["path"].(string)
				if seen[p] {
					t.Fatalf("%s listed twice", p)
				}
				seen[p] = true
			}
			next = ""
			if c, ok := m["nextCursor"].(string); ok {
				if link := res.header.Get("Link"); !regexp.MustCompile(`rel="next"`).MatchString(link) {
					t.Errorf("Link = %q", link)
				}
				next = entries + "?limit=10&cursor=" + url.QueryEscape(c)
			}
		}
		if len(seen) != 25 || pages != 3 {
			t.Errorf("%d entries on %d pages", len(seen), pages)
		}
		res := a.get("admin", entries+"?pathPrefix=pkg-02")
		if res.status != 200 || len(res.json(t)["items"].([]any)) != 1 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		problemOf(t, a.get("manager", entries+"?kind=everything"), 400, "malformed_request")
		problemOf(t, a.get("manager", entries+"?cursor=abc"), 400, "invalid_cursor")
	})
	t.Run("R-API-002 R-API-024 the answers of both operations were validated against the document", func(t *testing.T) {
		if missing := val.Uncovered("invalidateRepositoryCache", "listCacheEntries"); len(missing) > 0 {
			t.Fatalf("not reached: %v", missing)
		}
	})
}
