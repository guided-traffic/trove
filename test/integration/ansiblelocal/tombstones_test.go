//go:build integration

package ansiblelocal

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const mgmt = "/api/v1/projects/" + project + "/repositories/" + repo + "/ansible/"

func archivePath(version string) string {
	return "collections/acme/tools/acme-tools-" + version + ".tar.gz"
}

// deleteVersion deletes the archive of a version through /files/ with alice's token.
func (e *env) deleteVersion(version string) {
	e.t.Helper()
	a := e.st.Do(e.r, "DELETE", "/files/"+project+"/"+repo+"/"+archivePath(version), e.tokA, nil)
	if a.Status/100 != 2 {
		e.t.Fatalf("delete %s: %d %s", version, a.Status, a.Body)
	}
}

// outbox returns the entry of the first journal row of an event.
func (e *env) outbox(event string) json.RawMessage {
	e.t.Helper()
	var entry string
	waitFor(e.t, "the journal entry of "+event, 5*time.Second, func() bool {
		entry = e.st.Text(`SELECT COALESCE((SELECT entry::text FROM revocation_outbox WHERE event = $1 ORDER BY at LIMIT 1), '')`, event)
		return entry != ""
	})
	return json.RawMessage(entry)
}

// replay applies a journal entry as the replay of a restore does.
func (e *env) replay(db *postgres.DB, event string, entry json.RawMessage) journal.Result {
	e.t.Helper()
	h, ok := journal.HandlerFor(event)
	if !ok {
		e.t.Fatalf("no replay handler for %s", event)
	}
	var res journal.Result
	err := db.InTx(context.Background(), func(tx meta.Tx) (err error) {
		res, err = h.Apply(context.Background(), tx, journal.Env{Now: time.Now()}, journal.Object{V: journal.Version, ID: "01a1c6c0-0000-7000-8000-000000000001", Event: event, Entry: entry})
		return err
	})
	if err != nil {
		e.t.Fatalf("replay %s: %v", event, err)
	}
	return res
}

func (e *env) openDB() *postgres.DB {
	e.t.Helper()
	loaded, err := config.Load(config.Options{Environ: e.st.Environ(), Command: config.CommandAdmin})
	if err != nil {
		e.t.Fatal(err)
	}
	db, err := postgres.Open(context.Background(), loaded.Config.Database, "ansiblelocal", meta.RoleWeb, testenv.Quiet())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(db.Close)
	return db
}

// TestR_ANS_012_ADeletedVersionLeavesATombstoneUntilItIsCleared: the deletion removes the version from the documents, a publish of its
// number is 409 version_tombstoned whatever the bytes, the management API lists and clears the tombstone, and the clearing is audited.
func TestR_ANS_012_ADeletedVersionLeavesATombstoneUntilItIsCleared(t *testing.T) {
	e := newEnv(t)
	e.mustPublish("1.0.0")
	e.mustPublish("1.1.0")
	e.deleteVersion("1.0.0")

	list := e.get(e.tokC, "v3/collections/acme/tools/versions/")
	var doc struct{ Data []struct{ Version string } }
	if err := json.Unmarshal(list.Body, &doc); err != nil || len(doc.Data) != 1 || doc.Data[0].Version != "1.1.0" {
		t.Fatalf("the list after the deletion: %s", list.Body)
	}
	if a := e.get(e.tokC, "v3/collections/acme/tools/versions/1.0.0/"); a.Status != 404 {
		t.Errorf("the deleted version's document: %d", a.Status)
	}
	if a := e.get(e.tokC, "v3/artifacts/collections/acme-tools-1.0.0.tar.gz"); a.Status != 404 {
		t.Errorf("the deleted version's archive: %d", a.Status)
	}
	for _, v := range []string{"1.0.0", "1.0.0+rebuilt"} {
		s := spec(v)
		s.Files["README.md"] = "other bytes " + v
		if a := e.publish(e.tokA, specBytes(s), true); a.Status != 409 || code(t, a) != "version_tombstoned" {
			t.Errorf("publish %s: %d %s", v, a.Status, a.Body)
		}
	}
	if got := e.eventCount("ansible.version.deleted"); got != 1 {
		t.Errorf("ansible.version.deleted events = %d", got)
	}

	tombs := e.st.Do(e.r, "GET", mgmt+"tombstones", e.tokAdmin, nil)
	items, _ := tombs.JSON(t)["items"].([]any)
	if tombs.Status != 200 || len(items) != 1 || items[0].(map[string]any)["name"] != "acme.tools" || items[0].(map[string]any)["version"] != "1.0.0" {
		t.Errorf("tombstones: %d %s", tombs.Status, tombs.Body)
	}
	if a := e.st.Do(e.r, "DELETE", mgmt+"tombstones/acme.tools/1.0.0+rebuilt", e.tokC, nil); a.Status != 403 {
		t.Errorf("a reader clears a tombstone: %d", a.Status)
	}
	if a := e.st.Do(e.r, "DELETE", mgmt+"tombstones/acme.tools/1.0.0+rebuilt", e.tokAdmin, nil); a.Status != 204 {
		t.Errorf("clear (by the number): %d %s", a.Status, a.Body)
	}
	if a := e.st.Do(e.r, "DELETE", mgmt+"tombstones/acme.tools/1.0.0", e.tokAdmin, nil); a.Status != 404 {
		t.Errorf("clear twice: %d", a.Status)
	}
	if got := e.eventCount("ansible.tombstone.cleared"); got != 1 {
		t.Errorf("ansible.tombstone.cleared events = %d", got)
	}
	other := spec("1.0.0")
	other.Files["README.md"] = "other bytes after the clearing"
	if a := e.publish(e.tokA, specBytes(other), true); a.Status != 202 || e.task(e.tokA, a)["state"] != "completed" {
		t.Errorf("publish after the clearing: %d %s", a.Status, a.Body)
	}
}

// TestR_ANS_029_AReplayWithholdsADeletedVersionAgain: a restore from a backup that predates a deletion brings the version back; the
// replay of the journaled deletion withholds its number again, drops the row, makes the archive unavailable and deletes nothing, and the
// replay of the clearing frees the number (R-ANS-029, R-HA-054).
func TestR_ANS_029_AReplayWithholdsADeletedVersionAgain(t *testing.T) {
	e := newEnv(t)
	db := e.openDB()
	archive := specBytes(spec("1.0.0"))
	e.mustPublish("1.0.0")
	e.deleteVersion("1.0.0")
	deleted := e.outbox("ansible.version.deleted")
	if a := e.st.Do(e.r, "DELETE", mgmt+"tombstones/acme.tools/1.0.0", e.tokAdmin, nil); a.Status != 204 {
		t.Fatalf("clear: %d %s", a.Status, a.Body)
	}
	cleared := e.outbox("ansible.tombstone.cleared")
	// The state of the older backup: the version is published and its number is free.
	if a := e.publish(e.tokA, archive, true); a.Status != 202 || e.task(e.tokA, a)["state"] != "completed" {
		t.Fatalf("publish again: %d %s", a.Status, a.Body)
	}
	if a := e.get(e.tokC, "v3/artifacts/collections/acme-tools-1.0.0.tar.gz"); a.Status != 200 {
		t.Fatalf("download before the replay: %d", a.Status)
	}

	if res := e.replay(db, "ansible.version.deleted", deleted); res.Outcome != journal.Applied {
		t.Errorf("replay of the deletion = %+v", res)
	}
	if e.versionRows() != 0 {
		t.Errorf("the version row stays: %d", e.versionRows())
	}
	if a := e.get(e.tokC, "v3/artifacts/collections/acme-tools-1.0.0.tar.gz"); a.Status != 404 {
		t.Errorf("a withheld archive is served: %d", a.Status)
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND path = $2`, e.repoID(), archivePath("1.0.0")); n != 1 {
		t.Errorf("the replay deleted the archive's item: %d", n)
	}
	if a := e.publish(e.tokA, archive, true); a.Status != 409 || code(t, a) != "version_tombstoned" {
		t.Errorf("publish of a withheld number: %d %s", a.Status, a.Body)
	}
	if res := e.replay(db, "ansible.version.deleted", deleted); res.Outcome != journal.AlreadyInEffect {
		t.Errorf("second replay = %+v", res)
	}
	forged := json.RawMessage(`{"repository": 999999, "name": "acme.tools", "version": "1.0.0", "sha256": "` + sum(archive) + `"}`)
	if res := e.replay(db, "ansible.version.deleted", forged); res.Outcome != journal.SubjectUnknown {
		t.Errorf("an entry about another repository = %+v", res)
	}
	if res := e.replay(db, "ansible.version.deleted", json.RawMessage(`{"name": "x"}`)); res.Outcome != journal.Skipped {
		t.Errorf("an incomplete entry = %+v", res)
	}
	if res := e.replay(db, "ansible.tombstone.cleared", cleared); res.Outcome != journal.Applied {
		t.Errorf("replay of the clearing = %+v", res)
	}
	// The number is free again, and the reindex gives the archive that stayed its row back.
	if a := e.st.Do(e.r, "GET", mgmt+"tombstones", e.tokAdmin, nil); len(a.JSON(t)["items"].([]any)) != 0 {
		t.Errorf("tombstones after the clearing: %s", a.Body)
	}
}
