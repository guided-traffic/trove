package oci

import (
	"context"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

func del(t *testing.T, h *formattest.Host, path string) *formattest.Response {
	t.Helper()
	resp, err := h.Serve(context.Background(), http.MethodDelete, path, nil, false)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

func stored(h *formattest.Host, path string) bool {
	_, found, _ := h.Items().Stat(context.Background(), h.Ref(), formats.Path{Path: path})
	return found
}

const manifestType = "application/vnd.oci.image.manifest.v1+json"

// TestR_OCI_018_DeleteOfAManifestByDigest: the manifest and its row go with 202; a manifest an index lists is refused with 409 DENIED
// and stays; a digest the repository holds nothing under is 404 MANIFEST_UNKNOWN (R-OCI-018).
func TestR_OCI_018_DeleteOfAManifestByDigest(t *testing.T) {
	st := graph()
	h := listHost(t, st)
	for _, n := range []byte{1, 2, 7} {
		h.PutItem(mpath(n), manifestType, []byte{n})
	}
	ref := func(n byte) string { return "app/manifests/" + digestText(digestN(n)) }

	if resp := del(t, h, ref(2)); resp.Code != http.StatusConflict || registryCode(t, resp) != "DENIED" || !stored(h, mpath(2)) {
		t.Fatalf("a child of an index: %d %s", resp.Code, resp.Body)
	}
	if resp := del(t, h, ref(8)); resp.Code != http.StatusNotFound || registryCode(t, resp) != "MANIFEST_UNKNOWN" {
		t.Fatalf("an unknown digest: %d %s", resp.Code, resp.Body)
	}
	// 7 is a referrer with no tag and no child: it goes alone.
	if resp := del(t, h, ref(7)); resp.Code != http.StatusAccepted || resp.Body.Len() != 0 || stored(h, mpath(7)) {
		t.Fatalf("a manifest: %d %s", resp.Code, resp.Body)
	}
	if len(st.l().deleted) != 1 || st.l().deleted[0].Digest != digestN(7) || st.l().deleted[0].Deleted != 0 {
		t.Errorf("recorded %+v", st.l().deleted)
	}
	if _, ok := st.manifests[manifestKey("app", digestN(7))]; ok {
		t.Error("the row stayed")
	}
	// A second DELETE finds nothing.
	if resp := del(t, h, ref(7)); resp.Code != http.StatusNotFound {
		t.Fatalf("repeated: %d %s", resp.Code, resp.Body)
	}
}

func TestR_OCI_018_DeleteOfATagIsDecidedForTheTagAndTheName(t *testing.T) {
	st := newMemStore()
	h := listHost(t, st, func(o *formattest.HostOptions) {
		o.Allow = func(tg formats.Target) error {
			for _, a := range tg.Actions {
				if a == formats.ActionDelete && tg.Package == "locked" {
					return errs.E(errs.Forbidden, errs.CodeForbidden, "no delete")
				}
			}
			return nil
		}
	})
	h.PutItem(TagPath("locked", "1"), manifestType, []byte("x"))
	h.PutItem(TagPath("open", "1"), manifestType, []byte("x"))
	for _, n := range []string{"locked", "open"} {
		if err := h.Seed(context.Background(), func(w formats.PackageWriter) error {
			_, _, err := w.EnsureLocked(context.Background(), h.Ref(), n, n)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if resp := del(t, h, "locked/manifests/1"); resp.Code != http.StatusForbidden || registryCode(t, resp) != "DENIED" || !stored(h, TagPath("locked", "1")) {
		t.Fatalf("without delete: %d %s", resp.Code, resp.Body)
	}
	if resp := del(t, h, "open/manifests/1"); resp.Code != http.StatusAccepted || stored(h, TagPath("open", "1")) {
		t.Fatalf("with delete: %d %s", resp.Code, resp.Body)
	}
	var tagDecision bool
	for _, a := range h.Authorizations() {
		tagDecision = tagDecision || (a.Target.Package == "open" && a.Target.Version == "1" && len(a.Target.Actions) == 1 && a.Target.Actions[0] == formats.ActionDelete)
	}
	if !tagDecision {
		t.Errorf("no delete decision for the tag: %+v", h.Authorizations())
	}
	// The ownership of the image name is checked in the transaction of the removal.
	claimed := false
	for _, c := range h.Claims() {
		claimed = claimed || c.Target.Package == "open"
	}
	if !claimed {
		t.Errorf("no ownership claim: %+v", h.Claims())
	}
	if resp := del(t, h, "open/manifests/1"); resp.Code != http.StatusNotFound || registryCode(t, resp) != "MANIFEST_UNKNOWN" {
		t.Fatalf("an unknown tag: %d %s", resp.Code, resp.Body)
	}
}

func TestR_OCI_018_DeleteRefusesAReferenceOutsideTheGrammars(t *testing.T) {
	h := listHost(t, newMemStore())
	for path, code := range map[string]string{
		"app/manifests/-bad":                      "TAG_INVALID",
		"app/manifests/sha256:xyz":                "DIGEST_INVALID",
		"app/manifests/md5:" + "a":                "DIGEST_INVALID",
		"app/manifests/" + "sha256:" + "A" + "bc": "DIGEST_INVALID",
	} {
		// Match splits a reference with a colon to the digest routes and the rest to the tag routes; the handler refuses both.
		resp := del(t, h, path)
		if resp.Code != http.StatusBadRequest || registryCode(t, resp) != code {
			t.Errorf("%s: %d %s", path, resp.Code, resp.Body)
		}
	}
	if resp := del(t, h, "app/blobs/sha256:xyz"); resp.Code != http.StatusBadRequest || registryCode(t, resp) != "DIGEST_INVALID" {
		t.Errorf("blob: %d %s", resp.Code, resp.Body)
	}
}

// TestR_OCI_018_DeleteOfABlob: a blob no manifest of the name references is removed (202); a referenced one is refused (409 DENIED) and
// stays; a blob the name holds no item of is 404 BLOB_UNKNOWN.
func TestR_OCI_018_DeleteOfABlob(t *testing.T) {
	st := graph()
	h := listHost(t, st)
	blob := func(n byte) string { return BlobPath("app", digestText(digestN(n))) }
	h.PutItem(blob(20), "application/octet-stream", []byte("referenced"))
	h.PutItem(blob(99), "application/octet-stream", []byte("loose"))
	url := func(n byte) string { return "app/blobs/" + digestText(digestN(n)) }

	if resp := del(t, h, url(20)); resp.Code != http.StatusConflict || registryCode(t, resp) != "DENIED" || !stored(h, blob(20)) {
		t.Fatalf("a referenced blob: %d %s", resp.Code, resp.Body)
	}
	if resp := del(t, h, url(99)); resp.Code != http.StatusAccepted || stored(h, blob(99)) {
		t.Fatalf("an unreferenced blob: %d %s", resp.Code, resp.Body)
	}
	if resp := del(t, h, url(98)); resp.Code != http.StatusNotFound || registryCode(t, resp) != "BLOB_UNKNOWN" {
		t.Fatalf("an unknown blob: %d %s", resp.Code, resp.Body)
	}
	if got := st.l().deleted; len(got) != 0 {
		t.Errorf("a blob removal recorded a manifest deletion: %+v", got)
	}
}

// TestDeleteRoutesServeLocalAndVirtual: a delete on a remote repository is the dispatcher's 405; on a virtual repository the
// handler answers 405, because the dispatcher would carry a write to the deployment target (R-OCI-028).
func TestDeleteRoutesServeLocalAndVirtual(t *testing.T) {
	for _, r := range New().Routes() {
		if r.Name != routeManifestDelete && r.Name != routeBlobDelete {
			continue
		}
		if len(r.Classes) != 2 || r.Classes[0] != formats.Local || r.Classes[1] != formats.Virtual || r.Action != formats.ActionDelete {
			t.Errorf("%s: classes %v action %v", r.Name, r.Classes, r.Action)
		}
	}
}

// TestR_OCI_018_DeleteOfABlobMakesNoPackage: a name that holds only blobs has no package row, and deleting one of its blobs creates none
// (a row without a version is a row nobody reads).
func TestR_OCI_018_DeleteOfABlobMakesNoPackage(t *testing.T) {
	h := listHost(t, newMemStore())
	p := BlobPath("blobsonly", digestText(digestN(40)))
	h.PutItem(p, "application/octet-stream", []byte("layer"))
	if resp := del(t, h, "blobsonly/blobs/"+digestText(digestN(40))); resp.Code != http.StatusAccepted || stored(h, p) {
		t.Fatalf("%d %s", resp.Code, resp.Body)
	}
	if _, found, err := h.Packages().Package(context.Background(), h.Ref(), "blobsonly"); err != nil || found {
		t.Fatalf("a package row was made: %v %v", found, err)
	}
}
