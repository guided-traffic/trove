//go:build integration

package ci

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

func TestR_CI_030_ADueIssuerIsFetchedExactlyOnceByThreeWorkers(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.worker("worker-1")
	e.worker("worker-2")
	e.worker("worker-3")

	// An issuer without a row is due; so is one whose next_refresh_at lies in the past.
	wantStatus(t, e.do(a, "POST", issuersPath, d.body("gitlab-main", map[string]any{"validate": false})), 201, "")
	wantStatus(t, e.do(a, "POST", issuersPath, startDouble(t, oidcstub.Config{}).body("gitlab-off", map[string]any{"validate": false, "enabled": false})), 201, "")
	e.waitIdle("ci-keys-refresh")
	d.stub.Reset()
	e.runTask(t, a, "ci-keys-refresh")
	if c := d.stub.Counts(); c.Discovery != 1 || c.JWKS != 1 {
		t.Errorf("one due issuer was fetched %+v times, want exactly once", c)
	}
	if n := e.number("SELECT count(*) FROM ci_issuer_keys k JOIN ci_issuers i ON i.id = k.issuer_id WHERE i.name = 'gitlab-off'"); n != 0 {
		t.Error("a disabled issuer was fetched")
	}
	if e.events("ci.keys.refreshed") != 1 {
		t.Errorf("%d refreshed events", e.events("ci.keys.refreshed"))
	}

	// Idempotent and not due again: a second run fetches nothing until next_refresh_at has passed.
	d.stub.Reset()
	e.runTask(t, a, "ci-keys-refresh")
	if c := d.stub.Counts(); c.JWKS != 0 {
		t.Errorf("an issuer that is not due was fetched: %+v", c)
	}
	e.exec("UPDATE ci_issuer_keys SET next_refresh_at = now() - interval '1 minute'")
	e.runTask(t, a, "ci-keys-refresh")
	if c := d.stub.Counts(); c.JWKS != 1 {
		t.Errorf("a due issuer was fetched %d times, want once", c.JWKS)
	}
}

func TestR_CI_030_AFailureDoesNotStopTheRunForTheOthers(t *testing.T) {
	good, bad := startDouble(t, oidcstub.Config{}), startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	e.worker("worker-1")
	wantStatus(t, e.do(a, "POST", issuersPath, bad.body("gitlab-bad", map[string]any{"validate": false})), 201, "")
	wantStatus(t, e.do(a, "POST", issuersPath, good.body("gitlab-good", map[string]any{"validate": false})), 201, "")
	if err := bad.stub.SetFault(oidcstub.Fault{Status: 503}); err != nil {
		t.Fatal(err)
	}
	e.waitIdle("ci-keys-refresh")
	e.runTask(t, a, "ci-keys-refresh")
	if n := e.number("SELECT jsonb_array_length(jwks -> 'keys') FROM ci_issuer_keys k JOIN ci_issuers i ON i.id = k.issuer_id WHERE i.name = 'gitlab-good'"); n != gitlabKeys {
		t.Errorf("the healthy issuer has %d keys", n)
	}
	k := e.do(a, "GET", issuersPath+"/gitlab-bad/keys", nil).JSON(t)
	if le, _ := k["lastError"].(string); le != "http_status status=503 host=127.0.0.1" {
		t.Errorf("lastError = %q", le)
	}
	if e.number("SELECT (next_refresh_at > now() + interval '4 minutes')::int FROM ci_issuer_keys k JOIN ci_issuers i ON i.id = k.issuer_id WHERE i.name = 'gitlab-bad'") != 1 {
		t.Error("the next attempt after a failure is not due after min_refresh_interval")
	}
}

// succeeded counts the finished runs of a task's job kind.
func (e *env) succeeded(kind string) int64 {
	e.t.Helper()
	return e.number("SELECT count(*) FROM job WHERE kind = $1 AND state = 'succeeded'", kind)
}

// waitIdle waits until no run of the task is queued or running: a worker enqueues the scheduled runs of
// its tasks itself, and the exclusive mode refuses a second run while one is open.
func (e *env) waitIdle(kind string) {
	e.t.Helper()
	within(e.t, "no open run of "+kind, 30*time.Second, func() bool {
		return e.number("SELECT count(*) FROM job WHERE kind = $1 AND state IN ('queued', 'running')", kind) == 0
	})
}

// runTask starts a run through the API and waits until it has succeeded.
func (e *env) runTask(t *testing.T, r *testenv.Replica, name string) {
	t.Helper()
	e.waitIdle(name)
	done := e.succeeded(name)
	wantStatus(t, e.do(r, "POST", "/api/v1/system/tasks/"+name+"/run", nil), 202, "")
	within(t, "the run of "+name+" ending", 30*time.Second, func() bool { return e.succeeded(name) > done })
}
