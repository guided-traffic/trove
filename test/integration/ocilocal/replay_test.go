//go:build integration

package ocilocal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	ocidata "github.com/guided-traffic/trove/internal/formatdata/oci"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const actor = "5b2a0c0e-0000-4000-8000-000000000001"

// replay applies one entry to the restored database the way the replayer does: in a transaction of its own, through the handler the
// domain registered.
func (e *env) replay(entry any) journal.Result {
	e.t.Helper()
	h, ok := journal.HandlerFor(audit.EventOCIManifestDeleted)
	if !ok {
		e.t.Fatal("no replay handler for oci.manifest.deleted")
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		e.t.Fatal(err)
	}
	h32 := testenv.RandHex(16)
	id := h32[:8] + "-" + h32[8:12] + "-4" + h32[13:16] + "-8" + h32[17:20] + "-" + h32[20:]
	at := time.Now().Add(-time.Minute)
	o := journal.Object{V: journal.Version, ID: id, At: at.UTC(), Event: audit.EventOCIManifestDeleted, Entry: raw}
	if _, err := o.Encode(); err != nil {
		e.t.Fatalf("object: %v", err)
	}
	var res journal.Result
	err = e.db.InTx(context.Background(), func(tx meta.Tx) error {
		var err error
		res, err = h.Apply(context.Background(), tx, journal.Env{Now: time.Now(), RestoredTo: at.Add(-time.Hour)}, o)
		return err
	})
	if err != nil {
		e.t.Fatalf("replay: %v", err)
	}
	return res
}

func (e *env) deletedEntry(repo string, im image) map[string]any {
	return map[string]any{"repository": e.repoID(repo), "name": im.name, "digest": dg(im.digest), "actorId": actor}
}

func (e *env) tombstones(repo string) int64 {
	return e.st.Number(`SELECT count(*) FROM oci_tombstones WHERE repo_id = $1`, e.repoID(repo))
}

func (e *env) files(path string) int {
	return e.st.Do(e.r, "GET", "/files/"+project+"/"+repoLocal+"/"+path, e.tokAdmin, nil).Status
}

// TestR_OCI_033_ReplayWithholdsADeletedManifestAndDeletesNothing: after a restore, an image that was deleted after the snapshot is stored
// again; the replay writes its mark and raises the revision, the items and their bytes stay, no route serves the manifest or a tag that
// points at it, and the tags list omits the tag (R-OCI-033, R-HA-054).
func TestR_OCI_033_ReplayWithholdsADeletedManifestAndDeletesNothing(t *testing.T) {
	e := newEnv(t)
	im := e.manifestOf(repoLocal, "app", `{"image":"restored"}`, manifest, nil, "")
	e.tag(repoLocal, "app", "1", `{"image":"restored"}`)
	e.manifestOf(repoLocal, "app", `{"image":"kept"}`, manifest, nil, "")
	e.tag(repoLocal, "app", "2", `{"image":"kept"}`)
	e.put(repoLocal, "app/_blobs/"+dg(sha256Of("layer")), "layer", "application/octet-stream")
	repo := e.repoID(repoLocal)
	if e.files(im.path()) != 200 || e.files("app/_tags/1") != 200 {
		t.Fatal("the restored image is not served before the replay")
	}
	rev := e.revSeq(repoLocal, "app")
	usage := e.st.Number(`SELECT COALESCE(sum(logical_bytes), 0) FROM repository_usage`)

	res := e.replay(e.deletedEntry(repoLocal, im))
	if res.Outcome != journal.Applied || !strings.Contains(string(res.Detail), dg(im.digest)) || !strings.Contains(res.Line(), "applied") {
		t.Fatalf("outcome %q: %+v / %s", res.Outcome, res, res.Detail)
	}
	if e.tombstones(repoLocal) != 1 {
		t.Fatalf("%d marks", e.tombstones(repoLocal))
	}
	if by := e.st.Text(`SELECT COALESCE(deleted_by::text, '') FROM oci_tombstones WHERE repo_id = $1`, repo); by != actor {
		t.Errorf("deleted_by %q", by)
	}
	if got := e.revSeq(repoLocal, "app"); got != rev+1 {
		t.Errorf("rev_seq %d -> %d, want +1", rev, got)
	}

	t.Run("nothing is deleted", func(t *testing.T) {
		if e.itemCount(repoLocal, im.path()) != 1 || e.itemCount(repoLocal, "app/_tags/1") != 1 {
			t.Error("an item was removed")
		}
		if n := e.st.Number(`SELECT count(*) FROM oci_manifests WHERE repo_id = $1 AND digest = $2`, repo, im.digest[:]); n != 1 {
			t.Errorf("%d manifest rows", n)
		}
		if n := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'oci.manifest.deleted'`); n != 0 {
			t.Errorf("the replay wrote %d deletion events", n)
		}
		if got := e.st.Number(`SELECT COALESCE(sum(logical_bytes), 0) FROM repository_usage`); got != usage {
			t.Errorf("storage usage %d -> %d: a withheld manifest counts like a live one", usage, got)
		}
	})
	t.Run("no route serves the manifest or the tag that points at it", func(t *testing.T) {
		if s := e.files(im.path()); s != 404 {
			t.Errorf("the manifest answers %d", s)
		}
		if s := e.files("app/_tags/1"); s != 404 {
			t.Errorf("the tag answers %d", s)
		}
		if s := e.files("app/_tags/2"); s != 200 {
			t.Errorf("another tag answers %d", s)
		}
		if s := e.files("app/_blobs/" + dg(sha256Of("layer"))); s != 200 {
			t.Errorf("a blob answers %d: the mark is on manifests", s)
		}
	})
	t.Run("the tags list omits the tag", func(t *testing.T) {
		page, err := e.store.Tags(context.Background(), repo, "app", "", 10)
		must(t, err)
		if strings.Join(page.Tags, ",") != "2" {
			t.Fatalf("%v", page.Tags)
		}
	})
	t.Run("a second replay of the entry is already in effect", func(t *testing.T) {
		if res := e.replay(e.deletedEntry(repoLocal, im)); res.Outcome != journal.AlreadyInEffect {
			t.Fatalf("outcome %q", res.Outcome)
		}
		if e.tombstones(repoLocal) != 1 || e.revSeq(repoLocal, "app") != rev+1 {
			t.Error("a repeated replay changed something")
		}
	})
}

// TestR_OCI_033_ReplayReportsWhatTheRestoredRepositoryDoesNotHold: an entry about a manifest the repository does not hold, in a repository
// that is not a local OCI repository, or with other bytes is "subject unknown" and changes nothing; an entry that is no entry is skipped.
func TestR_OCI_033_ReplayReportsWhatTheRestoredRepositoryDoesNotHold(t *testing.T) {
	e := newEnv(t)
	held := e.manifestOf(repoLocal, "app", `{"image":"held"}`, manifest, nil, "")
	gone := image{name: "app", digest: sha256Of(`{"image":"never stored"}`)}
	rowOnly := image{name: "app", digest: sha256Of(`{"image":"row without item"}`)}
	e.row(repoLocal, rowOnly, manifest, nil, "", 1)
	e.st.Repository("npm-local", "local", "npm", "{}")
	entry := func(repo int64, name string, im image) map[string]any {
		return map[string]any{"repository": repo, "name": name, "digest": dg(im.digest), "actorId": actor}
	}
	unknown := map[string]map[string]any{
		"a manifest the repository never held":           entry(e.repoID(repoLocal), "app", gone),
		"a row whose item is not stored":                 entry(e.repoID(repoLocal), "app", rowOnly),
		"the digest under another image name":            entry(e.repoID(repoLocal), "other", held),
		"the digest in another repository":               entry(e.repoID(repoOther), "app", held),
		"a remote repository":                            entry(e.repoID(repoCache), "app", held),
		"a repository of another format":                 entry(e.repoID("npm-local"), "app", held),
		"a repository that does not exist":               entry(987654321, "app", held),
		"a digest whose item holds other bytes (forged)": entry(e.repoID(repoLocal), "app", image{name: "app", digest: sha256Of("other bytes")}),
	}
	for what, en := range unknown {
		if res := e.replay(en); res.Outcome != journal.SubjectUnknown {
			t.Errorf("%s: outcome %q", what, res.Outcome)
		}
	}
	skipped := map[string]any{
		"no repository":    map[string]any{"name": "app", "digest": dg(held.digest)},
		"no name":          map[string]any{"repository": e.repoID(repoLocal), "digest": dg(held.digest)},
		"a name too long":  map[string]any{"repository": e.repoID(repoLocal), "name": strings.Repeat("a", 256), "digest": dg(held.digest)},
		"no digest":        map[string]any{"repository": e.repoID(repoLocal), "name": "app"},
		"a digest of md5":  map[string]any{"repository": e.repoID(repoLocal), "name": "app", "digest": "md5:abc"},
		"an upper-case id": map[string]any{"repository": e.repoID(repoLocal), "name": "app", "digest": strings.ToUpper(dg(held.digest))},
	}
	for what, en := range skipped {
		if res := e.replay(en); res.Outcome != journal.Skipped {
			t.Errorf("%s: outcome %q", what, res.Outcome)
		}
	}
	for _, repo := range []string{repoLocal, repoOther, repoCache} {
		if e.tombstones(repo) != 0 {
			t.Errorf("%s holds a mark after entries that name nothing it holds", repo)
		}
	}
	if e.files(held.path()) != 200 {
		t.Error("the manifest the entries named is not served")
	}
}

// TestR_OCI_033_ReinstateLiftsTheMarkOnlyForTheSameBytes: the administrator's command removes the mark while the manifest row and its
// item are stored with the digest, raises the revision and writes oci.reinstate; it refuses a manifest without a mark and one whose
// bytes an administrator deleted to confirm the withdrawal (the mark stays) (R-OCI-033).
func TestR_OCI_033_ReinstateLiftsTheMarkOnlyForTheSameBytes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	im := e.manifestOf(repoLocal, "app", `{"image":"withheld"}`, manifest, nil, "")
	e.tag(repoLocal, "app", "1", `{"image":"withheld"}`)
	confirmed := e.manifestOf(repoLocal, "app", `{"image":"confirmed"}`, manifest, nil, "")
	e.replay(e.deletedEntry(repoLocal, im))
	e.replay(e.deletedEntry(repoLocal, confirmed))
	in := func(i image) ocidata.ReinstateInput {
		return ocidata.ReinstateInput{Project: project, Repository: repoLocal, Name: i.name, Digest: i.digest, Actor: audit.Actor{Kind: "system", Name: "admin-command"}}
	}
	kind := func(err error) errs.Kind {
		var ee *errs.Error
		if errors.As(err, &ee) {
			return ee.Kind
		}
		return 0
	}

	t.Run("an administrator confirms the withdrawal by deleting the manifest: the mark stays and the digest stays refused", func(t *testing.T) {
		if a := e.st.Do(e.r, "DELETE", "/files/"+project+"/"+repoLocal+"/"+confirmed.path(), e.tokAdmin, nil); a.Status/100 != 2 {
			t.Fatalf("DELETE: %d %s", a.Status, a.Body)
		}
		if e.tombstones(repoLocal) != 2 {
			t.Fatalf("%d marks after the confirming delete, want 2", e.tombstones(repoLocal))
		}
		_, err := e.store.Reinstate(ctx, in(confirmed))
		if kind(err) != errs.Conflict || !strings.Contains(err.Error(), "no longer stored") {
			t.Fatalf("err = %v", err)
		}
		if e.tombstones(repoLocal) != 2 {
			t.Error("a refused reinstate changed the marks")
		}
	})
	t.Run("a manifest without a mark is not withheld", func(t *testing.T) {
		other := e.manifestOf(repoLocal, "app", `{"image":"live"}`, manifest, nil, "")
		if _, err := e.store.Reinstate(ctx, in(other)); kind(err) != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the repository must be a local OCI repository", func(t *testing.T) {
		for _, c := range []struct {
			repo string
			want errs.Kind
		}{{repoCache, errs.Invalid}, {"nope", errs.NotFound}} {
			i := in(im)
			i.Repository = c.repo
			if _, err := e.store.Reinstate(ctx, i); kind(err) != c.want {
				t.Errorf("%s: err = %v", c.repo, err)
			}
		}
	})
	t.Run("the same bytes come back", func(t *testing.T) {
		rev := e.revSeq(repoLocal, "app")
		if e.files(im.path()) != 404 {
			t.Fatal("the manifest is served while withheld")
		}
		got, err := e.store.Reinstate(ctx, in(im))
		must(t, err)
		if got.Revision != rev+1 || e.revSeq(repoLocal, "app") != rev+1 {
			t.Errorf("revision %d, stored %d, want %d", got.Revision, e.revSeq(repoLocal, "app"), rev+1)
		}
		if e.files(im.path()) != 200 || e.files("app/_tags/1") != 200 {
			t.Error("the manifest or its tag is still refused")
		}
		page, err := e.store.Tags(ctx, e.repoID(repoLocal), "app", "", 10)
		must(t, err)
		if strings.Join(page.Tags, ",") != "1" {
			t.Errorf("tags %v", page.Tags)
		}
		var reinstated *audit.Event
		for i := range e.events.got {
			if e.events.got[i].Type == audit.EventOCIReinstate {
				reinstated = &e.events.got[i]
			}
		}
		if reinstated == nil || reinstated.Journal != nil || reinstated.Actor.Name != "admin-command" || !strings.Contains(string(reinstated.Details), dg(im.digest)) {
			t.Errorf("event %+v", reinstated)
		}
		if _, err := e.store.Reinstate(ctx, in(im)); kind(err) != errs.NotFound {
			t.Errorf("a second reinstate: %v", err)
		}
	})
}

// TestR_OCI_033_ADeletionReplaysAfterARestore: the whole road. An image is deleted through the item store, which journals the deletion;
// the restore brings the snapshot's image back; the replay of the journal's entry withholds it (R-OCI-033, Q-OCI-30).
func TestR_OCI_033_ADeletionReplaysAfterARestore(t *testing.T) {
	e := newEnv(t)
	body := `{"image":"deleted after the snapshot"}`
	im := e.manifestOf(repoLocal, "app", body, manifest, nil, "")
	e.tag(repoLocal, "app", "latest", body)
	if a := e.st.Do(e.r, "DELETE", "/files/"+project+"/"+repoLocal+"/"+im.path(), e.tokAdmin, nil); a.Status/100 != 2 {
		t.Fatalf("DELETE: %d %s", a.Status, a.Body)
	}
	entries := e.strings(`SELECT entry::text FROM revocation_outbox WHERE event = 'oci.manifest.deleted'`)
	if len(entries) != 1 {
		t.Fatalf("%d journal entries", len(entries))
	}
	if e.itemCount(repoLocal, im.path()) != 0 || e.files("app/_tags/latest") != 404 {
		t.Fatal("the delete left the image")
	}
	// The restore: the snapshot holds the image, its tag and its rows again.
	e.manifestOf(repoLocal, "app", body, manifest, nil, "")
	e.tag(repoLocal, "app", "latest", body)
	if e.files(im.path()) != 200 {
		t.Fatal("the restored image is not served")
	}
	var entry map[string]any
	must(t, json.Unmarshal([]byte(entries[0]), &entry))
	entry["repository"] = e.repoID(repoLocal)
	if res := e.replay(entry); res.Outcome != journal.Applied {
		t.Fatalf("outcome %q: %s", res.Outcome, res.Detail)
	}
	if e.files(im.path()) != 404 || e.files("app/_tags/latest") != 404 || e.itemCount(repoLocal, im.path()) != 1 {
		t.Error("the replayed deletion does not withhold the image")
	}
}
