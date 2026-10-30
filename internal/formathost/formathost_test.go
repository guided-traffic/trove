package formathost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/repository"
)

type store interface{ Name() string }

type port struct{}

func (port) Name() string { return "p" }

// portFormat declares a data port; it needs nothing else of a format.
type portFormat struct {
	formats.Base
	id   formats.ID
	port reflect.Type
}

func (f portFormat) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts:           catalog.Facts{ID: f.id, Classes: []formats.Class{formats.Local}, Layout: "x", PatternSubject: formats.SubjectPath},
		ContractVersion: formats.ContractVersion, DataPort: f.port,
	}
}
func (portFormat) Routes() []formats.Route { return nil }
func (portFormat) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(nil, m, p)
}

func (portFormat) Classify(formats.Path) formats.PathInfo {
	return formats.PathInfo{Kind: formats.Content}
}

func (portFormat) PackageDocumentPath(string) (formats.Path, bool) {
	return formats.Path{}, false
}

// TestQ_ARCH_193_ABindingThatIsMissingOrWrongIsAStartupError: the composition root
// binds one data port per format; Check names every format at fault.
func TestQ_ARCH_193_ABindingThatIsMissingOrWrongIsAStartupError(t *testing.T) {
	withPort := portFormat{id: "withport", port: reflect.TypeFor[store]()}
	noPort := portFormat{id: "noport"}
	check := func(bind map[formats.ID]any, fs ...formats.Format) error {
		b := NewBindings()
		for id, p := range bind {
			if err := b.Bind(id, p); err != nil {
				t.Fatal(err)
			}
		}
		return b.Check(fs)
	}
	t.Run("a port that implements the declared interface passes", func(t *testing.T) {
		if err := check(map[formats.ID]any{"withport": port{}}, withPort, noPort); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a missing binding names the format and the port", func(t *testing.T) {
		err := check(nil, withPort, noPort)
		if err == nil || !strings.Contains(err.Error(), `"withport"`) || !strings.Contains(err.Error(), "none is bound") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a binding that does not implement the port is refused", func(t *testing.T) {
		err := check(map[formats.ID]any{"withport": struct{}{}}, withPort)
		if err == nil || !strings.Contains(err.Error(), "does not implement") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a binding for a format without a port, or for no format, is refused", func(t *testing.T) {
		err := check(map[formats.ID]any{"noport": port{}, "ghost": port{}}, noPort)
		if err == nil || !strings.Contains(err.Error(), "declares no data port") || !strings.Contains(err.Error(), `"ghost"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a second binding and a nil port are refused", func(t *testing.T) {
		b := NewBindings()
		if err := b.Bind("withport", port{}); err != nil {
			t.Fatal(err)
		}
		if err := b.Bind("withport", port{}); err == nil {
			t.Fatal("bound twice")
		}
		if err := b.Bind("other", nil); err == nil {
			t.Fatal("bound nil")
		}
	})
	t.Run("the host hands the format its port", func(t *testing.T) {
		b := NewBindings()
		_ = b.Bind("withport", port{})
		if _, ok := b.Get("withport").(store); !ok || b.Get("noport") != nil {
			t.Fatal("Get")
		}
		var nilBindings *Bindings
		if nilBindings.Get("x") != nil {
			t.Fatal("a host without bindings has no port")
		}
	})
}

type fakeDecider struct {
	authz.Decider
	decision authz.Decision
	got      authz.Coordinate
	recorded []authz.Coordinate
}

func (d *fakeDecider) DecideOn(_ context.Context, _ *authz.Index, _ authz.Subject, c authz.Coordinate) authz.Decision {
	d.got = c
	return d.decision
}

func (d *fakeDecider) RecordAllowed(_ context.Context, _ meta.Tx, _ authz.Subject, c authz.Coordinate, _ authz.Decision) error {
	d.recorded = append(d.recorded, c)
	return nil
}

func testRequest(kind formats.RouteKind) *formats.Request {
	rq := &formats.Request{
		Request: httptest.NewRequest(http.MethodGet, "/api/probe/key/x", nil),
		Repo:    &repository.Repository{ID: "42", Key: "key", Class: repository.Local, Format: "probe"},
		Route:   &formats.Route{Name: "probe.get", Kind: kind},
		Subject: authz.Subject{Name: "alice", Endpoint: "default"},
	}
	Prepare(rq)
	return rq
}

func newTestHost(t *testing.T, d authz.Decider) *Host {
	t.Helper()
	pk, err := packages.NewStore(nopRunner{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Runner: nopRunner{}, Decider: d, Items: (*items.Store)(nil), Packages: pk})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

type nopRunner struct{}

func (nopRunner) InTx(context.Context, func(meta.Tx) error) error { return nil }

// TestR_ARCH_009_AuthorizeCompletesTheCoordinate: a format names a package, a
// version, a path and actions; the host names the repository, the class, the format
// and the kind of the route, and decides on the request's index (Q-ARCH-189).
func TestR_ARCH_009_AuthorizeCompletesTheCoordinate(t *testing.T) {
	d := &fakeDecider{}
	h := newTestHost(t, d)
	rq := testRequest(formats.Download)
	err := h.Authorize(context.Background(), rq, formats.Target{Package: "left-pad", Version: "1.0.0", Path: "left-pad/-/x.tgz", Actions: []formats.Action{formats.ActionRead}})
	if err != nil {
		t.Fatal(err)
	}
	want := authz.Coordinate{
		Repository: "key", Class: repository.Local, Format: "probe", Kind: authz.KindArtifact,
		Path: "left-pad/-/x.tgz", Package: "left-pad", Version: "1.0.0", Actions: []authz.Action{authz.Read},
	}
	if !reflect.DeepEqual(d.got, want) {
		t.Fatalf("coordinate = %+v\nwant         %+v", d.got, want)
	}
	if got := len(stateOf(rq).allowed); got != 0 {
		t.Fatalf("a read is no allowed write to record: %d", got)
	}

	t.Run("R-AUTHZ-029 an allowed write is remembered once and recorded in the write transaction", func(t *testing.T) {
		rq := testRequest(formats.Upload)
		tg := formats.Target{Package: "left-pad", Actions: []formats.Action{formats.ActionDeploy}}
		for range 2 {
			if err := h.Authorize(context.Background(), rq, tg); err != nil {
				t.Fatal(err)
			}
		}
		d.recorded = nil
		if err := stateOf(rq).record(context.Background(), nil, d, rq.Subject); err != nil {
			t.Fatal(err)
		}
		if len(d.recorded) != 1 || d.recorded[0].Package != "left-pad" {
			t.Fatalf("recorded = %+v", d.recorded)
		}
	})
	t.Run("R-AUTHZ-016 a refusal is the decision's error", func(t *testing.T) {
		d.decision = authz.Decision{Outcome: authz.DenyHidden}
		err := h.Authorize(context.Background(), testRequest(formats.Download), formats.Target{Actions: []formats.Action{formats.ActionRead}})
		if errs.KindOf(err) != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
		d.decision = authz.Decision{}
	})
	t.Run("a request the dispatcher did not prepare cannot record its writes", func(t *testing.T) {
		rq := testRequest(formats.Upload)
		rq.HostState = nil
		if err := stateOf(rq).record(context.Background(), nil, d, rq.Subject); err == nil {
			t.Fatal("recorded without a state")
		}
	})
}

type fakeOwnership struct{ got []OwnershipCheck }

func (o *fakeOwnership) EnforceTx(_ context.Context, _ meta.Tx, c OwnershipCheck) error {
	o.got = append(o.got, c)
	return errs.E(errs.Forbidden, "package_owned_by_other_owner", "owned")
}

// TestR_AUTHZ_018_ClaimFailsClosed: a writer of no request decides no ownership, and
// a host with no ownership rule refuses a claim instead of letting a publish through;
// with the rule bound, the request's index, subject and coordinate and the kind of
// write reach it (C-14).
func TestR_AUTHZ_018_ClaimFailsClosed(t *testing.T) {
	tg := formats.Target{Package: "left-pad", Actions: []formats.Action{formats.ActionDeploy}}
	t.Run("no request", func(t *testing.T) {
		w := &writer{}
		if err := w.Claim(context.Background(), tg, formats.OwnershipWrite{Publish: true}); errs.KindOf(err) != errs.Internal {
			t.Fatalf("err = %v", err)
		}
		if _, err := w.AutoClaim(context.Background(), "left-pad"); errs.KindOf(err) != errs.Internal {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no ownership rule bound", func(t *testing.T) {
		w := &writer{req: testRequest(formats.Upload)}
		if err := w.Claim(context.Background(), tg, formats.OwnershipWrite{Publish: true}); errs.KindOf(err) != errs.Internal {
			t.Fatalf("err = %v", err)
		}
		if _, err := w.AutoClaim(context.Background(), "left-pad"); errs.KindOf(err) != errs.Internal {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bound", func(t *testing.T) {
		own := &fakeOwnership{}
		rq := testRequest(formats.Upload)
		w := &writer{req: rq, d: writerDeps{own: own}}
		err := w.Claim(context.Background(), tg, formats.OwnershipWrite{Publish: true, FirstVersion: true})
		if errs.KindOf(err) != errs.Forbidden {
			t.Fatalf("the refusal of the rule must reach the caller: %v", err)
		}
		c := own.got[0]
		if !c.Write.Publish || !c.Write.FirstVersion || c.Coordinate.Package != "left-pad" || c.Coordinate.Repository != "key" ||
			c.Coordinate.Kind != authz.KindWrite || c.Subject.Endpoint != "default" {
			t.Fatalf("check = %+v", c)
		}
	})
}

func TestR_ARCH_009_PackageWriterNotifyAndEnqueueNeedTheirSeams(t *testing.T) {
	w := &writer{}
	if err := w.Notify("no_such_area", nil); errs.KindOf(err) != errs.Internal {
		t.Fatalf("an unknown area: %v", err)
	}
	if err := w.Notify("authz", nil); errs.KindOf(err) != errs.Internal {
		t.Fatalf("a change area without the change log: %v", err)
	}
	if err := w.Notify("metadata_changed", "x"); err != nil {
		t.Fatalf("a signal on a replica without signals is dropped: %v", err)
	}
	if err := w.Enqueue(context.Background(), formats.JobSpec{Kind: "x"}); errs.KindOf(err) != errs.Internal {
		t.Fatalf("a job without a queue: %v", err)
	}
}

func TestR_ARCH_007_LinksComeFromTheBaseURL(t *testing.T) {
	l := linkBuilder{base: "https://trove.example.com", format: "npm", key: "npm-local"}
	for got, want := range map[string]string{
		l.Base():                          "https://trove.example.com",
		l.Registry("npm-remote~cache"):    "https://trove.example.com/api/npm/npm-remote~cache/",
		l.Repository():                    "https://trove.example.com/api/npm/npm-local/",
		l.Path("@acme/ui/-/ui-1.0.0.tgz"): "https://trove.example.com/api/npm/npm-local/@acme/ui/-/ui-1.0.0.tgz",
		l.Path("a b/c#d"):                 "https://trove.example.com/api/npm/npm-local/a%20b/c%23d",
		l.Path(""):                        "https://trove.example.com/api/npm/npm-local/",
		l.File("npm-local", "@acme/ui/ui-1.0.0.tgz"): "https://trove.example.com/files/npm-local/@acme/ui/ui-1.0.0.tgz",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestR_ARCH_012_RenditionCacheStoresWhatFitsAnEighth(t *testing.T) {
	reg, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := NewRenditionCache(reg, 8_000)
	if err != nil {
		t.Fatal(err)
	}
	k := formats.RenditionKey{Repo: formats.RepoRef{ID: 1, Key: "k"}, Package: "p", RevSeq: 3, Variant: "full", Endpoint: "default"}
	rc.Put(k, make([]byte, 900))
	if doc, ok := rc.Get(k); !ok || len(doc) != 900 {
		t.Fatalf("a document within an eighth of the budget is stored: %d %v", len(doc), ok)
	}
	k2 := k
	k2.RevSeq = 4
	if _, ok := rc.Get(k2); ok {
		t.Fatal("another revision is another key")
	}
	k3 := k
	k3.Package = "big"
	rc.Put(k3, make([]byte, 1_100))
	if _, ok := rc.Get(k3); ok {
		t.Fatal("a document above an eighth of the budget is not stored")
	}
	off, err := NewRenditionCache(reg2(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	off.Put(k, []byte("x"))
	if _, ok := off.Get(k); ok {
		t.Fatal("a zero budget stores nothing")
	}
	if _, err := NewRenditionCache(nil, 1); err == nil {
		t.Fatal("no registry")
	}
}

func reg2(t *testing.T) *cache.Registry {
	t.Helper()
	r, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNewRefusesAHostWithoutItsParts(t *testing.T) {
	pk, _ := packages.NewStore(nopRunner{})
	for name, o := range map[string]Options{
		"runner":   {Decider: &fakeDecider{}, Items: (*items.Store)(nil), Packages: pk},
		"decider":  {Runner: nopRunner{}, Items: (*items.Store)(nil), Packages: pk},
		"items":    {Runner: nopRunner{}, Decider: &fakeDecider{}, Packages: pk},
		"packages": {Runner: nopRunner{}, Decider: &fakeDecider{}, Items: (*items.Store)(nil)},
	} {
		if _, err := New(o); err == nil {
			t.Errorf("a host without %s", name)
		}
	}
}

func TestEnginesNotWiredAnswerNotSupported(t *testing.T) {
	h := newTestHost(t, &fakeDecider{})
	rq := testRequest(formats.Download)
	if _, err := h.Remote().Metadata(context.Background(), rq, formats.Path{}, ""); errs.KindOf(err) != errs.Internal {
		t.Fatalf("remote: %v", err)
	}
	if _, err := h.Resolver().DeployTarget(context.Background(), rq); errs.KindOf(err) != errs.MethodNotAllowed {
		t.Fatalf("a virtual repository without resolution has no deploy target: %v", err)
	}
	if _, ok := h.Renditions().Get(formats.RenditionKey{}); ok {
		t.Fatal("a host without a rendition cache never hits")
	}
	if h.FormatData("x") != nil {
		t.Fatal("no data port")
	}
}

func TestHooksIgnoreARepositoryOfAnUnregisteredFormat(t *testing.T) {
	h := NewHooks(Deps{Formats: func(string) (formats.Format, bool) { return nil, false }})
	if err := h.OnCommit(context.Background(), nil, items.CommitEvent{Repo: items.RepoRef{Format: "ghost"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.OnItemsRemoved(context.Background(), nil, items.RemovedEvent{Repo: items.RepoRef{Format: ""}}); err != nil {
		t.Fatal(err)
	}
}
