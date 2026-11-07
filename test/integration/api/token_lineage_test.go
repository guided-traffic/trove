//go:build integration

package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The line of a narrowed token (R-AUTH-028 to R-AUTH-030, Q-AUTH-199) on the running composition root: a token whose scope
// does not inherit its owner's rights sees, revokes and rotates, by ownership, only itself and the tokens derived from it;
// any other token of its owner answers 404 like a token it may not see. A session and an inheriting token act on every
// token of the owner; a narrowed token that holds tokens:read sees, and one that holds tokens:revoke revokes, as any holder.

// narrowWire is the scope of narrowScopes["one repository"] in the form of the API.
var narrowWire = map[string]any{
	"inherit": false, "admin": false, "system": []string{},
	"repositories": []map[string]any{{"repositories": []string{testenv.DefaultProject + "/npm-local"}, "actions": []string{"read"}}},
}

// minted is a token the test holds: its value and its ID.
type minted struct{ value, id string }

// held returns the ID of a token value the seed minted.
func (e *env) held(r *testenv.Replica, value string) minted {
	e.t.Helper()
	got := e.stack.Do(r, "GET", "/api/v1/tokens/current", value, nil)
	id, _ := got.JSON(e.t)["id"].(string)
	if got.Status != http.StatusOK || id == "" {
		e.t.Fatalf("tokens/current: %d %s", got.Status, got.Body)
	}
	return minted{value: value, id: id}
}

// derive creates a personal token through POST /api/v1/tokens with the presenting token (R-AUTH-027), or with the session
// headers when presenting is empty.
func (e *env) derive(r *testenv.Replica, presenting string, ttl int, hdr ...string) minted {
	e.t.Helper()
	got := e.stack.Do(r, "POST", "/api/v1/tokens", presenting, map[string]any{"description": "derived", "ttlSeconds": ttl, "scope": narrowWire}, hdr...)
	m := got.JSON(e.t)
	value, _ := m["token"].(string)
	id, _ := m["id"].(string)
	if got.Status != http.StatusCreated || value == "" {
		e.t.Fatalf("create: %d %s", got.Status, got.Body)
	}
	return minted{value: value, id: id}
}

// works reports whether a token still authenticates.
func (e *env) works(r *testenv.Replica, value string) bool {
	e.t.Helper()
	return e.stack.Do(r, "GET", "/api/v1/auth/whoami", value, nil).Status == http.StatusOK
}

// hidden fails unless the answer is the 404 of a token the caller may not see (R-AUTH-028).
func hidden(t *testing.T, what string, a testenv.Answer) {
	t.Helper()
	if a.Status != http.StatusNotFound || a.Code(t) != "not_found" {
		t.Errorf("%s: %d %s, want 404 not_found", what, a.Status, a.Body)
	}
}

// lineageRefused fails unless the answer is the 403 of a rotation outside the line of a token that sees the target.
func lineageRefused(t *testing.T, what string, a testenv.Answer) {
	t.Helper()
	if a.Status != http.StatusForbidden || a.Code(t) != "forbidden" || !strings.Contains(string(a.Body), "rotates only itself and the tokens derived from it") {
		t.Errorf("%s: %d %s, want the 403 forbidden of a rotation outside the line", what, a.Status, a.Body)
	}
}

// listed returns the IDs of GET /api/v1/tokens with the query, on one page.
func (e *env) listed(r *testenv.Replica, query, value string, hdr ...string) map[string]bool {
	e.t.Helper()
	got := e.stack.Do(r, "GET", "/api/v1/tokens?limit=100"+query, value, nil, hdr...)
	items, _ := got.JSON(e.t)["items"].([]any)
	if got.Status != http.StatusOK {
		e.t.Fatalf("list: %d %s", got.Status, got.Body)
	}
	out := map[string]bool{}
	for _, it := range items {
		id, _ := it.(map[string]any)["id"].(string)
		out[id] = true
	}
	return out
}

func TestR_AUTH_028_ANarrowedTokenSeesItselfAndItsDerivedTokensOnly(t *testing.T) {
	e := newEnv(t, true)
	kim, lena := e.stack.User("kim"), e.stack.User("lena")
	e.stack.Bind("auditor", lena)
	r := e.stack.Start(t, testenv.ReplicaOptions{Name: "token-lineage-see", Role: "api"})
	narrow := e.held(r, e.stack.Token(kim, narrowScopes["one repository"]))
	inherit := e.held(r, e.stack.Token(kim, scope.Default()))
	sibling := e.held(r, e.stack.Token(kim, narrowScopes["one repository"]))
	child := e.derive(r, narrow.value, 3600)
	grandchild := e.derive(r, child.value, 1800)
	_, browser := e.browserSession(kim)
	all := []minted{narrow, inherit, sibling, child, grandchild}

	t.Run("R-AUTH-028 a narrowed token lists itself and the tokens derived from it", func(t *testing.T) {
		for _, query := range []string{"", "&owner=me", "&owner=kim", "&state=all"} {
			got := e.listed(r, query, narrow.value)
			if len(got) != 3 || !got[narrow.id] || !got[child.id] || !got[grandchild.id] {
				t.Errorf("list%s: %v, want the token, its child and its grandchild", query, got)
			}
		}
		if got := e.listed(r, "", child.value); len(got) != 2 || !got[child.id] || !got[grandchild.id] {
			t.Errorf("the child lists %v, want itself and its child", got)
		}
	})

	t.Run("R-AUTH-028 a narrowed token reads its line and gets 404 for any other token of its owner", func(t *testing.T) {
		for _, m := range []minted{narrow, child, grandchild} {
			if got := e.stack.Do(r, "GET", "/api/v1/tokens/"+m.id, narrow.value, nil); got.Status != http.StatusOK {
				t.Errorf("%s: %d %s", m.id, got.Status, got.Body)
			}
		}
		hidden(t, "a sibling", e.stack.Do(r, "GET", "/api/v1/tokens/"+sibling.id, narrow.value, nil))
		hidden(t, "an inheriting token", e.stack.Do(r, "GET", "/api/v1/tokens/"+inherit.id, narrow.value, nil))
		hidden(t, "its parent, read by the child", e.stack.Do(r, "GET", "/api/v1/tokens/"+narrow.id, child.value, nil))
		hidden(t, "an unknown ID", e.stack.Do(r, "GET", "/api/v1/tokens/"+session.NewID(), narrow.value, nil))
	})

	t.Run("R-AUTH-028 an inheriting token and a session see every token of the owner", func(t *testing.T) {
		for name, got := range map[string]map[string]bool{"the inheriting token": e.listed(r, "", inherit.value), "the session": e.listed(r, "", "", browser...)} {
			for _, m := range all {
				if !got[m.id] {
					t.Errorf("%s does not list %s", name, m.id)
				}
			}
		}
		if got := e.stack.Do(r, "GET", "/api/v1/tokens/"+sibling.id, inherit.value, nil); got.Status != http.StatusOK {
			t.Errorf("the inheriting token reads the sibling: %d %s", got.Status, got.Body)
		}
	})

	t.Run("R-AUTH-028 a narrowed token that holds tokens:read sees as any holder", func(t *testing.T) {
		reader := e.held(r, e.stack.Token(lena, scope.Scope{System: []string{"tokens:read"}}))
		got := e.listed(r, "&owner=kim", reader.value)
		for _, m := range all {
			if !got[m.id] {
				t.Errorf("the holder does not list %s", m.id)
			}
		}
		if got := e.stack.Do(r, "GET", "/api/v1/tokens/"+sibling.id, reader.value, nil); got.Status != http.StatusOK {
			t.Errorf("the holder reads the sibling: %d %s", got.Status, got.Body)
		}
	})

	t.Run("R-AUTH-028 a token rotated through an inheriting token leaves the line", func(t *testing.T) {
		c := e.derive(r, narrow.value, 3600)
		got := e.stack.Do(r, "POST", "/api/v1/tokens/"+c.id+"/rotate", inherit.value, map[string]any{"overlapSeconds": 3600})
		succ, _ := got.JSON(t)["id"].(string)
		if got.Status != http.StatusCreated || succ == "" {
			t.Fatalf("rotate: %d %s", got.Status, got.Body)
		}
		hidden(t, "the successor, derived from the inheriting token", e.stack.Do(r, "GET", "/api/v1/tokens/"+succ, narrow.value, nil))
		if got := e.listed(r, "", narrow.value); got[succ] || !got[c.id] {
			t.Errorf("the list holds the successor %v or misses the predecessor %v: %v", got[succ], got[c.id], got)
		}
		if got := e.stack.Do(r, "GET", "/api/v1/tokens/"+c.id, narrow.value, nil); got.Status != http.StatusOK {
			t.Errorf("the predecessor, still derived from the narrowed token: %d %s", got.Status, got.Body)
		}
	})

	t.Run("R-AUTH-028 a token of the line rotated through a session stays in the line", func(t *testing.T) {
		c := e.derive(r, narrow.value, 3600)
		got := e.stack.Do(r, "POST", "/api/v1/tokens/"+c.id+"/rotate", "", map[string]any{"overlapSeconds": 3600}, browser...)
		succ, _ := got.JSON(t)["id"].(string)
		if got.Status != http.StatusCreated || succ == "" {
			t.Fatalf("rotate through the session: %d %s", got.Status, got.Body)
		}
		if got := e.stack.Do(r, "GET", "/api/v1/tokens/"+succ, narrow.value, nil); got.Status != http.StatusOK {
			t.Fatalf("the narrowed token reads the successor: %d %s", got.Status, got.Body)
		}
		// No presenting token: the successor keeps the predecessor's lineage.
		if n := e.scalar("SELECT count(*) FROM trove.api_token WHERE id = $1::uuid AND derived_from = $2::uuid AND rotated_from = $3::uuid", succ, narrow.id, c.id); n != 1 {
			t.Fatalf("the successor %s is not derived from %s", succ, narrow.id)
		}
	})

	t.Run("R-AUTH-028 a line the retention task cut ends there: the token beyond it is a 404", func(t *testing.T) {
		mid := e.derive(r, narrow.value, 3600)
		leaf := e.derive(r, mid.value, 1800)
		e.exec("DELETE FROM trove.api_token WHERE id = $1::uuid", mid.id)
		hidden(t, "the token below the deleted one", e.stack.Do(r, "GET", "/api/v1/tokens/"+leaf.id, narrow.value, nil))
		if got := e.listed(r, "&state=all", narrow.value); got[leaf.id] {
			t.Errorf("the list holds the token below the deleted one: %v", got)
		}
		hidden(t, "revoking it", e.stack.Do(r, "DELETE", "/api/v1/tokens/"+leaf.id, narrow.value, nil))
	})
}

func TestR_AUTH_029_ANarrowedTokenRevokesItselfAndItsDerivedTokensOnly(t *testing.T) {
	e := newEnv(t, true)
	hank, ivy := e.stack.User("hank"), e.stack.User("ivy")
	e.stack.Bind("admin", ivy)
	bot := e.stack.ServiceAccount("deploy-bot")
	r := e.stack.Start(t, testenv.ReplicaOptions{Name: "token-lineage-revoke", Role: "api"})
	narrow := e.held(r, e.stack.Token(hank, narrowScopes["one repository"]))
	inherit := e.held(r, e.stack.Token(hank, scope.Default()))
	sibling := e.held(r, e.stack.Token(hank, narrowScopes["one repository"]))
	_, browser := e.browserSession(hank)
	revoke := func(by string, target minted, hdr ...string) testenv.Answer {
		return e.stack.Do(r, "DELETE", "/api/v1/tokens/"+target.id, by, nil, hdr...)
	}

	t.Run("R-AUTH-029 a narrowed token revokes its child and its grandchild", func(t *testing.T) {
		child := e.derive(r, narrow.value, 3600)
		grandchild := e.derive(r, child.value, 1800)
		if got := revoke(narrow.value, grandchild); got.Status != http.StatusNoContent {
			t.Fatalf("grandchild: %d %s", got.Status, got.Body)
		}
		if got := revoke(narrow.value, child); got.Status != http.StatusNoContent {
			t.Fatalf("child: %d %s", got.Status, got.Body)
		}
		waitFor(t, "the revoked child refused", revocationBound, func() bool { return !e.works(r, child.value) })
	})

	t.Run("R-AUTH-029 a narrowed token does not revoke a sibling, its parent or an inheriting token of its owner: 404", func(t *testing.T) {
		child := e.derive(r, narrow.value, 3600)
		hidden(t, "a sibling", revoke(narrow.value, sibling))
		hidden(t, "an inheriting token", revoke(narrow.value, inherit))
		hidden(t, "its parent", revoke(child.value, narrow))
		hidden(t, "a sibling of its parent", revoke(child.value, sibling))
		for name, m := range map[string]minted{"the sibling": sibling, "the inheriting token": inherit, "the parent": narrow, "the child": child} {
			if !e.works(r, m.value) {
				t.Errorf("%s no longer authenticates after the refusals", name)
			}
		}
	})

	t.Run("R-AUTH-029 a narrowed token of a service account does not revoke a sibling of the account", func(t *testing.T) {
		own, other := e.held(r, e.stack.Token(bot, narrowScopes["one repository"])), e.held(r, e.stack.Token(bot, narrowScopes["one repository"]))
		hidden(t, "the sibling", revoke(own.value, other))
		if !e.works(r, other.value) {
			t.Fatal("the sibling no longer authenticates")
		}
		if got := revoke(own.value, own); got.Status != http.StatusNoContent {
			t.Fatalf("itself: %d %s", got.Status, got.Body)
		}
	})

	t.Run("R-AUTH-029 a narrowed token that holds tokens:revoke revokes as any holder", func(t *testing.T) {
		holder := e.held(r, e.stack.Token(ivy, scope.Scope{System: []string{"tokens:revoke"}}))
		other := e.held(r, e.stack.Token(ivy, scope.Default()))
		if got := revoke(holder.value, other); got.Status != http.StatusNoContent {
			t.Fatalf("%d %s", got.Status, got.Body)
		}
	})

	t.Run("R-AUTH-029 an inheriting token and a session revoke any token of the owner", func(t *testing.T) {
		second := e.held(r, e.stack.Token(hank, narrowScopes["one repository"]))
		if got := revoke(inherit.value, sibling); got.Status != http.StatusNoContent {
			t.Fatalf("the inheriting token: %d %s", got.Status, got.Body)
		}
		if got := revoke("", second, browser...); got.Status != http.StatusNoContent {
			t.Fatalf("the session: %d %s", got.Status, got.Body)
		}
	})

	t.Run("R-AUTH-029 a narrowed token revokes itself, and its derived tokens go with it", func(t *testing.T) {
		child := e.derive(r, narrow.value, 3600)
		if got := revoke(narrow.value, narrow); got.Status != http.StatusNoContent {
			t.Fatalf("%d %s", got.Status, got.Body)
		}
		waitFor(t, "the child of the revoked token refused", revocationBound, func() bool { return !e.works(r, child.value) })
	})
}

func TestR_AUTH_030_ANarrowedTokenRotatesItselfAndItsDerivedTokensOnly(t *testing.T) {
	e := newEnv(t, true)
	jack, mona := e.stack.User("jack"), e.stack.User("mona")
	e.stack.Bind("auditor", mona)
	bot, watcherBot, adminBot := e.stack.ServiceAccount("deploy-bot"), e.stack.ServiceAccount("watch-bot"), e.stack.ServiceAccount("admin-bot")
	e.stack.Bind("auditor", watcherBot)
	e.stack.Bind("admin", adminBot)
	r := e.stack.Start(t, testenv.ReplicaOptions{Name: "token-lineage-rotate", Role: "api"})
	monaInherit := e.held(r, e.stack.Token(mona, scope.Default()))
	narrow := e.held(r, e.stack.Token(jack, narrowScopes["one repository"]))
	inherit := e.held(r, e.stack.Token(jack, scope.Default()))
	// The sibling is derived from the inheriting token with the narrow scope and a lifetime shorter than the narrow token's,
	// so the derivation rule alone would let the narrow token rotate it: the refusal is the lineage bound's.
	sibling := e.derive(r, inherit.value, 600)
	rotate := func(by string, target minted) testenv.Answer {
		return e.stack.Do(r, "POST", "/api/v1/tokens/"+target.id+"/rotate", by, map[string]any{"overlapSeconds": 0})
	}

	t.Run("R-AUTH-030 a narrowed token does not rotate a sibling: 404", func(t *testing.T) {
		hidden(t, "the sibling", rotate(narrow.value, sibling))
		if !e.works(r, sibling.value) {
			t.Fatal("the sibling no longer authenticates")
		}
		if n := e.scalar("SELECT count(*) FROM trove.api_token WHERE rotated_from = $1::uuid", sibling.id); n != 0 {
			t.Fatalf("the sibling has %d successors", n)
		}
	})

	t.Run("R-AUTH-030 a narrowed token rotates its child, and the successor is derived from it", func(t *testing.T) {
		child := e.derive(r, narrow.value, 3600)
		got := rotate(narrow.value, child)
		if got.Status != http.StatusCreated {
			t.Fatalf("%d %s", got.Status, got.Body)
		}
		succ, _ := got.JSON(t)["id"].(string)
		if n := e.scalar("SELECT count(*) FROM trove.api_token WHERE id = $1::uuid AND derived_from = $2::uuid AND rotated_from = $3::uuid", succ, narrow.id, child.id); n != 1 {
			t.Fatalf("the successor %s is not derived from the presenting token and rotated from the child", succ)
		}
	})

	t.Run("R-AUTH-030 a narrowed token of a service account does not rotate a sibling of the account", func(t *testing.T) {
		own, other := e.held(r, e.stack.Token(bot, narrowScopes["one repository"])), e.held(r, e.stack.Token(bot, narrowScopes["one repository"]))
		hidden(t, "the sibling", rotate(own.value, other))
		if !e.works(r, other.value) {
			t.Fatal("the sibling no longer authenticates")
		}
	})

	t.Run("R-AUTH-030 a narrowed token that sees a sibling through tokens:read does not rotate it: 403", func(t *testing.T) {
		// The watcher's scope covers the sibling's, so the derivation rule alone would let it rotate the sibling.
		sc := narrowScopes["one repository"]
		sc.System = []string{"tokens:read"}
		watcher := e.held(r, e.stack.Token(mona, sc))
		other := e.derive(r, monaInherit.value, 600)
		if got := e.stack.Do(r, "GET", "/api/v1/tokens/"+other.id, watcher.value, nil); got.Status != http.StatusOK {
			t.Fatalf("the holder of tokens:read reads the sibling: %d %s", got.Status, got.Body)
		}
		lineageRefused(t, "the sibling", rotate(watcher.value, other))
		if !e.works(r, other.value) {
			t.Fatal("the sibling no longer authenticates")
		}
	})

	t.Run("R-AUTH-030 a narrowed token of a service account that sees a sibling through tokens:read does not rotate it: 403", func(t *testing.T) {
		sc := narrowScopes["one repository"]
		sc.System = []string{"tokens:read"}
		watcher := e.held(r, e.stack.Token(watcherBot, sc))
		other := e.held(r, e.stack.Token(watcherBot, narrowScopes["one repository"]))
		if got := e.stack.Do(r, "GET", "/api/v1/tokens/"+other.id, watcher.value, nil); got.Status != http.StatusOK {
			t.Fatalf("the holder of tokens:read reads the sibling: %d %s", got.Status, got.Body)
		}
		lineageRefused(t, "the sibling", rotate(watcher.value, other))
		if !e.works(r, other.value) {
			t.Fatal("the sibling no longer authenticates")
		}
	})

	t.Run("R-AUTH-030 an administrator account's narrowed admin token rotates a sibling as an administrator", func(t *testing.T) {
		admin := e.held(r, e.stack.Token(adminBot, scope.Scope{Admin: true}))
		other := e.held(r, e.stack.Token(adminBot, narrowScopes["one repository"]))
		got := rotate(admin.value, other)
		succ, _ := got.JSON(t)["id"].(string)
		if got.Status != http.StatusCreated || succ == "" {
			t.Fatalf("%d %s", got.Status, got.Body)
		}
		// The administrator's rotation has no parent: the successor keeps the predecessor's lineage, here none.
		if n := e.scalar("SELECT count(*) FROM trove.api_token WHERE id = $1::uuid AND rotated_from = $2::uuid AND derived_from IS NULL", succ, other.id); n != 1 {
			t.Fatalf("the successor %s is not an administrator's rotation of %s", succ, other.id)
		}
	})

	t.Run("R-AUTH-030 a rotated narrowed token keeps the line of its predecessor", func(t *testing.T) {
		_, browser := e.browserSession(jack)
		created := e.stack.Do(r, "POST", "/api/v1/tokens", "", map[string]any{
			"description": "renews itself", "ttlSeconds": 7200, "scope": narrowWire, "selfRotatable": true,
		}, browser...)
		tv, _ := created.JSON(t)["token"].(string)
		if created.Status != http.StatusCreated || tv == "" {
			t.Fatalf("create through the session: %d %s", created.Status, created.Body)
		}
		before := e.held(r, tv)
		child := e.derive(r, before.value, 3600)
		got := e.stack.Do(r, "POST", "/api/v1/tokens/current/rotate", before.value, map[string]any{"overlapSeconds": 3600})
		next, _ := got.JSON(t)["token"].(string)
		if got.Status != http.StatusCreated || next == "" {
			t.Fatalf("self-rotation: %d %s", got.Status, got.Body)
		}
		if got := e.stack.Do(r, "GET", "/api/v1/tokens/"+child.id, next, nil); got.Status != http.StatusOK {
			t.Fatalf("the successor reads the child of its predecessor: %d %s", got.Status, got.Body)
		}
		if got := e.listed(r, "", next); !got[child.id] {
			t.Fatalf("the successor does not list the child of its predecessor: %v", got)
		}
		rot := e.stack.Do(r, "POST", "/api/v1/tokens/"+child.id+"/rotate", next, map[string]any{"overlapSeconds": 600})
		if rot.Status != http.StatusCreated {
			t.Fatalf("the successor rotates the child: %d %s", rot.Status, rot.Body)
		}
		if !e.works(r, child.value) {
			t.Fatal("the child ended with the rotation although it has an overlap")
		}
		if got := e.stack.Do(r, "DELETE", "/api/v1/tokens/"+child.id, next, nil); got.Status != http.StatusNoContent {
			t.Fatalf("the successor revokes the child: %d %s", got.Status, got.Body)
		}
		waitFor(t, "the revoked child refused", revocationBound, func() bool { return !e.works(r, child.value) })
		hidden(t, "the predecessor itself, not part of the line", e.stack.Do(r, "GET", "/api/v1/tokens/"+before.id, next, nil))
	})

	t.Run("R-AUTH-030 an inheriting token rotates any token of the owner", func(t *testing.T) {
		if got := rotate(inherit.value, sibling); got.Status != http.StatusCreated {
			t.Fatalf("%d %s", got.Status, got.Body)
		}
	})
}
