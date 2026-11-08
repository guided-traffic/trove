//go:build integration

package files

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/project"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/servefacts"
)

// afterFirstBatch runs fn once, after the transaction of the first batch of a removal that reaches the storage hook has committed and
// before the job's next batch opens its guard: meta.Tx.After runs in the job's goroutine.
type afterFirstBatch struct {
	once  sync.Once
	fn    func()
	calls atomic.Int64
	ran   atomic.Bool
}

func (h *afterFirstBatch) OnCommit(context.Context, meta.Tx, items.CommitEvent) error { return nil }

func (h *afterFirstBatch) OnItemsRemoved(_ context.Context, tx meta.Tx, _ items.RemovedEvent) error {
	h.calls.Add(1)
	h.once.Do(func() { tx.After(func() { h.fn(); h.ran.Store(true) }) })
	return nil
}

// jobEnv is a repository bulk with files under tree/ on a web replica, a deleter whose policy selects repositories by the pattern
// sel, and the delete of tree queued as a job; the worker is started by the test.
func jobEnv(t *testing.T, sel string, n int) (*env, *replica, func(*config.Storage)) {
	t.Helper()
	e := newEnv(t)
	e.breakGlass()
	small := func(c *config.Storage) { c.Delete.MaxSyncItems, c.Delete.BatchSize = 5, 2 }
	web := e.newReplica("web", replicaOpts{storage: small})
	web.createRepository("bulk", nil)
	e.user("deleter")
	e.grant(web, "bulk-del", []string{sel}, authz.PatternLists{}, map[string][]string{"user:deleter": {"read", "delete"}})
	for i := range n {
		if res := web.put("admin", fmt.Sprintf("/files/it/bulk/tree/d%d/f%d.bin", i%3, i), fmt.Sprint(i)); res.status != http.StatusCreated {
			t.Fatalf("%d %s", res.status, res.body)
		}
	}
	waitFor(t, "the deleter's grant", 10*time.Second, func() bool {
		return web.get("deleter", "/files/it/bulk/tree/d0/f0.bin").status == http.StatusOK
	})
	if del := web.do("deleter", http.MethodDelete, "/files/it/bulk/tree", nil); del.status != http.StatusAccepted {
		t.Fatalf("%d %s", del.status, del.body)
	}
	return e, web, small
}

// settled waits for the delete job to end and returns "state attempt error".
func settled(t *testing.T, e *env) string {
	t.Helper()
	waitFor(t, "the job to settle", 60*time.Second, func() bool {
		return e.number("SELECT count(*) FROM job WHERE kind = 'items-delete' AND state IN ('succeeded', 'failed', 'cancelled')") > 0
	})
	return e.text("SELECT state::text || ' ' || attempt || ' ' || COALESCE(error::text, '') FROM job WHERE kind = 'items-delete'")
}

// filesOf counts the files of the repository <projectKey>/bulk.
func filesOf(e *env, projectKey string) int64 {
	return e.number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id JOIN project p ON p.id = r.project_id
		WHERE p.key = $1 AND r.key = 'bulk' AND i.kind = 'file'`, projectKey)
}

// moveBulk moves it/bulk to the project other through the project service, as the operation of R-REPO-060 does, creates a
// repository it/bulk anew with one file, and waits until the worker's snapshot holds the moved repository under its new name.
func moveBulk(t *testing.T, e *env, web, worker *replica) {
	t.Helper()
	ctx := context.Background()
	id := e.number("SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = 'it' AND r.key = 'bulk'")
	svc := project.New(project.Options{DB: worker.db, Changes: worker.changes, Principals: sqlPrincipals{e}, Repositories: worker.reader, Policies: worker.policy, Recorder: worker.rec})
	if _, _, err := svc.MoveRepository(ctx, e.actor(), qname.Repo{Project: "it", Key: "bulk"}, "other", false); err != nil {
		t.Errorf("move: %v", err)
		return
	}
	if res := web.postJSON("admin", "/api/v1/projects/it/repositories", map[string]any{"key": "bulk", "class": "local", "format": "generic"}); res.status != http.StatusCreated {
		t.Errorf("create the new it/bulk: %d %s", res.status, res.body)
		return
	}
	if res := web.put("admin", "/files/it/bulk/tree/new.bin", "new"); res.status != http.StatusCreated {
		t.Errorf("the file of the new repository: %d %s", res.status, res.body)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if repo, err := worker.reader.ByID(ctx, id); err == nil && repo.Name.Project == "other" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the worker's snapshot does not hold the moved repository")
}

// TestR_STOR_018_TheDeleteJobFollowsItsRepositoryByID: the job holds its repository by ID (R-REPO-060). A repository moved to
// another project between two batches is decided where it is now, and a repository created under the old name is never touched;
// a repository deleted between two batches ends the job as done (Q-STOR-191).
func TestR_STOR_018_TheDeleteJobFollowsItsRepositoryByID(t *testing.T) {
	t.Run("R-STOR-018 a moved repository the caller may not delete in where it is now stops the job, and the new repository of the old name keeps its file", func(t *testing.T) {
		e, web, small := jobEnv(t, "it/*", 6)
		e.exec("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'other', 'other')")
		var worker *replica
		ready := make(chan struct{})
		hook := &afterFirstBatch{fn: func() { <-ready; moveBulk(t, e, web, worker) }}
		worker = e.newReplica("worker", replicaOpts{worker: true, storage: small, hooks: hook})
		close(ready)
		job := settled(t, e)
		if !strings.HasPrefix(job, "failed 1 ") || !strings.Contains(job, "not permitted on other/bulk/tree.") {
			t.Errorf("the job must stop on the moved repository, which the caller's grant no longer reaches: %s", job)
		}
		if moved := filesOf(e, "other"); moved != 4 {
			t.Errorf("%d files left in the moved repository, want the 4 the batch after the move did not remove", moved)
		}
		if fresh := filesOf(e, "it"); fresh != 1 {
			t.Errorf("the repository created under the old name has %d files, want its 1", fresh)
		}
	})

	t.Run("R-STOR-018 a moved repository the caller may still delete in is finished, and the new repository of the old name keeps its file", func(t *testing.T) {
		e, web, small := jobEnv(t, "*/bulk", 6)
		e.exec("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'other', 'other')")
		var worker *replica
		ready := make(chan struct{})
		hook := &afterFirstBatch{fn: func() { <-ready; moveBulk(t, e, web, worker) }}
		worker = e.newReplica("worker", replicaOpts{worker: true, storage: small, hooks: hook})
		close(ready)
		if job := settled(t, e); !strings.HasPrefix(job, "succeeded 1") {
			t.Errorf("the job must finish in the moved repository at its first attempt: %s", job)
		}
		if moved := filesOf(e, "other"); moved != 0 {
			t.Errorf("%d files left in the moved repository", moved)
		}
		if fresh := filesOf(e, "it"); fresh != 1 {
			t.Errorf("the repository created under the old name has %d files, want its 1; hook calls %d, ran %v; items: %s; repositories: %s", fresh,
				hook.calls.Load(), hook.ran.Load(),
				e.text(`SELECT COALESCE(string_agg(i.repo_id || ':' || i.path || ':' || i.id, ' '), '') FROM items i`),
				e.text(`SELECT COALESCE(string_agg(r.id || ':' || p.key || '/' || r.key || ':' || r.lifecycle::text, ' '), '') FROM repositories r JOIN project p ON p.id = r.project_id`))
		}
		if n := e.number(`SELECT count(*) FROM job WHERE kind = 'items-delete' AND state = 'succeeded'`); n != 1 {
			t.Errorf("%d succeeded delete jobs", n)
		}
	})

	t.Run("R-STOR-018 a repository deleted between two batches ends the job as done at its first attempt", func(t *testing.T) {
		e, web, small := jobEnv(t, "it/bulk", 6)
		var worker *replica
		ready := make(chan struct{})
		hook := &afterFirstBatch{fn: func() {
			<-ready
			if res := web.do("admin", http.MethodDelete, "/api/v1/projects/it/repositories/bulk", nil); res.status != http.StatusAccepted && res.status != http.StatusNoContent {
				t.Errorf("delete the repository: %d %s", res.status, res.body)
				return
			}
			id := e.number("SELECT id FROM repositories WHERE key = 'bulk'")
			for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				repo, err := worker.reader.ByID(context.Background(), id)
				if errs.KindOf(err) == errs.NotFound || (err == nil && repo.Lifecycle == repository.Deleting) {
					return
				}
			}
			t.Errorf("the worker's snapshot does not see the repository being deleted")
		}}
		worker = e.newReplica("worker", replicaOpts{worker: true, storage: small, hooks: hook})
		close(ready)
		if job := settled(t, e); !strings.HasPrefix(job, "succeeded 1") {
			t.Errorf("the job must end as done: %s", job)
		}
	})
}

// ownershipSeam is the format seam of a generic repository whose removal check refuses from its refuseFrom-th call on, as an owner
// change of a package format would (R-AUTHZ-018).
type ownershipSeam struct {
	calls      atomic.Int64
	refuseFrom int64
}

func (s *ownershipSeam) Classify(repository.Repository, repository.Cleaned) files.PathClass {
	return files.PathClass{}
}

func (s *ownershipSeam) ServeCheck(context.Context, repository.Repository, items.Item) error {
	return nil
}

func (s *ownershipSeam) ServeFactsKey(repository.Repository, string) servefacts.Key {
	return servefacts.Key{}
}

func (s *ownershipSeam) RawDeploy(repository.Repository) error { return nil }

func (s *ownershipSeam) CheckRemoval(context.Context, meta.Tx, files.Removal) error {
	if s.refuseFrom > 0 && s.calls.Add(1) >= s.refuseFrom {
		return errs.E(errs.Forbidden, "package_owned_by_other_owner", "The package is owned by another owner.")
	}
	return nil
}

// TestR_STOR_018_EveryBatchOfTheDeleteJobAsksTheFormatAndDecidesItsFolders: the job asks the format about the files of every batch
// in the batch's transaction (R-AUTHZ-018, Q-STOR-190) and decides its folders before removing them; a refusal of either ends the
// job at its first attempt, and what the earlier batches removed stays removed.
func TestR_STOR_018_EveryBatchOfTheDeleteJobAsksTheFormatAndDecidesItsFolders(t *testing.T) {
	t.Run("R-STOR-018 an ownership refusal of the second batch stops the job, the first batch stays removed", func(t *testing.T) {
		e := newEnv(t)
		e.breakGlass()
		small := func(c *config.Storage) { c.Delete.MaxSyncItems, c.Delete.BatchSize = 5, 2 }
		web := e.newReplica("web", replicaOpts{storage: small, formats: &ownershipSeam{}})
		web.createRepository("bulk", nil)
		for i := range 6 {
			if res := web.put("admin", fmt.Sprintf("/files/it/bulk/tree/f%d.bin", i), fmt.Sprint(i)); res.status != http.StatusCreated {
				t.Fatalf("%d %s", res.status, res.body)
			}
		}
		if del := web.do("admin", http.MethodDelete, "/files/it/bulk/tree", nil); del.status != http.StatusAccepted {
			t.Fatalf("%d %s", del.status, del.body)
		}
		seam := &ownershipSeam{refuseFrom: 2}
		_ = e.newReplica("worker", replicaOpts{worker: true, storage: small, formats: seam})
		job := settled(t, e)
		if !strings.HasPrefix(job, "failed 1 ") || !strings.Contains(job, "package_owned_by_other_owner") {
			t.Errorf("the job must stop at its first attempt with the format's refusal: %s", job)
		}
		if left := filesOf(e, "it"); left != 4 {
			t.Errorf("%d files left, want the 4 after the first batch", left)
		}
		if n := seam.calls.Load(); n != 2 {
			t.Errorf("the format was asked %d times by the job, want once per batch up to the refusal (2)", n)
		}
	})

	t.Run("R-STOR-018 a folder batch is decided before its folders go, and a refusal keeps them", func(t *testing.T) {
		e, _, small := jobEnv(t, "it/bulk", 6)
		var folderChecks atomic.Int64
		refuseFolders := func(inner items.JobGuard) items.JobGuard {
			return func(ctx context.Context, a items.DeleteArgs) (items.BatchGuard, error) {
				g, err := inner(ctx, a)
				if err != nil {
					return g, err
				}
				check := g.Check
				g.Check = func(ctx context.Context, paths []string) error {
					for _, p := range paths {
						if !strings.HasSuffix(p, ".bin") {
							folderChecks.Add(1)
							return errs.E(errs.Forbidden, errs.CodeForbidden, "The folder "+p+" may not be deleted.")
						}
					}
					return check(ctx, paths)
				}
				return g, nil
			}
		}
		_ = e.newReplica("worker", replicaOpts{worker: true, storage: small, guard: refuseFolders})
		job := settled(t, e)
		if !strings.HasPrefix(job, "failed 1 ") || folderChecks.Load() == 0 {
			t.Errorf("the job must stop at the folder batch: %s (folder checks %d)", job, folderChecks.Load())
		}
		if left := filesOf(e, "it"); left != 0 {
			t.Errorf("%d files left, want the files gone before the folders", left)
		}
		if folders := e.number("SELECT count(*) FROM items WHERE path LIKE 'tree%' AND kind = 'folder'"); folders == 0 {
			t.Error("the folders went although their batch was refused")
		}
	})
}
