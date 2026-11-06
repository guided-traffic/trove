package nuget

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

const (
	listPath  = "v3/package/Acme.Widgets/1.2.3.0"
	idPackage = "acme.widgets"
)

// served reports what ServeCheck says of the files of a version.
func (h *writeHost) served(t testing.TB, p string) error {
	t.Helper()
	return h.f.ServeCheck(context.Background(), h, h.Ref(), formats.ItemRef{Repo: h.Ref(), Path: p}, formats.ServeStored)
}

// TestR_NUGET_007_TheDeleteOfTheProtocolUnlistsByDefault: DELETE answers 204 and unlists the
// version, in whatever spelling the client names it; the files stay stored and served, the
// revision rises once and one metadata event records the unlist (Q-NUGET-1).
func TestR_NUGET_007_TheDeleteOfTheProtocolUnlistsByDefault(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	rev := h.RevSeq(idPackage)
	if r := h.do(t, http.MethodDelete, listPath); r.Code != http.StatusNoContent || r.Body.Len() != 0 {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	if got := h.RevSeq(idPackage); got != rev+1 {
		t.Errorf("rev_seq %d -> %d, want one more", rev, got)
	}
	facts, ok, _ := h.st.VersionFacts(context.Background(), h.Ref().ID, idPackage, "1.2.3")
	if !ok || facts.Listed {
		t.Fatalf("version found %v, listed %v: want stored and unlisted", ok, facts.Listed)
	}
	if !h.hasItem(t, acmePath) || !h.hasItem(t, acmeNuspec) {
		t.Error("unlisting removed a file")
	}
	if err := h.served(t, acmePath); err != nil {
		t.Errorf("an unlisted version is not served: %v", err)
	}
	if len(h.st.Listings) != 1 || h.st.Listings[0].Listed || h.st.Listings[0].LowerVersion != "1.2.3" {
		t.Errorf("listings = %+v", h.st.Listings)
	}
	if len(h.st.Tombstones) != 0 {
		t.Error("unlisting wrote a tombstone")
	}
}

// TestR_NUGET_007_AnUnlistIsRepeatable: unlisting an unlisted version answers 204 again and
// changes nothing: no revision, no event (R-NUGET-032).
func TestR_NUGET_007_AnUnlistIsRepeatable(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	h.do(t, http.MethodDelete, listPath)
	rev, events := h.RevSeq(idPackage), len(h.st.Listings)
	for range 2 {
		if r := h.do(t, http.MethodDelete, listPath); r.Code != http.StatusNoContent {
			t.Fatalf("status %d: %s", r.Code, r.Body.String())
		}
	}
	if h.RevSeq(idPackage) != rev || len(h.st.Listings) != events {
		t.Error("a repeated unlist changed the revision or recorded an event")
	}
}

// TestR_NUGET_007_ARelistListsTheVersionAgain: POST answers 200, also for a version that is
// listed already, and lists the version again with one revision and one event.
func TestR_NUGET_007_ARelistListsTheVersionAgain(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	h.do(t, http.MethodDelete, listPath)
	rev := h.RevSeq(idPackage)
	if r := h.do(t, http.MethodPost, listPath); r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	if facts, _, _ := h.st.VersionFacts(context.Background(), h.Ref().ID, idPackage, "1.2.3"); !facts.Listed {
		t.Error("the version is not listed again")
	}
	if got := h.RevSeq(idPackage); got != rev+1 {
		t.Errorf("rev_seq %d -> %d", rev, got)
	}
	events := len(h.st.Listings)
	if r := h.do(t, http.MethodPost, listPath); r.Code != http.StatusOK {
		t.Fatalf("second relist: status %d", r.Code)
	}
	if len(h.st.Listings) != events || h.RevSeq(idPackage) != rev+1 {
		t.Error("a relist of a listed version changed something")
	}
}

// TestR_NUGET_007_ADeleteOrRelistOfAVersionThatDoesNotExistIs404: an unknown ID, an unknown
// version and a relist of a hard-deleted version answer 404 in the native shape.
func TestR_NUGET_007_ADeleteOrRelistOfAVersionThatDoesNotExistIs404(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	for _, c := range []struct{ method, path string }{
		{http.MethodDelete, "v3/package/acme.nothing/1.0.0"},
		{http.MethodDelete, "v3/package/acme.widgets/9.9.9"},
		{http.MethodPost, "v3/package/acme.nothing/1.0.0"},
		{http.MethodPost, "v3/package/acme.widgets/9.9.9"},
	} {
		if r := h.do(t, c.method, c.path); r.Code != http.StatusNotFound || code(t, r) != errs.CodeNotFound {
			t.Errorf("%s %s: status %d: %s", c.method, c.path, r.Code, r.Body.String())
		}
	}
	if h.st.Listings != nil || len(h.st.Tombstones) != 0 {
		t.Error("a request for a version that does not exist changed something")
	}
}

// TestR_NUGET_007_TheDeleteOfADeleteModeRepositoryRemovesTheVersionForGood: in delete mode the
// files and the version go, the tombstone holds the SHA-512 of the removed package, the
// revision rises, and one deleted event with its journal entry records it (Q-NUGET-1,
// R-NUGET-006).
func TestR_NUGET_007_TheDeleteOfADeleteModeRepositoryRemovesTheVersionForGood(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.deleteMode(DeleteModeDelete)
	pkg := h.mustPush(t, "Acme.Widgets", "1.2.3")
	h.mustPush(t, "Acme.Widgets", "1.2.4")
	rev := h.RevSeq(idPackage)
	if r := h.do(t, http.MethodDelete, listPath); r.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	if h.hasItem(t, acmePath) || h.hasItem(t, acmeNuspec) {
		t.Error("the files of the deleted version are still stored")
	}
	if _, ok, _ := h.st.VersionFacts(context.Background(), h.Ref().ID, idPackage, "1.2.3"); ok {
		t.Error("the NuGet row of the deleted version is still there")
	}
	if !h.hasItem(t, "acme.widgets/1.2.4/acme.widgets.1.2.4.nupkg") {
		t.Error("the delete removed another version")
	}
	tomb, ok, _ := h.st.Tombstone(context.Background(), nil, h.Ref().ID, idPackage, "1.2.3")
	if !ok || tomb.Kind != rows.TombstoneRemoved || string(tomb.SHA512) != string(sha512Of(pkg)) {
		t.Errorf("tombstone = %+v (found %v), want a removal holding the SHA-512 of the package", tomb, ok)
	}
	if got := h.RevSeq(idPackage); got <= rev {
		t.Errorf("rev_seq %d -> %d, want it raised", rev, got)
	}
	if len(h.st.Tombstones) != 1 || h.st.Tombstones[0].Audit.RepoKey != "it/main" {
		t.Errorf("tombstone writes = %+v", h.st.Tombstones)
	}
	if err := h.served(t, acmePath); err == nil {
		t.Error("a deleted version is still served")
	}
}

// TestR_NUGET_007_ADeleteOfATombstonedVersionIs204: the repeat of a delete whose answer was
// lost does not fail, in either mode (R-NUGET-032); a relist of it is 404.
func TestR_NUGET_007_ADeleteOfATombstonedVersionIs204(t *testing.T) {
	for _, mode := range []string{DeleteModeUnlist, DeleteModeDelete} {
		t.Run(mode, func(t *testing.T) {
			h := newWriteHost(t, formattest.HostOptions{})
			h.deleteMode(DeleteModeDelete)
			h.mustPush(t, "Acme.Widgets", "1.2.3")
			h.do(t, http.MethodDelete, listPath)
			h.deleteMode(mode)
			tombs, rev := len(h.st.Tombstones), h.RevSeq(idPackage)
			for range 2 {
				if r := h.do(t, http.MethodDelete, listPath); r.Code != http.StatusNoContent {
					t.Fatalf("status %d: %s", r.Code, r.Body.String())
				}
			}
			if len(h.st.Tombstones) != tombs || h.RevSeq(idPackage) != rev {
				t.Error("a repeated delete changed something")
			}
			if r := h.do(t, http.MethodPost, listPath); r.Code != http.StatusNotFound {
				t.Errorf("relist of a deleted version: status %d, want 404", r.Code)
			}
		})
	}
}

// TestR_NUGET_007_AReservedTombstoneBecomesARemovalInDeleteMode: in delete mode a delete of a
// number a restore reserved turns the tombstone into a removal, so that the journaled bytes
// are refused too; in unlist mode it changes nothing. Both answer 204.
func TestR_NUGET_007_AReservedTombstoneBecomesARemovalInDeleteMode(t *testing.T) {
	for mode, want := range map[string]string{DeleteModeUnlist: rows.TombstoneReserved, DeleteModeDelete: rows.TombstoneRemoved} {
		t.Run(mode, func(t *testing.T) {
			h := newWriteHost(t, formattest.HostOptions{})
			h.deleteMode(mode)
			pkg := buildNupkg(t, "Acme.Widgets", "1.2.3")
			h.st.SeedTombstone(h.Ref().ID, idPackage, "1.2.3", sha512Of(pkg), rows.TombstoneReserved)
			if r := h.do(t, http.MethodDelete, listPath); r.Code != http.StatusNoContent {
				t.Fatalf("status %d: %s", r.Code, r.Body.String())
			}
			if got, _, _ := h.st.Tombstone(context.Background(), nil, h.Ref().ID, idPackage, "1.2.3"); got.Kind != want {
				t.Errorf("tombstone kind %q, want %q", got.Kind, want)
			}
			wantCode := errs.CodeNuGetVersionReserved
			if mode == DeleteModeDelete {
				wantCode = errs.CodeNuGetVersionDeleted
			}
			if r := h.push(t, pkg); r.Code == http.StatusConflict && code(t, r) != wantCode {
				t.Errorf("push of the journaled bytes: %s, want %s", r.Body.String(), wantCode)
			} else if r.Code == http.StatusCreated && mode == DeleteModeDelete {
				t.Error("the journaled bytes are admitted after the delete")
			}
		})
	}
}

// TestR_NUGET_007_ThePermissionsDecideBeforeAnythingIsAnswered: a caller without delete gets
// 403 for a delete, also of a tombstoned version, so that the 204 tells nothing to somebody
// who could not delete; a relist needs deploy and ownership, and a caller without them gets
// 403 and changes nothing.
func TestR_NUGET_007_ThePermissionsDecideBeforeAnythingIsAnswered(t *testing.T) {
	forbid := errs.E(errs.Forbidden, errs.CodeForbidden, "no")
	var deny formats.Action
	h := newWriteHost(t, formattest.HostOptions{
		Allow: func(t formats.Target) error {
			if len(t.Actions) == 1 && t.Actions[0] == deny {
				return forbid
			}
			return nil
		},
		Claim: func(t formats.Target, _ formats.OwnershipWrite) error {
			if len(t.Actions) == 1 && t.Actions[0] == deny {
				return forbid
			}
			return nil
		},
	})
	h.deleteMode(DeleteModeDelete)
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	h.mustPush(t, "Acme.Widgets", "1.2.4")
	h.do(t, http.MethodDelete, listPath) // tombstones 1.2.3
	deny = formats.ActionDelete
	for _, p := range []string{listPath, "v3/package/acme.widgets/1.2.4"} {
		if r := h.do(t, http.MethodDelete, p); r.Code != http.StatusForbidden {
			t.Errorf("DELETE %s: status %d, want 403", p, r.Code)
		}
	}
	deny = formats.ActionDeploy
	if r := h.do(t, http.MethodPost, "v3/package/acme.widgets/1.2.4"); r.Code != http.StatusForbidden {
		t.Errorf("relist: status %d, want 403", r.Code)
	}
	if !h.hasItem(t, "acme.widgets/1.2.4/acme.widgets.1.2.4.nupkg") {
		t.Error("a refused delete removed the version")
	}
}

// TestR_NUGET_007_TheOwnershipOfTheCommitTransactionRollsADeleteBack: the ownership rule is
// asked in the transaction under the package row lock; a refusal there leaves the version
// listed.
func TestR_NUGET_007_TheOwnershipOfTheCommitTransactionRollsADeleteBack(t *testing.T) {
	owned := errs.E(errs.Forbidden, errs.CodePackageOwnedByOtherOwner, "owned")
	refuse := false
	h := newWriteHost(t, formattest.HostOptions{
		Claim: func(formats.Target, formats.OwnershipWrite) error {
			if refuse {
				return owned
			}
			return nil
		},
	})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	refuse = true
	if r := h.do(t, http.MethodDelete, listPath); r.Code != http.StatusForbidden {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	if facts, _, _ := h.st.VersionFacts(context.Background(), h.Ref().ID, idPackage, "1.2.3"); !facts.Listed {
		t.Error("a refused unlist unlisted the version")
	}
}

// TestR_NUGET_007_AServeCheckDecidesOnTheNuGetRows: a listed and an unlisted version are
// served, a tombstoned one and a file of a version without a NuGet row are the 404 of a
// missing resource, and a file that names no version is not the format's to refuse.
func TestR_NUGET_007_AServeCheckDecidesOnTheNuGetRows(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	if err := h.served(t, acmePath); err != nil {
		t.Errorf("listed: %v", err)
	}
	h.do(t, http.MethodDelete, listPath)
	if err := h.served(t, acmeNuspec); err != nil {
		t.Errorf("unlisted: %v", err)
	}
	h.st.SeedTombstone(h.Ref().ID, idPackage, "1.2.4", sha512Of([]byte("x")), rows.TombstoneRemoved)
	for _, p := range []string{"acme.widgets/1.2.4/acme.widgets.1.2.4.nupkg", "acme.widgets/9.9.9/acme.widgets.9.9.9.nupkg"} {
		var e *errs.Error
		if err := h.served(t, p); !errors.As(err, &e) || e.Kind != errs.NotFound {
			t.Errorf("%s: %v, want 404", p, err)
		}
	}
	if err := h.served(t, "acme.widgets/index.json"); err != nil {
		t.Errorf("a document path: %v", err)
	}
	remote := formats.RepoRef{ID: 9, Name: qr("r"), Class: formats.Remote, Format: ID}
	if err := h.f.ServeCheck(context.Background(), h, remote, formats.ItemRef{Repo: remote, Path: acmePath}, formats.ServeStored); err != nil {
		t.Errorf("a remote repository is not checked yet: %v", err)
	}
}

// TestR_NUGET_005_ARawDeleteRemovesTheVersionAsAUnit: the removal of the .nupkg through
// /files/ removes the whole version and writes the tombstone, like the protocol delete; the
// removal of the generated .nuspec alone is refused, which rolls it back (Q-NUGET-13).
func TestR_NUGET_005_ARawDeleteRemovesTheVersionAsAUnit(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	pkg := h.mustPush(t, "Acme.Widgets", "1.2.3")
	rq, err := h.NewRequest(context.Background(), http.MethodDelete, "v3/package/acme.widgets/1.2.3", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = h.Remove(context.Background(), rq, "delete", acmeNuspec)
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.Conflict {
		t.Fatalf("removing the generated .nuspec alone = %v, want 409", err)
	}
	if !h.hasItem(t, acmeNuspec) {
		t.Error("the refused removal left the .nuspec removed")
	}
	if err := h.Remove(context.Background(), rq, "delete", acmePath, acmeNuspec); err != nil {
		t.Fatal(err)
	}
	tomb, ok, _ := h.st.Tombstone(context.Background(), nil, h.Ref().ID, idPackage, "1.2.3")
	if !ok || string(tomb.SHA512) != string(sha512Of(pkg)) {
		t.Errorf("tombstone = %+v, found %v", tomb, ok)
	}
	if r := h.push(t, pkg); r.Code != http.StatusConflict || code(t, r) != errs.CodeNuGetVersionDeleted {
		t.Errorf("push after the raw delete: %d %s", r.Code, r.Body.String())
	}
}

// TestR_NUGET_006_ARestoreFromTheTrashBringsTheTombstoneDown: the restore of the package of a
// version removes its tombstone in the restore transaction, because the original bytes come
// back, and raises the revision (R-STOR-023, P1). The trash can does not exist yet, so the
// event is built the way the restore will build it, over a version that holds both its row
// and its tombstone, as a trashed version does.
func TestR_NUGET_006_ARestoreFromTheTrashBringsTheTombstoneDown(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	pkg := h.mustPush(t, "Acme.Widgets", "1.2.3")
	h.st.SeedTombstone(h.Ref().ID, idPackage, "1.2.3", sha512Of(pkg), rows.TombstoneRemoved)
	it, _, _ := h.Items().Stat(context.Background(), h.Ref(), formats.Path{Path: acmePath})
	rev := h.RevSeq(idPackage)
	err := h.Write(context.Background(), nil, func(tx formats.Tx, w formats.PackageWriter) error {
		return h.f.OnItemsRestored(context.Background(), tx, formats.ItemsEvent{
			Repo: h.Ref(), Reason: "restore", Packages: w, Data: h.st,
			Items: []formats.RemovedItem{{Item: it.Ref, Version: it.Version}},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := h.st.Tombstone(context.Background(), nil, h.Ref().ID, idPackage, "1.2.3"); found {
		t.Error("the tombstone stays after the restore")
	}
	if h.RevSeq(idPackage) <= rev {
		t.Error("the restore did not raise the revision")
	}
}
