//go:build integration

package ocilocal

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The delete job of a whole name commits its batches one by one, blobs first. When a later batch is refused (here: an index pushed after
// the delete was authorized lists a manifest the job would remove), the blobs of the earlier batches are gone and the manifests that name
// them stay.
//
// Mechanism of R-OCI-018, not a proof of its criteria: it checks one step of the cascade, the hook or the grammar of a reference;
// the criteria are the index delete with a shared child, the 409 for a child and the conformance content-management category.
func TestADeleteJobThatStopsLeavesNoManifestOverARemovedBlob(t *testing.T) {
	e := newEnv(t, "TROVE_STORAGE_DELETE_MAX_SYNC_ITEMS=1", "TROVE_STORAGE_DELETE_BATCH_SIZE=1")
	layer := e.put(repoLocal, "app/_blobs/"+dg(sha256Of("layer")), "layer", "application/octet-stream")
	c1 := e.manifestOf(repoLocal, "app", `{"platform":"amd64"}`, manifest, nil, "", ref{layer, "blob"})
	a := e.st.Do(e.r, "DELETE", "/files/"+project+"/"+repoLocal+"/app", e.tokAdmin, nil)
	if a.Status != 202 {
		t.Fatalf("DELETE: %d %s, want the job", a.Status, a.Body)
	}
	// A push after the delete was authorized (an item above the job's max id): an index that lists c1.
	e.manifestOf(repoLocal, "app", `{"index":"late"}`, index, nil, "", ref{c1.digest, "manifest"})
	e.st.Start(t, testenv.ReplicaOptions{Name: "worker-1", Role: "worker"})
	deadline := time.Now().Add(90 * time.Second)
	for e.st.Number(`SELECT count(*) FROM job WHERE kind = 'items-delete' AND (state IN ('failed', 'succeeded') OR error IS NOT NULL)`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the job never ran")
		}
		time.Sleep(200 * time.Millisecond)
	}
	job := e.st.Text(`SELECT state::text || ' ' || COALESCE(left(error::text, 200), '') FROM job WHERE kind = 'items-delete' ORDER BY created_at DESC LIMIT 1`)
	blob := e.itemCount(repoLocal, "app/_blobs/"+dg(layer))
	man := e.itemCount(repoLocal, c1.path())
	refs := e.st.Number(`SELECT count(*) FROM oci_references WHERE repo_id = $1 AND manifest = $2 AND target = $3`, e.repoID(repoLocal), c1.digest[:], layer[:])
	t.Logf("job: %s; layer items %d, c1 items %d, c1 -> layer references %d", job, blob, man, refs)
	if man == 1 && refs == 1 && blob == 0 {
		t.Error("DANGLING: c1 stays and names a layer the job removed")
	}
	// The batch that holds the blob takes c1 with it or fails whole: either way no manifest stays over a removed blob.
	if (blob == 0) != (man == 0) && refs > 0 {
		t.Errorf("layer items %d, c1 items %d: the graph is not whole", blob, man)
	}
}
