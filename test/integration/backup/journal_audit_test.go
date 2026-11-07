//go:build integration

package backup

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
)

func (e *jenv) declaredServices() (*authz.Service, *principal.Manager) {
	e.t.Helper()
	reg := prometheus.NewRegistry()
	caches, err := cache.NewRegistry(e.changes, reg)
	if err != nil {
		e.t.Fatal(err)
	}
	pm, err := principal.NewMetrics(reg)
	if err != nil {
		e.t.Fatal(err)
	}
	svc := authz.NewService(authz.ServiceOptions{
		DB: e.db, Changes: e.changes, Recorder: e.rec,
		Principals: principal.NewStore(principal.StoreOptions{DB: e.db, Cache: caches, Gate: principal.Gate{Bound: time.Second, Metrics: pm}, SafetyTTL: time.Second}),
	})
	return svc, principal.NewManager(principal.ManagerOptions{DB: e.db, Notifier: e.changes, Recorder: e.rec, BreakGlassName: "breakglass"})
}

// TestR_AUTHZ_010_MarksCoverABindingAddedAndRemovedAfterTheRestorePoint: a declared binding that was bound and unbound after
// the restore point is absent from the snapshot, so the replay's delete finds nothing; the mark is written all the same, and
// trove migrate does not bind it (audit finding M4).
func TestR_AUTHZ_010_MarksCoverABindingAddedAndRemovedAfterTheRestorePoint(t *testing.T) {
	e := newJEnv(t)
	e.stack.User("m", "newcomers")
	gid := e.principalID("group", "newcomers")
	svc, groups := e.declaredServices()
	_, out := e.replayEvents(ev{"authz.role.unbound", fmt.Sprintf(`{"role":"admin","principalId":%q,"principal":"group:newcomers"}`, gid)})
	e.want(out, journal.AlreadyInEffect)
	res, err := svc.ApplyDeclared(t.Context(), []config.AuthzRoleBinding{{Role: "admin", Group: "newcomers"}}, groups)
	if err != nil || len(res.Suppressed) != 1 {
		t.Fatalf("ApplyDeclared = %+v, %v; want the binding suppressed", res, err)
	}
	if n := e.count("SELECT count(*)::text FROM authz_role_binding WHERE principal_id = $1::uuid", gid); n != "0" {
		t.Error("trove migrate bound a principal whose unbinding the journal recorded")
	}
}

// TestR_AUTHZ_010_MarksSurviveTheDeletionOfADeclaredGroup: the replay of group.deleted deletes the group and its binding;
// trove migrate creates the declared group again under a new ID and must not bind the role to it (audit finding M4).
func TestR_AUTHZ_010_MarksSurviveTheDeletionOfADeclaredGroup(t *testing.T) {
	e := newJEnv(t)
	e.stack.User("m", "platform-admins")
	gid := e.principalID("group", "platform-admins")
	e.stack.Exec("INSERT INTO authz_role_binding (role, principal_id, source) VALUES ('admin', $1::uuid, 'configuration')", gid)
	svc, groups := e.declaredServices()
	_, out := e.replayEvents(ev{"group.deleted", fmt.Sprintf(`{"principalId":%q,"name":"platform-admins"}`, gid)})
	e.want(out, journal.Applied)
	res, err := svc.ApplyDeclared(t.Context(), []config.AuthzRoleBinding{{Role: "admin", Group: "platform-admins"}}, groups)
	if err != nil || len(res.Suppressed) != 1 {
		t.Fatalf("ApplyDeclared = %+v, %v; want the binding suppressed", res, err)
	}
	if n := e.count("SELECT count(*)::text FROM authz_role_binding b JOIN principal p ON p.id = b.principal_id WHERE p.name = 'platform-admins'"); n != "0" {
		t.Error("the role was bound to the group created again under a new ID")
	}
}

// TestR_AUTHZ_031_AnUnorderedSettingSuspendsInsteadOfPanicking: a setting the replay has no order for cannot be proven
// narrowing and has no most restrictive value; the entry is suspended and names it, not skipped by a recovered panic (audit
// finding L1).
func TestR_AUTHZ_031_AnUnorderedSettingSuspendsInsteadOfPanicking(t *testing.T) {
	e := newJEnv(t)
	e.stack.Repository("npmjs", "remote", "npm", `{"remote":{"cacheFillAction":"read"}}`)
	repo := e.repoID("npmjs")
	entry := fmt.Sprintf(`{"repositoryId":%q,"before":{"weird":"b","cacheFillAction":"read"},"after":{"weird":"a","cacheFillAction":"read"}}`, repo)
	sum, out := e.replayEvents(ev{"authz.settings.changed", entry})
	e.want(out, journal.Suspended)
	if !strings.Contains(string(sum.Results[0].Detail), "weird") {
		t.Errorf("the output must name the unordered setting: %s", sum.Results[0].Detail)
	}
}

// TestR_HA_054_TheReplayStartsAtTheNewestAuditEventWhenTheRestorePointLiesLater: --restored-to later than the restored database
// must not skip the revocations in between (audit finding L2).
func TestR_HA_054_TheReplayStartsAtTheNewestAuditEventWhenTheRestorePointLiesLater(t *testing.T) {
	e := newJEnv(t)
	ctx := context.Background()
	alice := e.stack.User("alice")
	id := e.tokenID(e.stack.Token(alice, scope.Default()))
	if _, err := e.tokens.Revoke(ctx, principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}, id, token.ReasonAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := e.shipper.Ship(ctx); err != nil {
		t.Fatal(err)
	}
	e.unrevoke(id)
	// The operator names a restore point an hour after everything the database holds.
	sum, out := e.replayAt(e.replayer(e.store, nil), time.Now().Add(time.Hour))
	if sum.Counts[journal.Applied] != 1 || !strings.Contains(out, "replaying from it") || !e.revokedAt(id) {
		t.Fatalf("replay = %v\n%s\nwant the revocation replayed from the newest audit event", sum.Counts, out)
	}
}

// TestR_HA_054_APlantedCopyUnderAnotherDayDoesNotHideTheGenuineEntry: the holder of the runtime credential copies the entry of
// a revoked token under the day before the restore point; the genuine entry is still replayed and the copy flagged (audit
// finding H2), against the real object store.
func TestR_HA_054_APlantedCopyUnderAnotherDayDoesNotHideTheGenuineEntry(t *testing.T) {
	e := newJEnv(t)
	alice := e.stack.User("alice")
	tokenID := e.tokenID(e.stack.Token(alice, scope.Default()))
	ctx := context.Background()
	restorePoint := time.Now().Add(-30 * time.Minute)
	entry := fmt.Sprintf(`{"tokenId":%q,"reason":"admin"}`, tokenID)
	genuineAt := time.Now().Add(-time.Minute)
	id := e.put(e.store, genuineAt, "token.revoked", entry)
	// The planted copy: the same ID, a time two days before the restore point, so its key names another day.
	planted := journal.Object{V: journal.Version, ID: id, At: restorePoint.Add(-48 * time.Hour), Event: "token.revoked", Entry: []byte(entry)}
	body, err := planted.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.Put(ctx, journal.Key(e.prefix, planted), body); err != nil {
		t.Fatal(err)
	}
	// The window reaches back over the planted day, as the margin of a long restore would.
	var out stringsBuilder
	sum, err := e.replayer(e.store, nil).Replay(ctx, journal.ReplayOptions{RestoredTo: restorePoint, Margin: 72 * time.Hour, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if !e.revokedAt(tokenID) {
		t.Fatalf("the genuine entry was hidden:\n%s", out.String())
	}
	if len(sum.Warnings) == 0 || !strings.Contains(out.String(), "also under") {
		t.Errorf("the duplicate must be flagged: %v\n%s", sum.Warnings, out.String())
	}
}

// TestR_HA_054_AnUnencodableRowIsQuarantinedInTheDatabase: an outbox row that is no journal entry leaves the queue, stays
// recorded in system_state and does not hold back the next revocation (audit finding L4).
func TestR_HA_054_AnUnencodableRowIsQuarantinedInTheDatabase(t *testing.T) {
	e := newJEnv(t)
	ctx := context.Background()
	e.stack.Exec(`INSERT INTO revocation_outbox (id, at, event, entry) VALUES ('0192f0c4-0000-7000-8000-0000000000aa', now() - interval '1 minute', 'token.revoked', '[1]'::jsonb)`)
	alice := e.stack.User("alice")
	id := e.tokenID(e.stack.Token(alice, scope.Default()))
	if _, err := e.tokens.Revoke(ctx, principal.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}, id, token.ReasonAdmin); err != nil {
		t.Fatal(err)
	}
	var quarantined int
	sh := e.shipperWith(func(journal.OutboxRow, error) { quarantined++ })
	n, err := sh.Ship(ctx)
	if err != nil || n != 1 || quarantined != 1 {
		t.Fatalf("Ship = %d, %v, quarantined %d; want the good row shipped and the bad one set aside", n, err, quarantined)
	}
	if p := pending(t, e); p != 0 {
		t.Errorf("%d rows remain in the outbox", p)
	}
	if got := e.count(`SELECT jsonb_array_length(value)::text FROM system_state WHERE key = 'journal_quarantined'`); got != "1" {
		t.Errorf("journal_quarantined holds %s entries, want 1", got)
	}
}
