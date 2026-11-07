//go:build integration

package backup

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
)

const (
	widgets = "acme.widgets"
	system  = "00000000-0000-0000-0000-000000000000"
)

func (e *fenv) published(id, version, sum string) journal.Result {
	e.t.Helper()
	return e.replay(audit.EventPackagePublished, e.nugetEntry(id, version, sum), time.Now())
}

func (e *fenv) deleted(id, version, sum string) journal.Result {
	e.t.Helper()
	return e.replay(audit.EventPackageDeleted, e.nugetEntry(id, version, sum), time.Now())
}

func TestR_NUGET_033_EveryRowOfTheReplayTable(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	push := func(pkg []byte) (int, string) {
		a := e.pushNuGet(r, pkg)
		return a.Status, nugetCode(a)
	}

	t.Run("R-NUGET-033 published, absent without a tombstone: a reserved tombstone with the journaled SHA-512 (applied)", func(t *testing.T) {
		orig := nupkg(t, "Acme.Widgets", "3.0.0", "the original build")
		rebuilt := nupkg(t, "Acme.Widgets", "3.0.0", "a rebuild")
		res := e.published(widgets, "3.0.0", sha512hex(orig))
		if res.Outcome != journal.Applied {
			t.Fatalf("outcome %q", res.Outcome)
		}
		if kind, sha := e.nugetTombstone(widgets, "3.0.0"); kind != "reserved" || sha != sha512hex(orig) {
			t.Fatalf("tombstone = %q %s", kind, sha)
		}
		if got := e.st.Text(`SELECT deleted_by::text FROM nuget_tombstone WHERE lower_version = '3.0.0'`); got != system {
			t.Errorf("deleted_by = %s, want the system principal of the restore", got)
		}
		if n := e.nugetVersions(widgets); n != 0 {
			t.Errorf("%d versions: the replay stored a version", n)
		}
		// A reserved number refuses other bytes and admits the identical file, which removes the tombstone in its commit.
		if status, code := push(rebuilt); status != http.StatusConflict || code != "nuget_version_reserved" {
			t.Errorf("a rebuilt 3.0.0 = %d %s, want 409 nuget_version_reserved", status, code)
		}
		if status, code := push(orig); status != http.StatusCreated {
			t.Errorf("the original 3.0.0 = %d %s, want 201", status, code)
		}
		if kind, _ := e.nugetTombstone(widgets, "3.0.0"); kind != "" {
			t.Errorf("the reservation (%s) outlived the push that used it", kind)
		}
		a := e.nugetGet(r, repoNuGet, "v3/flatcontainer/"+widgets+"/3.0.0/"+widgets+".3.0.0.nupkg")
		if a.Status != 200 || sha512hex(a.Body) != sha512hex(orig) {
			t.Errorf("the restored version through the flat container = %d", a.Status)
		}
	})
	t.Run("R-NUGET-033 published, present: nothing (already in effect)", func(t *testing.T) {
		pkg := nupkg(t, "Acme.Widgets", "2.0.0", "two")
		e.mustPushNuGet(r, pkg)
		rev := e.revSeq(repoNuGet, widgets)
		if res := e.published(widgets, "2.0.0", sha512hex(pkg)); res.Outcome != journal.AlreadyInEffect {
			t.Fatalf("outcome %q", res.Outcome)
		}
		if res := e.published(widgets, "2.0.0", sha512hex(nupkg(t, "Acme.Widgets", "2.0.0", "other bytes"))); res.Outcome != journal.AlreadyInEffect {
			t.Fatalf("a present version with other bytes: outcome %q", res.Outcome)
		}
		if kind, _ := e.nugetTombstone(widgets, "2.0.0"); kind != "" || e.revSeq(repoNuGet, widgets) != rev {
			t.Errorf("tombstone %q, rev_seq %d -> %d", kind, rev, e.revSeq(repoNuGet, widgets))
		}
	})
	t.Run("R-NUGET-033 published, tombstoned: nothing (already in effect)", func(t *testing.T) {
		sum := sha512hex(nupkg(t, "Acme.Widgets", "1.9.0", "gone"))
		e.st.Exec(`INSERT INTO nuget_tombstone (repository_id, lower_id, lower_version, sha512, deleted_at, deleted_by, kind)
			VALUES ($1, $2, '1.9.0', decode($3, 'hex'), now(), $4::uuid, 'removed')`, e.repoID(repoNuGet), widgets, sum, system)
		if res := e.published(widgets, "1.9.0", sum); res.Outcome != journal.AlreadyInEffect {
			t.Fatalf("outcome %q", res.Outcome)
		}
		if kind, _ := e.nugetTombstone(widgets, "1.9.0"); kind != "removed" {
			t.Errorf("kind = %q: a removal became a reservation", kind)
		}
	})
	t.Run("R-NUGET-033 removed, present with the journaled SHA-512: nothing is deleted and the version is revived", func(t *testing.T) {
		pkg := nupkg(t, "Acme.Widgets", "2.5.0", "kept")
		e.mustPushNuGet(r, pkg)
		files, rev := e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path LIKE 'acme.widgets/2.5.0/%'`, repoNuGet), e.revSeq(repoNuGet, widgets)
		res := e.deleted(widgets, "2.5.0", sha512hex(pkg))
		if res.Outcome != journal.Revived || !strings.Contains(string(res.Detail), "2.5.0") {
			t.Fatalf("outcome %q detail %s", res.Outcome, res.Detail)
		}
		if kind, _ := e.nugetTombstone(widgets, "2.5.0"); kind != "" {
			t.Errorf("a tombstone %q was written for a version that is stored", kind)
		}
		after := e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path LIKE 'acme.widgets/2.5.0/%'`, repoNuGet)
		if after != files || e.revSeq(repoNuGet, widgets) != rev {
			t.Errorf("items %d -> %d, rev_seq %d -> %d: the replay deleted or changed something", files, after, rev, e.revSeq(repoNuGet, widgets))
		}
		if a := e.nugetGet(r, repoNuGet, "v3/flatcontainer/"+widgets+"/2.5.0/"+widgets+".2.5.0.nupkg"); a.Status != 200 {
			t.Errorf("the revived version is served: %d", a.Status)
		}
	})
	t.Run("R-NUGET-033 removed, present with other bytes: nothing (subject unknown)", func(t *testing.T) {
		pkg := nupkg(t, "Acme.Widgets", "2.6.0", "stored")
		e.mustPushNuGet(r, pkg)
		res := e.deleted(widgets, "2.6.0", sha512hex(nupkg(t, "Acme.Widgets", "2.6.0", "another build")))
		if res.Outcome != journal.SubjectUnknown {
			t.Fatalf("outcome %q", res.Outcome)
		}
		if kind, _ := e.nugetTombstone(widgets, "2.6.0"); kind != "" || e.nugetVersions(widgets) == 0 {
			t.Errorf("tombstone %q", kind)
		}
	})
	t.Run("R-NUGET-033 removed, absent with a reserved tombstone: the tombstone becomes removed (applied), the deleted bytes stay refused", func(t *testing.T) {
		orig := nupkg(t, "Acme.Widgets", "3.1.0", "published then deleted")
		if res := e.published(widgets, "3.1.0", sha512hex(orig)); res.Outcome != journal.Applied {
			t.Fatalf("published: %q", res.Outcome)
		}
		res := e.deleted(widgets, "3.1.0", sha512hex(orig))
		if res.Outcome != journal.Applied {
			t.Fatalf("deleted: %q", res.Outcome)
		}
		if kind, sha := e.nugetTombstone(widgets, "3.1.0"); kind != "removed" || sha != sha512hex(orig) {
			t.Errorf("tombstone = %q %s", kind, sha)
		}
		if status, code := push(orig); status != http.StatusConflict || code != "nuget_version_deleted" {
			t.Errorf("the original 3.1.0 = %d %s, want 409 nuget_version_deleted", status, code)
		}
	})
	t.Run("R-NUGET-033 removed, absent without a tombstone: a removed tombstone with the journaled SHA-512 (applied)", func(t *testing.T) {
		orig := nupkg(t, "Acme.Widgets", "1.5.0", "deleted after the snapshot and lost with it")
		res := e.deleted(widgets, "1.5.0", sha512hex(orig))
		if res.Outcome != journal.Applied {
			t.Fatalf("outcome %q", res.Outcome)
		}
		if kind, sha := e.nugetTombstone(widgets, "1.5.0"); kind != "removed" || sha != sha512hex(orig) {
			t.Errorf("tombstone = %q %s", kind, sha)
		}
		if status, code := push(orig); status != http.StatusConflict || code != "nuget_version_deleted" {
			t.Errorf("push = %d %s, want 409 nuget_version_deleted", status, code)
		}
	})
	t.Run("R-NUGET-033 removed, absent with a removed tombstone: nothing (already in effect)", func(t *testing.T) {
		sum := sha512hex(nupkg(t, "Acme.Widgets", "1.5.0", "deleted after the snapshot and lost with it"))
		if res := e.deleted(widgets, "1.5.0", sum); res.Outcome != journal.AlreadyInEffect {
			t.Fatalf("outcome %q", res.Outcome)
		}
	})
	t.Run("R-NUGET-033 any entry about a repository that is unknown, not NuGet, remote or virtual is subject unknown", func(t *testing.T) {
		sum := sha512hex([]byte("x"))
		for _, id := range []int64{987654, e.repoID(repoNPMLocal), e.repoID(repoNuGetOrg), e.repoID(repoNPMAll)} {
			for _, ev := range []string{audit.EventPackagePublished, audit.EventPackageDeleted} {
				entry := map[string]any{"repository": id, "id": widgets, "version": "8.0.0", "sha512": sum}
				if res := e.replay(ev, entry, time.Now()); res.Outcome != journal.SubjectUnknown {
					t.Errorf("%s about repository %d: %q", ev, id, res.Outcome)
				}
			}
		}
		if n := e.st.Number(`SELECT count(*) FROM nuget_tombstone WHERE lower_version = '8.0.0'`); n != 0 {
			t.Errorf("%d tombstones written for unknown subjects", n)
		}
	})
	t.Run("R-NUGET-033 a second replay changes nothing", func(t *testing.T) {
		sum := sha512hex(nupkg(t, "Acme.Widgets", "4.0.0", "idempotent"))
		if res := e.published(widgets, "4.0.0", sum); res.Outcome != journal.Applied {
			t.Fatalf("first: %q", res.Outcome)
		}
		if res := e.published(widgets, "4.0.0", sum); res.Outcome != journal.AlreadyInEffect {
			t.Fatalf("second: %q", res.Outcome)
		}
		if res := e.deleted(widgets, "4.0.0", sum); res.Outcome != journal.Applied {
			t.Fatalf("deleted first: %q", res.Outcome)
		}
		if res := e.deleted(widgets, "4.0.0", sum); res.Outcome != journal.AlreadyInEffect {
			t.Fatalf("deleted second: %q", res.Outcome)
		}
	})
}

// The acceptance criterion of R-NUGET-033 in one run: B is taken, 3.0.0 is pushed and 2.0.0 hard-deleted afterwards, B is
// restored (2.0.0 is back, 3.0.0 is gone) and the entries are replayed.
func TestR_NUGET_033_TheListingAfterTheReplayIsTheSnapshotsAndNoVersionIsDeleted(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	v1, v2 := nupkg(t, "Acme.Gadgets", "1.0.0", "one"), nupkg(t, "Acme.Gadgets", "2.0.0", "two")
	e.mustPushNuGet(r, v1)
	e.mustPushNuGet(r, v2)
	v3 := nupkg(t, "Acme.Gadgets", "3.0.0", "three")
	entry := func(version string, b []byte) map[string]any {
		return map[string]any{"repository": e.repoID(repoNuGet), "id": "acme.gadgets", "version": version, "sha512": sha512hex(b)}
	}
	// Entries in the order of the journal: the push of 3.0.0, then the delete of 2.0.0.
	pub := e.replay(audit.EventPackagePublished, entry("3.0.0", v3), time.Now())
	del := e.replay(audit.EventPackageDeleted, entry("2.0.0", v2), time.Now())
	if pub.Outcome != journal.Applied || del.Outcome != journal.Revived {
		t.Fatalf("3.0.0 %q, 2.0.0 %q; want applied and revived", pub.Outcome, del.Outcome)
	}
	a := e.nugetGet(r, repoNuGet, "v3/flatcontainer/acme.gadgets/index.json")
	var list struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(a.Body, &list); err != nil || strings.Join(list.Versions, ",") != "1.0.0,2.0.0" {
		t.Errorf("listing = %s (%v), want the versions of the snapshot", a.Body, err)
	}
	if status := e.pushNuGet(r, nupkg(t, "Acme.Gadgets", "3.0.0", "rebuilt")); status.Status != http.StatusConflict || nugetCode(status) != "nuget_version_reserved" {
		t.Errorf("a rebuilt 3.0.0 = %d %s", status.Status, status.Body)
	}
	if status := e.pushNuGet(r, v3); status.Status != http.StatusCreated {
		t.Errorf("the original 3.0.0 = %d %s", status.Status, status.Body)
	}
}

// The entry the producer writes is the entry the handler reads (R-NUGET-033).
func TestR_NUGET_033_TheEntryOfARealPushAndDeleteIsTheEntryTheReplayReads(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	pkg := nupkg(t, "Acme.Entries", "1.0.0", "entries")
	e.mustPushNuGet(r, pkg)
	raw := e.st.Text(`SELECT entry::text FROM revocation_outbox WHERE event = 'package.published'`)
	var entry map[string]any
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatal(err)
	}
	if len(entry) != 4 || entry["id"] != "acme.entries" || entry["version"] != "1.0.0" || entry["sha512"] != sha512hex(pkg) {
		t.Fatalf("the producer's entry = %s", raw)
	}
	if res := e.replay(audit.EventPackagePublished, entry, time.Now()); res.Outcome != journal.AlreadyInEffect {
		t.Errorf("replay of the published entry = %q", res.Outcome)
	}
	// A hard delete (deleteMode delete): the repository of the default mode unlists, so remove through the management delete
	// of the item instead, which writes the tombstone and package.deleted.
	a := e.st.Do(r, "DELETE", "/files/"+project+"/"+repoNuGet+"/acme.entries/1.0.0/acme.entries.1.0.0.nupkg", e.tokAdmin, nil)
	if a.Status != 200 && a.Status != 204 {
		t.Fatalf("delete: %d %s", a.Status, a.Body)
	}
	raw = e.st.Text(`SELECT entry::text FROM revocation_outbox WHERE event = 'package.deleted'`)
	if err := json.Unmarshal([]byte(raw), &entry); err != nil || entry["sha512"] != sha512hex(pkg) {
		t.Fatalf("the deleted entry = %s (%v)", raw, err)
	}
	if res := e.replay(audit.EventPackageDeleted, entry, time.Now()); res.Outcome != journal.AlreadyInEffect {
		t.Errorf("replay of the deleted entry = %q", res.Outcome)
	}
}
