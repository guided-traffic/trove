//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
)

// exposureDeleted returns the endpoint.exposure.deleted events the recorder kept.
func (r *recorder) exposureDeleted() []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Event
	for _, ev := range r.events {
		if ev.Type == "endpoint.exposure.deleted" {
			out = append(out, ev)
		}
	}
	return out
}

// TestH164_DeletingARepositoryRecordsItsExposureRules: the foreign key removes
// the rules with the repository row, and the delete hook records each one as
// endpoint.exposure.deleted with the cause repository_deleted, with the journal
// entry, in the transaction that removes the row (R-EP-020, H-164). The deletion
// job (finishDelete) is what runs here; the immediate removal of a virtual
// repository (deleteNow) calls the same hook before it deletes the row.
func TestH164_DeletingARepositoryRecordsItsExposureRules(t *testing.T) {
	e := newEnv(t)
	rec := &recorder{}
	_ = e.newReplica("h164-worker", replicaOpts{role: "worker", poll: 200 * time.Millisecond, rec: rec, exposure: true})
	web := e.newReplica("h164-web", replicaOpts{rec: &recorder{}})
	ctx := context.Background()

	rule := func(repo, endpoint, name string) {
		e.exec(`INSERT INTO endpoint_exposure_rule (id, endpoint, name, repo_id)
		        SELECT gen_random_uuid(), $1, $2, id FROM repositories WHERE key = $3`, endpoint, name, repo)
	}
	check := func(t *testing.T, ev audit.Event, endpoint, name, repo string) {
		t.Helper()
		var d map[string]any
		if err := json.Unmarshal(ev.Details, &d); err != nil {
			t.Fatal(err)
		}
		if d["endpoint"] != endpoint || d["name"] != name || d["repository"] != repo || d["cause"] != "repository_deleted" {
			t.Errorf("details = %v", d)
		}
		if e, _ := d["epoch"].(float64); e < 1 {
			t.Errorf("epoch = %v", d["epoch"])
		}
		if len(ev.Journal) == 0 {
			t.Error("the event carries no journal entry (R-EP-020)")
		}
	}

	t.Run("H-164 the deletion job records the rules of the repository, one event each", func(t *testing.T) {
		if _, err := web.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "doomed-rules"))); err != nil {
			t.Fatal(err)
		}
		rule("doomed-rules", "customers", "rel")
		rule("doomed-rules", "partners", "rel")
		if _, err := web.svc.Delete(ctx, caller, "doomed-rules", false); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the rules gone with the repository", 10*time.Second, func() bool {
			return e.scalar(`SELECT count(*) FROM repositories WHERE key = 'doomed-rules'`) == 0
		})
		if n := e.scalar(`SELECT count(*) FROM endpoint_exposure_rule`); n != 0 {
			t.Fatalf("%d rules left", n)
		}
		waitFor(t, "the events", 5*time.Second, func() bool { return len(rec.exposureDeleted()) == 2 })
		got := rec.exposureDeleted()
		check(t, got[0], "customers", "rel", "doomed-rules")
		check(t, got[1], "partners", "rel", "doomed-rules")
	})

	t.Run("H-164 a repository without rules records no exposure event", func(t *testing.T) {
		before := len(rec.exposureDeleted())
		if _, err := web.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "doomed-plain"))); err != nil {
			t.Fatal(err)
		}
		if _, err := web.svc.Delete(ctx, caller, "doomed-plain", false); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the repository gone", 10*time.Second, func() bool {
			return e.scalar(`SELECT count(*) FROM repositories WHERE key = 'doomed-plain'`) == 0
		})
		if got := len(rec.exposureDeleted()); got != before {
			t.Errorf("%d exposure events, want %d", got, before)
		}
	})

	t.Run("H-164 a failing recorder keeps the repository and its rules (one transaction)", func(t *testing.T) {
		if _, err := web.svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "doomed-atomic"))); err != nil {
			t.Fatal(err)
		}
		rule("doomed-atomic", "customers", "atomic")
		rec.setFail(true)
		if _, err := web.svc.Delete(ctx, caller, "doomed-atomic", false); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second) // the job fails and retries while the recorder refuses
		if e.scalar(`SELECT count(*) FROM repositories WHERE key = 'doomed-atomic'`) != 1 ||
			e.scalar(`SELECT count(*) FROM endpoint_exposure_rule WHERE name = 'atomic'`) != 1 {
			t.Fatal("the row or the rule is gone although its audit record could not be written")
		}
		rec.setFail(false)
		waitFor(t, "the retry to finish", 30*time.Second, func() bool {
			return e.scalar(`SELECT count(*) FROM endpoint_exposure_rule WHERE name = 'atomic'`) == 0
		})
	})
}
