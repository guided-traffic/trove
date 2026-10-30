package formathost

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
)

// TestR_REPO_020_PackageChangedIsThePayloadOfTheEngine: the payload a format attaches to
// metadata_changed (formats.PackageChanged, which the contract spells out because it
// may not import the engine) is the one the engine parses.
func TestR_REPO_020_PackageChangedIsThePayloadOfTheEngine(t *testing.T) {
	for _, name := range []string{"lodash", "@acme/ui", "JSONStream", ""} {
		for _, id := range []int64{0, 1, 4096} {
			if got, want := formats.PackageChanged(id, name), virtual.PackageChanged(id, name); got != want {
				t.Errorf("PackageChanged(%d, %q) = %q, the engine's is %q", id, name, got, want)
			}
		}
	}
}

type recordRemover struct{ got []items.RemoveSpec }

func (r *recordRemover) RemoveIn(_ context.Context, _ meta.Tx, spec items.RemoveSpec) error {
	r.got = append(r.got, spec)
	return nil
}

// TestR_STOR_018_DeleteItemsOfThePackageWriter: the writer of a request removes items
// through the item store in its transaction, and the writer of a hook, which has no
// request, refuses.
func TestR_STOR_018_DeleteItemsOfThePackageWriter(t *testing.T) {
	repo := formats.RepoRef{ID: 7, Key: "npm-local", Class: formats.Local, Format: "npm"}
	rq := &formats.Request{Repo: &repository.Repository{Key: "npm-local"}}
	rm := &recordRemover{}
	w := &writer{req: rq, d: writerDeps{remove: rm}}
	if err := w.DeleteItems(t.Context(), repo, "a/-/a-1.0.0.tgz", "a/-/a-1.0.0.sigstore"); err != nil {
		t.Fatal(err)
	}
	if len(rm.got) != 1 || rm.got[0].Repo.ID != 7 || rm.got[0].Repo.Key != "npm-local" || rm.got[0].Repo.Format != "npm" || len(rm.got[0].Paths) != 2 {
		t.Fatalf("%+v", rm.got)
	}
	for name, w := range map[string]*writer{
		"a writer bound to no request": {d: writerDeps{remove: rm}},
		"a host without a remover":     {req: rq},
	} {
		if err := w.DeleteItems(t.Context(), repo, "a"); err == nil {
			t.Errorf("%s removed items", name)
		}
	}
}
