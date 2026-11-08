package oci

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

func runScoped(t *testing.T, st *memStore, scope func(string) bool, paths ...string) (cascaded []string, err error) {
	t.Helper()
	h := listHost(t, st)
	err = h.Write(context.Background(), nil, func(tx formats.Tx, w formats.PackageWriter) error {
		var items []formats.RemovedItem
		for _, p := range paths {
			items = append(items, formats.RemovedItem{Item: formats.ItemRef{Repo: h.Ref(), Path: p}})
		}
		return New().OnItemsRemoved(context.Background(), tx, formats.ItemsEvent{
			Repo: h.Ref(), Items: items, Reason: "delete", Packages: w, Data: st,
			Cascade: func(_ context.Context, p ...string) error { cascaded = append(cascaded, p...); return nil },
			InScope: func(_ context.Context, p string) (bool, error) { return scope(p), nil },
		})
	})
	return cascaded, err
}

// TestR_OCI_018_ABlobGoesWithTheManifestsThatTheSameDeleteTakesLater: the batches of the delete job commit one by one and the blobs of a name
// come first; a blob that a manifest of the same delete references is removed together with that manifest, in the transaction of the
// blob, so a batch that is refused later cannot leave the manifest over a removed blob (Q-OCI-73).
func TestR_OCI_018_ABlobGoesWithTheManifestsThatTheSameDeleteTakesLater(t *testing.T) {
	blob := BlobPath("app", digestText(digestN(20))) // referenced by the manifest 2 of graph()
	inScope := func(p string) bool { return p == mpath(2) }

	st := graph()
	got, err := runScoped(t, st, inScope, blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != mpath(2) {
		t.Fatalf("cascade %q, want the manifest that references the blob", got)
	}

	// A manifest outside the removal keeps the blob, and the refusal is the registry's DENIED.
	st = graph()
	_, err = runScoped(t, st, func(string) bool { return false }, blob)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.CodeOCIReferenced || e.Kind != errs.Conflict {
		t.Fatalf("err = %v, want the reference refusal", err)
	}

	// A child listed by an index of the same delete goes after the index, in the transaction of the child.
	st = graph()
	got, err = runScoped(t, st, func(p string) bool { return p == mpath(1) }, mpath(2))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range got {
		found = found || p == mpath(1)
	}
	if !found {
		t.Fatalf("cascade %q, want the index that lists the child", got)
	}
}
