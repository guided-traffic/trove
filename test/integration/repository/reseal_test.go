//go:build integration

package repository

import (
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
)

// TestSecretsReencryptTask runs the task on a worker of the harness: the one
// registered sealed column in this process is the repository secrets, so the job
// walks it with repository.ResealSecrets and records what it did (R-ARCH-021
// rotation step 3, R-OPS-019). The remote settings themselves are tested with the
// component environment (remote_test.go: TestRemoteSecrets).
func TestSecretsReencryptTask(t *testing.T) {
	e := newStackEnv(t)
	admin := e.stack.User("reseal-admin")
	e.stack.Bind("admin", admin)
	tok := e.stack.Token(admin, scope.Scope{Inherit: true, Admin: true})
	web := e.newReplica("reseal-web", stackOpts{})
	worker := e.newReplica("reseal-worker", stackOpts{role: "worker"})

	res := e.stack.Do(web.Replica, "POST", "/api/v1/system/tasks/secrets-reencrypt/run", tok, nil)
	if res.Status != 202 {
		t.Fatalf("%d %s", res.Status, res.Body)
	}
	id := res.JSON(t)["job"].(map[string]any)["id"].(string)
	job := e.waitJob(worker.App.Queue(), id)
	if job.State != "succeeded" {
		t.Fatalf("job = %+v", job)
	}
	got := e.stack.Do(web.Replica, "GET", "/api/v1/system/jobs/"+id, tok, nil).JSON(t)["result"].(map[string]any)
	if got["columns"] != float64(1) || got["failed"] != float64(0) || got["resealed"] != float64(0) {
		t.Fatalf("result = %v", got)
	}
}
