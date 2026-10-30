package formattest_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/formattest/testformat"
)

func newHost(t *testing.T) *formattest.Host {
	t.Helper()
	return formattest.NewHost(testformat.New(), formattest.HostOptions{Data: &testformat.MemStore{}})
}

func wantPanic(t *testing.T, contains string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("no panic")
		}
		if s, _ := r.(string); !strings.Contains(s, contains) {
			t.Fatalf("panic = %v, want %q", r, contains)
		}
	}()
	fn()
}

func TestR_ARCH_009_InMemoryHost(t *testing.T) {
	ctx := context.Background()
	t.Run("R-ARCH-009 the data port is bound and checked as the composition root does", func(t *testing.T) {
		wantPanic(t, "HostOptions.Data is not set", func() { formattest.NewHost(testformat.New(), formattest.HostOptions{}) })
		wantPanic(t, "does not implement the data port", func() {
			formattest.NewHost(testformat.New(), formattest.HostOptions{Data: struct{}{}})
		})
		h := newHost(t)
		if _, ok := h.FormatData(testformat.ID).(testformat.Store); !ok {
			t.Fatal("the bound port is not handed out")
		}
		if h.FormatData("other") != nil {
			t.Fatal("the port of another format is handed out")
		}
	})
	t.Run("R-ARCH-009 a package row write raises rev_seq once per package per transaction", func(t *testing.T) {
		h := newHost(t)
		var created bool
		err := h.Seed(ctx, func(w formats.PackageWriter) error {
			p, c, err := w.EnsureLocked(ctx, h.Ref(), "left-pad", "Left-Pad")
			if err != nil {
				return err
			}
			created = c
			v, err := w.InsertVersion(ctx, p, formats.NewVersion{Version: "1.0.0"})
			if err != nil {
				return err
			}
			if err := w.SetVersionAttributes(ctx, v, []byte(`{"a":1}`)); err != nil {
				return err
			}
			return w.SetPackageAttributes(ctx, p, []byte(`{"b":1}`))
		})
		if err != nil || !created {
			t.Fatalf("seed: %v created=%v", err, created)
		}
		if got := h.RevSeq("left-pad"); got != 1 {
			t.Fatalf("a created package with three more writes has rev_seq %d, want 1", got)
		}
		err = h.Seed(ctx, func(w formats.PackageWriter) error {
			p, c, err := w.EnsureLocked(ctx, h.Ref(), "left-pad", "")
			if err != nil || c {
				t.Fatalf("lock of an existing package: %v created=%v", err, c)
			}
			if got := h.RevSeq("left-pad"); got != 1 {
				t.Fatalf("locking raised the revision to %d", got)
			}
			if err := w.SetPackageAttributes(ctx, p, []byte(`{"c":1}`)); err != nil {
				return err
			}
			if _, _, err := w.BumpRevision(ctx, p); err != nil {
				return err
			}
			return w.SetPackageAttributes(ctx, p, []byte(`{"d":1}`))
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := h.RevSeq("left-pad"); got != 2 {
			t.Fatalf("rev_seq = %d, want 2: one raise for the second transaction", got)
		}
		rows, rev, _ := h.Packages().Versions(ctx, h.Ref(), "left-pad")
		if len(rows) != 1 || rev != 2 {
			t.Fatalf("versions = %v rev = %d", rows, rev)
		}
		revs, _ := h.Packages().RevSeqs(ctx, []formats.RepoRef{h.Ref(), {ID: 99}}, "left-pad")
		if revs[h.Ref().ID] != 2 || revs[99] != 0 {
			t.Fatalf("RevSeqs = %v", revs)
		}
	})
	t.Run("R-ARCH-009 a rolled-back transaction leaves no row, no revision, no notification and no job", func(t *testing.T) {
		h := newHost(t)
		boom := errors.New("boom")
		rolled := false
		err := h.Write(ctx, nil, func(tx formats.Tx, w formats.PackageWriter) error {
			tx.OnRollback(func() { rolled = true })
			p, _, _ := w.EnsureLocked(ctx, h.Ref(), "left-pad", "")
			if _, err := w.InsertVersion(ctx, p, formats.NewVersion{Version: "1.0.0"}); err != nil {
				return err
			}
			_ = w.Notify("area", "payload")
			_ = w.Enqueue(ctx, formats.JobSpec{Kind: "k"})
			return boom
		})
		if !errors.Is(err, boom) || !rolled {
			t.Fatalf("err = %v rolled = %v", err, rolled)
		}
		if _, ok, _ := h.Packages().Package(ctx, h.Ref(), "left-pad"); ok {
			t.Fatal("the package survived the rollback")
		}
		if len(h.Notifications()) != 0 || len(h.Jobs()) != 0 {
			t.Fatal("a rollback sent a notification or a job")
		}
	})
	t.Run("R-ARCH-018 a commit sends the notifications and the jobs and runs the after hooks", func(t *testing.T) {
		h := newHost(t)
		after := 0
		err := h.Write(ctx, nil, func(tx formats.Tx, w formats.PackageWriter) error {
			tx.After(func() { after++ })
			if err := w.Notify("area", 1); err != nil {
				return err
			}
			return w.Enqueue(ctx, formats.JobSpec{Kind: "k", DedupKey: "d"})
		})
		if err != nil || after != 1 || len(h.Notifications()) != 1 || len(h.Jobs()) != 1 || h.Jobs()[0].DedupKey != "d" {
			t.Fatalf("err = %v after = %d notes = %v jobs = %v", err, after, h.Notifications(), h.Jobs())
		}
		err = h.Write(ctx, nil, func(_ formats.Tx, w formats.PackageWriter) error { return w.Notify("", nil) })
		if err == nil {
			t.Fatal("a notification without an area was accepted")
		}
	})
	t.Run("R-AUTHZ-018 a writer bound to no request decides no ownership, a bound one records the claim", func(t *testing.T) {
		h := newHost(t)
		err := h.Seed(ctx, func(w formats.PackageWriter) error {
			return w.Claim(ctx, formats.Target{Package: "a"}, formats.OwnershipWrite{Publish: true})
		})
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.Internal {
			t.Fatalf("unbound claim: %v", err)
		}
		deny := errors.New("owned by another")
		h = formattest.NewHost(testformat.New(), formattest.HostOptions{
			Data:  &testformat.MemStore{},
			Claim: func(formats.Target, formats.OwnershipWrite) error { return deny },
		})
		err = testformat.Publish(ctx, h, "left-pad", "1.0.0") // the route's tag change claims
		if err != nil {
			t.Fatal(err)
		}
		resp, err := h.Serve(ctx, http.MethodPut, "left-pad/tags/latest", strings.NewReader(`"1.0.0"`), false)
		if err != nil || resp.Code != http.StatusInternalServerError {
			t.Fatalf("a refused claim answered %v %v", resp, err)
		}
		if cs := h.Claims(); len(cs) != 1 || cs[0].Target.Package != "left-pad" {
			t.Fatalf("claims = %v", cs)
		}
	})
	t.Run("R-ARCH-009 properties are accepted only below the format's namespace", func(t *testing.T) {
		h := newHost(t)
		err := h.Seed(ctx, func(w formats.PackageWriter) error {
			it := h.PutItem("x", "", []byte("x"))
			if err := w.SetProperties(ctx, it, map[string]string{"tf.a": "1"}); err != nil {
				t.Fatal(err)
			}
			return w.SetProperties(ctx, it, map[string]string{"other.a": "1"})
		})
		if err == nil {
			t.Fatal("a property outside the namespace was accepted")
		}
	})
	t.Run("R-ARCH-009 a version exists once, a trashed one is gone from the view", func(t *testing.T) {
		h := newHost(t)
		if err := testformat.Publish(ctx, h, "left-pad", "1.0.0"); err != nil {
			t.Fatal(err)
		}
		resp, err := h.Serve(ctx, http.MethodPut, "left-pad/-/left-pad-1.0.0.tgz", bytes.NewReader(testformat.Tarball("left-pad", "1.0.1")), false)
		if err != nil || resp.Code != http.StatusForbidden {
			t.Fatalf("other content at the path of an existing version: %v %v", resp, err)
		}
		resp, err = h.Serve(ctx, http.MethodDelete, "left-pad/-/left-pad-1.0.0.tgz", nil, false)
		if err != nil || resp.Code != http.StatusNoContent {
			t.Fatalf("delete: %v %v", resp, err)
		}
		if rows, _, _ := h.Packages().Versions(ctx, h.Ref(), "left-pad"); len(rows) != 0 {
			t.Fatalf("the trashed version is listed: %v", rows)
		}
		if _, ok, _ := h.Items().Stat(ctx, h.Ref(), formats.Path{Path: "left-pad/-/left-pad-1.0.0.tgz"}); ok {
			t.Fatal("the removed item is still stat-able")
		}
	})
}

func TestR_ARCH_009_IngestAndItems(t *testing.T) {
	ctx := context.Background()
	h := newHost(t)
	t.Run("R-STOR-004 the spool hashes the body and refuses a digest the protocol did not announce", func(t *testing.T) {
		rq, _ := h.NewRequest(ctx, http.MethodPut, "left-pad/-/left-pad-1.0.0.tgz", nil)
		sp, err := h.Ingest().Spool(ctx, rq, bytes.NewReader([]byte("abc")), formats.IngestOptions{})
		if err != nil || sp.Size() != 3 || len(sp.Digests().SHA256) != 32 || len(sp.Digests().SHA512) != 64 || len(sp.Digests().SHA1) != 20 || len(sp.Digests().MD5) != 16 {
			t.Fatalf("spool: %v", err)
		}
		_, err = h.Ingest().Spool(ctx, rq, bytes.NewReader([]byte("abc")), formats.IngestOptions{Expect: formats.Digests{SHA256: make([]byte, 32)}})
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.Conflict {
			t.Fatalf("a body that differs from its announced digest: %v", err)
		}
		_, err = h.Ingest().Spool(ctx, rq, bytes.NewReader([]byte("abcd")), formats.IngestOptions{MaxSize: 3})
		if !errors.As(err, &e) || e.Kind != errs.TooLarge {
			t.Fatalf("a body above MaxSize: %v", err)
		}
	})
	t.Run("R-ARCH-014 the same content at the same path is an identical commit that runs no hook", func(t *testing.T) {
		if err := testformat.Publish(ctx, h, "left-pad", "1.0.0"); err != nil {
			t.Fatal(err)
		}
		rq, _ := h.NewRequest(ctx, http.MethodPut, "left-pad/-/left-pad-1.0.0.tgz", nil)
		sp, _ := h.Ingest().Spool(ctx, rq, bytes.NewReader(testformat.Tarball("left-pad", "1.0.0")), formats.IngestOptions{})
		before := h.RevSeq("left-pad")
		got, err := h.Ingest().Commit(ctx, rq, sp, formats.Commit{Path: rq.Path, CreateOnly: true})
		if err != nil || !got.Identical || h.RevSeq("left-pad") != before {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("R-ARCH-009 Serve runs ServeCheck, sets the entity headers and answers HEAD without a body", func(t *testing.T) {
		resp, err := h.Serve(ctx, http.MethodHead, "left-pad/-/left-pad-1.0.0.tgz", nil, false)
		if err != nil || resp.Code != 200 || resp.Body.Len() != 0 || resp.Header().Get("ETag") == "" {
			t.Fatalf("HEAD: %v %v", resp, err)
		}
		resp, err = h.Serve(ctx, http.MethodGet, "left-pad/-/left-pad-1.0.0.tgz", nil, false)
		if err != nil || resp.Body.Len() == 0 || resp.Header().Get("Content-Length") == "" {
			t.Fatalf("GET: %v %v", resp, err)
		}
		it, ok, _ := h.Items().Stat(ctx, h.Ref(), formats.Path{Path: "left-pad/-/left-pad-1.0.0.tgz"})
		rc, err := h.Items().Open(ctx, it)
		if !ok || err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		if _, err := h.Items().Open(ctx, formats.Item{}); err == nil {
			t.Fatal("an item the host did not read was opened")
		}
	})
	t.Run("R-ARCH-023 an error before the status line is rendered by the format's WriteError", func(t *testing.T) {
		resp, err := h.Serve(ctx, http.MethodGet, "no-such/-/no-such-1.0.0.tgz", nil, false)
		if err != nil || resp.Code != http.StatusNotFound || !strings.Contains(resp.Header().Get("Content-Type"), "json") {
			t.Fatalf("%v %v", resp, err)
		}
	})
	t.Run("R-ARCH-008 a route the format does not have is the format's Match error", func(t *testing.T) {
		if _, err := h.Serve(ctx, http.MethodGet, "a/b/c/d/e", nil, false); err == nil {
			t.Fatal("served a path no route matches")
		}
		_, err := h.Serve(ctx, http.MethodPost, "left-pad", nil, false)
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.MethodNotAllowed {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestR_ARCH_009_DeleteItemsOfThePackageWriter: the writer of a request removes the files
// at the paths and a folder's subtree, OnItemsRemoved runs in the same transaction, a
// failure plays the removal backwards, and a writer bound to no request removes nothing.
func TestR_ARCH_009_DeleteItemsOfThePackageWriter(t *testing.T) {
	ctx := context.Background()
	h := newHost(t)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if err := testformat.Publish(ctx, h, "left-pad", v); err != nil {
			t.Fatal(err)
		}
	}
	rq, err := h.NewRequest(ctx, http.MethodPut, "left-pad/-/left-pad-1.0.0.tgz", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := h.RevSeq("left-pad")
	t.Run("a failing function leaves every file", func(t *testing.T) {
		err := h.Write(ctx, rq, func(_ formats.Tx, w formats.PackageWriter) error {
			if err := w.DeleteItems(ctx, h.Ref(), "left-pad/-/left-pad-1.0.0.tgz"); err != nil {
				return err
			}
			return errors.New("refused after the removal")
		})
		if err == nil {
			t.Fatal("no error")
		}
		if _, ok, _ := h.Items().Stat(ctx, h.Ref(), formats.Path{Path: "left-pad/-/left-pad-1.0.0.tgz"}); !ok || h.RevSeq("left-pad") != before {
			t.Fatalf("the removal was not undone (rev %d, was %d)", h.RevSeq("left-pad"), before)
		}
	})
	t.Run("a file and a folder's subtree are removed, the hook runs in the transaction", func(t *testing.T) {
		err := h.Write(ctx, rq, func(_ formats.Tx, w formats.PackageWriter) error {
			return w.DeleteItems(ctx, h.Ref(), "left-pad/-/left-pad-1.0.0.tgz", "no/such/file")
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := h.Items().Stat(ctx, h.Ref(), formats.Path{Path: "left-pad/-/left-pad-1.0.0.tgz"}); ok {
			t.Fatal("the file is stored")
		}
		if h.RevSeq("left-pad") <= before {
			t.Fatal("the hook did not raise the revision")
		}
		if err := h.Write(ctx, rq, func(_ formats.Tx, w formats.PackageWriter) error { return w.DeleteItems(ctx, h.Ref(), "left-pad") }); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := h.Items().Stat(ctx, h.Ref(), formats.Path{Path: "left-pad/-/left-pad-1.1.0.tgz"}); ok {
			t.Fatal("the subtree is stored")
		}
	})
	t.Run("a writer bound to no request removes nothing", func(t *testing.T) {
		err := h.Seed(ctx, func(w formats.PackageWriter) error { return w.DeleteItems(ctx, h.Ref(), "left-pad") })
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.Internal {
			t.Fatalf("%v", err)
		}
	})
}

func TestR_ARCH_009_LinksRenditionsEngines(t *testing.T) {
	ctx := context.Background()
	h := newHost(t)
	rq, err := h.NewRequest(ctx, http.MethodGet, "left-pad", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("R-ARCH-007 links are built once-escaped on the base URL of the host", func(t *testing.T) {
		ln := h.Links(rq)
		if ln.Base() != formattest.DefaultBaseURL || ln.Repository() != "https://trove.test/api/testfmt/main/" || ln.Registry("r~cache") != "https://trove.test/api/testfmt/r~cache/" {
			t.Fatalf("%s %s", ln.Base(), ln.Repository())
		}
		if got := ln.Path("@scope/na me/-/x.tgz"); got != "https://trove.test/api/testfmt/main/@scope/na%20me/-/x.tgz" {
			t.Fatalf("Path = %s", got)
		}
		if got := ln.File("main", "a b/c"); got != "https://trove.test/files/main/a%20b/c" {
			t.Fatalf("File = %s", got)
		}
		custom := formattest.NewHost(testformat.New(), formattest.HostOptions{Data: &testformat.MemStore{}, BaseURL: "https://x.example/"})
		if custom.Links(rq).Base() != "https://x.example" {
			t.Fatal("the base URL option is ignored")
		}
		if got := formattest.EscapePath(formats.Path{Path: "a b/c", Folder: true}); got != "a%20b/c/" {
			t.Fatalf("EscapePath = %s", got)
		}
	})
	t.Run("R-ARCH-012 the rendition cache stores and returns a copy", func(t *testing.T) {
		k := formats.RenditionKey{Package: "p", RevSeq: 3}
		if _, ok := h.Renditions().Get(k); ok {
			t.Fatal("hit on an empty cache")
		}
		doc := []byte("doc")
		h.Renditions().Put(k, doc)
		doc[0] = 'X'
		if got, ok := h.Renditions().Get(k); !ok || string(got) != "doc" {
			t.Fatalf("got %q %v", got, ok)
		}
		if _, ok := h.Renditions().Get(formats.RenditionKey{Package: "p", RevSeq: 4}); ok {
			t.Fatal("hit on another revision")
		}
	})
	t.Run("R-ARCH-009 the engines answer not supported until they are bound, and restored_at is the option", func(t *testing.T) {
		var e *errs.Error
		if _, err := h.Remote().Metadata(ctx, rq, rq.Path, ""); !errors.As(err, &e) || e.Kind != errs.Unavailable {
			t.Fatalf("remote: %v", err)
		}
		if _, err := h.Resolver().Metadata(ctx, rq, rq.Path, formats.PathInfo{}, ""); !errors.As(err, &e) || e.Kind != errs.Unavailable {
			t.Fatalf("resolver: %v", err)
		}
		if _, err := h.Resolver().DeployTarget(ctx, rq); !errors.As(err, &e) || e.Kind != errs.MethodNotAllowed {
			t.Fatalf("deploy target: %v", err)
		}
		at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		withAt := formattest.NewHost(testformat.New(), formattest.HostOptions{Data: &testformat.MemStore{}, RestoredAt: at})
		if got, _ := withAt.RestoredAt(ctx); !got.Equal(at) {
			t.Fatalf("RestoredAt = %v", got)
		}
	})
	t.Run("R-ARCH-009 the in-memory remote serves documents and artifacts and the host records what a handler read", func(t *testing.T) {
		m := &formattest.MemRemote{Docs: map[string][]byte{"left-pad": []byte("{}")}, Artifacts: map[string][]byte{"left-pad/-/x.tgz": []byte("tarball")}}
		h := formattest.NewHost(testformat.New(), formattest.HostOptions{Data: &testformat.MemStore{}, Remote: m, Class: formats.Remote})
		rq, _ := h.NewRequest(ctx, http.MethodGet, "left-pad", nil)
		d, err := h.Remote().Metadata(ctx, rq, rq.Path, "")
		if err != nil || d.Size != 2 {
			t.Fatalf("%v %v", d, err)
		}
		if b, _ := d.Bytes(ctx); string(b) != "{}" {
			t.Fatalf("%q", b)
		}
		if _, _, err := h.Remote().EnsureCurrent(ctx, rq, rq.Path, ""); err != nil {
			t.Fatal(err)
		}
		if st, _ := h.Remote().EntryState(ctx, rq, rq.Path); !st.Stored {
			t.Fatal("entry state")
		}
		if _, err := h.Remote().Metadata(ctx, rq, formats.Path{Path: "nope"}, ""); err == nil {
			t.Fatal("a missing document")
		}
		if _, ok, _ := h.Remote().Cached(ctx, rq, rq.Path); ok {
			t.Fatal("cached")
		}
		rec := &recorder{}
		if err := h.Remote().Artifact(ctx, rq, formats.Path{Path: "left-pad/-/x.tgz"}, formats.Digests{}, rec); err != nil || rec.body.String() != "tarball" {
			t.Fatalf("artifact: %v %q", err, rec.body.String())
		}
		if err := h.Remote().Artifact(ctx, rq, formats.Path{Path: "nope"}, formats.Digests{}, rec); err == nil {
			t.Fatal("a missing artifact")
		}
	})
}

type recorder struct {
	h    http.Header
	body bytes.Buffer
}

func (r *recorder) Header() http.Header {
	if r.h == nil {
		r.h = http.Header{}
	}
	return r.h
}
func (r *recorder) Write(p []byte) (int, error) { return r.body.Write(p) }
func (r *recorder) WriteHeader(int)             {}
