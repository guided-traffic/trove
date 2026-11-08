//go:build integration

package ocilocal

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	ocidata "github.com/guided-traffic/trove/internal/formatdata/oci"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func (e *env) sweeper(grace time.Duration, now func() time.Time) *ocidata.Sweeper {
	e.t.Helper()
	sw, err := ocidata.NewSweeper(ocidata.SweepOptions{
		Store: e.store, DB: e.db, Grace: grace, Now: now, Batch: 2,
		Items: func() ocidata.Remover { return e.r.App.Items() },
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return sw
}

func blobPath(name, body string) string { return name + "/_blobs/" + dg(sha256Of(body)) }

// TestR_OCI_019_TheSweepRemovesOnlyUnreferencedBlobsAfterTheGrace: a blob item that no manifest of its name references, that is older than
// the grace and that no unfinished upload session holds, is removed; one a manifest references (in its own name only), a fresh one, one a
// session holds and every blob of a remote repository stay (R-OCI-019).
func TestR_OCI_019_TheSweepRemovesOnlyUnreferencedBlobsAfterTheGrace(t *testing.T) {
	e := newEnv(t)
	const old = 48 * time.Hour
	layer := "layer shared by two names"
	put := func(repo, name, body string) string {
		p := blobPath(name, body)
		e.put(repo, p, body, "application/octet-stream")
		return p
	}
	referenced := put(repoLocal, "app", layer)
	e.manifestOf(repoLocal, "app", `{"image":1}`, manifest, nil, "", ref{sha256Of(layer), "blob"})
	sameDigestOtherName := put(repoLocal, "other", layer)
	loose := put(repoLocal, "app", "loose layer")
	fresh := put(repoLocal, "app", "fresh layer")
	held := put(repoLocal, "app", "held by a session")
	otherRepo := put(repoOther, "app", "loose layer in another repository")
	cached := put(repoCache, "library/nginx", "cached layer of a remote")
	e.age(repoLocal, old, referenced, sameDigestOtherName, loose, held)
	e.age(repoOther, old, otherRepo)
	e.age(repoCache, old, cached)
	heldDigest := sha256Of("held by a session")
	e.st.Exec(`INSERT INTO upload_sessions (id, repo_id, addressed_repo_id, binding, received, hash_state, part_size, s3_upload_id, digest, state)
		VALUES (gen_random_uuid(), $1, $1, 'app', 0, '\x00', 5242880, 'x', $2, 'completing')`, e.repoID(repoLocal), heldDigest[:])

	res, err := e.sweeper(24*time.Hour, nil).Sweep(context.Background())
	must(t, err)
	if res.Removed != 3 || res.Scanned != 3 || res.Kept != 0 {
		t.Fatalf("result %+v, want 3 removed (the loose blob, the same bytes under another name, the loose blob of another repository)", res)
	}
	for _, c := range []struct {
		repo, path string
		want       int64
	}{
		{repoLocal, referenced, 1},
		{repoLocal, sameDigestOtherName, 0},
		{repoLocal, loose, 0},
		{repoLocal, fresh, 1},
		{repoLocal, held, 1},
		{repoOther, otherRepo, 0},
		{repoCache, cached, 1},
	} {
		if got := e.itemCount(c.repo, c.path); got != c.want {
			t.Errorf("%s/%s: %d items, want %d", c.repo, c.path, got, c.want)
		}
	}
	// What the sweep removes goes through the item store: the removal is an audited deletion by the system, and the object of a blob
	// no item holds is the garbage collection's.
	if n := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'artifact.deleted' AND actor_type = 'system' AND actor_name = 'removal'`); n != 3 {
		t.Errorf("%d deletions by the system actor, want 3", n)
	}

	t.Run("a second run finds nothing", func(t *testing.T) {
		res, err := e.sweeper(24*time.Hour, nil).Sweep(context.Background())
		must(t, err)
		if res.Removed != 0 || res.Scanned != 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("the grace is measured from the item and the clock is the sweeper's", func(t *testing.T) {
		later := func() time.Time { return time.Now().Add(25 * time.Hour) }
		res, err := e.sweeper(24*time.Hour, later).Sweep(context.Background())
		must(t, err)
		if res.Removed != 1 || e.itemCount(repoLocal, fresh) != 0 {
			t.Fatalf("%+v: the fresh blob is old enough now", res)
		}
		if e.itemCount(repoLocal, referenced) != 1 || e.itemCount(repoLocal, held) != 1 || e.itemCount(repoCache, cached) != 1 {
			t.Error("a referenced blob, a blob a session holds or a remote's blob was swept")
		}
	})
	t.Run("a session that ended no longer protects its blob", func(t *testing.T) {
		e.st.Exec(`UPDATE upload_sessions SET state = 'done' WHERE repo_id = $1`, e.repoID(repoLocal))
		res, err := e.sweeper(24*time.Hour, nil).Sweep(context.Background())
		must(t, err)
		if res.Removed != 1 || e.itemCount(repoLocal, held) != 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("a manifest that took the blob stays with it", func(t *testing.T) {
		kept := put(repoLocal, "app", "layer a later manifest names")
		e.age(repoLocal, old, kept)
		e.manifestOf(repoLocal, "app", `{"image":2}`, manifest, nil, "", ref{sha256Of("layer a later manifest names"), "blob"})
		res, err := e.sweeper(24*time.Hour, nil).Sweep(context.Background())
		must(t, err)
		if res.Removed != 0 || e.itemCount(repoLocal, kept) != 1 {
			t.Fatalf("%+v", res)
		}
	})
}

// TestAPushThatCommitsBetweenTheScanAndTheRemovalKeepsItsBlob: the sweep takes the lock of the image name's package row before
// it removes a blob and reads the references again, so a manifest that references the blob and commits while the sweep waits for the lock
// wins (R-OCI-019, R-OCI-007).
//
// Mechanism of R-OCI-019, not a proof of its criteria: it checks a race or the schedule of the job; the criterion is the removal
// of an unreferenced blob after the grace and a sweep.
func TestAPushThatCommitsBetweenTheScanAndTheRemovalKeepsItsBlob(t *testing.T) {
	e := newEnv(t)
	p := blobPath("race", "layer of a push in flight")
	e.put(repoLocal, p, "layer of a push in flight", "application/octet-stream")
	e.age(repoLocal, 48*time.Hour, p)
	repo := e.repoID(repoLocal)
	pushed := image{name: "race", digest: sha256Of("manifest of the push")}

	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- e.db.InTx(context.Background(), func(mtx meta.Tx) error {
			tx, _ := postgres.Unwrap(mtx)
			// The push: lock the package of the name, then (once the sweep waits) record the manifest and its reference.
			if _, err := tx.Exec(context.Background(), `INSERT INTO packages (repo_id, name, display_name) VALUES ($1, 'race', 'race') ON CONFLICT DO NOTHING`, repo); err != nil {
				return err
			}
			if _, err := tx.Exec(context.Background(), `SELECT 1 FROM packages WHERE repo_id = $1 AND name = 'race' FOR UPDATE`, repo); err != nil {
				return err
			}
			// As the commit hook of a push does: the rows of the layers it names are held FOR SHARE until its own rows are written.
			if _, err := tx.Exec(context.Background(), `SELECT 1 FROM items WHERE repo_id = $1 AND path = $2 FOR SHARE`, repo, p); err != nil {
				return err
			}
			close(locked)
			<-release
			if _, err := tx.Exec(context.Background(), `INSERT INTO oci_manifests (repo_id, name, digest, media_type, size) VALUES ($1, 'race', $2, $3, 1)`,
				repo, pushed.digest[:], manifest); err != nil {
				return err
			}
			target := sha256Of("layer of a push in flight")
			_, err := tx.Exec(context.Background(), `INSERT INTO oci_references (repo_id, name, manifest, target, kind) VALUES ($1, 'race', $2, $3, 'blob')`,
				repo, pushed.digest[:], target[:])
			return err
		})
	}()
	<-locked

	var wg sync.WaitGroup
	wg.Add(1)
	var res ocidata.SweepResult
	var sweepErr error
	go func() {
		defer wg.Done()
		res, sweepErr = e.sweeper(24*time.Hour, nil).Sweep(context.Background())
	}()
	// Wait until the sweep stands at the lock, then let the push commit.
	deadline := time.Now().Add(30 * time.Second)
	for e.st.Number(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the sweep never waited for the lock")
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	must(t, sweepErr)
	if res.Removed != 0 || res.Kept != 1 || e.itemCount(repoLocal, p) != 1 {
		t.Fatalf("%+v, blob stored %d: the blob a committed manifest references was removed", res, e.itemCount(repoLocal, p))
	}
}

// TestTheJobRunsOnAWorkerAsAnHourlyMaintenanceTask: the task oci-sweep is registered with the hourly schedule, a manual run is
// executed by a worker replica, and the job records what it removed (R-OCI-019, R-STOR-025).
//
// Mechanism of R-OCI-019, not a proof of its criteria: it checks a race or the schedule of the job; the criterion is the removal
// of an unreferenced blob after the grace and a sweep.
func TestTheJobRunsOnAWorkerAsAnHourlyMaintenanceTask(t *testing.T) {
	e := newEnv(t)
	// The blob is old before the worker starts, so every run that follows sees it (the worker's scheduler may queue a run on its own).
	p := blobPath("app", "loose layer of a job")
	e.put(repoLocal, p, "loose layer of a job", "application/octet-stream")
	e.age(repoLocal, 48*time.Hour, p)
	worker := e.st.Start(t, testenv.ReplicaOptions{Name: "worker-1", Role: "worker"})

	list := e.st.Do(e.r, "GET", "/api/v1/system/tasks?limit=100", e.tokAdmin, nil)
	var doc struct {
		Items []struct {
			Name     string `json:"name"`
			Schedule string `json:"schedule"`
			Enabled  bool   `json:"enabled"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list.Body, &doc); err != nil || list.Status != 200 {
		t.Fatalf("%d %s (%v)", list.Status, list.Body, err)
	}
	found := false
	for _, task := range doc.Items {
		if task.Name == ocidata.SweepJobKind {
			found = true
			if task.Schedule != "0 * * * *" || !task.Enabled {
				t.Errorf("task %+v", task)
			}
		}
	}
	if !found {
		t.Fatalf("no task %s in %s", ocidata.SweepJobKind, list.Body)
	}

	// A run is queued by the scheduler or by this request; a request that meets the exclusive key answers 409 with the queued run.
	if a := e.st.Do(e.r, "POST", "/api/v1/system/tasks/"+ocidata.SweepJobKind+"/run", e.tokAdmin, nil); a.Status != 202 && a.Status != 409 {
		t.Fatalf("run: %d %s", a.Status, a.Body)
	}
	deadline := time.Now().Add(90 * time.Second)
	for e.st.Number(`SELECT count(*) FROM job WHERE kind = 'oci-sweep' AND state = 'succeeded'`) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no run succeeded: %s", e.st.Text(`SELECT COALESCE((SELECT state::text || ' ' || COALESCE(error::text, '') FROM job WHERE kind = 'oci-sweep' ORDER BY created_at DESC LIMIT 1), 'no job')`))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := e.st.Number(`SELECT COALESCE(sum((result->>'removed')::int), 0) FROM job WHERE kind = 'oci-sweep' AND state = 'succeeded'`); got != 1 {
		t.Errorf("the runs removed %d blobs, want 1", got)
	}
	if e.itemCount(repoLocal, p) != 0 {
		t.Error("the blob stayed")
	}
	if got := testenv.MetricValue(worker.Metrics(t), "trove_oci_sweep_blobs_removed_total"); got != 1 {
		t.Errorf("trove_oci_sweep_blobs_removed_total = %v, want 1", got)
	}
}
