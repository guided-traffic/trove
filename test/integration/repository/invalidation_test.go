//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/repository"
)

// The cache invalidation of remote repositories (R-REPO-045, R-REPO-032) against
// PostgreSQL: the insert with its count, the change-log row, the audit event, the
// fold into the metadata entries and the retention of the rows, each with replicas
// that have a pool and a set of their own and no Valkey.

// invReplica is a replica with the invalidation components.
type invReplica struct {
	*replica
	set  *invalidation.Set
	svc  *invalidation.Service
	fold *invalidation.Folder
}

// newInv adds the invalidation components to a replica; the clock is the folder's.
func (e *env) newInv(rep *replica, clock func() time.Time) *invReplica {
	e.t.Helper()
	set, err := invalidation.NewSet(invalidation.SetOptions{Runner: rep.db, Changes: rep.changes, RetryInterval: 50 * time.Millisecond, Log: quiet})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := set.Start(context.Background()); err != nil {
		e.t.Fatalf("set.Start: %v", err)
	}
	e.t.Cleanup(func() { _ = set.Stop(context.Background()) })
	svc, err := invalidation.NewService(invalidation.ServiceOptions{Runner: rep.db, Notifier: rep.changes, Recorder: rep.rec})
	if err != nil {
		e.t.Fatal(err)
	}
	fold, err := invalidation.NewFolder(invalidation.FolderOptions{Runner: rep.db, Notifier: rep.changes, Clock: clock, Log: quiet})
	if err != nil {
		e.t.Fatal(err)
	}
	return &invReplica{replica: rep, set: set, svc: svc, fold: fold}
}

// repoRow inserts a repository of a class and returns it as the service takes it.
func (e *env) repoRow(key string, class repository.Class) repository.Repository {
	e.t.Helper()
	var id int64
	if err := e.admin.QueryRow(context.Background(),
		`INSERT INTO repositories (key, format, class) VALUES ($1, 'npm', $2) RETURNING id`, key, string(class)).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return repository.Repository{ID: strconv.FormatInt(id, 10), Key: key, Class: class, Format: "npm", Lifecycle: repository.Active}
}

func repoID(r repository.Repository) int64 {
	n, _ := strconv.ParseInt(r.ID, 10, 64)
	return n
}

// liveBlob makes sure one live blob exists that cache entries can reference.
func (e *env) liveBlob() {
	e.t.Helper()
	e.exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size)
	        VALUES (decode(repeat('ab', 32), 'hex'), decode(repeat('cd', 20), 'hex'), decode(repeat('ef', 16), 'hex'), decode(repeat('01', 64), 'hex'), 10)
	        ON CONFLICT DO NOTHING`)
}

// entries stores n metadata entries pkg-0000001 … of a repository, fetched and
// validated one hour ago and expiring in one hour.
func (e *env) entries(repoID int64, n int) {
	e.t.Helper()
	e.liveBlob()
	e.exec(`INSERT INTO remote_cache_entries (repo_id, path, variant, kind, blob_sha256, upstream_url, fetched_at, validated_at, expires_at, last_used_at)
	        SELECT $1, 'pkg-' || lpad(g::text, 7, '0'), 'full', 'metadata', decode(repeat('ab', 32), 'hex'), 'https://registry.example/pkg',
	               now() - interval '1 hour', now() - interval '1 hour', now() + interval '1 hour', now() - interval '1 hour'
	          FROM generate_series(1, $2::int) g`, repoID, n)
}

// entry stores one metadata entry with the given validity.
func (e *env) entry(repoID int64, path, validated, expires string) {
	e.t.Helper()
	e.liveBlob()
	e.exec(`INSERT INTO remote_cache_entries (repo_id, path, variant, kind, blob_sha256, upstream_url, fetched_at, validated_at, expires_at, last_used_at)
	        VALUES ($1, $2, 'full', 'metadata', decode(repeat('ab', 32), 'hex'), 'https://registry.example/x',
	                now() - interval '1 hour', now() + $3::interval, now() + $4::interval, now() - interval '1 hour')`, repoID, path, validated, expires)
}

func (e *env) expiresAt(repoID int64, path string) time.Time {
	e.t.Helper()
	var t time.Time
	if err := e.admin.QueryRow(context.Background(), `SELECT expires_at FROM remote_cache_entries WHERE repo_id = $1 AND path = $2`, repoID, path).Scan(&t); err != nil {
		e.t.Fatal(err)
	}
	return t
}

func (e *env) invalidationRows() int64 {
	return e.scalar(`SELECT count(*) FROM remote_cache_invalidations`)
}

// createdAt is the creation time of an invalidation row.
func (e *env) createdAt(id int64) time.Time {
	e.t.Helper()
	var t time.Time
	if err := e.admin.QueryRow(context.Background(), `SELECT created_at FROM remote_cache_invalidations WHERE id = $1`, id).Scan(&t); err != nil {
		e.t.Fatal(err)
	}
	return t
}

func TestInvalidationAtScale(t *testing.T) {
	const entries = 1_000_000
	e := newEnv(t)
	a := e.newInv(e.newReplica("scale-a", replicaOpts{}), nil)
	b := e.newInv(e.newReplica("scale-b", replicaOpts{}), nil)
	repo := e.repoRow("npm-remote", repository.Remote)
	id := repoID(repo)
	seedStart := time.Now()
	e.entries(id, entries)
	e.exec(`VACUUM (ANALYZE) remote_cache_entries`) // the steady state of a cache: the visibility map is current
	t.Logf("seeded %d entries in %s", entries, time.Since(seedStart).Round(time.Millisecond))
	validated := time.Now().Add(-30 * time.Minute)

	t.Run("R-REPO-045 1,000,000 entries are invalidated in under one second with one row and one count", func(t *testing.T) {
		start := time.Now()
		n, err := a.svc.Invalidate(context.Background(), caller, repo, "", "metadata")
		took := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Invalidate of %d entries took %s", n, took.Round(time.Millisecond))
		if took >= time.Second {
			t.Errorf("Invalidate took %s; the budget is 1 s", took)
		}
		if n != entries {
			t.Errorf("invalidated = %d; want %d", n, entries)
		}
		if rows := e.invalidationRows(); rows != 1 {
			t.Errorf("%d invalidation rows; the call is one insert", rows)
		}
		if moved := e.scalar(`SELECT count(*) FROM remote_cache_entries WHERE expires_at < now() + interval '30 minutes'`); moved != 0 {
			t.Errorf("%d entries were touched by the call; the fold task does that later", moved)
		}
	})
	t.Run("R-REPO-045 the next request on every replica finds every cached package invalid, without a query", func(t *testing.T) {
		for _, r := range []*invReplica{a, b} {
			waitFor(t, r.name+" to hold the row", 5*time.Second, func() bool { return r.set.Len() == 1 })
			for _, p := range []string{"pkg-0000001", "pkg-0500000", "pkg-1000000"} {
				if r.set.MetadataValid(id, p, validated) {
					t.Errorf("%s: %s still counts as valid", r.name, p)
				}
			}
			if !r.set.MetadataValid(id, "pkg-0000001", time.Now().Add(time.Minute)) {
				t.Errorf("%s: an entry validated after the invalidation does not count", r.name)
			}
		}
	})
	t.Run("R-REPO-045 the change log and the audit trail record the call", func(t *testing.T) {
		if got := e.scalar(`SELECT count(*) FROM cluster_change WHERE area = 'cache_invalidation'`); got != 1 {
			t.Errorf("%d change rows", got)
		}
		ev := a.rec.last()
		var d map[string]any
		_ = json.Unmarshal(ev.Details, &d)
		if ev.Type != audit.EventRepositoryCacheInvalidated || ev.Repository != "npm-remote" || d["scope"] != "metadata" || d["path"] != "" || d["count"] != float64(entries) {
			t.Errorf("event = %+v details %v", ev, d)
		}
	})
	t.Run("R-REPO-045 the fold task expires every entry validated before the row, in batches, and sets folded_at", func(t *testing.T) {
		start := time.Now()
		res, err := a.fold.Fold(context.Background())
		t.Logf("Fold of %d entries took %s: %+v", entries, time.Since(start).Round(time.Millisecond), res)
		if err != nil {
			t.Fatal(err)
		}
		if res.ExpiredEntries != entries || res.FoldedRows != 1 || res.DeletedRows != 0 {
			t.Errorf("result = %+v", res)
		}
		if live := e.scalar(`SELECT count(*) FROM remote_cache_entries e, remote_cache_invalidations i WHERE e.expires_at > i.created_at`); live != 0 {
			t.Errorf("%d entries still expire after the invalidation", live)
		}
		if e.scalar(`SELECT count(*) FROM remote_cache_invalidations WHERE folded_at IS NOT NULL`) != 1 {
			t.Error("the row is not marked folded")
		}
		again, err := a.fold.Fold(context.Background())
		if err != nil || again.ExpiredEntries != 0 || again.FoldedRows != 0 {
			t.Errorf("a second run did work: %+v, %v", again, err)
		}
	})
}

func TestInvalidationPrefixes(t *testing.T) {
	e := newEnv(t)
	a := e.newInv(e.newReplica("prefix-a", replicaOpts{}), nil)
	b := e.newInv(e.newReplica("prefix-b", replicaOpts{}), nil)
	repo := e.repoRow("npm-remote", repository.Remote)
	id := repoID(repo)
	for _, p := range []string{"@acme/util", "@acme/other", "@acmecorp/util", "left-pad"} {
		e.entry(id, p, "-1 hour", "+1 hour")
	}
	// An entry refreshed after the invalidation keeps its own expiry.
	e.entry(id, "@acme/fresh", "+10 minutes", "+2 hours")
	valid := time.Now().Add(-time.Hour)

	n, err := a.svc.Invalidate(context.Background(), caller, repo, "@acme", "metadata")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("R-REPO-045 the count is the covered metadata entries and @acme does not cover @acmecorp", func(t *testing.T) {
		if n != 3 {
			t.Errorf("invalidated = %d; want 3 (@acme/util, @acme/other, @acme/fresh)", n)
		}
	})
	t.Run("R-REPO-045 on every replica @acme/util is invalid and @acmecorp/util is not", func(t *testing.T) {
		for _, r := range []*invReplica{a, b} {
			waitFor(t, r.name+" to hold the row", 5*time.Second, func() bool { return r.set.Len() == 1 })
			if r.set.MetadataValid(id, "@acme/util", valid) || r.set.MetadataValid(id, "@acme/other", valid) {
				t.Errorf("%s: @acme entries count as valid", r.name)
			}
			if !r.set.MetadataValid(id, "@acmecorp/util", valid) || !r.set.MetadataValid(id, "left-pad", valid) {
				t.Errorf("%s: an entry outside the prefix was invalidated", r.name)
			}
			if !r.set.MetadataValid(id, "@acme/fresh", time.Now().Add(10*time.Minute)) {
				t.Errorf("%s: an entry refreshed after the invalidation was invalidated", r.name)
			}
		}
	})
	t.Run("R-REPO-045 the fold expires @acme/util at the invalidation time and leaves the others alone", func(t *testing.T) {
		if _, err := a.fold.Fold(context.Background()); err != nil {
			t.Fatal(err)
		}
		var inv time.Time
		if err := e.admin.QueryRow(context.Background(), `SELECT created_at FROM remote_cache_invalidations`).Scan(&inv); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"@acme/util", "@acme/other"} {
			if got := e.expiresAt(id, p); !got.Equal(inv) {
				t.Errorf("%s expires at %s; want the invalidation time %s", p, got, inv)
			}
		}
		for _, p := range []string{"@acmecorp/util", "left-pad"} {
			if got := e.expiresAt(id, p); !got.After(inv.Add(30 * time.Minute)) {
				t.Errorf("%s expires at %s; the fold touched an entry outside the prefix", p, got)
			}
		}
		if got := e.expiresAt(id, "@acme/fresh"); !got.After(inv.Add(time.Hour)) {
			t.Errorf("@acme/fresh expires at %s; the fold shortened an entry refreshed after the invalidation", got)
		}
	})
	t.Run("the scope negative counts no metadata entry and the whole-repository row covers everything", func(t *testing.T) {
		n, err := a.svc.Invalidate(context.Background(), caller, repo, "left-pad", "negative")
		if err != nil || n != 0 {
			t.Errorf("negative: n = %d, err = %v", n, err)
		}
		n, err = a.svc.Invalidate(context.Background(), caller, repo, "left-pad", "all")
		if err != nil || n != 1 {
			t.Errorf("all: n = %d, err = %v", n, err)
		}
		if got := e.scalar(`SELECT count(*) FROM remote_cache_invalidations`); got != 3 {
			t.Errorf("%d rows", got)
		}
	})
}

func TestInvalidationNegativeRetention(t *testing.T) {
	e := newEnv(t)
	var now time.Time
	clock := func() time.Time { return now }
	a := e.newInv(e.newReplica("neg-a", replicaOpts{}), clock)
	b := e.newInv(e.newReplica("neg-b", replicaOpts{}), clock)
	repo := e.repoRow("npm-remote", repository.Remote)
	id := repoID(repo)

	sent := time.Now() // the negative entry for @acme/late, written before the invalidation
	time.Sleep(10 * time.Millisecond)
	if _, err := a.svc.Invalidate(context.Background(), caller, repo, "@acme", "negative"); err != nil {
		t.Fatal(err)
	}
	var rowID int64
	if err := e.admin.QueryRow(context.Background(), `SELECT id FROM remote_cache_invalidations`).Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	created := e.createdAt(rowID)
	for _, r := range []*invReplica{a, b} {
		waitFor(t, r.name+" to hold the row", 5*time.Second, func() bool { return r.set.Len() == 1 })
	}
	counts := func(r *invReplica) bool { return r.set.NegativeValid(id, "@acme/late", sent) }

	run := func(at time.Time) invalidation.FoldResult {
		t.Helper()
		now = at
		res, err := a.fold.Fold(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	t.Run("R-REPO-045 the entry sent before the row does not count, on every replica", func(t *testing.T) {
		if counts(a) || counts(b) {
			t.Fatal("the negative entry counts")
		}
		if !a.set.MetadataValid(id, "@acme/late", sent) {
			t.Error("a negative invalidation expired metadata")
		}
	})
	t.Run("R-REPO-045 after the job ran and an hour passed, the entry still does not count", func(t *testing.T) {
		res := run(time.Now())
		if res.FoldedRows != 1 || res.DeletedRows != 0 {
			t.Fatalf("first run = %+v", res)
		}
		res = run(time.Now().Add(time.Hour))
		if res.DeletedRows != 0 || e.invalidationRows() != 1 {
			t.Fatalf("a run an hour later deleted the row: %+v", res)
		}
		if counts(a) || counts(b) {
			t.Error("the negative entry counts after an hour")
		}
	})
	t.Run("R-REPO-045 the row is kept for 86,405 s and not a second less", func(t *testing.T) {
		if res := run(created.Add(86404*time.Second + 900*time.Millisecond)); res.DeletedRows != 0 || e.invalidationRows() != 1 {
			t.Fatalf("deleted 0.1 s early: %+v", res)
		}
		if res := run(created.Add(86405 * time.Second)); res.DeletedRows != 1 || e.invalidationRows() != 0 {
			t.Fatalf("not deleted at 86,405 s: %+v", res)
		}
	})
	t.Run("R-REPO-045 the deletion is announced and every replica drops the row", func(t *testing.T) {
		for _, r := range []*invReplica{a, b} {
			waitFor(t, r.name+" to drop the row", 5*time.Second, func() bool { return r.set.Len() == 0 })
			if !r.set.NegativeValid(id, "@acme/late", sent) {
				t.Errorf("%s: the dropped row still invalidates", r.name)
			}
		}
	})
}

func TestInvalidationRetention(t *testing.T) {
	e := newEnv(t)
	var now time.Time
	a := e.newInv(e.newReplica("ret-a", replicaOpts{}), func() time.Time { return now })
	repo := e.repoRow("npm-remote", repository.Remote)
	id := repoID(repo)
	e.entry(id, "@acme/util", "-1 hour", "+1 hour")

	if _, err := a.svc.Invalidate(context.Background(), caller, repo, "@acme", "metadata"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Invalidate(context.Background(), caller, repo, "@acme", "all"); err != nil {
		t.Fatal(err)
	}
	now = time.Now()
	t.Run("R-REPO-045 a folded metadata row is kept for its grace, an all row for 86,405 s", func(t *testing.T) {
		res, err := a.fold.Fold(context.Background())
		if err != nil || res.FoldedRows != 2 || res.DeletedRows != 0 {
			t.Fatalf("%+v, %v", res, err)
		}
		now = time.Now().Add(invalidation.DefaultMetadataGrace + time.Minute)
		res, err = a.fold.Fold(context.Background())
		if err != nil || res.DeletedRows != 1 {
			t.Fatalf("after the grace: %+v, %v", res, err)
		}
		if got := e.scalar(`SELECT count(*) FROM remote_cache_invalidations WHERE scope = 'all'`); got != 1 {
			t.Errorf("the all row is gone after %s", invalidation.DefaultMetadataGrace)
		}
		now = time.Now().Add(invalidation.NegativeRetention + time.Minute)
		res, err = a.fold.Fold(context.Background())
		if err != nil || res.DeletedRows != 1 || e.invalidationRows() != 0 {
			t.Fatalf("after 86,405 s: %+v, %v", res, err)
		}
	})
	t.Run("R-REPO-045 an entry validated before a row but stored after the fold is found by the last check before the deletion", func(t *testing.T) {
		repo2 := e.repoRow("npm-remote-2", repository.Remote)
		id2 := repoID(repo2)
		e.entry(id2, "late/fill", "-1 hour", "+1 hour")
		if _, err := a.svc.Invalidate(context.Background(), caller, repo2, "late", "metadata"); err != nil {
			t.Fatal(err)
		}
		now = time.Now()
		if _, err := a.fold.Fold(context.Background()); err != nil {
			t.Fatal(err)
		}
		// A fill that was in flight when the row committed lands after the fold.
		e.entry(id2, "late/inflight", "-30 minutes", "+1 hour")
		now = time.Now().Add(invalidation.DefaultMetadataGrace + time.Minute)
		res, err := a.fold.Fold(context.Background())
		if err != nil || res.DeletedRows != 1 || res.ExpiredEntries != 1 {
			t.Fatalf("%+v, %v", res, err)
		}
		if got := e.expiresAt(id2, "late/inflight"); got.After(time.Now()) {
			t.Errorf("the late entry expires at %s", got)
		}
	})
}

func TestInvalidationService(t *testing.T) {
	e := newEnv(t)
	a := e.newInv(e.newReplica("svc-a", replicaOpts{}), nil)
	local := e.repoRow("npm-local", repository.Local)
	virtual := e.repoRow("npm-virtual", repository.Virtual)
	remote := e.repoRow("npm-remote", repository.Remote)

	t.Run("R-API-024 R-OPS-021 a local repository answers 400 not_applicable and writes nothing", func(t *testing.T) {
		_, err := a.svc.Invalidate(context.Background(), caller, local, "x", "all")
		wantCode(t, err, 400, errs.CodeNotApplicable)
		if e.invalidationRows() != 0 || e.scalar(`SELECT count(*) FROM cluster_change WHERE area = 'cache_invalidation'`) != 0 || len(a.rec.types()) != 0 {
			t.Error("a refused call left a trace")
		}
	})
	t.Run("R-OPS-021 R-REPO-045 a virtual repository gets a change for its merged entries and no row", func(t *testing.T) {
		n, err := a.svc.Invalidate(context.Background(), caller, virtual, "@acme", "all")
		if err != nil || n != 0 {
			t.Fatalf("n = %d, err = %v", n, err)
		}
		if e.invalidationRows() != 0 {
			t.Error("a virtual invalidation wrote a row")
		}
		var payload string
		if err := e.admin.QueryRow(context.Background(), `SELECT payload::text FROM cluster_change WHERE area = 'cache_invalidation'`).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var env struct {
			V int                  `json:"v"`
			P invalidation.Payload `json:"p"`
		}
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			t.Fatal(err)
		}
		prefix, _ := env.P.DecodePrefix()
		if env.P.Kind != invalidation.KindVirtual || env.P.Repo != repoID(virtual) || prefix != "@acme" || env.P.Scope != "all" {
			t.Errorf("payload = %s", payload)
		}
		if a.set.Len() != 0 {
			t.Error("the set kept a virtual change")
		}
	})
	t.Run("R-OPS-015 a failing audit record rolls the whole call back", func(t *testing.T) {
		before := e.scalar(`SELECT epoch FROM cluster_epoch WHERE area = 'cache_invalidation'`)
		a.rec.setFail(true)
		_, err := a.svc.Invalidate(context.Background(), caller, remote, "x", "metadata")
		a.rec.setFail(false)
		if err == nil {
			t.Fatal("no error")
		}
		if e.invalidationRows() != 0 || e.scalar(`SELECT epoch FROM cluster_epoch WHERE area = 'cache_invalidation'`) != before || a.set.Len() != 0 {
			t.Error("the failed call left a row, an epoch or a set entry")
		}
	})
	t.Run("R-OPS-016 the audit event names the repository, the prefix, the scope and the count", func(t *testing.T) {
		e.entry(repoID(remote), "left-pad", "-1 hour", "+1 hour")
		if _, err := a.svc.Invalidate(context.Background(), caller, remote, "left-pad", "all"); err != nil {
			t.Fatal(err)
		}
		ev := a.rec.last()
		var d map[string]any
		_ = json.Unmarshal(ev.Details, &d)
		if ev.Type != "repository.cache_invalidated" || ev.Subject.Name != "npm-remote" || ev.Actor.Name != "alice" ||
			fmt.Sprint(d) != "map[count:1 path:left-pad scope:all]" {
			t.Errorf("event = %+v %v", ev, d)
		}
		if got := e.scalar(`SELECT count(*) FROM remote_cache_invalidations WHERE created_by = $1::uuid`, caller.Actor.ID); got != 1 {
			t.Errorf("created_by is not the caller: %d rows", got)
		}
	})
	t.Run("a repository being deleted is not found", func(t *testing.T) {
		gone := remote
		gone.Lifecycle = repository.Deleting
		_, err := a.svc.Invalidate(context.Background(), caller, gone, "", "metadata")
		wantCode(t, err, 404, errs.CodeRepositoryNotFound)
	})
}

func TestCacheEntryList(t *testing.T) {
	e := newEnv(t)
	a := e.newInv(e.newReplica("list-a", replicaOpts{}), nil)
	repo := e.repoRow("npm-remote", repository.Remote)
	other := e.repoRow("npm-remote-2", repository.Remote)
	id := repoID(repo)
	for i := range 25 {
		e.entry(id, fmt.Sprintf("@acme/pkg-%02d", i), "-1 hour", "+1 hour")
	}
	e.entry(id, "left-pad", "-1 hour", "+1 hour")
	e.entry(id, "@acmecorp/util", "-1 hour", "+1 hour")
	e.exec(`INSERT INTO remote_cache_entries (repo_id, path, variant, kind, blob_sha256, upstream_url, fetched_at, validated_at, expires_at, last_used_at, upstream_gone_at)
	        VALUES ($1, 'gone-pkg', 'full', 'metadata', decode(repeat('ab', 32), 'hex'), 'https://registry.example/gone', now(), now(), now(), now(), now())`, id)
	e.entry(repoID(other), "foreign", "-1 hour", "+1 hour")
	e.exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size)
	        VALUES ($1, 'left-pad/-/left-pad-1.0.0.tgz', 'left-pad/-', 'file', decode(repeat('ab', 32), 'hex'), 10)`, id)
	itemID := e.scalar(`SELECT id FROM items WHERE repo_id = $1`, id)
	e.exec(`INSERT INTO remote_cache_entries (repo_id, path, variant, kind, item_id, blob_sha256, upstream_url, fetched_at, validated_at, last_used_at)
	        VALUES ($1, 'left-pad/-/left-pad-1.0.0.tgz', '', 'artifact', $2, decode(repeat('ab', 32), 'hex'), 'https://registry.example/tgz', now(), now(), now())`, id, itemID)

	walk := func(f invalidation.EntryFilter, page int) (paths []string, pages int) {
		t.Helper()
		f.Limit = page + 1
		for {
			rows, err := a.svc.Entries(context.Background(), id, f)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) > page {
				rows = rows[:page]
				last := rows[len(rows)-1].Key()
				f.After = &last
				pages++
				for _, r := range rows {
					paths = append(paths, r.Path)
				}
				continue
			}
			for _, r := range rows {
				paths = append(paths, r.Path)
			}
			return paths, pages + 1
		}
	}
	t.Run("R-REPO-032 the metadata entries of one repository, paged in byte order without a gap or a repeat", func(t *testing.T) {
		got, pages := walk(invalidation.EntryFilter{}, 10)
		if len(got) != 25+3 {
			t.Fatalf("%d entries: %v", len(got), got)
		}
		if pages != 3 {
			t.Errorf("%d pages", pages)
		}
		for i := 1; i < len(got); i++ {
			if got[i-1] >= got[i] {
				t.Fatalf("%q is not before %q", got[i-1], got[i])
			}
		}
		for _, p := range got {
			if p == "foreign" || p == "left-pad/-/left-pad-1.0.0.tgz" {
				t.Errorf("%s does not belong in the list", p)
			}
		}
	})
	t.Run("R-REPO-032 pathPrefix limits the list to whole segments", func(t *testing.T) {
		got, _ := walk(invalidation.EntryFilter{Prefix: "@acme"}, 7)
		if len(got) != 25 || got[0] != "@acme/pkg-00" || got[24] != "@acme/pkg-24" {
			t.Errorf("%d entries: %v", len(got), got)
		}
		got, _ = walk(invalidation.EntryFilter{Prefix: "left-pad"}, 7)
		if len(got) != 1 || got[0] != "left-pad" {
			t.Errorf("left-pad: %v", got)
		}
	})
	t.Run("R-REPO-032 kind selects the artifacts, and the entry carries every column", func(t *testing.T) {
		rows, err := a.svc.Entries(context.Background(), id, invalidation.EntryFilter{Kinds: []string{invalidation.EntryKindArtifact}, Limit: 10})
		if err != nil || len(rows) != 1 {
			t.Fatalf("%v, %v", rows, err)
		}
		r := rows[0]
		if r.Kind != "artifact" || r.ExpiresAt != nil || r.UpstreamURL != "https://registry.example/tgz" || r.FetchedAt.IsZero() || r.LastUsedAt.IsZero() {
			t.Errorf("artifact = %+v", r)
		}
		rows, err = a.svc.Entries(context.Background(), id, invalidation.EntryFilter{Prefix: "gone-pkg", Limit: 10})
		if err != nil || len(rows) != 1 || rows[0].UpstreamGoneAt == nil || rows[0].ExpiresAt == nil {
			t.Errorf("gone entry = %+v, %v", rows, err)
		}
	})
	t.Run("an invalid prefix is refused before the query", func(t *testing.T) {
		if _, err := a.svc.Entries(context.Background(), id, invalidation.EntryFilter{Prefix: "a//b", Limit: 10}); err == nil {
			t.Error("accepted")
		}
	})
}
