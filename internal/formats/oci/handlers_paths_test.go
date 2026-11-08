package oci

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// pathHidden is a decision point that refuses the actions on the paths and decides everything else: a grant with a path exclude.
func pathHidden(actions []formats.Action, paths ...string) func(formats.Target) error {
	return func(t formats.Target) error {
		if slices.Contains(paths, t.Path) && slices.ContainsFunc(t.Actions, func(a formats.Action) bool { return slices.Contains(actions, a) }) {
			return errs.E(errs.Forbidden, errs.CodeForbidden, "excluded")
		}
		return nil
	}
}

// TestADeleteHonoursTheGrantsThatExcludeAPath: the tag, the manifest and the blob are decided by their paths, as the same removal
// through /files/ is, and a delete by digest is refused when its cascade would take an item whose path the grant excludes (Q-OCI-76).
//
// Mechanism of R-OCI-018, not a proof of its criteria: it checks one step of the cascade, the hook or the grammar of a reference;
// the criteria are the index delete with a shared child, the 409 for a child and the conformance content-management category.
func TestADeleteHonoursTheGrantsThatExcludeAPath(t *testing.T) {
	st := graph()
	st.putTag("app", "keep", digestN(7))
	blob99 := BlobPath("app", digestText(digestN(99)))
	h := listHost(t, st, func(o *formattest.HostOptions) {
		o.Allow = pathHidden([]formats.Action{formats.ActionDelete}, TagPath("app", "keep"), blob99, mpath(2))
	})
	h.PutItem(TagPath("app", "keep"), manifestType, []byte("x"))
	h.PutItem(blob99, "application/octet-stream", []byte("b"))
	for _, n := range []byte{1, 7} {
		h.PutItem(mpath(n), manifestType, []byte{n})
	}
	if err := h.Seed(context.Background(), func(w formats.PackageWriter) error {
		_, _, err := w.EnsureLocked(context.Background(), h.Ref(), "app", "app")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if resp := del(t, h, "app/manifests/keep"); resp.Code != http.StatusForbidden || !stored(h, TagPath("app", "keep")) {
		t.Fatalf("the excluded tag: %d %s", resp.Code, resp.Body)
	}
	if resp := del(t, h, "app/blobs/"+digestText(digestN(99))); resp.Code != http.StatusForbidden || !stored(h, blob99) {
		t.Fatalf("the excluded blob: %d %s", resp.Code, resp.Body)
	}
	// The index 1 lists the child 2, which the grant excludes: deleting the index would take it.
	if resp := del(t, h, "app/manifests/"+digestText(digestN(1))); resp.Code != http.StatusForbidden || !stored(h, mpath(1)) {
		t.Fatalf("an index whose cascade takes an excluded child: %d %s", resp.Code, resp.Body)
	}
	// The manifest 7 has the tag keep, which the grant excludes: deleting it by digest would remove that tag.
	if resp := del(t, h, "app/manifests/"+digestText(digestN(7))); resp.Code != http.StatusForbidden || !stored(h, mpath(7)) {
		t.Fatalf("a manifest whose cascade takes an excluded tag: %d %s", resp.Code, resp.Body)
	}
	if len(st.l().deleted) != 0 {
		t.Errorf("a refused delete recorded %+v", st.l().deleted)
	}
}

// TestTheListsShowOnlyWhatTheCallerMayRead: a tag and a referrer whose path a grant excludes from read are left out of the lists,
// as a pull of them is refused (R-AUTHZ-005, R-OCI-006).
//
// Mechanism of R-OCI-015, not a proof of its criteria: it checks hidden-tag handling or an edge case; the criteria are the n=2
// paging and the conformance content-discovery category.
func TestTheListsShowOnlyWhatTheCallerMayRead(t *testing.T) {
	st := newMemStore()
	for _, tag := range []string{"a", "keep", "z"} {
		st.putTag("app", tag, digestN(1))
	}
	subject := digestN(9)
	st.putManifest(referrer(1, "t", subject, ""))
	st.putManifest(referrer(2, "t", subject, ""))
	h := listHost(t, st, func(o *formattest.HostOptions) {
		o.Allow = pathHidden([]formats.Action{formats.ActionRead}, TagPath("app", "keep"), ManifestPath("app", digestText(digestN(2))))
	})
	_, tags := tagsOf(t, get(t, h, "app/tags/list"))
	if strings.Join(tags, ",") != "a,z" {
		t.Fatalf("tags %v", tags)
	}
	doc := referrersIndexOf(t, get(t, h, "app/referrers/"+digestText(subject)))
	if len(doc.Manifests) != 1 || doc.Manifests[0].Digest != digestText(digestN(1)) {
		t.Fatalf("referrers %+v", doc.Manifests)
	}
	// Every decision names a path (the gate of the list and each entry).
	for _, a := range h.Authorizations() {
		if a.Target.Package != "" && a.Target.Path == "" {
			t.Errorf("a decision without a path: %+v", a.Target)
		}
	}
}
