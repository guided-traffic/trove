package oci

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

// runHook runs OnItemsRemoved over an event that removed the items at the paths, in the transaction of the twin host, and returns what
// the hook asked to cascade.
func runHook(t *testing.T, h *formattest.Host, st *memStore, reason string, paths ...string) (cascaded []string, err error) {
	t.Helper()
	err = h.Write(context.Background(), nil, func(tx formats.Tx, w formats.PackageWriter) error {
		var items []formats.RemovedItem
		for _, p := range paths {
			items = append(items, formats.RemovedItem{Item: formats.ItemRef{Repo: h.Ref(), Path: p}})
		}
		return New().OnItemsRemoved(context.Background(), tx, formats.ItemsEvent{
			Repo: h.Ref(), Items: items, Reason: reason, Actor: "5b2a0c0e-0000-4000-8000-000000000001", Packages: w, Data: st,
			Cascade: func(_ context.Context, p ...string) error { cascaded = append(cascaded, p...); return nil },
		})
	})
	sort.Strings(cascaded)
	return cascaded, err
}

func mpath(n byte) string { return ManifestPath("app", digestText(digestN(n))) }

// graph is an index (1) over four platform manifests (2, 3, 4, 6), a second index (5) over 4, a tagged platform manifest (6), a referrer
// (7) of the first index and the tags latest and v1 on it.
func graph() *memStore {
	st := newMemStore()
	st.putManifest(rows.Manifest{Name: "app", Digest: digestN(1)},
		rows.Reference{Target: digestN(2), Kind: rows.KindManifest}, rows.Reference{Target: digestN(3), Kind: rows.KindManifest},
		rows.Reference{Target: digestN(4), Kind: rows.KindManifest}, rows.Reference{Target: digestN(6), Kind: rows.KindManifest})
	st.putManifest(rows.Manifest{Name: "app", Digest: digestN(2)}, rows.Reference{Target: digestN(20), Kind: rows.KindBlob})
	st.putManifest(rows.Manifest{Name: "app", Digest: digestN(3)}, rows.Reference{Target: digestN(30), Kind: rows.KindBlob})
	st.putManifest(rows.Manifest{Name: "app", Digest: digestN(4)}, rows.Reference{Target: digestN(40), Kind: rows.KindBlob})
	st.putManifest(rows.Manifest{Name: "app", Digest: digestN(5)}, rows.Reference{Target: digestN(4), Kind: rows.KindManifest})
	st.putManifest(rows.Manifest{Name: "app", Digest: digestN(6)}, rows.Reference{Target: digestN(60), Kind: rows.KindBlob})
	subject := digestN(1)
	st.putManifest(rows.Manifest{Name: "app", Digest: digestN(7), Subject: &subject})
	st.putTag("app", "latest", digestN(1))
	st.putTag("app", "v1", digestN(1))
	st.putTag("app", "linux", digestN(6))
	return st
}

// TestR_OCI_018_ARemovedManifestTakesItsTagsReferrersAndExclusiveChildren: the hook of a removed index cascades to its tags, its referrers
// and the children no other manifest and no tag names, and records the deletion with its journal entry (R-OCI-018, R-OCI-033).
func TestR_OCI_018_ARemovedManifestTakesItsTagsReferrersAndExclusiveChildren(t *testing.T) {
	st := graph()
	h := listHost(t, st)
	got, err := runHook(t, h, st, "delete", mpath(1))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		TagPath("app", "latest"), TagPath("app", "v1"), mpath(7), mpath(2), mpath(3), // 4 is listed by the second index, 6 is tagged
	}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("cascade\n got %q\nwant %q", got, want)
	}
	if _, ok := st.manifests[manifestKey("app", digestN(1))]; ok {
		t.Error("the row of the index is still there")
	}
	if len(st.l().deleted) != 1 || st.l().deleted[0].Digest != digestN(1) || st.l().deleted[0].Deleted != len(want) || st.l().deleted[0].Name != "app" ||
		st.l().deleted[0].Actor != "5b2a0c0e-0000-4000-8000-000000000001" || st.l().deleted[0].RepoKey != "it/main" {
		t.Errorf("recorded %+v", st.l().deleted)
	}
	if rev := h.RevSeq("app"); rev == 0 {
		t.Error("the revision of the package did not rise")
	}
}

// TestR_OCI_018_AnIndexMustGoBeforeItsChild: a manifest that an index of the name lists is refused, unless the same removal takes the
// index as well (a folder deleted with its subtree).
func TestR_OCI_018_AnIndexMustGoBeforeItsChild(t *testing.T) {
	st := graph()
	h := listHost(t, st)
	_, err := runHook(t, h, st, "delete", mpath(2))
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.Conflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if len(st.l().deleted) != 0 {
		t.Errorf("a refused removal recorded %+v", st.l().deleted)
	}
	if _, ok := st.manifests[manifestKey("app", digestN(2))]; !ok {
		t.Error("the refused manifest lost its row")
	}
	if _, err := runHook(t, h, st, "delete", mpath(2), mpath(1), mpath(3), mpath(4), mpath(6), mpath(7), mpath(5)); err != nil {
		t.Fatalf("a removal that takes the indexes too: %v", err)
	}
}

func TestR_OCI_018_ABlobAManifestReferencesIsNotRemoved(t *testing.T) {
	st := graph()
	h := listHost(t, st)
	blob := func(n byte) string { return BlobPath("app", digestText(digestN(n))) }
	var e *errs.Error
	if _, err := runHook(t, h, st, "delete", blob(20)); !errors.As(err, &e) || e.Kind != errs.Conflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if _, err := runHook(t, h, st, "delete", blob(99)); err != nil {
		t.Fatalf("a blob nobody references: %v", err)
	}
	// The manifest that references the blob goes in the same removal: nothing references it any more.
	if _, err := runHook(t, h, st, "delete", blob(30), mpath(3), mpath(1), mpath(2), mpath(4), mpath(6), mpath(7), mpath(5)); err != nil {
		t.Fatalf("a blob and its manifest: %v", err)
	}
}

// TestR_OCI_018_ATrashedManifestKeepsItsRowsAndItsTags: the rows stay while an item is in the trash (R-OCI-007), and a cascade follows
// the reason of its event (ItemsEvent.Cascade).
func TestR_OCI_018_ATrashedManifestKeepsItsRowsAndItsTags(t *testing.T) {
	st := graph()
	h := listHost(t, st)
	got, err := runHook(t, h, st, "trash", mpath(7))
	if err != nil || len(got) != 0 || len(st.l().deleted) != 0 {
		t.Fatalf("cascade %q, recorded %v, err %v", got, st.l().deleted, err)
	}
	if _, ok := st.manifests[manifestKey("app", digestN(7))]; !ok {
		t.Error("the row of a trashed manifest was removed")
	}
	// The refusal applies to the trash as well: an index would keep a dangling reference.
	if _, err := runHook(t, h, st, "trash", mpath(2)); err == nil {
		t.Error("a listed child was trashed")
	}
}

func TestR_OCI_018_ARemoteRemovesTheRowsOfAnEvictedManifest(t *testing.T) {
	st := graph()
	h := listHost(t, st, func(o *formattest.HostOptions) { o.Class = formats.Remote })
	if _, err := runHook(t, h, st, "delete", mpath(7), TagPath("app", "v1"), BlobPath("app", digestText(digestN(20)))); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.manifests[manifestKey("app", digestN(7))]; ok {
		t.Error("the row of the evicted manifest is still there")
	}
	if _, ok := st.manifests[manifestKey("app", digestN(2))]; !ok {
		t.Error("a row of a manifest that stayed was removed")
	}
	if len(st.l().deleted) != 0 {
		t.Errorf("a cache eviction is no deletion event: %+v", st.l().deleted)
	}
}

func TestR_OCI_018_AnItemOutsideTheLayoutIsNotTheFormats(t *testing.T) {
	st := graph()
	h := listHost(t, st)
	if got, err := runHook(t, h, st, "delete", "app/_taglist", "x", "Bad/_manifests/sha256:xx", "app/_manifests/notadigest"); err != nil || len(got) != 0 {
		t.Fatalf("%q %v", got, err)
	}
}

// TestR_OCI_018_ARemovedTagTakesItsVersionRow: DELETE of a tag removes the tag item and the version that its item led; the manifest stays.
func TestR_OCI_018_ARemovedTagTakesItsVersionRow(t *testing.T) {
	st := newMemStore()
	st.putManifest(rows.Manifest{Name: "app", Digest: digestN(1)})
	h := listHost(t, st)
	manifest := h.PutItem(mpath(1), "application/vnd.oci.image.manifest.v1+json", []byte(`{"m":1}`))
	tag := h.PutItem(TagPath("app", "1"), "application/vnd.oci.image.manifest.v1+json", []byte(`{"m":1}`))
	if err := h.Seed(context.Background(), func(w formats.PackageWriter) error {
		pkg, _, err := w.EnsureLocked(context.Background(), h.Ref(), "app", "app")
		if err != nil {
			return err
		}
		_, err = w.InsertVersion(context.Background(), pkg, formats.NewVersion{Version: "1", LeadItem: &tag})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = manifest
	before := h.RevSeq("app")
	resp, err := h.Serve(context.Background(), http.MethodDelete, "app/manifests/1", nil, false)
	if err != nil || resp.Code != http.StatusAccepted {
		t.Fatalf("%v %d %s", err, resp.Code, resp.Body)
	}
	vs, _, err := h.Packages().Versions(context.Background(), h.Ref(), "app")
	if err != nil || len(vs) != 0 {
		t.Fatalf("versions after the delete: %+v %v", vs, err)
	}
	if _, found, _ := h.Items().Stat(context.Background(), h.Ref(), formats.Path{Path: TagPath("app", "1")}); found {
		t.Error("the tag item is still there")
	}
	if _, found, _ := h.Items().Stat(context.Background(), h.Ref(), formats.Path{Path: mpath(1)}); !found {
		t.Error("deleting a tag removed the manifest")
	}
	if h.RevSeq("app") <= before {
		t.Error("the revision did not rise")
	}
}
