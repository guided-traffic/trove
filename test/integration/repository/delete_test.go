//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/repository"
)

func (e *env) addItems(repoKey string, folders, files int) {
	e.t.Helper()
	e.exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size)
	        SELECT decode(lpad(to_hex(i), 64, '0'), 'hex'), decode(lpad(to_hex(i), 40, '0'), 'hex'),
	               decode(lpad(to_hex(i), 32, '0'), 'hex'), decode(lpad(to_hex(i), 128, '0'), 'hex'), 10 * i
	          FROM generate_series(1, $1::int) AS i`, files)
	e.exec(`INSERT INTO items (repo_id, path, parent_path, kind, explicit_folder)
	        SELECT r.id, 'dir' || i, '', 'folder', true
	          FROM repositories r, generate_series(1, $2::int) AS i WHERE r.key = $1`, repoKey, folders)
	e.exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size)
	        SELECT r.id, 'dir1/file' || i, 'dir1', 'file', decode(lpad(to_hex(i), 64, '0'), 'hex'), 10 * i
	          FROM repositories r, generate_series(1, $2::int) AS i WHERE r.key = $1`, repoKey, files)
}

func (e *env) waitJob(q *jobs.Queue, id string) jobs.Job {
	e.t.Helper()
	var j jobs.Job
	waitFor(e.t, "the deletion job to end", 30*time.Second, func() bool {
		got, ok, err := q.Get(context.Background(), id)
		if err != nil || !ok {
			return false
		}
		j = got
		return j.Terminal()
	})
	return j
}

func TestDelete(t *testing.T) {
	e := newEnv(t)
	poll := 300 * time.Millisecond
	// The worker replica runs the job and the web replica asks for it, as in the
	// chart; the batch size of 2 makes the job loop over several transactions.
	w := e.newReplica("del-worker", replicaOpts{role: "worker", deleteBatch: 2, poll: poll})
	web := e.newReplica("del-web", replicaOpts{deleteBatch: 2, poll: poll})
	ctx := context.Background()

	if _, err := web.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "to-delete"))); err != nil {
		t.Fatal(err)
	}
	if _, err := web.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "keeper"))); err != nil {
		t.Fatal(err)
	}
	e.addItems("to-delete", 3, 5)
	e.addItems("keeper", 1, 0) // other repository's items must survive; blobs 1..5 exist already
	if e.scalar(`SELECT count(*) FROM items`) != 9 {
		t.Fatalf("items = %d", e.scalar(`SELECT count(*) FROM items`))
	}

	var job string
	t.Run("R-REPO-013 the first transaction marks the repository deleting and enqueues the job", func(t *testing.T) {
		// Stop the worker from racing the assertions below: the job is claimed within
		// a poll interval, so look at the state in the same transaction instead.
		res, err := web.svc.Delete(ctx, caller, "to-delete", false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Done || res.JobID == "" {
			t.Fatalf("result = %+v: a repository with content is deleted by a job", res)
		}
		job = res.JobID
		if got := web.rec.types(); got[len(got)-1] != repository.EventDeleted || !strings.Contains(string(web.rec.last().Details), `"phase":"started"`) {
			t.Fatalf("events = %v", got)
		}
	})

	var finished jobs.Job
	t.Run("R-REPO-013 the job removes the items in batches and the row last", func(t *testing.T) {
		finished = e.waitJob(w.queue, job)
		if finished.State != jobs.StateSucceeded {
			t.Fatalf("job = %+v", finished)
		}
		if finished.ProgressDone != 8 || finished.ProgressUnit != "items" {
			t.Fatalf("progress = %d %s, want 8 items", finished.ProgressDone, finished.ProgressUnit)
		}
		if e.scalar(`SELECT count(*) FROM repositories WHERE key = 'to-delete'`) != 0 {
			t.Fatal("the row is still there")
		}
		if e.scalar(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = 'keeper'`) != 1 {
			t.Fatal("the other repository lost items")
		}
		if e.scalar(`SELECT count(*) FROM items`) != 1 {
			t.Fatal("items of the deleted repository remain")
		}
		if e.scalar(`SELECT count(*) FROM blob_gc_queue`) != 5 {
			t.Fatalf("blob_gc_queue = %d, want the 5 blobs the deleted files referenced", e.scalar(`SELECT count(*) FROM blob_gc_queue`))
		}
	})

	t.Run("R-REPO-013 the completion is audited and announced to every replica", func(t *testing.T) {
		types := web.rec.types()
		if len(types) < 3 { // two creates, the started event
			t.Fatalf("events on the web replica: %v", types)
		}
		waitFor(t, "the worker to record the completion", 5*time.Second, func() bool {
			w.rec.mu.Lock()
			defer w.rec.mu.Unlock()
			return len(w.rec.events) == 1 && strings.Contains(string(w.rec.events[0].Details), `"phase":"completed"`)
		})
		waitFor(t, "the web replica to drop the repository", 3*poll, func() bool {
			_, err := web.reader.ByKey(ctx, "to-delete")
			return err != nil
		})
		_, err := web.reader.ByKey(ctx, "to-delete")
		wantCode(t, err, 404, errs.CodeRepositoryNotFound)
	})

	t.Run("R-REPO-013 the key is free again and the new repository is empty", func(t *testing.T) {
		if _, err := web.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "to-delete"))); err != nil {
			t.Fatal(err)
		}
		if e.scalar(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = 'to-delete'`) != 0 {
			t.Fatal("the new repository is not empty")
		}
	})

	t.Run("R-REPO-013 a repository being deleted is not resolvable by clients and its key stays taken", func(t *testing.T) {
		// A paused world: mark the row by hand-free means by deleting through a replica
		// that has no worker to run the job (the web replica enqueues, the worker
		// would claim it), so use a repository the worker cannot start on yet.
		if _, err := web.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "held"))); err != nil {
			t.Fatal(err)
		}
		e.exec(`UPDATE repositories SET lifecycle = 'deleting' WHERE key = 'held'`)
		_, err := web.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "held")))
		wantCode(t, err, 409, errs.CodeRepositoryExists)
		_, err = web.svc.Patch(ctx, caller, "held", repository.MatchAny(), []byte(`{"description":"x"}`))
		wantCode(t, err, 409, errs.CodeConflict)
		got, err := web.svc.Get(ctx, "held")
		if err != nil || got.Repository.Lifecycle != repository.Deleting {
			t.Fatalf("Get = %+v, %v", got.Repository, err)
		}
		list, _ := web.svc.List(ctx, repository.ListFilter{})
		for _, it := range list.Items {
			if it.Repository.Key == "held" {
				t.Fatal("a repository being deleted is listed without IncludeDeleting")
			}
		}
		list, _ = web.svc.List(ctx, repository.ListFilter{IncludeDeleting: true})
		found := false
		for _, it := range list.Items {
			found = found || it.Repository.Key == "held"
		}
		if !found {
			t.Fatal("IncludeDeleting does not list it")
		}
		// A delete of it joins the job: the worker finishes the deletion.
		res, err := web.svc.Delete(ctx, caller, "held", false)
		if err != nil || res.JobID == "" {
			t.Fatalf("Delete = %+v, %v", res, err)
		}
		if j := e.waitJob(w.queue, res.JobID); j.State != jobs.StateSucceeded {
			t.Fatalf("job = %+v", j)
		}
		if e.scalar(`SELECT count(*) FROM repositories WHERE key = 'held'`) != 0 {
			t.Fatal("the row is still there")
		}
	})

	t.Run("R-REPO-013 deleting a repository that does not exist is 404", func(t *testing.T) {
		_, err := web.svc.Delete(ctx, caller, "never-was", false)
		wantCode(t, err, 404, errs.CodeRepositoryNotFound)
	})
}
