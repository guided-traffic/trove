//go:build integration

// The harness environment of the suites that go through the production composition
// root. Package repository tests the repository model against the integration stack:
// create, read, update, list and delete with their audit events and change-log
// rows, the per-replica snapshot with its change-log invalidation and the lookup of
// a key the snapshot does not hold, each with several in-process replicas that
// have connection pools of their own and no Valkey (R-REPO-049: the bounds hold
// without it). Every test creates a database of its own, so parallel tests and
// agents never collide.
//
// The replicas are the production composition root started through the harness
// (test/integration/testenv): the service, the reader, the exposure and settings
// hooks, the audit recorder and the deletion job are the ones a deployment runs.
// Audit events are read from the audit table, where the recorder writes them in
// the transaction of the change. The one component built next to the replicas is
// the counting reader of the snapshot tests (probe.go): it counts the
// transactions the reader starts, which no replica exposes.
package repository

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// stackEnv is one test's installation on the harness.
type stackEnv struct {
	t     *testing.T
	stack *testenv.Stack
}

// newStackEnv creates a database trove_repo_<random> and migrates it. No replica
// has Valkey: every bound of this suite holds without it (R-REPO-049).
func newStackEnv(t *testing.T) *stackEnv {
	t.Helper()
	return &stackEnv{t: t, stack: testenv.NewStack(t, testenv.StackOptions{Name: "repo"})}
}

func (e *stackEnv) exec(sql string, args ...any) { e.t.Helper(); e.stack.Exec(sql, args...) }

func (e *stackEnv) scalar(sql string, args ...any) int64 {
	e.t.Helper()
	return e.stack.Number(sql, args...)
}

type stackOpts struct {
	role        string
	deleteBatch int
	poll        time.Duration
}

// stackReplica is one in-process replica started through the harness.
type stackReplica struct {
	*testenv.Replica
	svc    *repository.Service
	reader repository.Reader
}

func (e *stackEnv) newReplica(name string, o stackOpts) *stackReplica {
	t := e.t
	t.Helper()
	if o.poll == 0 {
		o.poll = 200 * time.Millisecond
	}
	env := []string{
		"TROVE_CLUSTER_EPOCH_POLL_INTERVAL=" + o.poll.String(),
		"TROVE_JOBS_POLL_INTERVAL=200ms",
	}
	if o.deleteBatch > 0 {
		env = append(env, "TROVE_STORAGE_DELETE_BATCH_SIZE="+strconv.Itoa(o.deleteBatch))
	}
	h := e.stack.Start(t, testenv.ReplicaOptions{Name: name, Role: o.role, Env: env})
	return &stackReplica{Replica: h, svc: h.App.RepositoryService(), reader: h.App.Repositories()}
}

func wantCode(t *testing.T, err error, status int, code string) *errs.Error {
	t.Helper()
	e, ok := errs.As(err)
	if !ok || e.Status() != status || e.Code != code {
		t.Fatalf("err = %v; want %d %s", err, status, code)
	}
	return e
}

// event is one audit event of the repository area as the audit table holds it.
type event struct {
	Type    string
	Actor   string
	Kind    string
	Subject string
	Details string
}

// events returns the repository.* events a replica recorded, oldest first. The
// recorder writes them in the transaction of the change, so they are there when
// the call returned.
func (e *stackEnv) events(replica string) []event {
	e.t.Helper()
	return e.eventsLike(replica, "repository.%")
}

func (e *stackEnv) eventsLike(replica, action string) []event {
	e.t.Helper()
	var out []event
	e.stack.Query(`SELECT action, COALESCE(actor_name, ''), COALESCE(resource_type, ''), COALESCE(repository, ''), details::text
		   FROM audit_event WHERE replica = $1 AND action LIKE $2 ORDER BY occurred_at, id`,
		func(r pgx.Rows) error {
			var ev event
			if err := r.Scan(&ev.Type, &ev.Actor, &ev.Kind, &ev.Subject, &ev.Details); err != nil {
				return err
			}
			out = append(out, ev)
			return nil
		}, replica, action)
	return out
}

func types(evs []event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

// failAudit makes the audit table refuse the insert of an event, to show that a
// change never exists without its audit record (R-OPS-015): the recorder's insert
// is part of the change's transaction, so a refusal rolls the change back. action
// limits it to one event type, "" refuses every event. The returned function lifts it.
func (e *stackEnv) failAudit(action string) (lift func()) {
	e.t.Helper()
	when := ""
	if action != "" {
		when = " WHEN (NEW.action = " + quote(action) + ")"
	}
	e.exec(`CREATE OR REPLACE FUNCTION trove.refuse_audit() RETURNS trigger LANGUAGE plpgsql AS
		$$ BEGIN RAISE EXCEPTION 'audit store unavailable'; END $$`)
	e.exec("CREATE TRIGGER refuse_audit BEFORE INSERT ON trove.audit_event FOR EACH ROW" + when + " EXECUTE FUNCTION trove.refuse_audit()")
	return func() { e.exec("DROP TRIGGER IF EXISTS refuse_audit ON trove.audit_event") }
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return "'" + string(b[1:len(b)-1]) + "'"
}
