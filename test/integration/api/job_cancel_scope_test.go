//go:build integration

package api

import (
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_API_015_ANarrowedTokenCancelsItsOwnersJobsOnlyWithSettingsWrite is the starter's way of R-API-015 on the running
// composition root (Q-API-215): a job records the principal that started it, not the credential, so the principal cancels
// it through a session or a token that inherits its rights; a token whose scope does not inherit them is refused with 403
// and the job keeps running, unless the scope holds settings:write. Reading the job stays open to it.
func TestR_API_015_ANarrowedTokenCancelsItsOwnersJobsOnlyWithSettingsWrite(t *testing.T) {
	e := newEnv(t, true)
	nora, owen := e.stack.User("nora"), e.stack.User("owen")
	e.stack.Bind("admin", owen)
	r := e.stack.Start(t, testenv.ReplicaOptions{Name: "job-cancel", Role: "api"})
	// A kind without a handler: the job stays queued until it is cancelled.
	job := func(createdBy testenv.Person) string {
		id := uuidV4()
		e.exec("INSERT INTO trove.job (id, kind, queue, created_by) VALUES ($1, 'probe', 'maintenance', $2::uuid)", id, createdBy.ID)
		return id
	}
	state := func(id string) string { return e.stack.Text("SELECT state::text FROM trove.job WHERE id = $1", id) }
	cancel := func(id, token string, hdr ...string) testenv.Answer {
		return e.stack.Do(r, "POST", "/api/v1/system/jobs/"+id+"/cancel", token, nil, hdr...)
	}

	t.Run("R-API-015 the starter's narrowed token is refused and the job stays queued; it still reads the job", func(t *testing.T) {
		id := job(nora)
		for name, sc := range narrowScopes {
			tok := e.stack.Token(nora, sc)
			if got := cancel(id, tok); got.Status != http.StatusForbidden || got.Code(t) != "forbidden" {
				t.Errorf("%s: %d %s, want 403 forbidden", name, got.Status, got.Body)
			}
			if got := e.stack.Do(r, "GET", "/api/v1/system/jobs/"+id, tok, nil); got.Status != http.StatusOK {
				t.Errorf("%s reads the job: %d %s", name, got.Status, got.Body)
			}
		}
		if s := state(id); s != "queued" {
			t.Fatalf("the job is %s after the refusals", s)
		}
	})

	t.Run("R-API-015 the starter's inheriting token cancels", func(t *testing.T) {
		id := job(nora)
		if got := cancel(id, e.stack.Token(nora, scope.Default())); got.Status != http.StatusAccepted {
			t.Fatalf("%d %s", got.Status, got.Body)
		}
		if s := state(id); s != "cancelled" {
			t.Fatalf("the job is %s", s)
		}
	})

	t.Run("R-API-015 the starter's session cancels", func(t *testing.T) {
		id := job(nora)
		_, browser := e.browserSession(nora)
		if got := cancel(id, "", browser...); got.Status != http.StatusAccepted {
			t.Fatalf("%d %s", got.Status, got.Body)
		}
	})

	t.Run("R-API-015 a narrowed token whose scope holds settings:write cancels as any holder", func(t *testing.T) {
		own, others := job(owen), job(nora)
		// settings:read lets it see another principal's job at all; settings:write lets it cancel.
		tok := e.stack.Token(owen, scope.Scope{System: []string{"settings:read", "settings:write"}})
		for _, id := range []string{own, others} {
			if got := cancel(id, tok); got.Status != http.StatusAccepted {
				t.Fatalf("%s: %d %s", id, got.Status, got.Body)
			}
		}
	})
}
