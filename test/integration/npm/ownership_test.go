//go:build integration

package npm

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// owners lists the owner names of a package of a repository, sorted.
func (e *env) owners(repo, name string) string {
	e.t.Helper()
	return e.st.Text(`SELECT COALESCE(string_agg(o.display, ',' ORDER BY o.display), '') FROM package_owner o
		JOIN repositories r ON o.domain = 'repo:' || r.id WHERE r.key = $1 AND o.package_name = $2`, repo, name)
}

// TestR_AUTHZ_018_OwnershipAtPublish: the first publish of a name claims it for the
// publisher; another principal that holds deploy is refused 403 in the native shape
// and nothing is stored; an owner publishes on.
func TestR_AUTHZ_018_OwnershipAtPublish(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	b := e.replica("web-b")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/owned", Version: "1.0.0"})
	if got := e.owners("npm-local", "@acme/owned"); got != "alice" {
		t.Fatalf("owners %q after the first publish", got)
	}
	if e.events("authz.ownership.claimed", "@acme/owned", "") != 1 {
		t.Fatalf("claimed events: %d", e.events("authz.ownership.claimed", "@acme/owned", ""))
	}
	t.Run("R-AUTHZ-018 a second principal is refused, on another replica too", func(t *testing.T) {
		for _, r := range []*testenv.Replica{a, b} {
			ans := e.publish(r, "npm-local", e.tokB, pkg{Name: "@acme/owned", Version: "1.0.1"})
			if ans.Status != 403 || code(t, ans) != "package_owned_by_other_owner" {
				t.Fatalf("%s: %d %s", r.Name, ans.Status, ans.Body)
			}
		}
		if e.versions("npm-local", "@acme/owned") != 1 || e.revSeq("npm-local", "@acme/owned") != 1 {
			t.Fatal("a refused publish changed the package")
		}
		items := e.st.Number(`SELECT count(*) FROM items WHERE path LIKE '@acme/owned/-/%'`)
		if items != 1 {
			t.Fatalf("items: %d", items)
		}
	})
	t.Run("S-4 R-AUTHZ-018 a second principal is refused before the body is read: a body that is no publish is a 403, not a 400", func(t *testing.T) {
		for _, r := range []*testenv.Replica{a, b} {
			ans := e.st.Do(r, "PUT", pubPath("npm-local", "@acme/owned"), e.tokB, []byte("this is not a publish document"))
			if ans.Status != 403 || code(t, ans) != "package_owned_by_other_owner" {
				t.Fatalf("%s: %d %s", r.Name, ans.Status, ans.Body)
			}
		}
		// The owner's garbage is the 400 of the scanner: the question passed and the body was read.
		if ans := e.st.Do(a, "PUT", pubPath("npm-local", "@acme/owned"), e.tokA, []byte("this is not a publish document")); ans.Status != 400 {
			t.Fatalf("the owner: %d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-AUTHZ-018 the owner publishes on, whichever replica", func(t *testing.T) {
		e.mustPublish(b, "npm-local", e.tokA, pkg{Name: "@acme/owned", Version: "1.0.1"})
		if e.versions("npm-local", "@acme/owned") != 2 {
			t.Fatal("not stored")
		}
	})
	t.Run("R-AUTHZ-007 a reader may not publish", func(t *testing.T) {
		ans := e.publish(a, "npm-local", e.tokC, pkg{Name: "readonly-try"})
		if ans.Status != 403 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		code(t, ans)
	})
	t.Run("R-AUTHZ-007 no credential is a 401 that never asks for a one-time password", func(t *testing.T) {
		ans := e.publish(a, "npm-local", "", pkg{Name: "anon-try"})
		if ans.Status != 401 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		code(t, ans)
		if h := ans.Header.Get("WWW-Authenticate"); h == "" || contains(lower(h), "otp") || contains(lower(h), "ipaddress") {
			t.Fatalf("WWW-Authenticate %q", h)
		}
	})
	t.Run("R-AUTHZ-018 two concurrent first publishes of one name by different principals: one wins, one is refused", func(t *testing.T) {
		for round := range 6 {
			name := fmt.Sprintf("race-owner-%d", round)
			var wg sync.WaitGroup
			ans := make([]int, 2)
			start := make(chan struct{})
			for i, who := range []struct {
				rep *testenv.Replica
				tok string
			}{{a, e.tokA}, {b, e.tokB}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					ans[i] = tryPublish(who.rep, "npm-local", who.tok, name, fmt.Sprintf("1.0.%d", i), body(t, pkg{Name: name, Version: fmt.Sprintf("1.0.%d", i)}))
				}()
			}
			close(start)
			wg.Wait()
			created, refused := 0, 0
			for _, s := range ans {
				switch s {
				case 201:
					created++
				case 403:
					refused++
				default:
					t.Errorf("%s: status %d", name, s)
				}
			}
			if created != 1 || refused != 1 {
				t.Errorf("%s: statuses %v", name, ans)
			}
			if e.versions("npm-local", name) != 1 {
				t.Errorf("%s: %d versions", name, e.versions("npm-local", name))
			}
		}
	})
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// TestR_REPO_023_AutoClaimAtPublish: the first publish of a name creates the exact claim
// for it in the same transaction; a later version creates none; a repository with
// autoClaim off creates none; the audit event of the publish says which.
func TestR_REPO_023_AutoClaimAtPublish(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	claims := func(repo, pattern string) int64 {
		return e.st.Number(`SELECT count(*) FROM namespace_claims nc JOIN repositories r ON r.id = nc.repo_id WHERE r.key = $1 AND nc.pattern = $2`, repo, pattern)
	}
	e.mustPublish(a, "npm-claims", e.tokA, pkg{Name: "@acme/claimed", Version: "1.0.0"})
	if claims("npm-claims", "@acme/claimed") != 1 {
		t.Fatal("the first publish created no claim")
	}
	e.mustPublish(a, "npm-claims", e.tokA, pkg{Name: "@acme/claimed", Version: "1.0.1"})
	if claims("npm-claims", "@acme/claimed") != 1 {
		t.Fatal("a later version created another claim")
	}
	if got := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'repository.claims_changed' AND repository = 'npm-claims'`); got != 1 {
		t.Fatalf("claims_changed events: %d", got)
	}
	details := e.st.Text(`SELECT details::text FROM audit_event WHERE action = 'npm.publish' AND path = '@acme/claimed' ORDER BY occurred_at, id LIMIT 1`)
	if !contains(details, `created`) {
		t.Fatalf("the first publish event does not record the claim: %s", details)
	}
	second := e.st.Text(`SELECT details::text FROM audit_event WHERE action = 'npm.publish' AND path = '@acme/claimed' ORDER BY occurred_at DESC, id DESC LIMIT 1`)
	if contains(second, `"claim"`) {
		t.Fatalf("a later version records a claim: %s", second)
	}
	e.mustPublish(a, "npm-noclaim", e.tokA, pkg{Name: "@acme/unclaimed", Version: "1.0.0"})
	if claims("npm-noclaim", "@acme/unclaimed") != 0 {
		t.Fatal("autoClaim off created a claim")
	}
	if d := e.st.Text(`SELECT details::text FROM audit_event WHERE action = 'npm.publish' AND path = '@acme/unclaimed'`); !contains(d, `disabled`) {
		t.Fatalf("event %s", d)
	}
}

// tryPublish is publish for a goroutine of the test: it reports the status, or -1.
func tryPublish(r *testenv.Replica, repo, token, name, version string, b []byte) int {
	ans, err := rawPutErr(r, pubPath(repo, name), token, bytes.NewReader(b), int64(len(b)), false)
	if err != nil {
		return -1
	}
	return ans.Status
}

// TestR_AUTHZ_018_ANewNPMRepositoryEnforcesOwnership: a local npm repository created
// through the API holds packageOwnership enforce while its document says nothing, the
// catalog fact of the format (Q-AUTHZ-2).
func TestR_AUTHZ_018_ANewNPMRepositoryEnforcesOwnership(t *testing.T) {
	e := newEnv(t)
	root := e.st.User("root")
	e.st.Bind("admin", root)
	tok := e.st.Token(root, scope.Scope{Inherit: true, Admin: true})
	a := e.replica("web-a")
	ans := e.st.Do(a, "POST", "/api/v1/repositories", tok, map[string]any{"key": "npm-created", "class": "local", "format": "npm"})
	if ans.Status != 201 {
		t.Fatalf("%d %s", ans.Status, ans.Body)
	}
	local, _ := ans.JSON(t)["local"].(map[string]any)
	if local["packageOwnership"] != "enforce" {
		t.Fatalf("local %v", local)
	}
}
