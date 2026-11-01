package formathost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/repository"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

// recordSignals is a Signaler that remembers what was attached to a transaction.
type recordSignals struct{ got [][2]string }

func (r *recordSignals) Notify(_ meta.Tx, area, payload string) {
	r.got = append(r.got, [2]string{area, payload})
}

// TestQ_ARCH_259_NotifyIsLimitedToTheAreasTheFormatDeclared: a format notifies the areas
// its descriptor lists and no other; one (area, payload) is attached once per
// transaction, so a signal the writer raised for a package is not repeated.
func TestQ_ARCH_259_NotifyIsLimitedToTheAreasTheFormatDeclared(t *testing.T) {
	sig := &recordSignals{}
	w := &writer{tx: &meta.TxState{}, d: writerDeps{signals: sig, areas: []string{catalog.MetadataChangedArea}}}
	if err := w.Notify("authz", "x"); errs.KindOf(err) != errs.Internal {
		t.Fatalf("an area the format did not declare: %v", err)
	}
	if err := w.Notify("no_such_area", nil); errs.KindOf(err) != errs.Internal {
		t.Fatalf("an unknown area: %v", err)
	}
	if len(sig.got) != 0 {
		t.Fatalf("a refused notification attached %v", sig.got)
	}
	p := catalog.PackageChanged(7, "react")
	for range 2 {
		if err := w.Notify(catalog.MetadataChangedArea, p); err != nil {
			t.Fatal(err)
		}
	}
	if len(sig.got) != 1 || sig.got[0] != [2]string{catalog.MetadataChangedArea, p} {
		t.Fatalf("attached %v, want the signal once", sig.got)
	}
	t.Run("a replica without signals drops the wake-up", func(t *testing.T) {
		w := &writer{tx: &meta.TxState{}, d: writerDeps{areas: []string{catalog.MetadataChangedArea}}}
		if err := w.Notify(catalog.MetadataChangedArea, "x"); err != nil {
			t.Fatal(err)
		}
	})
}

// TestQ_ARCH_259_APackageTheTransactionRaisedSignalsOnce: the function the package
// writer calls on a raise attaches metadata_changed with the payload of the engine,
// once per package, and a format that names the same payload adds nothing.
func TestQ_ARCH_259_APackageTheTransactionRaisedSignalsOnce(t *testing.T) {
	sig := &recordSignals{}
	tx := &meta.TxState{}
	raised := packageRaised(sig)
	a, b := packages.Ref{ID: 1, RepoID: 7, Name: "react"}, packages.Ref{ID: 2, RepoID: 7, Name: "left-pad"}
	for _, r := range []packages.Ref{a, a, b, a} {
		if err := raised(context.Background(), tx, r); err != nil {
			t.Fatal(err)
		}
	}
	want := [][2]string{
		{catalog.MetadataChangedArea, catalog.PackageChanged(7, "react")},
		{catalog.MetadataChangedArea, catalog.PackageChanged(7, "left-pad")},
	}
	if len(sig.got) != 2 || sig.got[0] != want[0] || sig.got[1] != want[1] {
		t.Fatalf("attached %v, want %v", sig.got, want)
	}
	w := &writer{tx: tx, d: writerDeps{signals: sig, areas: []string{catalog.MetadataChangedArea}}}
	if err := w.Notify(catalog.MetadataChangedArea, catalog.PackageChanged(7, "react")); err != nil {
		t.Fatal(err)
	}
	if len(sig.got) != 2 {
		t.Fatalf("the format repeated a signal the writer had raised: %v", sig.got)
	}
	t.Run("another transaction signals again", func(t *testing.T) {
		if err := raised(context.Background(), &meta.TxState{}, a); err != nil || len(sig.got) != 3 {
			t.Fatalf("err = %v attached %v", err, sig.got)
		}
	})
}

// TestQ_ARCH_260_AWriterWritesToItsOwnRepositoryOnly: a writer bound to the request's or
// the event's repository refuses EnsureLocked and DeleteItems for any other.
func TestQ_ARCH_260_AWriterWritesToItsOwnRepositoryOnly(t *testing.T) {
	own := formats.RepoRef{ID: 7, Key: "npm-local", Class: formats.Local, Format: "npm"}
	other := formats.RepoRef{ID: 8, Key: "npm-team", Class: formats.Local, Format: "npm"}
	rq := &formats.Request{Repo: &repository.Repository{Key: "npm-local"}}
	rm := &recordRemover{}
	w := &writer{req: rq, bound: &own, d: writerDeps{remove: rm}}

	if err := w.DeleteItems(t.Context(), other, "a/-/a-1.0.0.tgz"); errs.KindOf(err) != errs.Internal {
		t.Fatalf("a delete in another repository: %v", err)
	}
	if len(rm.got) != 0 {
		t.Fatalf("the other repository's items were removed: %+v", rm.got)
	}
	if _, _, err := w.EnsureLocked(t.Context(), other, "left-pad", ""); errs.KindOf(err) != errs.Internal {
		t.Fatalf("a package row in another repository: %v", err)
	}
	if err := w.DeleteItems(t.Context(), own, "a/-/a-1.0.0.tgz"); err != nil || len(rm.got) != 1 {
		t.Fatalf("a delete in the bound repository: %v %+v", err, rm.got)
	}
}

// TestQ_ARCH_260_TheStoresRefuseARepositoryOutsideTheRequest: inside ServeRoute the read
// stores answer for the request's repository and the members below it, and refuse every
// other repository with an internal error.
func TestQ_ARCH_260_TheStoresRefuseARepositoryOutsideTheRequest(t *testing.T) {
	local := vt.LocalRepo(1, "member-local")
	remote := vt.RemoteRepo(2, "member-remote")
	v := vt.VirtualRepo(9, "all", 60, nil, local, remote)
	v.Format = "probe"
	h := newTestHost(t, &fakeDecider{})
	ref := func(id int64, key string) formats.RepoRef {
		return formats.RepoRef{ID: id, Key: key, Class: formats.Local, Format: "probe"}
	}

	run := func(t *testing.T, repo repository.Repository, probe func(ctx context.Context) error) error {
		t.Helper()
		var got error
		rq := &formats.Request{
			Request: httptest.NewRequest(http.MethodGet, "/api/probe/"+repo.Key+"/x", nil),
			Repo:    &repo,
			Route: &formats.Route{Name: "probe.get", Kind: formats.Metadata, Handler: func(ctx context.Context, _ *formats.Request, _ http.ResponseWriter) error {
				got = probe(ctx)
				return nil
			}},
			Subject: authz.Subject{Name: "alice", Endpoint: "default"},
		}
		Prepare(rq)
		formats.ServeRoute(portFormat{id: "probe"}, httptest.NewRecorder(), rq, false)
		return got
	}
	pk := func(id int64, key string) func(context.Context) error {
		return func(ctx context.Context) error {
			_, _, err := h.Packages().Package(ctx, ref(id, key), "left-pad")
			return err
		}
	}
	t.Run("the request's own repository and its members are read", func(t *testing.T) {
		for _, c := range []struct {
			id  int64
			key string
		}{{9, "all"}, {1, "member-local"}, {2, "member-remote"}} {
			if err := run(t, v, pk(c.id, c.key)); err != nil {
				t.Errorf("%s: %v", c.key, err)
			}
		}
	})
	t.Run("another repository is refused by every read store", func(t *testing.T) {
		other := ref(99, "somebody-elses")
		for name, probe := range map[string]func(context.Context) error{
			"Package": pk(99, "somebody-elses"),
			"Versions": func(ctx context.Context) error {
				_, _, err := h.Packages().Versions(ctx, other, "left-pad")
				return err
			},
			"RevSeqs": func(ctx context.Context) error {
				_, err := h.Packages().RevSeqs(ctx, []formats.RepoRef{ref(1, "member-local"), other}, "left-pad")
				return err
			},
			"Stat": func(ctx context.Context) error {
				_, _, err := h.Items().Stat(ctx, other, formats.Path{Path: "left-pad"})
				return err
			},
			"Open": func(ctx context.Context) error {
				_, err := h.Items().Open(ctx, formats.Item{Ref: formats.ItemRef{Repo: other, Path: "left-pad"}})
				return err
			},
		} {
			if err := run(t, v, probe); errs.KindOf(err) != errs.Internal {
				t.Errorf("%s: %v, want an internal error", name, err)
			}
		}
	})
	t.Run("a member is no scope of a request for the sibling", func(t *testing.T) {
		if err := run(t, local, pk(2, "member-remote")); errs.KindOf(err) != errs.Internal {
			t.Fatalf("a request for member-local read member-remote: %v", err)
		}
	})
	t.Run("a context without a request scope is not checked: a hook acts on its event", func(t *testing.T) {
		if err := pk(99, "somebody-elses")(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
