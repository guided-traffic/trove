//go:build integration

package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/fixup"
	"github.com/guided-traffic/trove/internal/backup/postrestore"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
)

// TestR_HA_025_PostRestoreRunsTheStepsAndReplaysTheJournal: the clean-up of a platform restore deletes the replica rows, sets
// the deletion pause, ends the sessions, runs the fix-ups of jobs, system state and revisions in one transaction, sets
// restored_at, replays the revocation journal (a token revoked after the restore point is revoked again), writes
// system.restored, and a second run reports the replay as already in effect.
func TestR_HA_025_PostRestoreRunsTheStepsAndReplaysTheJournal(t *testing.T) {
	e := newJEnv(t)
	ctx := context.Background()
	alice := e.stack.User("alice")
	raw := e.stack.Token(alice, scope.Default())
	id := e.tokenID(raw)
	restorePoint := time.Now().Add(-time.Second)
	if _, err := e.tokens.Revoke(ctx, principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}, id, token.ReasonAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := e.shipper.Ship(ctx); err != nil {
		t.Fatal(err)
	}
	e.unrevoke(id)
	e.stack.Exec(`INSERT INTO replica (instance_uid, name, version, revision, roles, state, secret_key_ids, active_secret_key_id, started_at, heartbeat_at)
		VALUES (gen_random_uuid(), 'gone', '0', '0', '{web}', 'ready', '{k}', 'k', now(), now())`)
	e.stack.Exec(`INSERT INTO ui_session (id, secret_sha256, csrf_sha256, principal_id, auth_method, authenticated_at, idle_timeout, absolute_expires_at)
		VALUES (gen_random_uuid(), decode(repeat('01', 32), 'hex'), decode(repeat('02', 32), 'hex'), $1::uuid, 'oidc', now(), interval '1 hour', now() + interval '1 day')`, alice.ID)

	var ran []string
	fx := func(name string, order int) fixup.Fixup {
		return fixup.Fixup{Name: name, Order: order, Apply: func(_ context.Context, _ meta.Tx, env fixup.Env) error {
			if env.NewKeyspaceID == "" || env.NewIncarnation == "" {
				t.Errorf("fix-up %s got no new keyspace or incarnation", name)
			}
			ran = append(ran, name)
			return nil
		}}
	}
	mk := func(fixups []fixup.Fixup) *postrestore.PostRestorer {
		return postrestore.New(postrestore.Options{
			DB: e.db, Fixups: fixups, Recorder: e.rec, Margin: time.Minute, Log: quietLog,
			OpenReplayer: func(context.Context, string) (journal.Replayer, error) { return e.replayer(e.store, e.rec), nil },
		})
	}

	// A build without the fix-ups refuses rather than leave a stale keyspace behind.
	if _, err := mk(nil).PostRestore(ctx, backup.PostRestoreOptions{RestoredTo: restorePoint}); err == nil || !strings.Contains(err.Error(), "no restore fix-up") {
		t.Fatalf("PostRestore without fix-ups = %v, want a refusal", err)
	}
	if e.revokedAt(id) {
		t.Fatal("a refused post-restore must change nothing")
	}

	pr := mk([]fixup.Fixup{fx("jobs", fixup.OrderJobs), fx("system_state", fixup.OrderSystemState), fx("revisions", fixup.OrderRevisions), fx("storage_usage", fixup.OrderStorageUsage)})
	rep, err := pr.PostRestore(ctx, backup.PostRestoreOptions{RestoredTo: restorePoint})
	if err != nil {
		t.Fatalf("PostRestore: %v", err)
	}
	if strings.Join(ran, ",") != "jobs,system_state,revisions" {
		t.Errorf("fix-ups run = %v, want the PITR subset jobs, system_state, revisions", ran)
	}
	text := strings.Join(rep.Steps, "\n")
	for _, want := range []string{"deleted 1 replica rows", "deletion pause set", "ended 1 sessions", "restored_at set", "token.revoked", "applied", "system.restored written", "H-37"} {
		if !strings.Contains(text, want) {
			t.Errorf("the output lacks %q:\n%s", want, text)
		}
	}
	if !e.revokedAt(id) {
		t.Error("the token revoked after the restore point must be revoked after post-restore")
	}
	if n := e.count("SELECT count(*)::text FROM replica"); n != "0" {
		t.Errorf("replica rows = %s, want none", n)
	}
	if v := e.count(`SELECT value #>> '{}' FROM system_state WHERE key = 'deletions_paused_at'`); v == "" || v == "null" {
		t.Error("the deletion pause must be set")
	}
	if v := e.count(`SELECT value #>> '{}' FROM system_state WHERE key = 'restored_at'`); v == "" {
		t.Error("restored_at must be set")
	}
	if n := e.count("SELECT count(*)::text FROM ui_session WHERE ended_at IS NULL"); n != "0" {
		t.Errorf("%s sessions are still open", n)
	}
	if n := e.count("SELECT count(*)::text FROM audit_event WHERE action = 'system.restored'"); n != "1" {
		t.Errorf("system.restored written %s times, want 1", n)
	}

	again, err := pr.PostRestore(ctx, backup.PostRestoreOptions{RestoredTo: restorePoint})
	if err != nil || !strings.Contains(strings.Join(again.Steps, "\n"), "1 already in effect") {
		t.Fatalf("second PostRestore = %v, %v; want the entry already in effect", again, err)
	}

	skipped, err := pr.PostRestore(ctx, backup.PostRestoreOptions{RestoredTo: restorePoint, SkipJournal: true})
	if err != nil || !strings.Contains(strings.Join(skipped.Steps, "\n"), "revocation journal skipped") {
		t.Fatalf("PostRestore --skip-revocation-journal = %v, %v", skipped, err)
	}
}
