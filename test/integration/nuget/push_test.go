//go:build integration

package nuget

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_NUGET_006_APushIsStoredAuditedAndJournaled: a push of a package answers 201, stores the
// package and its generated .nuspec on the canonical paths, writes the NuGet row, raises
// packages.rev_seq, records package.published and artifact.deployed in the transaction, and
// writes the journal entry of R-NUGET-033 to the revocation outbox.
func TestR_NUGET_006_APushIsStoredAuditedAndJournaled(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	pkg := nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.3.0"})

	if ans := e.push(a, "nuget-local", e.tokDev, pkg); ans.Status != 201 || len(ans.Body) != 0 {
		t.Fatalf("%d %s", ans.Status, ans.Body)
	}
	if got := e.versions("nuget-local", "acme.widgets"); got != 1 {
		t.Fatalf("%d versions", got)
	}
	if e.rev("nuget-local", "acme.widgets") != 1 {
		t.Errorf("rev_seq = %d, want 1", e.rev("nuget-local", "acme.widgets"))
	}
	if e.listed("nuget-local", "acme.widgets", "1.2.3") != "true" {
		t.Error("the version is not listed")
	}
	if got := e.st.Text(`SELECT declared_id || '|' || original_version || '|' || origin || '|' || is_prerelease || '|' || is_semver2 FROM nuget_version WHERE lower_id = 'acme.widgets'`); got != "Acme.Widgets|1.2.3.0|push|false|false" {
		t.Errorf("row = %q", got)
	}
	if got := e.st.Text(`SELECT display_name FROM packages WHERE name = 'acme.widgets'`); got != "Acme.Widgets" {
		t.Errorf("display name = %q", got)
	}
	if got := e.files("nuget-local", "acme.widgets", "1.2.3"); got != 2 {
		t.Errorf("%d files, want the package and the .nuspec", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM items WHERE path = 'acme.widgets/1.2.3/acme.widgets.nuspec' AND generated`); got != 1 {
		t.Errorf("the .nuspec is not a generated item (%d)", got)
	}
	if got := e.st.Text(`SELECT encode(b.sha512, 'hex') FROM items i JOIN blobs b ON b.sha256 = i.blob_sha256 WHERE i.path = 'acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg'`); got != sha512hex(pkg) {
		t.Errorf("the stored package differs from the pushed one: %s", got)
	}
	if got := e.events("package.published", "nuget-local", "acme.widgets/1.2.3"); got != 1 {
		t.Errorf("%d package.published events", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'artifact.deployed' AND path LIKE 'acme.widgets/1.2.3/%'`); got != 2 {
		t.Errorf("%d artifact.deployed events (the package and the .nuspec)", got)
	}
	// the actor of the event is the developer, by token
	if got := e.st.Text(`SELECT actor_id::text FROM audit_event WHERE action = 'package.published' LIMIT 1`); got != e.devID {
		t.Errorf("actor = %q, want %q", got, e.devID)
	}
	var entry struct {
		Repository int64  `json:"repository"`
		ID         string `json:"id"`
		Version    string `json:"version"`
		SHA512     string `json:"sha512"`
	}
	raw := e.st.Text(`SELECT entry::text FROM revocation_outbox WHERE event = 'package.published'`)
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatalf("journal entry %q: %v", raw, err)
	}
	if entry.ID != "acme.widgets" || entry.Version != "1.2.3" || entry.SHA512 != sha512hex(pkg) ||
		entry.Repository != e.st.Number(`SELECT id FROM repositories WHERE key = 'nuget-local'`) {
		t.Errorf("journal entry = %+v", entry)
	}
}

// TestR_NUGET_006_APushRefusedForItsContentStoresNothing: the packages of R-NUGET-005 that break a
// rule answer 400 with their code, and nothing is left in the tables, the items or the audit.
func TestR_NUGET_006_APushRefusedForItsContentStoresNothing(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	for name, c := range map[string]struct {
		pkg  []byte
		code string
	}{
		"ID with two dots":             {nupkg(t, pkgSpec{id: "Acme..Widgets", version: "1.0.0"}), "nuget_invalid_id"},
		"version with a trailing dash": {nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0-"}), "nuget_invalid_version"},
		"symbol package":               {nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0", symbols: true}), "nuget_invalid_package"},
		"no archive":                   {[]byte("not a package"), "nuget_invalid_package"},
	} {
		t.Run(name, func(t *testing.T) {
			ans := e.push(a, "nuget-local", e.tokDev, c.pkg)
			if ans.Status != 400 || code(t, ans) != c.code {
				t.Fatalf("%d %s", ans.Status, ans.Body)
			}
			line(t, ans)
		})
	}
	if e.st.Number(`SELECT count(*) FROM nuget_version`) != 0 || e.st.Number(`SELECT count(*) FROM items WHERE kind = 'file'`) != 0 ||
		e.st.Number(`SELECT count(*) FROM audit_event WHERE action IN ('package.published', 'artifact.deployed')`) != 0 {
		t.Fatal("a refused push left rows behind")
	}
}

// TestR_NUGET_006_APushNeedsDeployOnTheDeclaredID: a reader, a person without a grant and an
// anonymous caller are refused (403, 403, 401 with the Basic challenge); a caller who holds deploy
// pushes, and the first version records what the claim rule did.
func TestR_NUGET_006_APushNeedsDeployOnTheDeclaredID(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	pkg := nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.0.0"})
	for name, c := range map[string]struct {
		token  string
		status int
	}{"reader": {e.tokReader, 403}, "nobody": {e.tokNobody, 403}, "anonymous": {"", 401}} {
		ans := e.push(a, "nuget-local", c.token, pkg)
		if ans.Status != c.status {
			t.Errorf("%s: %d %s", name, ans.Status, ans.Body)
		}
		if c.status == 401 && ans.Header.Get("WWW-Authenticate") != `Basic realm="trove"` {
			t.Errorf("%s: challenge %q", name, ans.Header.Get("WWW-Authenticate"))
		}
	}
	e.mustPush(a, "nuget-local", e.tokDev, pkg)
	// The first version of an ID gives the repository's claim rule its chance (R-REPO-023); the
	// outcome is part of the event. Without a designated owner the peer, who holds deploy,
	// pushes a further version.
	if got := e.st.Text(`SELECT COALESCE(details->>'claim', '') FROM audit_event WHERE action = 'package.published' LIMIT 1`); got == "" {
		t.Error("the published event of a first version records no claim outcome")
	}
	if got := e.push(a, "nuget-local", e.tokPeer, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.1.0"})); got.Status != 201 {
		t.Errorf("the peer who holds deploy: %d %s", got.Status, got.Body)
	}
	if e.versions("nuget-local", "acme.widgets") != 2 {
		t.Error("the versions are not stored")
	}
	if got := e.st.Text(`SELECT COALESCE(details->>'claim', 'none') FROM audit_event WHERE action = 'package.published' AND path = 'acme.widgets/1.1.0'`); got != "none" {
		t.Errorf("a later version records a claim outcome (%q)", got)
	}
}

// TestR_NUGET_006_ABareBodyIsTakenAsThePackage: a body that is not multipart/form-data is the
// raw package, for scripted uploads with curl.
func TestR_NUGET_006_ABareBodyIsTakenAsThePackage(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	pkg := nupkg(t, pkgSpec{id: "Acme.Scripted", version: "1.0.0"})
	ans := e.st.Do(a, "PUT", "/api/nuget/it/nuget-local/v3/package", e.tokDev, pkg, "Content-Type", "application/octet-stream")
	if ans.Status != 201 {
		t.Fatalf("%d %s", ans.Status, ans.Body)
	}
	if e.versions("nuget-local", "acme.scripted") != 1 {
		t.Error("the version is not stored")
	}
}

// TestR_NUGET_006_AReplayChangesNothing: the identical package of a stored version answers 201 on
// any replica and changes no row, no revision and no event; the counter says it was a replay.
// Other bytes under the number are 409 nuget_version_exists, in any spelling of the version.
func TestR_NUGET_006_AReplayChangesNothing(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	b := e.replica("web-b")
	pkg := nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.3"})
	e.mustPush(a, "nuget-local", e.tokDev, pkg)
	rev, items := e.rev("nuget-local", "acme.widgets"), e.st.Number(`SELECT count(*) FROM items`)
	events, outbox := e.st.Number(`SELECT count(*) FROM audit_event`), e.st.Number(`SELECT count(*) FROM revocation_outbox`)

	for _, r := range []*testenv.Replica{a, b} {
		if ans := e.push(r, "nuget-local", e.tokDev, pkg); ans.Status != 201 || len(ans.Body) != 0 {
			t.Fatalf("replay: %d %s", ans.Status, ans.Body)
		}
	}
	if e.rev("nuget-local", "acme.widgets") != rev || e.st.Number(`SELECT count(*) FROM items`) != items || e.versions("nuget-local", "acme.widgets") != 1 {
		t.Error("a replay changed a row")
	}
	if e.st.Number(`SELECT count(*) FROM audit_event`) < events {
		t.Fatal("audit rows vanished")
	}
	if got := e.events("package.published", "nuget-local", "acme.widgets/1.2.3"); got != 1 {
		t.Errorf("%d package.published events after two replays", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM revocation_outbox`); got != outbox {
		t.Errorf("the outbox grew by %d", got-outbox)
	}
	if sumSeries(a.Metrics(t), "trove_nuget_push_replayed_total{")+sumSeries(b.Metrics(t), "trove_nuget_push_replayed_total{") != 2 {
		t.Error("trove_nuget_push_replayed_total did not count the two replays")
	}
	rebuilt := nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.3.0", description: "rebuilt"})
	if ans := e.push(b, "nuget-local", e.tokDev, rebuilt); ans.Status != 409 || code(t, ans) != "nuget_version_exists" {
		t.Fatalf("rebuilt: %d %s", ans.Status, ans.Body)
	}
	if e.rev("nuget-local", "acme.widgets") != rev {
		t.Error("a refused push raised the revision")
	}
}

// sumSeries adds the samples of the metric lines that start with prefix.
func sumSeries(text, prefix string) float64 {
	var sum float64
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, prefix) {
			var v float64
			if i := strings.LastIndexByte(l, ' '); i > 0 {
				_, _ = fmt.Sscanf(l[i+1:], "%g", &v)
				sum += v
			}
		}
	}
	return sum
}

// TestR_NUGET_007_UnlistRelistAndDeleteOverHTTP: the protocol DELETE unlists by default and keeps
// the files served, a repeat answers 204, a relist answers 200, the revision rises with each
// change, and the metadata events record them; in a repository of deleteMode delete the same
// request removes the version for good and leaves a tombstone with the SHA-512 of the package.
func TestR_NUGET_007_UnlistRelistAndDeleteOverHTTP(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	pkg := nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.3"})
	e.mustPush(a, "nuget-local", e.tokDev, pkg)
	file := "/files/it/nuget-local/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"

	t.Run("R-NUGET-007 unlist", func(t *testing.T) {
		rev := e.rev("nuget-local", "acme.widgets")
		if ans := e.send(a, "DELETE", "nuget-local", "Acme.Widgets/1.2.3.0", e.tokDev); ans.Status != 204 || len(ans.Body) != 0 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if e.listed("nuget-local", "acme.widgets", "1.2.3") != "false" || e.rev("nuget-local", "acme.widgets") != rev+1 {
			t.Error("the version is not unlisted, or the revision did not rise once")
		}
		if ans := e.st.Do(a, "GET", file, e.tokReader, nil); ans.Status != 200 || sha512hex(ans.Body) != sha512hex(pkg) {
			t.Fatalf("an unlisted version is not downloadable: %d", ans.Status)
		}
		if e.events("package.metadata_changed", "nuget-local", "acme.widgets/1.2.3") != 1 {
			t.Error("no package.metadata_changed event")
		}
		if got := e.st.Text(`SELECT details->>'change' FROM audit_event WHERE action = 'package.metadata_changed'`); got != "unlist" {
			t.Errorf("change = %q", got)
		}
		for range 2 {
			if ans := e.send(a, "DELETE", "nuget-local", "acme.widgets/1.2.3", e.tokDev); ans.Status != 204 {
				t.Fatalf("repeat: %d %s", ans.Status, ans.Body)
			}
		}
		if e.rev("nuget-local", "acme.widgets") != rev+1 || e.events("package.metadata_changed", "nuget-local", "acme.widgets/1.2.3") != 1 {
			t.Error("a repeated unlist changed something")
		}
		if kind, _ := e.tombstone("nuget-local", "acme.widgets", "1.2.3"); kind != "" {
			t.Error("unlisting wrote a tombstone")
		}
	})
	t.Run("R-NUGET-007 relist", func(t *testing.T) {
		if ans := e.send(a, "POST", "nuget-local", "acme.widgets/1.2.3", e.tokDev); ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if e.listed("nuget-local", "acme.widgets", "1.2.3") != "true" {
			t.Error("the version is not listed again")
		}
		if ans := e.send(a, "POST", "nuget-local", "acme.widgets/1.2.3", e.tokDev); ans.Status != 200 {
			t.Fatalf("repeat: %d", ans.Status)
		}
		if got := e.st.Text(`SELECT string_agg(details->>'change', ',' ORDER BY occurred_at) FROM audit_event WHERE action = 'package.metadata_changed'`); got != "unlist,relist" {
			t.Errorf("changes = %q", got)
		}
	})
	t.Run("R-NUGET-007 the peer who may deploy relists, the reader may not", func(t *testing.T) {
		if ans := e.send(a, "POST", "nuget-local", "acme.widgets/1.2.3", e.tokReader); ans.Status != 403 {
			t.Fatalf("reader: %d %s", ans.Status, ans.Body)
		}
		if ans := e.send(a, "DELETE", "nuget-local", "acme.widgets/1.2.3", e.tokPeer); ans.Status != 403 {
			t.Fatalf("peer without delete: %d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NUGET-007 a version that does not exist is 404", func(t *testing.T) {
		for _, c := range []struct{ method, path string }{
			{"DELETE", "acme.widgets/9.9.9"}, {"DELETE", "acme.nothing/1.0.0"}, {"POST", "acme.widgets/9.9.9"}, {"POST", "acme.nothing/1.0.0"},
		} {
			if ans := e.send(a, c.method, "nuget-local", c.path, e.tokDev); ans.Status != 404 {
				t.Errorf("%s %s: %d %s", c.method, c.path, ans.Status, ans.Body)
			}
		}
	})
	t.Run("R-NUGET-007 a delete in delete mode removes the version for good", func(t *testing.T) {
		hard := nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.4"})
		e.mustPush(a, "nuget-hard", e.tokDev, hard)
		e.mustPush(a, "nuget-hard", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.5"}))
		rev := e.rev("nuget-hard", "acme.widgets")
		if ans := e.send(a, "DELETE", "nuget-hard", "acme.widgets/1.2.4", e.tokDev); ans.Status != 204 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		if e.versions("nuget-hard", "acme.widgets") != 1 || e.files("nuget-hard", "acme.widgets", "1.2.4") != 0 || e.files("nuget-hard", "acme.widgets", "1.2.5") != 2 {
			t.Error("the delete did not remove exactly the version")
		}
		if kind, sum := e.tombstone("nuget-hard", "acme.widgets", "1.2.4"); kind != "removed" || sum != sha512hex(hard) {
			t.Errorf("tombstone %q %q, want a removal holding the SHA-512 of the package", kind, sum)
		}
		if e.rev("nuget-hard", "acme.widgets") <= rev {
			t.Error("the revision did not rise")
		}
		if e.listed("nuget-hard", "acme.widgets", "1.2.4") != "none" {
			t.Error("the NuGet row stays")
		}
		if ans := e.st.Do(a, "GET", "/files/it/nuget-hard/acme.widgets/1.2.4/acme.widgets.1.2.4.nupkg", e.tokReader, nil); ans.Status != 404 {
			t.Errorf("a deleted file is served: %d", ans.Status)
		}
		if e.events("package.deleted", "nuget-hard", "acme.widgets/1.2.4") != 1 || e.events("artifact.deleted", "nuget-hard", "acme.widgets/1.2.4/acme.widgets.1.2.4.nupkg") != 1 {
			t.Error("package.deleted or artifact.deleted is missing")
		}
		var entry struct{ ID, Version, SHA512 string }
		raw := e.st.Text(`SELECT entry::text FROM revocation_outbox WHERE event = 'package.deleted'`)
		if err := json.Unmarshal([]byte(raw), &entry); err != nil || entry.ID != "acme.widgets" || entry.Version != "1.2.4" || entry.SHA512 != sha512hex(hard) {
			t.Errorf("journal entry %q: %v", raw, err)
		}
		// the number is taken for good; the delete is repeatable; a relist is 404
		if ans := e.push(a, "nuget-hard", e.tokDev, hard); ans.Status != 409 || code(t, ans) != "nuget_version_deleted" {
			t.Errorf("push of the identical bytes: %d %s", ans.Status, ans.Body)
		}
		if ans := e.push(a, "nuget-hard", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.4", description: "again"})); ans.Status != 409 || code(t, ans) != "nuget_version_deleted" {
			t.Errorf("push of other bytes: %d %s", ans.Status, ans.Body)
		}
		rev = e.rev("nuget-hard", "acme.widgets")
		if ans := e.send(a, "DELETE", "nuget-hard", "acme.widgets/1.2.4", e.tokDev); ans.Status != 204 {
			t.Errorf("repeated delete: %d %s", ans.Status, ans.Body)
		}
		if ans := e.send(a, "POST", "nuget-hard", "acme.widgets/1.2.4", e.tokDev); ans.Status != 404 {
			t.Errorf("relist of a deleted version: %d", ans.Status)
		}
		if e.rev("nuget-hard", "acme.widgets") != rev || e.events("package.deleted", "nuget-hard", "acme.widgets/1.2.4") != 1 {
			t.Error("a repeated delete changed something")
		}
		e.mustPush(a, "nuget-hard", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.6"}))
	})
	t.Run("R-NUGET-007 a caller without delete learns nothing from the 204 of a deleted version", func(t *testing.T) {
		if ans := e.send(a, "DELETE", "nuget-hard", "acme.widgets/1.2.4", e.tokPeer); ans.Status != 403 {
			t.Errorf("%d %s", ans.Status, ans.Body)
		}
		if ans := e.send(a, "DELETE", "nuget-hard", "acme.widgets/1.2.4", e.tokReader); ans.Status != 403 {
			t.Errorf("%d %s", ans.Status, ans.Body)
		}
	})
}

// TestR_NUGET_005_ARawDeleteOfThePackageIsAHardDelete: removing the .nupkg through /files/ removes
// the version as a unit with its tombstone, whatever deleteMode says, and removing the generated
// .nuspec alone is refused with 409 and changes nothing (Q-NUGET-13).
func TestR_NUGET_005_ARawDeleteOfThePackageIsAHardDelete(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	pkg := nupkg(t, pkgSpec{id: "Acme.Widgets", version: "1.2.3"})
	e.mustPush(a, "nuget-local", e.tokDev, pkg)
	nuspec := "/files/it/nuget-local/acme.widgets/1.2.3/acme.widgets.nuspec"
	file := "/files/it/nuget-local/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"

	if ans := e.st.Do(a, "DELETE", nuspec, e.tokAdmin, nil); ans.Status != 409 {
		t.Fatalf("delete of the generated .nuspec: %d %s", ans.Status, ans.Body)
	}
	if e.files("nuget-local", "acme.widgets", "1.2.3") != 2 || e.versions("nuget-local", "acme.widgets") != 1 {
		t.Fatal("the refused delete changed something")
	}
	if ans := e.st.Do(a, "DELETE", file, e.tokAdmin, nil); ans.Status/100 != 2 {
		t.Fatalf("delete of the package: %d %s", ans.Status, ans.Body)
	}
	if e.versions("nuget-local", "acme.widgets") != 0 {
		t.Error("the version stays")
	}
	if kind, sum := e.tombstone("nuget-local", "acme.widgets", "1.2.3"); kind != "removed" || sum != sha512hex(pkg) {
		t.Errorf("tombstone %q %q", kind, sum)
	}
	if ans := e.push(a, "nuget-local", e.tokDev, pkg); ans.Status != 409 || code(t, ans) != "nuget_version_deleted" {
		t.Errorf("push after the raw delete: %d %s", ans.Status, ans.Body)
	}
}

// TestR_NUGET_006_ReservedTombstoneAdmitsOnlyItsBytes: a tombstone of kind reserved, as a restore's
// replay writes it, refuses other bytes with nuget_version_reserved, admits the package whose
// SHA-512 it holds and removes itself in that push's transaction; in a delete-mode repository the
// protocol delete turns an unredeemed reservation into a removal.
func TestR_NUGET_006_ReservedTombstoneAdmitsOnlyItsBytes(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	orig := nupkg(t, pkgSpec{id: "Acme.Widgets", version: "3.0.0"})
	reserve := func(repo, version string, pkg []byte) {
		e.st.Exec(`INSERT INTO nuget_tombstone (repository_id, lower_id, lower_version, sha512, deleted_at, deleted_by, kind)
			SELECT id, 'acme.widgets', $2, decode($3, 'hex'), now(), '00000000-0000-0000-0000-000000000000', 'reserved' FROM repositories WHERE key = $1`,
			repo, version, sha512hex(pkg))
	}
	reserve("nuget-local", "3.0.0", orig)
	if ans := e.push(a, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Widgets", version: "3.0.0", description: "rebuilt"})); ans.Status != 409 || code(t, ans) != "nuget_version_reserved" {
		t.Fatalf("rebuilt: %d %s", ans.Status, ans.Body)
	}
	e.mustPush(a, "nuget-local", e.tokDev, orig)
	if kind, _ := e.tombstone("nuget-local", "acme.widgets", "3.0.0"); kind != "" {
		t.Error("the reservation stays after the package it was kept for was pushed")
	}
	if e.versions("nuget-local", "acme.widgets") != 1 {
		t.Error("the version is not stored")
	}

	third := nupkg(t, pkgSpec{id: "Acme.Widgets", version: "4.0.0"})
	reserve("nuget-hard", "4.0.0", third)
	if ans := e.send(a, "DELETE", "nuget-hard", "acme.widgets/4.0.0", e.tokDev); ans.Status != 204 {
		t.Fatalf("delete of a reserved number: %d %s", ans.Status, ans.Body)
	}
	if kind, _ := e.tombstone("nuget-hard", "acme.widgets", "4.0.0"); kind != "removed" {
		t.Errorf("tombstone kind %q, want removed", kind)
	}
	if ans := e.push(a, "nuget-hard", e.tokDev, third); ans.Status != 409 || code(t, ans) != "nuget_version_deleted" {
		t.Errorf("the journaled bytes after the delete: %d %s", ans.Status, ans.Body)
	}
	if e.events("package.deleted", "nuget-hard", "acme.widgets/4.0.0") != 1 {
		t.Error("the promotion recorded no package.deleted event")
	}
}

// TestR_NUGET_006_ConcurrentPushesOfOneVersion: ten parallel pushes of one file, spread over two
// replicas, all answer 201 and leave one version and one published event; ten parallel pushes of
// ten different builds of one version yield one 201 and nine 409.
func TestR_NUGET_006_ConcurrentPushesOfOneVersion(t *testing.T) {
	e := newWriteEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	run := func(repo, id string, build func(i int) []byte) map[int]int {
		var mu sync.Mutex
		statuses := map[int]int{}
		var wg sync.WaitGroup
		for i := range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := a
				if i%2 == 1 {
					r = b
				}
				ans := e.push(r, repo, e.tokDev, build(i))
				mu.Lock()
				statuses[ans.Status]++
				mu.Unlock()
				if ans.Status == 409 && code(t, ans) != "nuget_version_exists" {
					t.Errorf("409 with code %q", code(t, ans))
				}
			}()
		}
		wg.Wait()
		return statuses
	}
	same := nupkg(t, pkgSpec{id: "Acme.Same", version: "2.0.0"})
	if got := run("nuget-local", "acme.same", func(int) []byte { return same }); got[201] != 10 {
		t.Fatalf("identical pushes: %v, want ten 201", got)
	}
	if e.versions("nuget-local", "acme.same") != 1 || e.events("package.published", "nuget-local", "acme.same/2.0.0") != 1 {
		t.Errorf("%d versions, %d events", e.versions("nuget-local", "acme.same"), e.events("package.published", "nuget-local", "acme.same/2.0.0"))
	}
	got := run("nuget-local", "acme.diff", func(i int) []byte {
		return nupkg(t, pkgSpec{id: "Acme.Diff", version: "2.0.0", description: fmt.Sprintf("build %d", i)})
	})
	if got[201] != 1 || got[409] != 9 {
		t.Fatalf("different builds: %v, want one 201 and nine 409", got)
	}
	if e.versions("nuget-local", "acme.diff") != 1 || e.events("package.published", "nuget-local", "acme.diff/2.0.0") != 1 {
		t.Error("more than one version came out of the race")
	}
}

// TestR_NUGET_006_APushRacingAHardDeleteSeesTheVersionOrItsTombstone: pushes and deletes of one
// number in parallel end in a state with exactly one of the version and its tombstone, never
// neither and never both (R-NUGET-006: the package row is locked first, the number is checked
// after it).
func TestR_NUGET_006_APushRacingAHardDeleteSeesTheVersionOrItsTombstone(t *testing.T) {
	e := newWriteEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	pkg := nupkg(t, pkgSpec{id: "Acme.Race", version: "1.0.0"})
	e.mustPush(a, "nuget-hard", e.tokDev, pkg)
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(2)
		r := a
		if i%2 == 1 {
			r = b
		}
		go func() { defer wg.Done(); _ = e.push(r, "nuget-hard", e.tokDev, pkg) }()
		go func() { defer wg.Done(); _ = e.send(r, "DELETE", "nuget-hard", "acme.race/1.0.0", e.tokDev) }()
	}
	wg.Wait()
	versions := e.versions("nuget-hard", "acme.race")
	kind, _ := e.tombstone("nuget-hard", "acme.race", "1.0.0")
	if (versions == 1) == (kind != "") {
		t.Fatalf("%d versions and tombstone %q: want exactly one of them", versions, kind)
	}
	if e.st.Number(`SELECT count(*) FROM nuget_version`) != versions {
		t.Error("the NuGet rows disagree with the version rows")
	}
}
