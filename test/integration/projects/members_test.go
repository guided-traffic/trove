//go:build integration

package projects

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/test/integration/stack"
)

// TestR_AUTHZ_026_Members: the members of a project and their roles over HTTP (R-AUTHZ-026, Q-AUTHZ-207,
// Q-AUTHZ-208, Q-AUTHZ-193): a PUT adds (201) or sets the role (200), the same role again changes nothing, a DELETE
// removes (204); only users and groups are members; the writes leave the project's version alone (Q-AUTHZ-185).
func TestR_AUTHZ_026_Members(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	f.e.User("dave")
	f.e.Group("everyone")
	f.e.Exec("UPDATE user_group SET auto_join = true WHERE principal_id = (SELECT id FROM principal WHERE name = 'everyone' AND kind = 'group')")

	t.Run("R-AUTHZ-026 adding a group answers 201, the same role again 200 and records nothing", func(t *testing.T) {
		r := f.member("web", "group:web-leads", "owner")
		wantStatus(t, r, 201, "")
		if m := r.JSON(t); m["principal"] != "group:web-leads" || m["kind"] != "group" || m["role"] != "owner" || m["project"] != "web" {
			t.Fatalf("%v", m)
		}
		if f.events("authz.project.member_added") != 1 || f.journal("authz.project.member_added") != 0 {
			t.Errorf("events %d, journal %d", f.events("authz.project.member_added"), f.journal("authz.project.member_added"))
		}
		epoch := f.epoch()
		again := f.member("web", "group:web-leads", "owner")
		wantStatus(t, again, 200, "")
		if f.epoch() != epoch || again.Epoch() != epoch || f.events("authz.project.member_added") != 1 || f.events("authz.project.member_changed") != 0 {
			t.Error("a write that changes nothing raised the epoch or recorded an event")
		}
	})
	t.Run("R-AUTHZ-026 a user is a member too, and the reference may be percent-encoded", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "PUT", base+"/web/members/user%3Adave", map[string]any{"role": "maintainer"}), 201, "")
		list := f.do(f.admin, "GET", base+"/web/members", nil)
		wantStatus(t, list, 200, "")
		var got []string
		for _, it := range list.Items(t) {
			got = append(got, fmt.Sprintf("%v=%v", it["principal"], it["role"]))
		}
		if !slices.Equal(got, []string{"group:web-leads=owner", "user:dave=maintainer"}) {
			t.Fatalf("members %v", got)
		}
	})
	t.Run("R-AUTHZ-026 only users and groups are members: 422 principal_not_allowed for the rest, 404 for the unknown", func(t *testing.T) {
		for _, ref := range []string{"sa:ci-bot", "authenticated", "anonymous", "group:everyone", "ci:gitlab%2Fmain", "bastion:partners"} {
			wantStatus(t, f.member("web", ref, "reader"), 422, "principal_not_allowed")
		}
		wantStatus(t, f.member("web", "user:nobody", "reader"), 404, "")
		wantStatus(t, f.member("web", "user:dave", "admin"), 422, "")
		wantStatus(t, f.member("nothing", "user:dave", "reader"), 404, "")
	})
	t.Run("R-AUTHZ-026 a lower role is a change, journaled as a narrowing (R-AUTHZ-031)", func(t *testing.T) {
		wantStatus(t, f.member("web", "user:dave", "reader"), 200, "")
		if f.events("authz.project.member_changed") != 1 || f.journal("authz.project.member_changed") != 1 {
			t.Errorf("events %d, journal %d", f.events("authz.project.member_changed"), f.journal("authz.project.member_changed"))
		}
		if f.detail("authz.project.member_changed", "before") != "maintainer" || f.detail("authz.project.member_changed", "after") != "reader" {
			t.Errorf("before %s, after %s", f.detail("authz.project.member_changed", "before"), f.detail("authz.project.member_changed", "after"))
		}
	})
	t.Run("R-AUTHZ-026 a member write leaves the project's version alone (Q-AUTHZ-185)", func(t *testing.T) {
		if got := f.do(f.admin, "GET", base+"/web", nil); got.Header.Get("ETag") != `"1"` || got.JSON(t)["members"] != float64(2) {
			t.Errorf("ETag %s, members %v", got.Header.Get("ETag"), got.JSON(t)["members"])
		}
	})
	t.Run("R-AUTHZ-026 a removal answers 204 and is journaled, a second one 404", func(t *testing.T) {
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web/members/user:dave", nil), 204, "")
		if f.events("authz.project.member_removed") != 1 || f.journal("authz.project.member_removed") != 1 {
			t.Errorf("events %d, journal %d", f.events("authz.project.member_removed"), f.journal("authz.project.member_removed"))
		}
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web/members/user:dave", nil), 404, "")
	})
	t.Run("R-AUTHZ-026 an Owner adds a member and changes its role; a Developer is refused, a caller that is no member hidden", func(t *testing.T) {
		epoch := fmt.Sprint(f.epoch())
		wantStatus(t, f.do(f.alice, "PUT", base+"/web/members/user:dave", map[string]any{"role": "maintainer"}, authz.HeaderMinEpoch, epoch), 201, "")
		r := f.do(f.alice, "PUT", base+"/web/members/user:dave", map[string]any{"role": "developer"}, authz.HeaderMinEpoch, epoch)
		wantStatus(t, r, 200, "")
		dave := f.e.Known("user", "dave")
		wantStatus(t, f.do(dave, "PUT", base+"/web/members/user:bob", map[string]any{"role": "reader"}, authz.HeaderMinEpoch, fmt.Sprint(r.Epoch())), 403, "")
		wantStatus(t, f.do(f.bob, "PUT", base+"/web/members/user:bob", map[string]any{"role": "owner"}, authz.HeaderMinEpoch, fmt.Sprint(r.Epoch())), 404, "")
		wantStatus(t, f.do(f.bob, "GET", base+"/web/members", nil, authz.HeaderMinEpoch, fmt.Sprint(r.Epoch())), 404, "")
	})
	t.Run("R-AUTHZ-026 the auditor reads the members and writes none", func(t *testing.T) {
		wantStatus(t, f.do(f.auditor, "GET", base+"/web/members", nil), 200, "")
		wantStatus(t, f.member("web", "user:bob", "reader"), 201, "")
		wantStatus(t, f.do(f.auditor, "DELETE", base+"/web/members/user:bob", nil), 403, "")
	})
}

// TestQ_AUTHZ_217_TheLastOwner: the last membership with the role owner, a group among them, is removed or demoted
// only by a holder of projects:write (409 last_owner otherwise); two Owners who remove each other at once run one
// after the other under the project's lock: one removal commits, and the second request is refused as what its caller
// is when it is decided (409 last_owner, or the 404 of a non-member once its own removal is in force).
func TestQ_AUTHZ_217_TheLastOwner(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	wantStatus(t, f.member("web", "group:web-leads", "owner"), 201, "")
	epoch := fmt.Sprint(f.epoch())
	t.Run("Q-AUTHZ-217 the Owner group's member cannot remove or demote the only Owner", func(t *testing.T) {
		wantStatus(t, f.do(f.alice, "DELETE", base+"/web/members/group:web-leads", nil, authz.HeaderMinEpoch, epoch), 409, "last_owner")
		wantStatus(t, f.do(f.alice, "PUT", base+"/web/members/group:web-leads", map[string]any{"role": "maintainer"}, authz.HeaderMinEpoch, epoch), 409, "last_owner")
		if f.events("authz.project.member_removed")+f.events("authz.project.member_changed") != 0 {
			t.Error("a refused write recorded an event")
		}
	})
	t.Run("Q-AUTHZ-217 with a second Owner it can", func(t *testing.T) {
		r := f.do(f.alice, "PUT", base+"/web/members/user:bob", map[string]any{"role": "owner"}, authz.HeaderMinEpoch, epoch)
		wantStatus(t, r, 201, "")
		wantStatus(t, f.do(f.alice, "PUT", base+"/web/members/group:web-leads", map[string]any{"role": "developer"}, authz.HeaderMinEpoch, fmt.Sprint(r.Epoch())), 200, "")
		wantStatus(t, f.member("web", "group:web-leads", "owner"), 200, "")
	})
	t.Run("Q-AUTHZ-217 two Owners who remove each other at once: one removal commits, the other is refused as what its caller then is", func(t *testing.T) {
		// The spec (Q-AUTHZ-217, R-AUTHZ-026): the loser answers 409 last_owner while the index its request is decided on
		// still holds its role, and the 404 of a non-member once the winner's removal of its membership is in force there.
		// Either way exactly one removal commits and the winner stays the only Owner.
		nonMember := f.do(f.bob, "GET", base+"/nothing-here/members", nil)
		wantStatus(t, nonMember, 404, "not_found")
		hidden := strings.ReplaceAll(fmt.Sprint(nonMember.JSON(t)["detail"]), "nothing-here", "web")
		for trial := range 3 {
			f.e.Exec("DELETE FROM project_member WHERE project_id = (SELECT id FROM project WHERE key = 'web')")
			wantStatus(t, f.member("web", "user:alice", "owner"), 201, "")
			wantStatus(t, f.member("web", "user:bob", "owner"), 201, "")
			e := fmt.Sprint(f.epoch())
			removed := f.events("authz.project.member_removed")
			var wg sync.WaitGroup
			res := make([]stack.Response, 2)
			pairs := [][2]string{{"alice", "user:bob"}, {"bob", "user:alice"}}
			for i, pair := range pairs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res[i] = f.cl.Do(pair[0], "DELETE", base+"/web/members/"+pair[1], nil, authz.HeaderMinEpoch, e)
				}()
			}
			wg.Wait()
			winner := -1
			for i, r := range res {
				if r.Status == 204 {
					if winner >= 0 {
						t.Fatalf("trial %d: both removals committed", trial)
					}
					winner = i
				}
			}
			if winner < 0 {
				t.Fatalf("trial %d: no removal committed: %d %s, %d %s", trial, res[0].Status, res[0].Body, res[1].Status, res[1].Body)
			}
			loser := res[1-winner]
			switch {
			case loser.Status == 409 && loser.Code() == "last_owner":
			case loser.Status == 404 && loser.Code() == "not_found" && loser.JSON(t)["detail"] == hidden:
			default:
				t.Fatalf("trial %d: the loser %s answered %d %s, want 409 last_owner or the 404 of a non-member", trial, pairs[1-winner][0], loser.Status, loser.Body)
			}
			t.Logf("trial %d: %s won, the loser answered %d", trial, pairs[winner][0], loser.Status)
			owners := f.e.Text("SELECT string_agg(pr.name, ',') FROM project_member m JOIN project p ON p.id = m.project_id JOIN principal pr ON pr.id = m.principal_id WHERE p.key = 'web' AND m.role = 'owner'")
			if owners != pairs[winner][0] {
				t.Fatalf("trial %d: the Owners are %q, want the winner %s alone", trial, owners, pairs[winner][0])
			}
			if n := f.events("authz.project.member_removed"); n != removed+1 {
				t.Fatalf("trial %d: %d removals recorded, want 1", trial, n-removed)
			}
		}
	})
	t.Run("Q-AUTHZ-217 an administrator of trove removes the last Owner and leaves the project without one", func(t *testing.T) {
		f.e.Exec("DELETE FROM project_member WHERE project_id = (SELECT id FROM project WHERE key = 'web')")
		wantStatus(t, f.member("web", "user:alice", "owner"), 201, "")
		wantStatus(t, f.do(f.admin, "DELETE", base+"/web/members/user:alice", nil), 204, "")
		if n := f.e.Number("SELECT count(*) FROM project_member m JOIN project p ON p.id = m.project_id WHERE p.key = 'web'"); n != 0 {
			t.Fatalf("%d members left", n)
		}
	})
}

// TestR_AUTHZ_029_ARefusedWriteLeavesNoEvent: the audit event is recorded in the transaction of the write, so a write
// that is refused records nothing, raises no epoch and leaves no journal entry.
func TestR_AUTHZ_029_ARefusedWriteLeavesNoEvent(t *testing.T) {
	f := newFixture(t)
	f.create("web")
	f.seedRepo("web", "npm", "active")
	wantStatus(t, f.member("web", "group:web-leads", "owner"), 201, "")
	events := f.e.Number("SELECT count(*) FROM audit_event WHERE category = 'authz'")
	epoch, outbox := f.epoch(), f.e.Number("SELECT count(*) FROM revocation_outbox")

	wantStatus(t, f.do(f.admin, "DELETE", base+"/web", nil), 409, "project_not_empty")
	wantStatus(t, f.do(f.admin, "PUT", base+"/web", map[string]any{"displayName": "X"}), 428, "")
	wantStatus(t, f.do(f.admin, "PUT", base+"/web", map[string]any{"displayName": "X"}, "If-Match", `"9"`), 412, "")
	wantStatus(t, f.member("web", "user:nobody", "reader"), 404, "")
	wantStatus(t, f.member("web", "sa:ci-bot", "reader"), 422, "principal_not_allowed")
	wantStatus(t, f.do(f.alice, "DELETE", base+"/web/members/group:web-leads", nil), 409, "last_owner")
	wantStatus(t, f.do(f.admin, "POST", base+"/web/repositories/npm/move", map[string]any{"project": "nothing"}), 404, "")

	if got := f.e.Number("SELECT count(*) FROM audit_event WHERE category = 'authz'"); got != events {
		t.Errorf("%d authz events, was %d", got, events)
	}
	if f.epoch() != epoch || f.e.Number("SELECT count(*) FROM revocation_outbox") != outbox {
		t.Error("a refused write raised the epoch or wrote a journal entry")
	}
}
