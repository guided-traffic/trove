//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestDeleteTwice has no worker, so the job stays queued and the second delete
// meets it.
func TestDeleteTwice(t *testing.T) {
	e := newEnv(t)
	web := e.newReplica("twice-web", replicaOpts{})
	ctx := context.Background()
	if _, err := web.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "doomed"))); err != nil {
		t.Fatal(err)
	}
	first, err := web.svc.Delete(ctx, caller, "doomed", false)
	if err != nil || first.JobID == "" || first.Done {
		t.Fatalf("first delete = %+v, %v", first, err)
	}
	t.Run("R-REPO-013 from the first transaction on the repository is deleting and its key is taken", func(t *testing.T) {
		got, err := web.svc.Get(ctx, "doomed")
		if err != nil || got.Repository.Lifecycle != repository.Deleting || got.Repository.Version != 2 {
			t.Fatalf("Get = %+v, %v", got.Repository, err)
		}
		_, err = web.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "doomed")))
		wantCode(t, err, 409, errs.CodeRepositoryExists)
		if r, err := web.reader.ByKey(ctx, "doomed"); err != nil || r.Lifecycle != repository.Deleting {
			t.Fatalf("ByKey = %+v, %v", r, err)
		}
	})
	t.Run("R-REPO-013 a repeated delete while the job runs answers with the same job", func(t *testing.T) {
		for _, detach := range []bool{false, true} {
			again, err := web.svc.Delete(ctx, caller, "doomed", detach)
			if err != nil || again.JobID != first.JobID || again.Done {
				t.Fatalf("again = %+v, %v; first job %s", again, err, first.JobID)
			}
		}
		if e.scalar(`SELECT count(*) FROM job WHERE kind = 'repository-delete'`) != 1 {
			t.Fatal("a second job was queued")
		}
		if got := web.rec.types(); len(got) != 2 { // created, deleted(started): the repeats record nothing
			t.Fatalf("events = %v", got)
		}
	})
}
