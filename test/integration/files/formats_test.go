//go:build integration

package files

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
)

// formatsFixture is one replica with the format dispatcher, the storage hooks and
// the files seam of the probe format on it (WP2.1-a).
type formatsFixture struct {
	e    *env
	r    *replica
	host *formathost.Host
	disp *dispatch.Dispatcher
	// router is the replica's router, for the route table.
	router *httpserver.Router
}

func newFormatsFixture(t *testing.T) *formatsFixture {
	t.Helper()
	probeReset()
	e := newEnv(t)
	e.breakGlass()
	b := formathost.NewBindings()
	if err := b.Bind("probe", probePort{}); err != nil {
		t.Fatal(err)
	}
	deps := formathost.Deps{Bindings: b}
	f := &formatsFixture{e: e}
	seam := formathost.NewFilesSeam(func() (formats.Host, error) { return f.host, nil }, deps)
	f.r = e.newReplica("a", replicaOpts{
		hooks: formathost.NewHooks(deps), formats: seam,
		routes: func(r *replica, router *httpserver.Router) {
			pkgs, err := packages.NewStore(r.db)
			if err != nil {
				t.Fatal(err)
			}
			if f.host, err = formathost.New(formathost.Options{Runner: r.db, Decider: r.engine, Items: r.items, Packages: pkgs, Deps: deps}); err != nil {
				t.Fatal(err)
			}
			if f.disp, err = dispatch.New(dispatch.Options{Host: f.host, Decider: r.engine, Repos: r.reader}); err != nil {
				t.Fatal(err)
			}
			f.disp.Register(router)
			f.router = router
		},
	})
	f.r.createRepository("probe-local", map[string]any{"format": "probe", "excludePatterns": []string{"hidden-*"}})
	f.r.createRepository("gen-local", nil)
	e.user("writer")
	e.user("reader")
	e.user("nobody")
	e.grant(f.r, "probe-w", []string{"probe-local", "gen-local"}, authz.PatternLists{}, map[string][]string{"user:writer": {"read", "deploy", "delete"}})
	e.grant(f.r, "probe-r", []string{"probe-local"}, authz.PatternLists{}, map[string][]string{"user:reader": {"read"}})
	// Wait for the policies, with a file the hooks of the format ignore.
	waitFor(t, "the policies", 10*time.Second, func() bool {
		return f.r.put("writer", "/files/probe-local/.wait", "x").status/100 == 2 && f.r.get("reader", "/files/probe-local/.wait").status == 200
	})
	if res := f.r.do("writer", http.MethodDelete, "/files/probe-local/.wait", nil); res.status != 204 {
		t.Fatalf("remove the probe: %d %s", res.status, res.body)
	}
	probeReset()
	return f
}

func (f *formatsFixture) rev(name string) int64 {
	return f.e.number("SELECT rev_seq FROM packages WHERE name = $1", name)
}

func (f *formatsFixture) publish(user, name, version string) response {
	return f.r.put(user, "/api/probe/probe-local/"+name+"/-/"+name+"-"+version+".tgz", "content of "+name+"@"+version)
}

// TestR_ARCH_009_PackageWriterBumpsRevSeqOncePerTransaction: every write to the rows
// of a package raises packages.rev_seq of the package once per transaction,
// whatever the number of writes, and a rolled-back transaction raises nothing.
func TestR_ARCH_009_PackageWriterBumpsRevSeqOncePerTransaction(t *testing.T) {
	f := newFormatsFixture(t)
	ctx := context.Background()
	repoID := f.e.number("SELECT id FROM repositories WHERE key = 'probe-local'")
	inTx := func(fn func(w *packages.Writer) error) error {
		return f.r.db.InTx(ctx, func(tx meta.Tx) error {
			w, err := packages.NewWriter(tx, "probe.")
			if err != nil {
				return err
			}
			return fn(w)
		})
	}

	t.Run("a new package starts at 1 and its versions in the same transaction do not raise it again", func(t *testing.T) {
		err := inTx(func(w *packages.Writer) error {
			pkg, created, err := w.EnsureLocked(ctx, repoID, "alpha", "Alpha")
			if err != nil || !created {
				t.Fatalf("EnsureLocked: %v created=%v", err, created)
			}
			for _, v := range []string{"1.0.0", "1.1.0"} {
				if _, err := w.InsertVersion(ctx, pkg, packages.NewVersion{Version: v}); err != nil {
					return err
				}
			}
			return w.SetPackageAttributes(ctx, pkg, []byte(`{"k":"v"}`))
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.rev("alpha"); got != 1 {
			t.Fatalf("rev_seq = %d, want 1", got)
		}
	})

	t.Run("an existing package is raised once by three writes in one transaction", func(t *testing.T) {
		err := inTx(func(w *packages.Writer) error {
			pkg, created, err := w.EnsureLocked(ctx, repoID, "alpha", "Alpha")
			if err != nil || created {
				t.Fatalf("EnsureLocked: %v created=%v", err, created)
			}
			if _, err := w.InsertVersion(ctx, pkg, packages.NewVersion{Version: "2.0.0"}); err != nil {
				return err
			}
			vs, err := w.Versions(ctx, pkg)
			if err != nil || len(vs) != 3 {
				t.Fatalf("versions = %v %v", vs, err)
			}
			if err := w.SetVersionAttributes(ctx, vs[0].ID, []byte(`{"deprecated":"x"}`)); err != nil {
				return err
			}
			rev, at, err := w.BumpRevision(ctx, pkg)
			if err != nil || rev != 2 || at.IsZero() || time.Since(at) > time.Minute {
				t.Fatalf("BumpRevision = %d %v %v: the revision the transaction owns, with the database clock", rev, at, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.rev("alpha"); got != 2 {
			t.Fatalf("rev_seq = %d, want 2", got)
		}
	})

	t.Run("a change that writes only the side rows raises the revision through BumpRevision alone", func(t *testing.T) {
		err := inTx(func(w *packages.Writer) error {
			pkg, _, err := w.EnsureLocked(ctx, repoID, "alpha", "Alpha")
			if err != nil {
				return err
			}
			_, _, err = w.BumpRevision(ctx, pkg)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.rev("alpha"); got != 3 {
			t.Fatalf("rev_seq = %d, want 3", got)
		}
	})

	t.Run("locking a package changes nothing, and a rolled-back transaction raises nothing", func(t *testing.T) {
		if err := inTx(func(w *packages.Writer) error {
			_, _, err := w.EnsureLocked(ctx, repoID, "alpha", "Alpha")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		err := inTx(func(w *packages.Writer) error {
			pkg, _, err := w.EnsureLocked(ctx, repoID, "alpha", "Alpha")
			if err != nil {
				return err
			}
			if _, err := w.InsertVersion(ctx, pkg, packages.NewVersion{Version: "9.9.9"}); err != nil {
				return err
			}
			return context.Canceled
		})
		if err == nil {
			t.Fatal("the failing transaction committed")
		}
		if got := f.rev("alpha"); got != 3 {
			t.Fatalf("rev_seq = %d, want 3", got)
		}
		if n := f.e.number("SELECT count(*) FROM package_versions WHERE version = '9.9.9'"); n != 0 {
			t.Fatalf("%d rows of the rolled-back version", n)
		}
	})

	t.Run("a live version of the same name is a conflict and a trashed one is free", func(t *testing.T) {
		err := inTx(func(w *packages.Writer) error {
			pkg, _, err := w.EnsureLocked(ctx, repoID, "alpha", "Alpha")
			if err != nil {
				return err
			}
			_, err = w.InsertVersion(ctx, pkg, packages.NewVersion{Version: "1.0.0"})
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("err = %v", err)
		}
		err = inTx(func(w *packages.Writer) error {
			pkg, _, err := w.EnsureLocked(ctx, repoID, "alpha", "Alpha")
			if err != nil {
				return err
			}
			vs, _ := w.Versions(ctx, pkg)
			if err := w.TrashOrDeleteVersion(ctx, vs[0].ID, false); err != nil {
				return err
			}
			_, err = w.InsertVersion(ctx, pkg, packages.NewVersion{Version: vs[0].Version})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := f.rev("alpha"); got != 4 {
			t.Fatalf("rev_seq = %d, want 4", got)
		}
	})

	t.Run("the property namespace is the format's", func(t *testing.T) {
		err := inTx(func(w *packages.Writer) error {
			return w.SetProperties(ctx, f.e.number("SELECT 1"), map[string]string{"other.key": "x"})
		})
		if err == nil || !strings.Contains(err.Error(), "outside the namespace") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestR_ARCH_009_FormatHooksRunInTheItemTransaction: a publish through the
// dispatcher commits the item, runs OnCommit with the package writer and the data
// port in the item's transaction, and the package rows follow the item: a hook that
// fails leaves no item and no package.
func TestR_ARCH_009_FormatHooksRunInTheItemTransaction(t *testing.T) {
	f := newFormatsFixture(t)

	t.Run("a publish creates the item, the package, the version and the property, at revision 1", func(t *testing.T) {
		if res := f.publish("writer", "left-pad", "1.0.0"); res.status != http.StatusCreated {
			t.Fatalf("publish: %d %s", res.status, res.body)
		}
		if got := f.rev("left-pad"); got != 1 {
			t.Fatalf("rev_seq = %d", got)
		}
		if n := f.e.number("SELECT count(*) FROM package_versions v JOIN packages p ON p.id = v.package_id JOIN items i ON i.id = v.lead_item_id AND i.version_id = v.id WHERE p.name = 'left-pad' AND v.version = '1.0.0'"); n != 1 {
			t.Fatalf("version rows linked to the lead item: %d", n)
		}
		if n := f.e.number("SELECT count(*) FROM item_properties WHERE key = 'probe.name' AND value = 'left-pad'"); n != 1 {
			t.Fatalf("property rows: %d", n)
		}
		probeLog.mu.Lock()
		defer probeLog.mu.Unlock()
		if len(probeLog.data) != 1 {
			t.Fatalf("hook calls: %d", len(probeLog.data))
		}
		if p, ok := probeLog.data[0].(probeStore); !ok || p.Name() != "probe-port" {
			t.Fatalf("the data port in the event: %T", probeLog.data[0])
		}
	})

	t.Run("a second version raises the revision once", func(t *testing.T) {
		if res := f.publish("writer", "left-pad", "1.1.0"); res.status != http.StatusCreated {
			t.Fatalf("publish: %d %s", res.status, res.body)
		}
		if got := f.rev("left-pad"); got != 2 {
			t.Fatalf("rev_seq = %d", got)
		}
	})

	t.Run("R-ARCH-014 an existing path is 412 and changes nothing", func(t *testing.T) {
		res := f.publish("writer", "left-pad", "1.1.0")
		if res.status != http.StatusPreconditionFailed || res.header.Get("X-Probe-Error") == "" {
			t.Fatalf("republish: %d %v %s", res.status, res.header, res.body)
		}
		if got := f.rev("left-pad"); got != 2 {
			t.Fatalf("rev_seq = %d", got)
		}
	})

	t.Run("a failing hook leaves no item and no package", func(t *testing.T) {
		probeLog.mu.Lock()
		probeLog.failFor = "doomed"
		probeLog.mu.Unlock()
		res := f.publish("writer", "doomed", "1.0.0")
		if res.status != http.StatusInternalServerError {
			t.Fatalf("publish: %d %s", res.status, res.body)
		}
		if n := f.e.number("SELECT count(*) FROM items WHERE path LIKE 'doomed/%'"); n != 0 {
			t.Fatalf("%d items of the rolled-back publish", n)
		}
		if n := f.e.number("SELECT count(*) FROM packages WHERE name = 'doomed'"); n != 0 {
			t.Fatalf("%d package rows of the rolled-back publish", n)
		}
	})

	t.Run("a tag change without content raises the revision through the format's own BumpRevision", func(t *testing.T) {
		res := f.r.do("writer", http.MethodPut, "/api/probe/probe-local/left-pad/tag/latest", nil)
		if res.status != http.StatusNoContent {
			t.Fatalf("tag: %d %s", res.status, res.body)
		}
		if got := f.rev("left-pad"); got != 3 {
			t.Fatalf("rev_seq = %d", got)
		}
	})

	t.Run("the package document is rendered from the rows and carries its revision", func(t *testing.T) {
		res := f.r.get("reader", "/api/probe/probe-local/left-pad")
		if res.status != http.StatusOK {
			t.Fatalf("get: %d %s", res.status, res.body)
		}
		doc := res.json(t)
		if doc["rev"].(float64) != 3 || len(doc["versions"].([]any)) != 2 {
			t.Fatalf("document = %v", doc)
		}
		if !strings.HasPrefix(doc["base"].(string), f.r.baseURL+"/api/probe/probe-local/") {
			t.Fatalf("base = %v: links come from the link builder on the endpoint's base URL", doc["base"])
		}
	})
}

// TestR_ARCH_005_DispatcherRoutesAndRefuses: the checks every protocol request owes
// before a format sees it, in order, and the format's own error shape on every one.
func TestR_ARCH_005_DispatcherRoutesAndRefuses(t *testing.T) {
	f := newFormatsFixture(t)
	if res := f.publish("writer", "left-pad", "1.0.0"); res.status != http.StatusCreated {
		t.Fatalf("seed: %d %s", res.status, res.body)
	}
	shaped := func(t *testing.T, res response, status int, code string) {
		t.Helper()
		if res.status != status || res.header.Get("X-Probe-Error") != code {
			t.Fatalf("answer %d (probe error %q), want %d %q: %s", res.status, res.header.Get("X-Probe-Error"), status, code, res.body)
		}
	}

	t.Run("R-ARCH-005 the prefix of a format with routes is registered and generic has none", func(t *testing.T) {
		if got := strings.Join(f.disp.Prefixes(), ","); got != "probe" {
			t.Fatalf("prefixes = %s", got)
		}
		for _, p := range f.router.Patterns() {
			if strings.Contains(p, "/api/generic/") {
				t.Fatalf("generic registered %s", p)
			}
		}
		res := f.r.get("writer", "/api/generic/gen-local/x")
		if res.status != http.StatusNotFound || res.header.Get("X-Probe-Error") != "" {
			t.Fatalf("/api/generic/: %d %v", res.status, res.header)
		}
	})
	t.Run("R-ARCH-005 an unknown route is 404 in the format's shape", func(t *testing.T) {
		shaped(t, f.r.get("writer", "/api/probe/probe-local/a/b/c/d"), 404, "not_found")
	})
	t.Run("R-ARCH-005 a wrong method is 405 with Allow", func(t *testing.T) {
		res := f.r.do("writer", http.MethodPost, "/api/probe/probe-local/left-pad", nil)
		shaped(t, res, 405, "method_not_allowed")
		if res.header.Get("Allow") != "GET, HEAD" {
			t.Fatalf("Allow = %q", res.header.Get("Allow"))
		}
	})
	t.Run("R-ARCH-005 a path with an escaped dot is 400 invalid_path before authorization", func(t *testing.T) {
		shaped(t, f.r.get("", "/api/probe/probe-local/%2e%2e/x"), 400, "invalid_path")
		shaped(t, f.r.get("", "/api/probe/probe-local/a//b"), 400, "invalid_path")
		shaped(t, f.r.get("", "/api/probe/probe-local/a/../b"), 400, "invalid_path")
	})
	t.Run("R-ARCH-005 a repository key of the wrong shape is repository_not_found without a lookup", func(t *testing.T) {
		shaped(t, f.r.get("writer", "/api/probe/UPPER/left-pad"), 404, "repository_not_found")
	})
	t.Run("R-ARCH-011 the coarse check runs first: a caller without read gets the same 404 as a missing repository", func(t *testing.T) {
		shaped(t, f.r.get("nobody", "/api/probe/probe-local/left-pad"), 404, "not_found")
		shaped(t, f.r.get("nobody", "/api/probe/no-such-repo/left-pad"), 404, "not_found")
	})
	t.Run("R-ARCH-011 a write without deploy is 403 before the body is read", func(t *testing.T) {
		shaped(t, f.r.put("reader", "/api/probe/probe-local/left-pad/-/left-pad-2.0.0.tgz", "x"), 403, "forbidden")
		if n := f.e.number("SELECT count(*) FROM items WHERE path LIKE 'left-pad/-/left-pad-2.0.0%'"); n != 0 {
			t.Fatalf("%d items", n)
		}
	})
	t.Run("R-ARCH-005 a repository of another format is a missing repository", func(t *testing.T) {
		shaped(t, f.r.get("writer", "/api/probe/gen-local/left-pad"), 404, "repository_not_found")
	})
	t.Run("R-REPO-005 a package the repository's patterns exclude is not admitted: 403 for a write", func(t *testing.T) {
		shaped(t, f.publish("writer", "hidden-pkg", "1.0.0"), 403, "not_admitted")
	})
	t.Run("R-ARCH-023 a refusal of the authentication stage on the prefix is rendered by the format", func(t *testing.T) {
		res := f.r.get("not-a-user", "/api/probe/probe-local/left-pad")
		shaped(t, res, 401, "invalid_token")
		if res.header.Get("WWW-Authenticate") == "" {
			t.Fatal("the challenge was lost")
		}
		if res := f.r.get("not-a-user", "/api/v1/system/ping"); res.status != 401 || res.header.Get("X-Probe-Error") != "" {
			t.Fatalf("the management API keeps problem+json: %d %v", res.status, res.header)
		}
	})
}

// TestR_ARCH_007_FormatRoutesCarryTheContentHeaders: the tarball, the package document
// and the /files/ view carry the headers of R-ARCH-007, also when the handler tries
// to remove them; a route of another kind does not get them.
func TestR_ARCH_007_FormatRoutesCarryTheContentHeaders(t *testing.T) {
	f := newFormatsFixture(t)
	if res := f.publish("writer", "left-pad", "1.0.0"); res.status != http.StatusCreated {
		t.Fatalf("seed: %d %s", res.status, res.body)
	}
	check := func(t *testing.T, res response, disposition bool) {
		t.Helper()
		h := res.header
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox; default-src 'none'" ||
			h.Get("Cross-Origin-Resource-Policy") != "same-site" {
			t.Fatalf("headers = %v", h)
		}
		if got := h.Get("Content-Disposition") != ""; got != disposition {
			t.Fatalf("Content-Disposition = %q, want present=%v", h.Get("Content-Disposition"), disposition)
		}
	}
	t.Run("a Download route", func(t *testing.T) {
		res := f.r.get("reader", "/api/probe/probe-local/left-pad/-/left-pad-1.0.0.tgz")
		if res.status != 200 || res.text() != "content of left-pad@1.0.0" {
			t.Fatalf("get: %d %s", res.status, res.body)
		}
		check(t, res, true)
		if !strings.Contains(res.header.Get("Content-Disposition"), "left-pad-1.0.0.tgz") {
			t.Fatalf("disposition = %q", res.header.Get("Content-Disposition"))
		}
	})
	t.Run("a Metadata route has the policy and no file name", func(t *testing.T) {
		check(t, f.r.get("reader", "/api/probe/probe-local/left-pad"), false)
	})
	t.Run("a handler that removes and weakens the headers still sends them", func(t *testing.T) {
		check(t, f.r.get("reader", "/api/probe/probe-local/left-pad?strip=1"), false)
	})
	t.Run("a failing Download route is no attachment", func(t *testing.T) {
		res := f.r.get("reader", "/api/probe/probe-local/left-pad/-/left-pad-9.9.9.tgz")
		if res.status != 404 || res.header.Get("Content-Disposition") != "" {
			t.Fatalf("answer %d disposition %q", res.status, res.header.Get("Content-Disposition"))
		}
	})
	t.Run("a route of another kind gets no content headers from the dispatcher", func(t *testing.T) {
		res := f.r.get("reader", "/api/probe/probe-local/left-pad/leak")
		if res.status != 200 || res.header.Get("Content-Security-Policy") != "" {
			t.Fatalf("answer %d headers %v: a Volatile route that returns bytes is a contract violation the kit catches", res.status, res.header)
		}
	})
}

// TestR_ARCH_008_ServeCheckRunsOnEveryRoute: a format refuses an item on its own
// route and on the raw /files/ view (R-ARCH-008 "Serve-time check").
func TestR_ARCH_008_ServeCheckRunsOnEveryRoute(t *testing.T) {
	f := newFormatsFixture(t)
	for _, name := range []string{"tombstoned", "fine"} {
		if res := f.publish("writer", name, "1.0.0"); res.status != http.StatusCreated {
			t.Fatalf("seed %s: %d %s", name, res.status, res.body)
		}
	}
	t.Run("the format's route", func(t *testing.T) {
		if res := f.r.get("reader", "/api/probe/probe-local/fine/-/fine-1.0.0.tgz"); res.status != 200 {
			t.Fatalf("fine: %d", res.status)
		}
		if res := f.r.get("reader", "/api/probe/probe-local/tombstoned/-/tombstoned-1.0.0.tgz"); res.status != 404 || res.header.Get("X-Probe-Error") == "" {
			t.Fatalf("tombstoned: %d %v", res.status, res.header)
		}
	})
	t.Run("the /files/ view", func(t *testing.T) {
		if res := f.r.get("reader", "/files/probe-local/fine/-/fine-1.0.0.tgz"); res.status != 200 {
			t.Fatalf("fine: %d %s", res.status, res.body)
		}
		if res := f.r.get("reader", "/files/probe-local/tombstoned/-/tombstoned-1.0.0.tgz"); res.status != 404 {
			t.Fatalf("tombstoned: %d %s", res.status, res.body)
		}
	})
	t.Run("a generic repository is unaffected", func(t *testing.T) {
		if res := f.r.put("writer", "/files/gen-local/tombstoned/x.bin", "x"); res.status != 201 {
			t.Fatalf("put: %d %s", res.status, res.body)
		}
		if res := f.r.get("writer", "/files/gen-local/tombstoned/x.bin"); res.status != 200 {
			t.Fatalf("get: %d %s", res.status, res.body)
		}
	})
}

// TestR_ARCH_008_FilesDeleteRunsOnItemsRemoved: a delete through the raw view of a
// format's repository reaches the format's hook with the version of the item, so the
// package rows follow and the revision is raised (storage.md hooks table).
func TestR_ARCH_008_FilesDeleteRunsOnItemsRemoved(t *testing.T) {
	f := newFormatsFixture(t)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if res := f.publish("writer", "left-pad", v); res.status != http.StatusCreated {
			t.Fatalf("seed: %d %s", res.status, res.body)
		}
	}
	before := f.rev("left-pad")
	if res := f.r.do("writer", http.MethodDelete, "/files/probe-local/left-pad/-/left-pad-1.0.0.tgz", nil); res.status != 204 && res.status != 200 {
		t.Fatalf("delete: %d %s", res.status, res.body)
	}
	probeLog.mu.Lock()
	if len(probeLog.removed) != 1 || len(probeLog.removed[0].Items) != 1 || probeLog.removed[0].Items[0].Version == 0 {
		t.Fatalf("removed events = %+v", probeLog.removed)
	}
	if probeLog.removed[0].Repo.Format != "probe" || probeLog.removed[0].Reason != "delete" {
		t.Fatalf("event = %+v", probeLog.removed[0])
	}
	probeLog.mu.Unlock()
	if got := f.rev("left-pad"); got != before+1 {
		t.Fatalf("rev_seq = %d, want %d", got, before+1)
	}
	if n := f.e.number("SELECT count(*) FROM package_versions WHERE version = '1.0.0' AND trashed_at IS NOT NULL"); n != 1 {
		t.Fatalf("trashed versions: %d", n)
	}
}
