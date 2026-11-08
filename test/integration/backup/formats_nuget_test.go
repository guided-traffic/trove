//go:build integration

package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/journal/replay"
	"github.com/guided-traffic/trove/test/integration/testenv"
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

// Two additions of one version that name different bytes leave the number refused, whichever comes first (R-NUGET-033).
func TestR_NUGET_033_TwoAdditionsThatNameDifferentBytesLeaveTheNumberRefusedInEitherOrder(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	push := func(pkg []byte) (int, string) {
		a := e.pushNuGet(r, pkg)
		return a.Status, nugetCode(a)
	}
	pair := func(v string) (genuine, forged []byte) {
		return nupkg(t, "Acme.Widgets", v, "the genuine build"), nupkg(t, "Acme.Widgets", v, "the forged build")
	}
	bothDigests := func(t *testing.T, note string, a, b []byte) {
		t.Helper()
		if !strings.Contains(note, sha512hex(a)) || !strings.Contains(note, sha512hex(b)) {
			t.Errorf("note %q does not name both SHA-512s", note)
		}
	}
	refused := func(t *testing.T, v string, pkgs ...[]byte) {
		t.Helper()
		for _, p := range pkgs {
			if status, code := push(p); status != http.StatusConflict || code != "nuget_version_deleted" {
				t.Errorf("push of %s = %d %s, want 409 nuget_version_deleted", v, status, code)
			}
		}
		if n := e.st.Number(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id WHERE p.name = $1 AND pv.version = $2`, widgets, v); n != 0 {
			t.Errorf("%d rows for %s", n, v)
		}
	}
	order := func(t *testing.T, v string, first, second []byte) {
		t.Helper()
		if res := e.published(widgets, v, sha512hex(first)); res.Outcome != journal.Applied || res.Note != "" {
			t.Fatalf("first: %q note %q", res.Outcome, res.Note)
		}
		if kind, sha := e.nugetTombstone(widgets, v); kind != "reserved" || sha != sha512hex(first) {
			t.Fatalf("tombstone = %q %s", kind, sha)
		}
		res := e.published(widgets, v, sha512hex(second))
		if res.Outcome != journal.Applied {
			t.Fatalf("second: %q", res.Outcome)
		}
		bothDigests(t, res.Note, first, second)
		if kind, sha := e.nugetTombstone(widgets, v); kind != "removed" || sha != sha512hex(first) {
			t.Errorf("tombstone = %q %s, want removed with the first digest", kind, sha)
		}
		if got := e.st.Text(`SELECT deleted_by::text FROM nuget_tombstone WHERE lower_version = $1`, v); got != system {
			t.Errorf("deleted_by = %s", got)
		}
		refused(t, v, first, second)
		for _, p := range [][]byte{first, second} {
			if res := e.published(widgets, v, sha512hex(p)); res.Outcome != journal.AlreadyInEffect || res.Note != "" {
				t.Errorf("second pass: %q note %q", res.Outcome, res.Note)
			}
		}
	}
	t.Run("the forged entry first", func(t *testing.T) {
		g, f := pair("5.0.0")
		order(t, "5.0.0", f, g)
	})
	t.Run("the genuine entry first", func(t *testing.T) {
		g, f := pair("5.1.0")
		order(t, "5.1.0", g, f)
	})
	t.Run("the same bytes twice leave the number reserved for them", func(t *testing.T) {
		g, f := pair("5.2.0")
		if res := e.published(widgets, "5.2.0", sha512hex(g)); res.Outcome != journal.Applied {
			t.Fatalf("first: %q", res.Outcome)
		}
		if res := e.published(widgets, "5.2.0", sha512hex(g)); res.Outcome != journal.AlreadyInEffect || res.Note != "" {
			t.Fatalf("second: %q note %q", res.Outcome, res.Note)
		}
		if status, code := push(f); status != http.StatusConflict || code != "nuget_version_reserved" {
			t.Errorf("other bytes = %d %s, want 409 nuget_version_reserved", status, code)
		}
		if status, code := push(g); status != http.StatusCreated {
			t.Errorf("same bytes = %d %s, want 201", status, code)
		}
	})
	t.Run("a reservation in the restored database", func(t *testing.T) {
		a, b := pair("5.3.0")
		e.st.Exec(`INSERT INTO nuget_tombstone (repository_id, lower_id, lower_version, sha512, deleted_at, deleted_by, kind)
			VALUES ($1, $2, '5.3.0', decode($3, 'hex'), now(), $4::uuid, 'reserved')`, e.repoID(repoNuGet), widgets, sha512hex(a), system)
		res := e.published(widgets, "5.3.0", sha512hex(b))
		if res.Outcome != journal.Applied {
			t.Fatalf("other bytes: %q", res.Outcome)
		}
		bothDigests(t, res.Note, a, b)
		if kind, _ := e.nugetTombstone(widgets, "5.3.0"); kind != "removed" {
			t.Errorf("kind = %q", kind)
		}
		if res := e.published(widgets, "5.3.0", sha512hex(a)); res.Outcome != journal.AlreadyInEffect {
			t.Errorf("the reserved bytes: %q", res.Outcome)
		}
		refused(t, "5.3.0", a)
	})
	t.Run("a removal that names other bytes than the reservation warns", func(t *testing.T) {
		a, b := pair("5.5.0")
		if res := e.published(widgets, "5.5.0", sha512hex(a)); res.Outcome != journal.Applied || res.Note != "" {
			t.Fatalf("published: %q note %q", res.Outcome, res.Note)
		}
		res := e.deleted(widgets, "5.5.0", sha512hex(b))
		if res.Outcome != journal.Applied {
			t.Fatalf("deleted: %q", res.Outcome)
		}
		bothDigests(t, res.Note, a, b)
		if kind, sha := e.nugetTombstone(widgets, "5.5.0"); kind != "removed" || sha != sha512hex(a) {
			t.Errorf("tombstone = %q %s", kind, sha)
		}
		refused(t, "5.5.0", a, b)
		if res := e.deleted(widgets, "5.5.0", sha512hex(b)); res.Outcome != journal.AlreadyInEffect || res.Note != "" {
			t.Errorf("second pass: %q note %q", res.Outcome, res.Note)
		}
	})
	t.Run("a removal in the restored database stays", func(t *testing.T) {
		a, b := pair("5.4.0")
		e.st.Exec(`INSERT INTO nuget_tombstone (repository_id, lower_id, lower_version, sha512, deleted_at, deleted_by, kind)
			VALUES ($1, $2, '5.4.0', decode($3, 'hex'), now(), $4::uuid, 'removed')`, e.repoID(repoNuGet), widgets, sha512hex(a), system)
		if res := e.published(widgets, "5.4.0", sha512hex(b)); res.Outcome != journal.AlreadyInEffect || res.Note != "" {
			t.Fatalf("%q note %q", res.Outcome, res.Note)
		}
		if kind, sha := e.nugetTombstone(widgets, "5.4.0"); kind != "removed" || sha != sha512hex(a) {
			t.Errorf("tombstone = %q %s", kind, sha)
		}
	})
}

// The order of the replay comes from the entries' own times, which the writer of an entry chooses; the end state must not.
func TestR_NUGET_033_AForgedAdditionWithAnEarlierTimeCannotTakeTheNumberOfAGenuineOne(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		forgedAt      time.Duration
	}{{"forged earlier", "6.0.0", 10 * time.Minute}, {"forged later", "6.1.0", 30 * time.Minute}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newFormatsEnv(t)
			r := e.replica("web-1")
			genuine, forged := nupkg(t, "Acme.Widgets", tc.version, "the genuine build"), nupkg(t, "Acme.Widgets", tc.version, "the forged build")
			store := newMemStore()
			restoredTo := time.Now().Add(-time.Hour)
			write := func(pkg []byte, at time.Time) string {
				raw, err := json.Marshal(e.nugetEntry(widgets, tc.version, sha512hex(pkg)))
				if err != nil {
					t.Fatal(err)
				}
				return handWrittenAt(t, store, audit.EventPackagePublished, raw, at)
			}
			gid := write(genuine, restoredTo.Add(20*time.Minute))
			fid := write(forged, restoredTo.Add(tc.forgedAt))
			later := fid
			if tc.forgedAt < 20*time.Minute {
				later = gid
			}
			run := func() (*journal.Summary, string) {
				var out bytes.Buffer
				sum, err := replay.New(replay.Options{DB: e.db, Reader: replay.NewReader(store, twinPrefix), Log: testenv.Quiet()}).
					Replay(context.Background(), journal.ReplayOptions{RestoredTo: restoredTo, Margin: 5 * time.Minute, Out: &out})
				if err != nil {
					t.Fatal(err)
				}
				return sum, out.String()
			}
			sum, out := run()
			if sum.Counts[journal.Applied] != 2 {
				t.Fatalf("counts = %v", sum.Counts)
			}
			for _, res := range sum.Results {
				if (res.ID == later) != (res.Note != "") {
					t.Errorf("entry %s: note %q", res.ID, res.Note)
				}
			}
			if len(sum.Warnings) != 1 || !strings.Contains(sum.Warnings[0], later) ||
				!strings.Contains(sum.Warnings[0], sha512hex(genuine)) || !strings.Contains(sum.Warnings[0], sha512hex(forged)) {
				t.Errorf("warnings = %q", sum.Warnings)
			}
			if !strings.Contains(out, "  warning: ") || !strings.Contains(out, sha512hex(genuine)) || !strings.Contains(out, sha512hex(forged)) {
				t.Errorf("output lacks the warning:\n%s", out)
			}
			if kind, _ := e.nugetTombstone(widgets, tc.version); kind != "removed" {
				t.Errorf("kind = %q", kind)
			}
			for _, p := range [][]byte{genuine, forged} {
				if a := e.pushNuGet(r, p); a.Status != http.StatusConflict || nugetCode(a) != "nuget_version_deleted" {
					t.Errorf("push = %d %s", a.Status, a.Body)
				}
			}
			sum, _ = run()
			if sum.Counts[journal.AlreadyInEffect] != 2 || len(sum.Warnings) != 0 {
				t.Errorf("second replay: counts %v warnings %q", sum.Counts, sum.Warnings)
			}
		})
	}
}
