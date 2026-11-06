//go:build integration

package ci

// Package ownership of CI projects (R-CI-015, R-AUTHZ-014's defaults, R-AUTHZ-018, R-AUTHZ-019): the first publish
// of a name claims it for the GitLab project when a bind_project grant allowed it, other projects get 403
// package_owned_by_other_owner, an administrator adds a project as an owner. The tokens and mappings are the
// fixture's of use_test.go.

import (
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func ownersPath(repo, name string) string {
	return "/api/v1/projects/it/repositories/" + repo + "/packages/" + url.PathEscape(name) + "/owners"
}

// ownerRefs lists the owner references and display values of a GET …/owners answer.
func ownerRefs(t *testing.T, body map[string]any) (refs, displays []string) {
	t.Helper()
	list, _ := body["owners"].([]any)
	for _, o := range list {
		m := o.(map[string]any)
		refs = append(refs, m["owner"].(string))
		displays = append(displays, m["display"].(string))
	}
	return refs, displays
}

// wantOwned asserts the refusal of R-CI-015: 403 with the code package_owned_by_other_owner in the npm error body.
func wantOwned(t *testing.T, what string, a testenv.Answer) {
	t.Helper()
	if a.Status != 403 || npmCode(a) != "package_owned_by_other_owner" {
		t.Fatalf("%s: %d %s, want 403 package_owned_by_other_owner", what, a.Status, a.Body)
	}
}

// R-CI-015: the four criteria, with the grant @acme-billing/* that two projects render.
func TestR_CI_015_APackageIsBoundToTheProjectThatFirstPublishesIt(t *testing.T) {
	e := newCIEnv(t, false, "acme-release")
	m := e.maps["acme-release"]
	lead := e.stack.User("lead")
	e.stack.Policy("lead", []string{"read", "deploy", "manage"}, []string{"npm-internal"}, lead)
	leadTok := e.stack.Token(lead, scope.Default())
	a, b := e.web("web-a"), e.web("web-b")
	billing := func(projectID, path string) ciToken {
		return e.issue(a, e.bundle(projectID, path, m.contribution(nil,
			grant([]string{"it/npm-internal"}, []string{"@acme-billing/*"}, claims.Read, claims.Deploy))), job{"p" + projectID, "j" + projectID}, 10*time.Minute)
	}
	projectA, projectB := billing("101", "acme/billing/client"), billing("202", "acme-billing/client")

	t.Run("R-CI-015 project A claims the name, project B gets 403 package_owned_by_other_owner", func(t *testing.T) {
		mustStatus(t, "A publishes 1.0.0", e.publish(a, "npm-internal", projectA.secret, "@acme-billing/client", "1.0.0"), 201)
		wantOwned(t, "B publishes 1.0.1", e.publish(b, "npm-internal", projectB.secret, "@acme-billing/client", "1.0.1"))
		got := e.do(a, "GET", ownersPath("npm-internal", "@acme-billing/client"), nil)
		wantStatus(t, got, 200, "")
		refs, displays := ownerRefs(t, got.JSON(t))
		if len(refs) != 1 || refs[0] != "ci:gitlab-main:project:101" || displays[0] != "acme/billing/client" {
			t.Fatalf("owners %v %v", refs, displays)
		}
		if n := e.events("ci.package_ownership.claimed"); n != 1 {
			t.Errorf("%d claim events", n)
		}
		within(t, "the conflict event through the buffer", 5*time.Second, func() bool { return e.events("ci.package_ownership.conflict") == 1 })
		if tok := e.text(`SELECT details->>'tokenId' FROM audit_event WHERE action = 'ci.package_ownership.conflict'`); tok != projectB.id {
			t.Errorf("the conflict names token %q, want %q", tok, projectB.id)
		}
	})

	t.Run("R-CI-015 after a rename of project A its next publish succeeds and the owner shows the new path", func(t *testing.T) {
		renamed := billing("101", "acme/payments/client")
		mustStatus(t, "A publishes 1.0.2", e.publish(b, "npm-internal", renamed.secret, "@acme-billing/client", "1.0.2"), 201)
		_, displays := ownerRefs(t, e.do(a, "GET", ownersPath("npm-internal", "@acme-billing/client"), nil).JSON(t))
		if len(displays) != 1 || displays[0] != "acme/payments/client" {
			t.Fatalf("display %v", displays)
		}
	})

	t.Run("R-CI-015 two concurrent first publishes from two projects leave exactly one owner", func(t *testing.T) {
		for trial := range 3 {
			name := "@acme-billing/race-" + string(rune('a'+trial))
			var wg sync.WaitGroup
			answers := make([]testenv.Answer, 2)
			for i, p := range []struct {
				tok ciToken
				rep *testenv.Replica
			}{{projectA, a}, {projectB, b}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					answers[i] = e.stack.Do(p.rep, "PUT", npmPath("npm-internal", name), p.tok.secret, npmBody(t, name, fmt.Sprintf("1.0.%d", i)))
				}()
			}
			wg.Wait()
			won := 0
			for _, ans := range answers {
				switch {
				case ans.Status == 201:
					won++
				case ans.Status == 403 && npmCode(ans) == "package_owned_by_other_owner":
				default:
					t.Fatalf("trial %d: %d %s", trial, ans.Status, ans.Body)
				}
			}
			if won != 1 {
				t.Fatalf("trial %d: %d publishes succeeded", trial, won)
			}
			if n := e.number(`SELECT count(*) FROM package_owner WHERE package_name = $1`, name); n != 1 {
				t.Fatalf("trial %d: %d owners", trial, n)
			}
		}
	})

	t.Run("R-CI-015 an imported name without owner is refused until an administrator adds the project", func(t *testing.T) {
		mustStatus(t, "the lead imports @acme-billing/legacy", e.publish(a, "npm-internal", leadTok, "@acme-billing/legacy", "0.9.0"), 201)
		e.exec(`DELETE FROM package_ownership WHERE package_name = '@acme-billing/legacy'`) // versions from before ownership
		wantOwned(t, "A publishes the ownerless name", e.publish(a, "npm-internal", projectA.secret, "@acme-billing/legacy", "1.0.0"))
		added := e.do(a, "POST", ownersPath("npm-internal", "@acme-billing/legacy"), map[string]any{"owner": "ci:gitlab-main:project:101"})
		wantStatus(t, added, 201, "")
		if got := added.JSON(t); got["owner"] != "ci:gitlab-main:project:101" || got["reason"] != "added" {
			t.Errorf("added %v", got)
		}
		mustStatus(t, "A publishes after the owner was added", e.publish(b, "npm-internal", projectA.secret, "@acme-billing/legacy", "1.0.0"), 201)
		listed := e.do(a, "GET", "/api/v1/package-owners?owner=ci:gitlab-main:project:101", nil)
		wantStatus(t, listed, 200, "")
		if items := listed.JSON(t)["items"].([]any); len(items) < 2 {
			t.Errorf("names the project owns: %s", listed.Body)
		}
		wantStatus(t, e.do(a, "POST", ownersPath("npm-internal", "@acme-billing/legacy"), map[string]any{"owner": "ci:gitlab-nowhere:project:101"}), 422, "")
	})
}

// R-AUTHZ-014 (Q-AUTHZ-6): a writing inline grant without ownership is bind_project, so of two projects matched by
// one mapping the first owns @acme/tools and the second gets 403; with ownership none the mapping owns it and both
// publish. A bind_project grant is checked also where packageOwnership is off (R-AUTHZ-018).
func TestR_AUTHZ_014_TheOwnershipDefaultsOfAWritingInlineGrant(t *testing.T) {
	e := newCIEnv(t, false, "acme-tools", "acme-shared")
	a := e.web("web-a")
	tools := func(m ciMapping, projectID string, own claims.Ownership, repo string) ciToken {
		g := grant([]string{"it/" + repo}, []string{"@acme/tools"}, claims.Read, claims.Deploy)
		g.Ownership = own
		return e.issue(a, e.bundle(projectID, "acme/p"+projectID, m.contribution(nil, g)), job{"1", projectID}, 10*time.Minute)
	}
	t.Run("R-AUTHZ-014 bind_project by default: the first project owns the name, the second gets 403", func(t *testing.T) {
		first, second := tools(e.maps["acme-tools"], "301", "", "npm-internal"), tools(e.maps["acme-tools"], "302", "", "npm-internal")
		mustStatus(t, "first", e.publish(a, "npm-internal", first.secret, "@acme/tools", "1.0.0"), 201)
		wantOwned(t, "second", e.publish(a, "npm-internal", second.secret, "@acme/tools", "1.0.1"))
	})
	t.Run("R-AUTHZ-018 ownership none: the mapping owns the name and every project it matches publishes", func(t *testing.T) {
		g := grant([]string{"it/npm-internal"}, []string{"@acme/shared"}, claims.Read, claims.Deploy)
		g.Ownership = claims.OwnershipNone
		first := e.issue(a, e.bundle("401", "acme/p401", e.maps["acme-shared"].contribution(nil, g)), job{"1", "401"}, 10*time.Minute)
		second := e.issue(a, e.bundle("402", "acme/p402", e.maps["acme-shared"].contribution(nil, g)), job{"1", "402"}, 10*time.Minute)
		mustStatus(t, "first", e.publish(a, "npm-internal", first.secret, "@acme/shared", "1.0.0"), 201)
		mustStatus(t, "second", e.publish(a, "npm-internal", second.secret, "@acme/shared", "1.0.1"), 201)
		refs, _ := ownerRefs(t, e.do(a, "GET", ownersPath("npm-internal", "@acme/shared"), nil).JSON(t))
		if len(refs) != 1 || refs[0] != "ci:gitlab-main/acme-shared" {
			t.Fatalf("owners %v", refs)
		}
	})
	t.Run("R-AUTHZ-018 a bind_project grant is checked where packageOwnership is off", func(t *testing.T) {
		first, second := tools(e.maps["acme-tools"], "501", "", "npm-loose"), tools(e.maps["acme-tools"], "502", "", "npm-loose")
		mustStatus(t, "first", e.publish(a, "npm-loose", first.secret, "@acme/tools", "3.0.0"), 201)
		wantOwned(t, "second", e.publish(a, "npm-loose", second.secret, "@acme/tools", "3.0.1"))
	})
}
