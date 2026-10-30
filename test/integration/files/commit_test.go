//go:build integration

package files

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/repository"
)

// orderLog records the steps of a commit in the order they ran.
type orderLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *orderLog) add(s string) {
	l.mu.Lock()
	l.steps = append(l.steps, s)
	l.mu.Unlock()
}

func (l *orderLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.steps
	l.steps = nil
	return out
}

// hookProbe is the storage hook of the item store under test: it records what it
// is told and what it can see of the transaction it runs in.
type hookProbe struct {
	log *orderLog

	mu          sync.Mutex
	commits     []items.CommitEvent
	removed     []items.RemovedEvent
	rowInTx     []bool // whether the committed item's row was visible to the hook's own transaction
	failCommit  error
	failRemoved error
	// failPath fails the hook of that path only, after the hooks before it ran.
	failPath string
}

func (h *hookProbe) OnCommit(ctx context.Context, tx meta.Tx, e items.CommitEvent) error {
	h.log.add("hook")
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return errors.New("not a postgres transaction")
	}
	var n int
	if err := ptx.QueryRow(ctx, "SELECT count(*) FROM items WHERE id = $1", e.Item.ID).Scan(&n); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commits = append(h.commits, e)
	h.rowInTx = append(h.rowInTx, n == 1)
	if h.failPath != "" && e.Item.Path == h.failPath {
		return errors.New("the format refused " + e.Item.Path)
	}
	return h.failCommit
}

func (h *hookProbe) OnItemsRemoved(_ context.Context, _ meta.Tx, e items.RemovedEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removed = append(h.removed, e)
	return h.failRemoved
}

func (h *hookProbe) events() []items.CommitEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]items.CommitEvent(nil), h.commits...)
}

// orderRecorder notes the audit row of a commit in the order log, at the moment
// the item store writes it into its transaction.
type orderRecorder struct {
	audit.Recorder
	log *orderLog
}

func (o orderRecorder) RecordTx(ctx context.Context, tx meta.Tx, ev audit.Event) error {
	o.log.add("audit:" + ev.Type)
	return o.Recorder.RecordTx(ctx, tx, ev)
}

// commitFixture is one replica whose item store runs the probe hook.
type commitFixture struct {
	e     *env
	r     *replica
	log   *orderLog
	hooks *hookProbe
}

func newCommitFixture(t *testing.T) *commitFixture {
	t.Helper()
	e := newEnv(t)
	e.breakGlass()
	log := &orderLog{}
	hooks := &hookProbe{log: log}
	r := e.newReplica("a", replicaOpts{
		hooks:    hooks,
		recorder: func(next audit.Recorder) audit.Recorder { return orderRecorder{Recorder: next, log: log} },
	})
	r.createRepository("generic-local", nil)
	return &commitFixture{e: e, r: r, log: log, hooks: hooks}
}

// extraRow is a CommitSpec.Extra that writes a row naming the new item: it
// proves the callback runs in the commit transaction and sees the item id.
func (f *commitFixture) extraRow(mark string) func(context.Context, meta.Tx, items.Ref) error {
	return func(ctx context.Context, tx meta.Tx, it items.Ref) error {
		f.log.add("extra")
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("not a postgres transaction")
		}
		_, err := ptx.Exec(ctx, "INSERT INTO item_properties (item_id, key, value) VALUES ($1, 'probe', $2)", it.ID, mark)
		return err
	}
}

// commit spools body and commits it at path of generic-local.
func (f *commitFixture) commit(t *testing.T, path, body string, mut func(*items.CommitSpec)) (items.CommitResult, error) {
	t.Helper()
	ctx := context.Background()
	repo, err := f.r.reader.ByKey(ctx, "generic-local")
	if err != nil {
		t.Fatal(err)
	}
	sp, err := f.r.items.Spool(ctx, strings.NewReader(body), items.IngestOptions{ContentLength: int64(len(body))})
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	spec := items.CommitSpec{
		Repo: repo, Path: repository.Cleaned{Path: path}, Spooled: sp, MediaType: "text/plain",
		Origin: items.OriginUpload, MayOverwrite: true, Actor: audit.Actor{Kind: "user", Name: "alice"},
	}
	if mut != nil {
		mut(&spec)
	}
	return f.r.items.Commit(ctx, spec)
}

// TestR_ARCH_009_ItemStoreRunsTheStorageHooksInItsTransaction: the item store
// commits an upload and a remote fill through one function, runs the committer's
// callback and the format's hook in the transaction of the item row, in that
// order, and writes the audit row last; a failure of any step leaves no item.
func TestR_ARCH_009_ItemStoreRunsTheStorageHooksInItsTransaction(t *testing.T) {
	f := newCommitFixture(t)
	e := f.e

	t.Run("R-ARCH-009 an upload runs the item row, Extra, the hook and the audit row in this order, in one transaction", func(t *testing.T) {
		res, err := f.commit(t, "pkg/a.txt", "alpha", func(s *items.CommitSpec) { s.Extra = f.extraRow("a") })
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(f.log.take(), ","); got != "extra,hook,audit:artifact.deployed" {
			t.Fatalf("order = %s", got)
		}
		ev := f.hooks.events()
		if len(ev) != 1 || ev[0].Origin != items.OriginUpload || ev[0].Item.ID != res.Item.ID || ev[0].Item.Path != "pkg/a.txt" {
			t.Fatalf("event = %+v", ev)
		}
		if ev[0].Repo.Key != "generic-local" || ev[0].Repo.Class != "local" || ev[0].Repo.Format != "generic" || ev[0].Replaced {
			t.Errorf("repository of the event = %+v", ev[0].Repo)
		}
		if !f.hooks.rowInTx[0] {
			t.Error("the hook's transaction did not see the item row: it ran outside the commit transaction")
		}
		if n := e.number("SELECT count(*) FROM item_properties WHERE item_id = $1 AND key = 'probe'", res.Item.ID); n != 1 {
			t.Errorf("the row Extra wrote for the new item id: %d", n)
		}
		if n := len(f.r.rec.ofType(audit.EventArtifactDeployed)); n != 1 {
			t.Errorf("%d artifact.deployed events", n)
		}
	})

	t.Run("R-ARCH-009 a failing hook rolls the commit back: no item, no Extra row, no audit event", func(t *testing.T) {
		before := len(f.r.rec.ofType(audit.EventArtifactDeployed))
		f.hooks.failCommit = errors.New("the format refused the item")
		defer func() { f.hooks.failCommit = nil }()
		_, err := f.commit(t, "pkg/rolled-back.txt", "beta", func(s *items.CommitSpec) { s.Extra = f.extraRow("b") })
		if err == nil || !strings.Contains(err.Error(), "the format refused the item") {
			t.Fatalf("err = %v", err)
		}
		if got := strings.Join(f.log.take(), ","); got != "extra,hook" {
			t.Errorf("order = %s: the audit row must not be written after a failed hook", got)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = 'pkg/rolled-back.txt'"); n != 0 {
			t.Errorf("%d items were left by a rolled-back commit", n)
		}
		if n := e.number("SELECT count(*) FROM item_properties WHERE value = 'b'"); n != 0 {
			t.Errorf("%d Extra rows were left by a rolled-back commit", n)
		}
		if n := len(f.r.rec.ofType(audit.EventArtifactDeployed)); n != before {
			t.Errorf("the audit event of a rolled-back commit was recorded")
		}
		// The ancestor folder the link step created is part of the transaction too.
		f.hooks.failCommit = errors.New("again")
		if _, err := f.commit(t, "fresh/dir/c.txt", "gamma", nil); err == nil {
			t.Fatal("the commit succeeded")
		}
		if n := e.number("SELECT count(*) FROM items WHERE path IN ('fresh', 'fresh/dir')"); n != 0 {
			t.Errorf("%d ancestor folders were left", n)
		}
	})

	t.Run("R-ARCH-009 a failing Extra rolls the commit back and the hook never runs", func(t *testing.T) {
		f.log.take()
		_, err := f.commit(t, "pkg/extra-fails.txt", "delta", func(s *items.CommitSpec) {
			s.Extra = func(context.Context, meta.Tx, items.Ref) error {
				f.log.add("extra")
				return errors.New("the cache entry could not be written")
			}
		})
		if err == nil || !strings.Contains(err.Error(), "cache entry") {
			t.Fatalf("err = %v", err)
		}
		if got := strings.Join(f.log.take(), ","); got != "extra" {
			t.Errorf("order = %s", got)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = 'pkg/extra-fails.txt'"); n != 0 {
			t.Errorf("%d items were left", n)
		}
	})

	t.Run("R-REPO-025 a fill is committed with its upstream URL, tells the hook it is a fill and writes no deployed event", func(t *testing.T) {
		f.log.take()
		before := len(f.r.rec.ofType(audit.EventArtifactDeployed))
		const upstream = "https://registry.example.org/widget/-/widget-1.0.0.tgz"
		res, err := f.commit(t, "widget/-/widget-1.0.0.tgz", "tarball", func(s *items.CommitSpec) {
			s.Origin, s.UpstreamURL, s.Actor = items.OriginFill, upstream, audit.Actor{}
			s.Extra = f.extraRow("fill")
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(f.log.take(), ","); got != "extra,hook" {
			t.Errorf("order = %s: a fill has no artifact.deployed row (Q-STOR-146)", got)
		}
		ev := f.hooks.events()
		last := ev[len(ev)-1]
		if last.Origin != items.OriginFill || last.UpstreamURL != upstream || last.Actor != "" {
			t.Errorf("event = %+v", last)
		}
		if got := e.text("SELECT upstream_url FROM items WHERE id = $1", res.Item.ID); got != upstream {
			t.Errorf("upstream_url = %q", got)
		}
		if res.Item.UpstreamURL != upstream {
			t.Errorf("the committed item's upstream URL = %q", res.Item.UpstreamURL)
		}
		if n := len(f.r.rec.ofType(audit.EventArtifactDeployed)); n != before {
			t.Error("a fill wrote artifact.deployed")
		}
	})

	t.Run("R-STOR-007 an identical commit changes no row: Extra runs, the hook and the audit row do not", func(t *testing.T) {
		if _, err := f.commit(t, "pkg/same.txt", "same", nil); err != nil {
			t.Fatal(err)
		}
		f.log.take()
		hooksBefore := len(f.hooks.events())
		res, err := f.commit(t, "pkg/same.txt", "same", func(s *items.CommitSpec) { s.Extra = f.extraRow("same") })
		if err != nil {
			t.Fatal(err)
		}
		if res.Created || res.Overwritten {
			t.Errorf("created %v, overwritten %v", res.Created, res.Overwritten)
		}
		if got := strings.Join(f.log.take(), ","); got != "extra" {
			t.Errorf("order = %s", got)
		}
		if len(f.hooks.events()) != hooksBefore {
			t.Error("the hook ran for an identical commit")
		}
		if n := e.number("SELECT count(*) FROM item_properties WHERE item_id = $1 AND value = 'same'", res.Item.ID); n != 1 {
			t.Errorf("Extra saw the existing item id: %d rows", n)
		}
	})

	t.Run("R-STOR-007 other content at the path is replaced only when the spec says so, and the hook is told", func(t *testing.T) {
		if _, err := f.commit(t, "pkg/rep.txt", "first", nil); err != nil {
			t.Fatal(err)
		}
		f.log.take()
		_, err := f.commit(t, "pkg/rep.txt", "second", func(s *items.CommitSpec) { s.MayOverwrite = false })
		if !errors.Is(err, items.ErrOverwriteDenied) {
			t.Fatalf("err = %v, want ErrOverwriteDenied", err)
		}
		if got := e.text("SELECT size::text FROM items WHERE path = 'pkg/rep.txt'"); got != "5" {
			t.Errorf("the content was replaced without permission (size %s)", got)
		}
		if got := f.log.take(); len(got) != 0 {
			t.Errorf("steps after a refused overwrite: %v", got)
		}
		res, err := f.commit(t, "pkg/rep.txt", "second!", nil)
		if err != nil || !res.Overwritten {
			t.Fatalf("%+v, %v", res, err)
		}
		ev := f.hooks.events()
		if last := ev[len(ev)-1]; !last.Replaced || last.Item.Path != "pkg/rep.txt" {
			t.Errorf("event = %+v", last)
		}
	})

	t.Run("R-STOR-007 create-only refuses an existing path with nothing written", func(t *testing.T) {
		f.log.take()
		_, err := f.commit(t, "pkg/rep.txt", "other", func(s *items.CommitSpec) { s.CreateOnly = true })
		if err == nil || !strings.Contains(err.Error(), "create it only") {
			t.Fatalf("err = %v", err)
		}
		if got := f.log.take(); len(got) != 0 {
			t.Errorf("steps after a refused create-only commit: %v", got)
		}
	})

	t.Run("R-STOR-003 a commit refuses the repository root and a folder path", func(t *testing.T) {
		for _, p := range []repository.Cleaned{{Path: ""}, {Path: "pkg", Folder: true}} {
			if _, err := f.commit(t, "x", "x", func(s *items.CommitSpec) { s.Path = p }); err == nil {
				t.Errorf("%+v was committed", p)
			}
		}
	})
}

// TestR_ARCH_008_DeleteRunsOnItemsRemoved: the storage hook learns of the files a
// delete removes, in the removing transaction, for the synchronous delete and for
// every batch of the job; a failing hook leaves the files in place.
func TestR_ARCH_008_DeleteRunsOnItemsRemoved(t *testing.T) {
	log := &orderLog{}
	hooks := &hookProbe{log: log}
	e := newEnv(t)
	e.breakGlass()
	small := func(c *config.Storage) { c.Delete.MaxSyncItems, c.Delete.BatchSize = 5, 2 }
	web := e.newReplica("web", replicaOpts{storage: small, hooks: hooks})
	web.createRepository("bulk", nil)
	for i := range 9 {
		if res := web.put("admin", fmt.Sprintf("/files/bulk/tree/d%d/f%d.bin", i%3, i), fmt.Sprint(i)); res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
	}
	web.put("admin", "/files/bulk/one/x.bin", "x")

	t.Run("R-STOR-018 a delete that fails in the hook leaves the files", func(t *testing.T) {
		hooks.failRemoved = errors.New("the format refused the removal")
		res := web.do("admin", "DELETE", "/files/bulk/one/x.bin", nil)
		hooks.failRemoved = nil
		if res.status < 500 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = 'one/x.bin'"); n != 1 {
			t.Errorf("%d files left, want the file to stay", n)
		}
	})

	t.Run("R-STOR-018 a synchronous delete tells the hook which files went, with the repository and the actor", func(t *testing.T) {
		hooks.mu.Lock()
		hooks.removed = nil
		hooks.mu.Unlock()
		if res := web.do("admin", "DELETE", "/files/bulk/one/x.bin", nil); res.status != 204 {
			t.Fatalf("%d %s", res.status, res.body)
		}
		hooks.mu.Lock()
		defer hooks.mu.Unlock()
		if len(hooks.removed) != 1 || len(hooks.removed[0].Items) != 1 {
			t.Fatalf("events = %+v", hooks.removed)
		}
		ev := hooks.removed[0]
		if ev.Items[0].Item.Path != "one/x.bin" || ev.Repo.Key != "bulk" || ev.Repo.Format != "generic" || ev.Reason != "delete" {
			t.Errorf("event = %+v", ev)
		}
	})

	t.Run("R-STOR-018 the delete job tells the hook of every batch of files, with the repository's class and format", func(t *testing.T) {
		hooks.mu.Lock()
		hooks.removed = nil
		hooks.mu.Unlock()
		if del := web.do("admin", "DELETE", "/files/bulk/tree", nil); del.status != 202 {
			t.Fatalf("%d %s", del.status, del.body)
		}
		_ = e.newReplica("worker", replicaOpts{worker: true, storage: small, hooks: hooks})
		waitFor(t, "the job to settle", 30*time.Second, func() bool {
			return e.number("SELECT count(*) FROM job WHERE kind = 'items-delete' AND state IN ('succeeded', 'failed', 'cancelled')") > 0
		})
		if n := e.number("SELECT count(*) FROM job WHERE kind = 'items-delete' AND state = 'succeeded'"); n != 1 {
			t.Fatalf("the job did not succeed")
		}
		hooks.mu.Lock()
		defer hooks.mu.Unlock()
		total := 0
		for _, ev := range hooks.removed {
			total += len(ev.Items)
			if len(ev.Items) > 2 {
				t.Errorf("a batch of %d files, storage.delete.batch_size is 2", len(ev.Items))
			}
			if ev.Repo.Key != "bulk" || ev.Repo.Class != "local" || ev.Repo.Format != "generic" || ev.Reason != "delete" {
				t.Errorf("event = %+v", ev)
			}
		}
		if total != 9 {
			t.Errorf("the hook was told of %d files, want 9", total)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path LIKE 'tree/%' AND kind = 'file'"); n != 0 {
			t.Errorf("%d files left", n)
		}
	})
}
