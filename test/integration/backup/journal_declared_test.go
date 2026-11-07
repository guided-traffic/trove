//go:build integration

package backup

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cache"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/journal"
)

// TestR_AUTHZ_010_DeclaredBindingRemovedByReplayIsNotBroughtBack: a restore deployed with an older configuration declares a
// binding that the journal removed after the restore point. trove migrate (ApplyDeclared) must not re-create it, names it as
// suppressed, and takes it up again once an administrator binds it through the API (Q-AUTHZ-235).
func TestR_AUTHZ_010_DeclaredBindingRemovedByReplayIsNotBroughtBack(t *testing.T) {
	e := newJEnv(t)
	ctx := context.Background()
	e.stack.User("pa", "platform-admins")
	e.stack.User("keeper", "keepers")
	gid := e.principalID("group", "platform-admins")
	keep := e.principalID("group", "keepers")
	// The restored database holds both bindings; the configuration of the restore declares both.
	e.stack.Exec("INSERT INTO authz_role_binding (role, principal_id, source) VALUES ('admin', $1::uuid, 'configuration'), ('admin', $2::uuid, 'configuration')", gid, keep)
	declared := []config.AuthzRoleBinding{{Role: "admin", Group: "platform-admins"}, {Role: "admin", Group: "keepers"}}
	svc := authz.NewService(authz.ServiceOptions{DB: e.db, Changes: e.changes, Recorder: e.rec})
	reg := prometheus.NewRegistry()
	caches, err := cache.NewRegistry(e.changes, reg)
	if err != nil {
		t.Fatal(err)
	}
	pm, err := principal.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	api := authz.NewService(authz.ServiceOptions{
		DB: e.db, Changes: e.changes, Recorder: e.rec,
		Principals: principal.NewStore(principal.StoreOptions{DB: e.db, Cache: caches, Gate: principal.Gate{Bound: time.Second, Metrics: pm}, SafetyTTL: time.Second}),
	})
	groups := principal.NewManager(principal.ManagerOptions{DB: e.db, Notifier: e.changes, Recorder: e.rec, BreakGlassName: "breakglass"})

	_, out := e.replayEvents(ev{"authz.role.unbound", fmt.Sprintf(`{"role":"admin","principalId":%q}`, gid)})
	e.want(out, journal.Applied)
	has := func(id string) bool {
		return e.count("SELECT count(*)::text FROM authz_role_binding WHERE role = 'admin' AND principal_id = $1::uuid", id) == "1"
	}
	if has(gid) {
		t.Fatal("the replay must remove the binding")
	}

	res, err := svc.ApplyDeclared(ctx, declared, groups)
	if err != nil {
		t.Fatal(err)
	}
	if has(gid) {
		t.Fatal("trove migrate brought back a binding the replay removed")
	}
	if len(res.Suppressed) != 1 || !has(keep) {
		t.Fatalf("outcome = %+v; want the removed binding suppressed and the other kept", res)
	}
	if res, err = svc.ApplyDeclared(ctx, declared, groups); err != nil || has(gid) || len(res.Suppressed) != 1 {
		t.Fatalf("a second migrate = %+v, %v; the suppression must hold until confirmed", res, err)
	}

	// The administrator confirms by binding it through the API; the mark goes and the configuration keeps the binding.
	created, _, err := api.BindRole(ctx, authz.Subject{}, authz.RoleAdmin, "group:platform-admins")
	if err != nil || !created || !has(gid) {
		t.Fatalf("BindRole = %v, %v", created, err)
	}
	if res, err = svc.ApplyDeclared(ctx, declared, groups); err != nil || len(res.Suppressed) != 0 {
		t.Fatalf("after the confirmation = %+v, %v", res, err)
	}
	if src := e.count("SELECT source FROM authz_role_binding WHERE role = 'admin' AND principal_id = $1::uuid", gid); src != "configuration" {
		t.Errorf("the configuration must keep the confirmed binding, source %q", src)
	}
	if n := e.count("SELECT count(*)::text FROM system_state s, jsonb_array_elements_text(s.value) WHERE s.key = 'journal_suppressed_bindings'"); n != "0" {
		t.Errorf("%s marks remain", n)
	}
}
