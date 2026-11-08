//go:build integration

package ocilocal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// fixture is a multi-platform image as a push leaves it: an index over three platform manifests, a second index that lists one of
// them, a signature of the index, and tags on the index and on one platform manifest.
type fixture struct {
	index, other, c1, c2, c3, signature image
	layer                               [32]byte
}

func (e *env) multiPlatform() fixture {
	e.t.Helper()
	var f fixture
	f.layer = e.put(repoLocal, "app/_blobs/"+dg(sha256Of("layer")), "layer", "application/octet-stream")
	f.c1 = e.manifestOf(repoLocal, "app", `{"platform":"amd64"}`, manifest, nil, "", ref{f.layer, "blob"})
	f.c2 = e.manifestOf(repoLocal, "app", `{"platform":"arm64"}`, manifest, nil, "", ref{f.layer, "blob"})
	f.c3 = e.manifestOf(repoLocal, "app", `{"platform":"riscv"}`, manifest, nil, "")
	f.index = e.manifestOf(repoLocal, "app", `{"index":"main"}`, index, nil, "",
		ref{f.c1.digest, "manifest"}, ref{f.c2.digest, "manifest"}, ref{f.c3.digest, "manifest"})
	f.other = e.manifestOf(repoLocal, "app", `{"index":"other"}`, index, nil, "", ref{f.c3.digest, "manifest"})
	f.signature = e.manifestOf(repoLocal, "app", `{"signature":1}`, manifest, &f.index.digest, "application/vnd.example.sig")
	e.tag(repoLocal, "app", "latest", `{"index":"main"}`)
	e.tag(repoLocal, "app", "arm", `{"platform":"arm64"}`)
	return f
}

// TestR_OCI_018_ARawDeleteOfAnIndexTakesItsTagsReferrersAndExclusiveChildren: the delete of an index through the item store removes the
// index, its tags and their versions, its referrers and the children no other manifest and no tag names; a child a second index lists
// and a child a tag points at stay. Every removed manifest writes oci.manifest.deleted with a journal entry (R-OCI-018, R-OCI-033).
func TestR_OCI_018_ARawDeleteOfAnIndexTakesItsTagsReferrersAndExclusiveChildren(t *testing.T) {
	e := newEnv(t)
	f := e.multiPlatform()
	repo := e.repoID(repoLocal)
	rev := e.revSeq(repoLocal, "app")
	if e.liveVersions(repoLocal, "app") != 2 {
		t.Fatalf("seeded %d versions", e.liveVersions(repoLocal, "app"))
	}

	a := e.st.Do(e.r, "DELETE", "/files/"+project+"/"+repoLocal+"/"+f.index.path(), e.tokAdmin, nil)
	if a.Status/100 != 2 {
		t.Fatalf("DELETE: %d %s", a.Status, a.Body)
	}

	gone := []string{f.index.path(), "app/_tags/latest", f.signature.path(), f.c1.path()}
	stays := []string{f.other.path(), f.c2.path(), f.c3.path(), "app/_tags/arm", "app/_blobs/" + dg(f.layer)}
	for _, p := range gone {
		if e.itemCount(repoLocal, p) != 0 {
			t.Errorf("%s is still stored", p)
		}
	}
	for _, p := range stays {
		if e.itemCount(repoLocal, p) != 1 {
			t.Errorf("%s was removed", p)
		}
	}
	rowsOf := func(im image) int64 {
		return e.st.Number(`SELECT count(*) FROM oci_manifests WHERE repo_id = $1 AND name = $2 AND digest = $3`, repo, im.name, im.digest[:])
	}
	for _, im := range []image{f.index, f.signature, f.c1} {
		if rowsOf(im) != 0 {
			t.Errorf("the row of %s stayed", dg(im.digest))
		}
	}
	for _, im := range []image{f.other, f.c2, f.c3} {
		if rowsOf(im) != 1 {
			t.Errorf("the row of %s was removed", dg(im.digest))
		}
	}
	if n := e.st.Number(`SELECT count(*) FROM oci_references WHERE repo_id = $1 AND manifest = $2`, repo, f.index.digest[:]); n != 0 {
		t.Errorf("%d references of the removed index stayed", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM oci_references WHERE repo_id = $1 AND manifest = $2`, repo, f.other.digest[:]); n != 1 {
		t.Errorf("the second index lost its references: %d", n)
	}
	if v := e.liveVersions(repoLocal, "app"); v != 1 {
		t.Errorf("%d live versions after the delete, want 1 (the tag latest went, arm stayed)", v)
	}
	if got := e.revSeq(repoLocal, "app"); got <= rev {
		t.Errorf("rev_seq %d -> %d", rev, got)
	}

	t.Run("each removed manifest writes oci.manifest.deleted with a journal entry", func(t *testing.T) {
		got := map[string]int{}
		e.st.Query(`SELECT details->>'digest', (details->>'deleted')::int FROM audit_event WHERE action = 'oci.manifest.deleted' AND repository = 'it/docker-local'`,
			func(rs pgx.Rows) error {
				var d string
				var n int
				if err := rs.Scan(&d, &n); err != nil {
					return err
				}
				got[d] = n
				return nil
			})
		if len(got) != 3 || got[dg(f.index.digest)] != 3 || got[dg(f.signature.digest)] != 0 || got[dg(f.c1.digest)] != 0 {
			t.Fatalf("events %v, want the index (3 more items), its signature and the exclusive child", got)
		}
		entries := e.strings(`SELECT entry::text FROM revocation_outbox WHERE event = 'oci.manifest.deleted'`)
		if len(entries) != 3 {
			t.Fatalf("%d journal entries, want 3", len(entries))
		}
		for _, s := range entries {
			var en struct {
				Repository int64  `json:"repository"`
				Name       string `json:"name"`
				Digest     string `json:"digest"`
				ActorID    string `json:"actorId"`
			}
			if err := json.Unmarshal([]byte(s), &en); err != nil || en.Repository != repo || en.Name != "app" || !strings.HasPrefix(en.Digest, "sha256:") || en.ActorID == "" {
				t.Errorf("entry %s (%v)", s, err)
			}
		}
	})
}

// TestR_OCI_018_ADeleteThatWouldLeaveADanglingReferenceIsRefused: a child while its index exists, and a blob a manifest references
// (R-OCI-018). The refusal rolls the whole removal back.
func TestR_OCI_018_ADeleteThatWouldLeaveADanglingReferenceIsRefused(t *testing.T) {
	e := newEnv(t)
	f := e.multiPlatform()
	for _, p := range []string{f.c1.path(), "app/_blobs/" + dg(f.layer), f.c3.path()} {
		a := e.st.Do(e.r, "DELETE", "/files/"+project+"/"+repoLocal+"/"+p, e.tokAdmin, nil)
		if a.Status != 409 {
			t.Errorf("DELETE %s: %d %s, want 409", p, a.Status, a.Body)
		}
		if e.itemCount(repoLocal, p) != 1 {
			t.Errorf("%s was removed although it was refused", p)
		}
	}
	// A subtree that holds the indexes and their children goes whole: nothing is left to dangle.
	a := e.st.Do(e.r, "DELETE", "/files/"+project+"/"+repoLocal+"/app", e.tokAdmin, nil)
	if a.Status/100 != 2 {
		t.Fatalf("DELETE of the whole name: %d %s", a.Status, a.Body)
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND path LIKE 'app/%'`, e.repoID(repoLocal)); n != 0 {
		t.Errorf("%d items stayed", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM oci_manifests WHERE repo_id = $1`, e.repoID(repoLocal)); n != 0 {
		t.Errorf("%d manifest rows stayed", n)
	}
}

// TestR_OCI_018_ARemovalWithoutARequestIsTheSystemsAndCascades: a removal through the item store with no actor (a retention run, a job)
// runs the same hook, and the events it writes name the system actor "removal" (Q-OCI-24).
func TestR_OCI_018_ARemovalWithoutARequestIsTheSystemsAndCascades(t *testing.T) {
	e := newEnv(t)
	f := e.multiPlatform()
	rp, err := e.r.App.Repositories().ByName(context.Background(), qname.Repo{Project: project, Key: repoLocal})
	if err != nil {
		t.Fatal(err)
	}
	err = e.db.InTx(context.Background(), func(tx meta.Tx) error {
		return e.r.App.Items().RemoveIn(context.Background(), tx, items.RemoveSpec{Repo: items.RepoOf(rp), Paths: []string{f.index.path()}, Actor: audit.Actor{Kind: "system", Name: "removal"}})
	})
	must(t, err)
	if e.itemCount(repoLocal, "app/_tags/latest") != 0 || e.itemCount(repoLocal, f.signature.path()) != 0 || e.itemCount(repoLocal, f.c1.path()) != 0 {
		t.Error("the cascade did not run")
	}
	kinds := e.strings(`SELECT actor_type || '/' || COALESCE(actor_name, '') FROM audit_event WHERE action = 'oci.manifest.deleted'`)
	if len(kinds) != 3 {
		t.Fatalf("%d events: %v", len(kinds), kinds)
	}
	for _, k := range kinds {
		if k != "system/removal" {
			t.Errorf("actor %q, want system/removal", k)
		}
	}
}

// TestR_OCI_018_ADeleteOfAWholeNameBeyondOneEventIsOneRemoval: an image name with more than 5,000 files is removed through /files/ although
// its blobs and its manifests reach the hook in different events (the blobs of a name sort before its manifests): the item store tells the
// hook which referencing manifests belong to the same removal (ItemsEvent.InScope).
func TestR_OCI_018_ADeleteOfAWholeNameBeyondOneEventIsOneRemoval(t *testing.T) {
	e := newEnv(t)
	repo := e.repoID(repoLocal)
	layer := e.put(repoLocal, "big/_blobs/"+dg(sha256Of("big layer")), "big layer", "application/octet-stream")
	e.manifestOf(repoLocal, "big", `{"big":"image"}`, manifest, nil, "", ref{layer, "blob"})
	e.tag(repoLocal, "big", "1", `{"big":"image"}`)
	// Files between the blobs and the manifests in path order (the layout's `_c` is none of the format's): the event of the first 5,000
	// files holds the referenced blob, the event of the rest holds the manifest and the tag.
	e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size, media_type)
		SELECT repo_id, 'big/_c/f' || lpad(n::text, 5, '0'), 'big/_c', kind, blob_sha256, size, media_type
		FROM items, generate_series(1, 5001) AS n WHERE repo_id = $1 AND path = $2`, repo, "big/_blobs/"+dg(layer))
	e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, explicit_folder) VALUES ($1, 'big/_c', 'big', 'folder', false) ON CONFLICT DO NOTHING`, repo)

	if status, body := e.deleteWithin(t, "/files/"+project+"/"+repoLocal+"/big", 5004); status/100 != 2 {
		t.Fatalf("DELETE: %d %s", status, body)
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND path LIKE 'big/%'`, repo); n != 0 {
		t.Errorf("%d items stayed", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM oci_manifests WHERE repo_id = $1 AND name = 'big'`, repo); n != 0 {
		t.Errorf("%d manifest rows stayed", n)
	}
}

// TestR_OCI_018_TheDeleteJobOfAWholeNameIsOneRemovalToo: above storage.delete.max_sync_items the job removes in batches, each in a transaction
// of its own, and the blobs go before the manifests that reference them; the job takes the scope of the whole delete to the hook.
func TestR_OCI_018_TheDeleteJobOfAWholeNameIsOneRemovalToo(t *testing.T) {
	e := newEnv(t, "TROVE_STORAGE_DELETE_MAX_SYNC_ITEMS=5", "TROVE_STORAGE_DELETE_BATCH_SIZE=3")
	e.st.Start(t, testenv.ReplicaOptions{Name: "worker-1", Role: "worker"})
	f := e.multiPlatform()
	_ = f
	repo := e.repoID(repoLocal)
	a := e.st.Do(e.r, "DELETE", "/files/"+project+"/"+repoLocal+"/app", e.tokAdmin, nil)
	if a.Status != 202 {
		t.Fatalf("DELETE: %d %s, want the job (202)", a.Status, a.Body)
	}
	deadline := time.Now().Add(90 * time.Second)
	for e.st.Number(`SELECT count(*) FROM job WHERE kind = 'items-delete' AND state IN ('succeeded', 'failed', 'cancelled')`) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the delete job did not end: %s", e.st.Text(`SELECT COALESCE((SELECT state::text || ' attempt ' || attempt || ' ' || COALESCE(left(error::text, 600), '') FROM job WHERE kind = 'items-delete' ORDER BY created_at DESC LIMIT 1), 'no job')`))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if e.st.Number(`SELECT count(*) FROM job WHERE kind = 'items-delete' AND state = 'succeeded'`) != 1 {
		t.Fatalf("the job did not succeed: %s", e.st.Text(`SELECT COALESCE((SELECT error::text FROM job WHERE kind = 'items-delete' ORDER BY created_at DESC LIMIT 1), '')`))
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND path LIKE 'app/%'`, repo); n != 0 {
		t.Errorf("%d items stayed", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM oci_manifests WHERE repo_id = $1`, repo); n != 0 {
		t.Errorf("%d manifest rows stayed", n)
	}
}

// deleteWithin sends a DELETE whose work grows with the number of files: the bound of the request is a minute and a twentieth of a second per
// file, not the 30 seconds of the stack's client, which a loaded host overruns with a few thousand rows (the bound is for a hung server, not
// a measure of speed).
func (e *env) deleteWithin(t *testing.T, path string, files int) (int, string) {
	t.Helper()
	c := *testenv.Client
	c.Timeout = time.Minute + time.Duration(files)*50*time.Millisecond
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, "http://"+e.r.Addr("default")+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = testenv.DefaultHost
	req.Header.Set("Authorization", "Bearer "+e.tokAdmin)
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}
