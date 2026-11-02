//go:build integration

package projects

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/project"
)

// policyHook is the policy store of a project service that lets a test act between the
// steps of a delete: before a policy is deleted and after it was.
type policyHook struct {
	real          *authz.Service
	before, after func(name string)
	calls         atomic.Int32
}

func (h *policyHook) Delete(ctx context.Context, actor authz.Subject, name string, expected int64, opts ...authz.WriteOption) (int64, error) {
	h.calls.Add(1)
	if h.before != nil {
		h.before(name)
	}
	epoch, err := h.real.Delete(ctx, actor, name, expected, opts...)
	if h.after != nil {
		h.after(name)
	}
	return epoch, err
}

// serviceWith is a project service on the first replica's database whose policy store is
// the hook, so that a test forces the windows of a delete (Q-AUTHZ-192).
func (f *fixture) serviceWith(h *policyHook) (project.Service, authz.Subject) {
	f.t.Helper()
	h.real = f.a.Service
	svc := project.New(project.Options{DB: f.a.DB, Changes: f.a.Changes, Policies: h})
	return svc, authz.Subject{Kind: principal.KindUser, ID: f.admin.ID, Name: f.admin.Name}
}

// TestR_AUTHZ_026_DeleteRetriesForNewPolicies forces the retry branch of the delete: a
// policy created after the first round deleted the old ones sends the delete around
// again, and a project whose policies keep appearing is refused after three rounds.
func TestR_AUTHZ_026_DeleteRetriesForNewPolicies(t *testing.T) {
	t.Run("R-AUTHZ-026 a policy that appears between the policy deletion and the close is deleted in a second round", func(t *testing.T) {
		f := newFixture(t)
		f.create("web")
		f.seedProjectPolicy("web", "web-first")
		h := &policyHook{}
		h.after = func(name string) {
			if name == "web-first" {
				f.seedProjectPolicy("web", "web-late")
			}
		}
		svc, actor := f.serviceWith(h)
		epoch, err := svc.Delete(context.Background(), actor, "web", -1)
		if err != nil || epoch <= 0 {
			t.Fatalf("Delete = %d, %v", epoch, err)
		}
		if got := h.calls.Load(); got != 2 {
			t.Errorf("policy deletions = %d, want web-first and web-late", got)
		}
		if f.e.Number("SELECT count(*) FROM project WHERE key = 'web'") != 0 || f.e.Number("SELECT count(*) FROM authz_policy WHERE name LIKE 'web-%'") != 0 {
			t.Error("the project or one of its policies remains")
		}
		if f.events("authz.policy.deleted") != 2 {
			t.Errorf("%d policy deletions audited, want 2", f.events("authz.policy.deleted"))
		}
	})

	t.Run("R-AUTHZ-026 a project whose policies keep appearing answers 409 after three rounds and keeps what is left", func(t *testing.T) {
		f := newFixture(t)
		f.create("web")
		f.seedProjectPolicy("web", "web-first")
		h := &policyHook{}
		h.after = func(string) { f.seedProjectPolicy("web", fmt.Sprintf("web-again-%d", h.calls.Load())) }
		svc, actor := f.serviceWith(h)
		_, err := svc.Delete(context.Background(), actor, "web", -1)
		if errs.KindOf(err) != errs.Conflict {
			t.Fatalf("Delete = %v, want the conflict", err)
		}
		if f.e.Number("SELECT count(*) FROM project WHERE key = 'web'") != 1 {
			t.Error("the project was deleted although a policy was left")
		}
		if got := h.calls.Load(); got != 3 {
			t.Errorf("rounds = %d, want 3", got)
		}
	})
}

// TestR_AUTHZ_026_DeleteSparesAGlobalPolicyOfTheSameName forces the window of the delete
// by name: the policy of the project is gone and a global policy took its name before
// the policy store ran (Q-AUTHZ-204). The version read in the precheck is the If-Match
// of the deletion, so the global policy stays.
func TestR_AUTHZ_026_DeleteSparesAGlobalPolicyOfTheSameName(t *testing.T) {
	// The project delete lists its policies, then deletes each at the version it read and
	// for its project (Q-AUTHZ-204, Q-AUTHZ-199). A global policy created under the freed name
	// in between survives, whether its version differs from the one read or not (H-407).
	for _, c := range []struct {
		name    string
		version int
	}{{"an edited project policy (version 2)", 2}, {"a never-edited project policy (version 1)", 1}} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.create("web")
			f.seedProjectPolicy("web", "web-x")
			f.e.Exec(fmt.Sprintf("UPDATE authz_policy SET version = %d WHERE name = 'web-x'", c.version))
			h := &policyHook{}
			h.before = func(name string) {
				if name != "web-x" {
					return
				}
				f.e.Exec("DELETE FROM authz_policy WHERE name = 'web-x'")
				f.e.Exec("INSERT INTO authz_policy (id, name) VALUES (gen_random_uuid(), 'web-x')")
			}
			svc, actor := f.serviceWith(h)
			if _, err := svc.Delete(context.Background(), actor, "web", -1); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if f.e.Number("SELECT count(*) FROM authz_policy WHERE name = 'web-x' AND project_id IS NULL") != 1 {
				t.Error("the global policy that took the name was deleted")
			}
			if f.e.Number("SELECT count(*) FROM project WHERE key = 'web'") != 0 {
				t.Error("the project remains")
			}
		})
	}
}
