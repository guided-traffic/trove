//go:build integration

package authz

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/test/integration/stack"
)

// The role-binding guards against PostgreSQL (R-AUTHZ-022, R-AUTH-019): the
// last administrator is counted as the users and service accounts that can act
// as one, and the guards hold under concurrent changes on two replicas.

func codeOf(err error) string {
	if e, ok := errs.As(err); ok {
		return e.Code
	}
	return ""
}

func TestR_AUTHZ_022_LastAdminCountsTheHolders(t *testing.T) {
	e := stack.New(t)
	solo := e.User("solo")
	e.Admin(solo)
	nobody := e.Group("nobody")
	e.Admin(nobody)
	a := e.NewReplica("web-a", stack.Options{})
	ctx := context.Background()
	actor := authz.Subject{Kind: solo.Kind, ID: solo.ID, Name: solo.Name}

	// Two admin bindings, one holder: the binding on the empty group keeps nobody
	// in charge.
	_, err := a.Service.UnbindRole(ctx, actor, authz.RoleAdmin, "user:solo")
	if codeOf(err) != errs.CodeLastAdmin {
		t.Fatalf("unbinding the only holder: %v, want 409 last_admin", err)
	}
	if _, err := a.Service.UnbindRole(ctx, actor, authz.RoleAdmin, "group:nobody"); err != nil {
		t.Fatalf("the binding on the empty group holds nobody: %v", err)
	}
}

func TestR_AUTHZ_022_ConcurrentUnbindsLeaveAnAdministrator(t *testing.T) {
	e := stack.New(t)
	x, y := e.User("x"), e.User("y")
	e.Admin(x)
	e.Admin(y)
	a, b := e.NewReplica("web-a", stack.Options{}), e.NewReplica("web-b", stack.Options{})
	ctx := context.Background()
	actor := authz.Subject{Kind: x.Kind, ID: x.ID, Name: x.Name}
	for round := range 20 {
		var wg sync.WaitGroup
		results := make([]error, 2)
		start := make(chan struct{})
		for i, c := range []struct {
			r   *stack.Replica
			ref string
		}{{a, "user:x"}, {b, "user:y"}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, results[i] = c.r.Service.UnbindRole(ctx, actor, authz.RoleAdmin, c.ref)
			}()
		}
		close(start)
		wg.Wait()
		if n := e.Number("SELECT count(*) FROM authz_role_binding WHERE role = 'admin'"); n != 1 {
			t.Fatalf("round %d: %d admin bindings left (%v)", round, n, results)
		}
		for i, err := range results {
			if err != nil && codeOf(err) != errs.CodeLastAdmin {
				t.Fatalf("round %d, unbind %d: %v", round, i, err)
			}
		}
		e.Exec("INSERT INTO authz_role_binding (role, principal_id) VALUES ('admin', $1::uuid), ('admin', $2::uuid) ON CONFLICT DO NOTHING", x.ID, y.ID)
	}
}

func TestR_AUTH_019_ABindingWaitsForAutoJoinOfTheGroup(t *testing.T) {
	e := stack.New(t)
	root := e.User("root")
	e.Admin(root)
	ops := e.Group("ops")
	a := e.NewReplica("web-a", stack.Options{})
	ctx := context.Background()
	// Turning on auto-join, holding the group's lock as the identity area does,
	// not committed yet.
	tx, err := a.DB.ControlPool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT 1 FROM user_group WHERE principal_id = $1::uuid FOR UPDATE", ops.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE user_group SET auto_join = true WHERE principal_id = $1::uuid", ops.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var berr error
	go func() {
		defer close(done)
		_, _, berr = a.Service.BindRole(ctx, authz.Subject{Kind: root.Kind, ID: root.ID, Name: root.Name}, authz.RoleAuditor, "group:ops")
	}()
	select {
	case <-done:
		t.Fatalf("the binding did not wait for the auto-join change: %v", berr)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	if codeOf(berr) != "auto_join_admin_conflict" {
		t.Fatalf("binding a role to a group that became auto-join: %v", berr)
	}
	if n := e.Number("SELECT count(*) FROM authz_role_binding WHERE principal_id = $1::uuid", ops.ID); n != 0 {
		t.Fatal("an auto-join group got a role binding")
	}
}
