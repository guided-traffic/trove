package ansible

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/ansibletest"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

// TestR_ANS_029_AWithheldArchiveIsRefusedByTheServeCheck: an archive whose number a tombstone withholds keeps its item and is a 404 on
// every route that serves it until the tombstone is cleared; remote repositories and other paths are never refused.
func TestR_ANS_029_AWithheldArchiveIsRefusedByTheServeCheck(t *testing.T) {
	h := newHost(t)
	f := New()
	st := h.FormatData(ID).(*ansibletest.Store)
	ref := h.Ref()
	path := ArtifactPath("acme", "tools", "1.0.0")
	check := func(r formats.RepoRef, p string) error {
		return f.ServeCheck(context.Background(), h, r, formats.ItemRef{Repo: r, Path: p}, formats.ServeStored)
	}
	if err := check(ref, path); err != nil {
		t.Fatalf("a free number: %v", err)
	}
	if err := st.RemoveVersion(context.Background(), nil, ref.ID, "acme.tools", "1.0.0+b", make([]byte, 32), true, rows.Audit{}); err != nil {
		t.Fatal(err)
	}
	if e, ok := errs.As(check(ref, path)); !ok || e.Kind != errs.NotFound {
		t.Errorf("a withheld number: %v", e)
	}
	if err := check(ref, "collections/acme/tools/collection.json"); err != nil {
		t.Errorf("a document of the collection: %v", err)
	}
	remote := ref
	remote.Class = formats.Remote
	if err := check(remote, path); err != nil {
		t.Errorf("a remote repository's cache has no tombstones: %v", err)
	}
	if found, err := st.ClearTombstone(context.Background(), ref.ID, "acme.tools", version.Number("1.0.0"), rows.Audit{}); err != nil || !found {
		t.Fatalf("clear: %v %v", found, err)
	}
	if err := check(ref, path); err != nil {
		t.Errorf("after the clearing: %v", err)
	}
}

// TestR_ANS_011_ACommitThatIsNoPublishIsRefused: a collection enters a repository only through the publish; a copy or a move reaches
// OnCommit without the payload of one and is refused with 409.
func TestR_ANS_011_ACommitThatIsNoPublishIsRefused(t *testing.T) {
	h := newHost(t)
	err := New().OnCommit(context.Background(), nil, formats.CommitEvent{Origin: formats.OriginUpload, Repo: h.Ref(), Path: formats.Path{Path: ArtifactPath("acme", "tools", "1.0.0")}})
	if e, ok := errs.As(err); !ok || e.Kind != errs.Conflict {
		t.Errorf("OnCommit = %v, want 409", err)
	}
}
