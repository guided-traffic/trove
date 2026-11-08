//go:build integration

package transfer

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
)

// The delete job of a large subtree continues the request that queued it, and decides every batch again on what the subject that
// asked holds when the batch begins (R-STOR-018, Q-STOR-190, Q-AUTHZ-260, Q-AUTHZ-261). Most scenarios queue the job on a replica
// that runs no jobs, withdraw or narrow the subject through the management API, wait until a request with the same credential sees
// the change, and only then start the worker: the job's first batch begins after the change, as any later batch would. One revokes
// the token while a running worker is between two batches.

// standingFiles is the number of files of the subtree most scenarios delete: more than storage.delete.max_sync_items (5).
const standingFiles = 14

// queueStandingDelete builds the scenario: a deleter in the group cleaners, whose policy grants read and delete on bulk, queues the
// delete of bulk/tree, which holds n files, on a replica without a worker. It returns the web replica.
func queueStandingDelete(t *testing.T, e *webEnv, n int, env []string) *webReplica {
	t.Helper()
	e.breakGlass()
	web := e.newReplica("web", webOpts{env: env})
	web.createRepository("bulk", nil)
	e.user("deleter", "cleaners")
	e.grant(web, "bulk-clean", []string{"bulk"}, authz.PatternLists{}, map[string][]string{"group:cleaners": {"read", "delete"}})
	for i := range n {
		if res := web.put("admin", fmt.Sprintf("/files/it/bulk/tree/d%d/f%d.bin", i%3, i), fmt.Sprint(i)); res.status != http.StatusCreated {
			t.Fatalf("%d %s", res.status, res.body)
		}
	}
	waitFor(t, "the deleter's grant", 10*time.Second, func() bool {
		return web.get("deleter", "/files/it/bulk/tree/d0/f0.bin").status == http.StatusOK
	})
	if del := web.do("deleter", http.MethodDelete, "/files/it/bulk/tree", nil); del.status != http.StatusAccepted {
		t.Fatalf("the delete is not a job: %d %s", del.status, del.body)
	}
	if n := e.number("SELECT count(*) FROM job WHERE kind = 'items-delete' AND state = 'queued'"); n != 1 {
		t.Fatalf("%d queued delete jobs, want 1", n)
	}
	return web
}

// endedJob waits for the delete job to end and returns its state and error.
func endedJob(t *testing.T, e *webEnv) (state, failure string) {
	t.Helper()
	waitFor(t, "the delete job to end", 120*time.Second, func() bool {
		return e.number("SELECT count(*) FROM job WHERE kind = 'items-delete' AND state IN ('succeeded', 'failed', 'cancelled')") > 0
	})
	state = e.text("SELECT state::text FROM job WHERE kind = 'items-delete'")
	failure = e.text("SELECT COALESCE(error::text, '') FROM job WHERE kind = 'items-delete'")
	return state, failure
}

// runStandingJob starts a worker and returns the state and the error of the delete job once it ended.
func runStandingJob(t *testing.T, e *webEnv) (state, failure string) {
	t.Helper()
	e.newReplica("worker", webOpts{worker: true, env: smallDeletes})
	return endedJob(t, e)
}

// filesLeft counts the files of the subtree.
func filesLeft(e *webEnv) int64 {
	return e.number("SELECT count(*) FROM items WHERE path LIKE 'tree/%' AND kind = 'file'")
}

// wantStopped asserts that the job failed with a reason that names the change, removed nothing, and recorded artifact.delete_stopped
// with the deleter as the actor and the reason code (Q-STOR-193).
func wantStopped(t *testing.T, e *webEnv, state, failure, reason, code string) {
	t.Helper()
	if state != "failed" || !strings.Contains(failure, reason) {
		t.Errorf("the job must fail naming %q: state %s, error %q", reason, state, failure)
	}
	if left := filesLeft(e); left != standingFiles {
		t.Errorf("the job removed %d of %d files after the change", standingFiles-left, standingFiles)
	}
	wantStopEvent(t, e, code, 0)
}

// wantStopEvent waits for the asynchronous artifact.delete_stopped of the job and checks its reason and count.
func wantStopEvent(t *testing.T, e *webEnv, code string, removed int64) {
	t.Helper()
	waitFor(t, "artifact.delete_stopped", 30*time.Second, func() bool {
		return e.number(`SELECT count(*) FROM audit_event WHERE action = 'artifact.delete_stopped' AND actor_name = 'deleter' AND outcome = 'denied'
			AND reason = $1 AND path = 'tree' AND (details->>'removed')::bigint = $2
			AND details->>'job' = (SELECT id::text FROM job WHERE kind = 'items-delete')`, code, removed) == 1
	})
}

func TestR_STOR_018_TheDeleteJobDecidesEveryBatchOnTheStandingOfItsSubject(t *testing.T) {
	// R-AUTH-023: a revocation reaches the job as it reaches a request.
	t.Run("R-STOR-018 a token revoked before the batch stops the job", func(t *testing.T) {
		e := newWebEnv(t)
		web := queueStandingDelete(t, e, standingFiles, smallDeletes)
		h := token.Hash(e.token("deleter"))
		id := e.text("SELECT id::text FROM api_token WHERE secret_sha256 = $1", h[:])
		if res := web.do("admin", http.MethodDelete, "/api/v1/tokens/"+id, nil); res.status != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", res.status, res.body)
		}
		waitFor(t, "the revocation to reach the web replica", 10*time.Second, func() bool {
			return web.get("deleter", "/files/it/bulk/tree/d0/f0.bin").status == http.StatusUnauthorized
		})
		state, failure := runStandingJob(t, e)
		wantStopped(t, e, state, failure, "token that asked for this delete was revoked", "invalid_token")
	})

	// R-AUTH-016: a disable stops the principal's credentials, revoked or not.
	t.Run("R-STOR-018 a principal disabled before the batch stops the job, its tokens left unrevoked", func(t *testing.T) {
		e := newWebEnv(t)
		web := queueStandingDelete(t, e, standingFiles, smallDeletes)
		if res := web.postJSON("admin", "/api/v1/users/deleter/disable", map[string]any{"revokeTokens": false}); res.status != http.StatusNoContent {
			t.Fatalf("disable: %d %s", res.status, res.body)
		}
		if n := e.number("SELECT count(*) FROM api_token t JOIN principal p ON p.id = t.principal_id WHERE p.name = 'deleter' AND t.revoked_at IS NULL"); n != 1 {
			t.Fatalf("the disable revoked the token (%d unrevoked)", n)
		}
		waitFor(t, "the disable to reach the web replica", 10*time.Second, func() bool {
			return web.get("deleter", "/files/it/bulk/tree/d0/f0.bin").status == http.StatusUnauthorized
		})
		state, failure := runStandingJob(t, e)
		wantStopped(t, e, state, failure, "principal that asked for this delete is no longer active", "principal_disabled")
	})

	// R-AUTHZ-002: the groups are resolved when the batch is decided.
	t.Run("R-STOR-018 a group left before the batch takes its grant from the job", func(t *testing.T) {
		e := newWebEnv(t)
		web := queueStandingDelete(t, e, standingFiles, smallDeletes)
		res := web.do("admin", http.MethodPatch, "/api/v1/groups/cleaners/members", strings.NewReader(`{"remove":["deleter"]}`), "Content-Type", "application/json")
		if res.status != http.StatusOK {
			t.Fatalf("remove the member: %d %s", res.status, res.body)
		}
		waitFor(t, "the membership change to reach the web replica", 10*time.Second, func() bool {
			return web.get("deleter", "/files/it/bulk/tree/d0/f0.bin").status == http.StatusNotFound
		})
		state, failure := runStandingJob(t, e)
		wantStopped(t, e, state, failure, "The action delete is not permitted on it/bulk/tree.", "forbidden")
	})

	t.Run("R-STOR-018 a token that expired before the batch does not stop the job", func(t *testing.T) {
		e := newWebEnv(t)
		queueStandingDelete(t, e, standingFiles, smallDeletes)
		h := token.Hash(e.token("deleter"))
		e.exec("UPDATE api_token SET expires_at = now() - interval '1 second' WHERE secret_sha256 = $1", h[:])
		state, failure := runStandingJob(t, e)
		if state != "succeeded" {
			t.Fatalf("an expiry is no withdrawal, the job must finish: %s %s", state, failure)
		}
		if left := e.number("SELECT count(*) FROM items WHERE path LIKE 'tree%'"); left != 0 {
			t.Errorf("%d items left", left)
		}
	})

	// R-AUTH-023: the token row is read by ID for every batch, so the batch after the revocation's commit removes nothing; the batch
	// in flight at that moment finishes (H-658), which with batches of one file is at most one file.
	t.Run("R-STOR-018 a token revoked while a running worker is between two batches stops the job at the next one", func(t *testing.T) {
		const many = 120
		oneByOne := []string{"TROVE_STORAGE_DELETE_MAX_SYNC_ITEMS=5", "TROVE_STORAGE_DELETE_BATCH_SIZE=1"}
		e := newWebEnv(t)
		web := queueStandingDelete(t, e, many, oneByOne)
		h := token.Hash(e.token("deleter"))
		id := e.text("SELECT id::text FROM api_token WHERE secret_sha256 = $1", h[:])
		e.newReplica("worker", webOpts{worker: true, env: oneByOne})
		waitFor(t, "the job's first batch", 120*time.Second, func() bool {
			return e.number("SELECT COALESCE((SELECT progress_done FROM job WHERE kind = 'items-delete' AND state = 'running'), 0)") > 0
		})
		if res := web.do("admin", http.MethodDelete, "/api/v1/tokens/"+id, nil); res.status != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", res.status, res.body)
		}
		removedAtRevoke := many - filesLeft(e)
		if s := e.text("SELECT state::text FROM job WHERE kind = 'items-delete'"); s != "running" || removedAtRevoke >= many {
			t.Fatalf("the job is %s with %d of %d files removed when the revocation committed: the scenario needs it between two batches", s, removedAtRevoke, many)
		}
		state, failure := endedJob(t, e)
		removed := many - filesLeft(e)
		if state != "failed" || !strings.Contains(failure, "token that asked for this delete was revoked") {
			t.Errorf("the job must fail naming the revocation: %s %q", state, failure)
		}
		if removed > removedAtRevoke+1 {
			t.Errorf("%d files removed in all, %d when the revocation had committed: more than the one batch in flight went after it", removed, removedAtRevoke)
		}
		wantStopEvent(t, e, "invalid_token", removed)
	})
}
