//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// E1.11, the API part: requests that span replicas, in the scatter profile (three web replicas, every request to
// another one). The Playwright journeys of R-UI-047 are the other half; e1_11_journeys_test.go is their hook.

// itemKey names a list item for the comparison of two walks.
func itemKey(t testing.TB, raw json.RawMessage) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("list item is no object: %s", raw)
	}
	if p, ok := m["principal"].(string); ok {
		if r, ok := m["role"].(string); ok {
			return r + "/" + p
		}
	}
	for _, k := range []string{"id", "key", "name", "username"} {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	t.Fatalf("list item without an identity: %s", raw)
	return ""
}

// walkRetry reads a list with the limit and waits out a 429: a walk of hundreds of pages with limit=1 is more
// requests than the read class admits in a burst.
func walkRetry(t testing.TB, a *api, path string, limit int) ([]string, map[string]int) {
	t.Helper()
	var keys []string
	replicas := map[string]int{}
	cursor := ""
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 0; page < 20000; page++ {
		u := fmt.Sprintf("%s%slimit=%d", path, sep, limit)
		if cursor != "" {
			u += "&cursor=" + cursor
		}
		var r reply
		for try := 0; ; try++ {
			r = a.get(u)
			if r.Status != 429 || try > 20 {
				break
			}
			secs, _ := strconv.Atoi(r.Header.Get("Retry-After"))
			time.Sleep(time.Duration(max(secs, 1)) * time.Second)
		}
		r.want(t, "GET "+u, 200)
		replicas[r.Replica()]++
		var p struct {
			Items      []json.RawMessage `json:"items"`
			NextCursor *string           `json:"nextCursor"`
		}
		r.JSON(t, &p)
		if len(p.Items) > limit {
			t.Fatalf("GET %s: %d items on a page of limit %d", u, len(p.Items), limit)
		}
		for _, it := range p.Items {
			keys = append(keys, itemKey(t, it))
		}
		if p.NextCursor == nil || *p.NextCursor == "" {
			return keys, replicas
		}
		cursor = urlEscape(*p.NextCursor)
	}
	t.Fatalf("GET %s: the list did not end", path)
	return nil, nil
}

func urlEscape(s string) string {
	return strings.NewReplacer("+", "%2B", "/", "%2F", "=", "%3D", "&", "%26", " ", "%20").Replace(s)
}

// seedLists makes sure every list has at least three items (the walk with limit=1 has to cross pages).
func seedLists(t testing.TB, adm *api) {
	t.Helper()
	for i := 1; i <= 3; i++ {
		n := fmt.Sprintf("e2e-list-%d", i)
		ensureRepo(t, adm, n)
		adm.do(http.MethodPost, "/api/v1/users", map[string]string{"username": n}).want(t, "user "+n, 201, 409)
		adm.do(http.MethodPost, "/api/v1/groups", map[string]string{"name": n}).want(t, "group "+n, 201, 409)
		ensureSA(t, adm, n)
		ensurePolicy(t, adm, n, []string{n}, [2]any{"sa:" + n, []string{"read"}})
		adm.do(http.MethodPut, "/api/v1/role-bindings/auditor/sa:"+n, nil).want(t, "role binding "+n, 200, 201)
		newSAToken(t, adm, n, 600, map[string]any{"inherit": true})
	}
}

// freshAdmin is a new token of the bootstrap administrator: the shared one has spent its read budget (R-API-016) on the
// walks of hundreds of pages.
func freshAdmin(t testing.TB, adm *api) *api {
	t.Helper()
	c := newSAToken(t, adm, "e2e-admin", 3600, map[string]any{"inherit": true, "admin": true})
	t.Cleanup(func() { adm.do(http.MethodDelete, "/api/v1/tokens/"+c.ID, nil) })
	return adm.withToken(c.Token)
}

func TestE1_11Scatter(t *testing.T) {
	useScatter(t)
	adm := adminAPI(t)
	ensureRepo(t, adm, "generic-local")

	t.Run("R-API-010 R-API-051 E1.11 every list operation walked to its last page with limit=1 over three replicas returns what one page of 500 returns", func(t *testing.T) {
		seedLists(t, adm)
		for i := 1; i <= 3; i++ {
			n := fmt.Sprintf("e2e-list-%d", i)
			adm.do(http.MethodPost, "/api/v1/endpoints/customers/exposure-rules", map[string]any{
				"name": n, "repository": "generic-local", "paths": map[string]any{"include": []string{n + "/**"}},
			}).want(t, "exposure rule "+n, 201, 409)
		}
		t.Cleanup(func() {
			for i := 1; i <= 3; i++ {
				n := fmt.Sprintf("e2e-list-%d", i)
				g := adm.get("/api/v1/endpoints/customers/exposure-rules/" + n)
				if g.Status == 200 {
					adm.do(http.MethodDelete, "/api/v1/endpoints/customers/exposure-rules/"+n, nil, "If-Match", g.Header.Get("ETag"))
				}
			}
		})
		lists := []struct{ name, path string }{
			{"repositories", "/api/v1/repositories"},
			{"users", "/api/v1/users"},
			{"groups", "/api/v1/groups"},
			{"service accounts", "/api/v1/service-accounts"},
			{"tokens", "/api/v1/tokens"},
			{"permission policies", "/api/v1/permission-policies"},
			{"role bindings", "/api/v1/role-bindings"},
			{"audit events", "/api/v1/audit/events?action=authz.policy.created"},
			{"exposure rules", "/api/v1/endpoints/customers/exposure-rules"},
			{"jobs", "/api/v1/system/jobs"},
		}
		for _, l := range lists {
			t.Run(l.name, func(t *testing.T) {
				before, _ := walkRetry(t, adm, l.path, 500)
				walked, replicas := walkRetry(t, adm, l.path, 1)
				after, _ := walkRetry(t, adm, l.path, 500)
				t.Logf("%s: %d items by one page of 500, %d by pages of 1 over replicas %v", l.path, len(before), len(walked), replicas)
				if len(before) < 3 {
					t.Fatalf("%s holds %d items: the walk crosses no page", l.path, len(before))
				}
				seen := map[string]bool{}
				for _, k := range walked {
					if seen[k] {
						t.Errorf("%s: %q appears twice in the walk", l.path, k)
					}
					seen[k] = true
				}
				// Lists that others write while we read (jobs, audit events, tokens) may have grown; the
				// walk must hold everything that was there before it and nothing that was not there after.
				afterSet := map[string]bool{}
				for _, k := range after {
					afterSet[k] = true
				}
				for _, k := range before {
					if !seen[k] {
						t.Errorf("%s: %q was in the list before the walk and is missing from it", l.path, k)
					}
				}
				for _, k := range walked {
					if !afterSet[k] {
						t.Errorf("%s: %q is in the walk and not in the list after it", l.path, k)
					}
				}
				if len(replicas) < 2 {
					t.Errorf("%s: the pages were served by %v, want at least two replicas", l.path, sortedKeys(replicas))
				}
			})
		}
	})

	adm = freshAdmin(t, adm)

	t.Run("R-API-014 R-API-051 E1.11 an Idempotency-Key replayed through another replica returns the stored outcome", func(t *testing.T) {
		name := fmt.Sprintf("e2e-idem-%d", time.Now().Unix())
		t.Cleanup(func() { adm.do(http.MethodDelete, "/api/v1/groups/"+name, nil) })
		key := fmt.Sprintf("e2e-%d-%d", os.Getpid(), time.Now().UnixNano())
		body := map[string]string{"name": name, "description": "idempotent"}
		first := adm.do(http.MethodPost, "/api/v1/groups", body, "Idempotency-Key", key).want(t, "the first create", 201)
		if first.Header.Get("Idempotency-Replayed") != "" {
			t.Errorf("the first answer is marked as a replay")
		}
		replicas := map[string]bool{first.Replica(): true}
		other := 0
		for i := 0; i < 15; i++ {
			r := adm.do(http.MethodPost, "/api/v1/groups", body, "Idempotency-Key", key)
			if r.Status != 201 || r.Header.Get("Idempotency-Replayed") != "true" {
				t.Fatalf("replay %d (replica %s) = %s, replayed %q; want the stored 201 marked Idempotency-Replayed: true", i, r.Replica(), r, r.Header.Get("Idempotency-Replayed"))
			}
			if string(r.Body) != string(first.Body) || r.Header.Get("Location") != first.Header.Get("Location") {
				t.Errorf("replay %d differs from the first answer:\n%s\n%s", i, r.Body, first.Body)
			}
			if r.Replica() != first.Replica() {
				other++
			}
			replicas[r.Replica()] = true
		}
		t.Logf("first answered by %s; replays by %v (%d on another replica)", first.Replica(), sortedKeys(replicas), other)
		if other == 0 || len(replicas) < 3 {
			t.Errorf("the replays reached %v: want all three replicas", sortedKeys(replicas))
		}
		reused := adm.do(http.MethodPost, "/api/v1/groups", map[string]string{"name": name + "-x"}, "Idempotency-Key", key)
		if reused.Status != 422 || reused.Code() != "idempotency_key_reused" {
			t.Errorf("the key with another body = %s, want 422 idempotency_key_reused", reused)
		}
		if g := adm.get("/api/v1/groups/" + name + "-x"); g.Status != 404 {
			t.Errorf("the refused request created the group: %s", g)
		}
	})

	t.Run("R-API-052 R-API-051 E1.11 a management read on another replica shows a write at once", func(t *testing.T) {
		replicas := map[string]bool{}
		name := fmt.Sprintf("e2e-vis-%d", time.Now().Unix())
		t.Cleanup(func() { adm.do(http.MethodDelete, "/api/v1/groups/"+name, nil) })
		adm.do(http.MethodPost, "/api/v1/groups", map[string]string{"name": name, "description": "v0"}).want(t, "create group", 201)
		for i := 1; i <= 24; i++ {
			desc := "v" + strconv.Itoa(i)
			w := adm.do(http.MethodPatch, "/api/v1/groups/"+name, `{"description":"`+desc+`"}`, "Content-Type", "application/merge-patch+json").want(t, "patch "+desc, 200)
			for k := 0; k < 3; k++ { // three reads in a row reach the other replicas
				r := adm.get("/api/v1/groups/"+name).want(t, "read after write", 200)
				var g struct{ Description string }
				r.JSON(t, &g)
				if g.Description != desc {
					t.Fatalf("write %s through %s, read %d through %s shows %q", desc, w.Replica(), k, r.Replica(), g.Description)
				}
				replicas[r.Replica()] = true
			}
			time.Sleep(150 * time.Millisecond) // the write class admits 10 per second after its burst of 50 (R-API-016)
			// a repository created through one replica is usable at once through the others
			if i%8 == 0 {
				key := fmt.Sprintf("e2e-vis-%d-%d", time.Now().Unix(), i)
				adm.do(http.MethodPost, "/api/v1/repositories", map[string]string{"key": key, "class": "local", "format": "generic"}).want(t, "create repository", 201)
				for k := 0; k < 3; k++ { // one request per replica in turn
					r := adm.do(http.MethodPut, "/files/"+key+"/a.txt", "x")
					if r.Status != 201 && r.Status != 200 {
						t.Fatalf("upload to the new repository %s through %s = %s", key, r.Replica(), r)
					}
				}
				adm.do(http.MethodDelete, "/api/v1/repositories/"+key, nil)
			}
		}
		if len(replicas) != 3 {
			t.Errorf("the reads reached %v, want all three replicas", sortedKeys(replicas))
		}
	})

	// R-AUTH-042: the suite runs with Valkey up, with Valkey stopped for its whole duration, and with FLUSHALL in
	// the middle. The pacing keeps the browser logins below starts_per_address (30 per minute, R-AUTH-038): they
	// all come from the host's address behind the edge.
	t.Run("R-AUTH-042 E1.11 browser logins, re-authentication, logout, revocation and a break-glass lockout on consecutive requests to different replicas", func(t *testing.T) {
		r042Suite(t, adm, "Valkey up", 50, false)
	})
	t.Run("R-AUTH-042 R-AUTH-023 E1.11 the same suite with Valkey stopped for its whole duration, every replica staying ready with degraded valkey", func(t *testing.T) {
		withValkeyStopped(t, func() {
			webReadyDegraded(t, true)
			r042Suite(t, adm, "Valkey stopped", 25, false)
			webReadyDegraded(t, true)
		})
	})
	t.Run("R-AUTH-042 R-AUTH-023 E1.11 the same suite with FLUSHALL in the middle: no login, session or token request fails, no revoked token revives", func(t *testing.T) {
		r042Suite(t, adm, "FLUSHALL", 25, true)
	})
}

// r042Suite is the acceptance suite of R-AUTH-042 in one mode. logins is the number of browser logins; flush
// runs valkey-cli FLUSHALL at the points where a lost counter or marker could hurt.
func r042Suite(t *testing.T, adm *api, mode string, logins int, flush bool) {
	alicePw := secretValue(t, "trove-keycloak", "alice-password")
	bobPw := secretValue(t, "trove-keycloak", "bob-password")
	bgPw := secretValue(t, "trove-break-glass", "password")
	flushall := func() {
		if flush {
			valkeyPrimaryCLI(t, "flushall")
		}
	}

	t.Run(mode+" break-glass lockout across replicas", func(t *testing.T) {
		addrs, stop := podForwards(t)
		defer stop()
		direct := newAPI(t, withRoundRobin(addrs...))
		unlock := func() { adm.do(http.MethodPost, "/api/v1/users/admin/unlock", nil).want(t, "unlock", 204) }
		unlock()
		t.Cleanup(unlock)
		for i := 0; i < 5; i++ { // one after another, three replicas in turn
			r := newBrowserOn(t, direct).localLogin("admin", fmt.Sprintf("wrong-password-%d-0123456789", i))
			if r.Status != 401 || r.Code() != "invalid_credentials" {
				t.Fatalf("wrong password %d = %s, want 401 invalid_credentials", i, r)
			}
		}
		seen := map[string]bool{}
		for i := 0; i < 6; i++ {
			r := newBrowserOn(t, direct).localLogin("admin", bgPw)
			seen[r.Replica()] = true
			if r.Status != 429 || r.Code() != "break_glass_locked" || r.Header.Get("Retry-After") == "" {
				t.Fatalf("the correct password while locked, try %d = %s, want 429 break_glass_locked with Retry-After", i, r)
			}
		}
		if len(seen) != 3 {
			t.Errorf("the locked answers came from %v, want all three replicas", sortedKeys(seen))
		}
		flushall() // the lock lives in PostgreSQL: a flushed Valkey does not hand the guesser a new threshold
		if r := newBrowserOn(t, direct).localLogin("admin", bgPw); r.Status != 429 || r.Code() != "break_glass_locked" {
			t.Errorf("after FLUSHALL the locked account answers %s, want 429 break_glass_locked", r)
		}
		var u struct {
			LockedUntil *string `json:"lockedUntil"`
		}
		adm.get("/api/v1/users/admin").want(t, "the break-glass user", 200).JSON(t, &u)
		if u.LockedUntil == nil {
			t.Error("the user record shows no lockedUntil")
		}
		unlock()
		for i := 0; i < 3; i++ {
			newBrowserOn(t, direct).localLogin("admin", bgPw).want(t, "the break-glass login after the unlock", 204)
		}

		// Five wrong passwords at the same moment to three replicas lock it as well.
		var wg sync.WaitGroup
		results := make([]reply, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				b := newBrowserOn(t, direct)
				r, err := b.send(http.MethodPost, "/auth/local/login", map[string]string{"username": "admin", "password": fmt.Sprintf("wrong-at-once-%d-0123456789", i)}, "Origin", direct.base)
				if err == nil {
					results[i] = r
				}
			}(i)
		}
		wg.Wait()
		t.Logf("six wrong passwords at once: %d %d %d %d %d %d", results[0].Status, results[1].Status, results[2].Status, results[3].Status, results[4].Status, results[5].Status)
		if r := newBrowserOn(t, direct).localLogin("admin", bgPw); r.Status != 429 || r.Code() != "break_glass_locked" {
			t.Errorf("after five concurrent failures the correct password = %s, want 429 break_glass_locked", r)
		}
		unlock()
		newBrowserOn(t, direct).localLogin("admin", bgPw).want(t, "the break-glass login after the second unlock", 204)
	})

	t.Run(mode+" revocation reaches every replica", func(t *testing.T) {
		b := newBrowser(t)
		b.oidcLogin("alice", alicePw)
		r := b.do(http.MethodPost, "/api/v1/tokens", map[string]any{"description": "e2e r042", "ttlSeconds": 600, "scope": map[string]any{"inherit": true}}).want(t, "alice's PAT", 201)
		var c createdToken
		r.JSON(t, &c)
		tok := newAPI(t).withToken(c.Token)
		seen := map[string]bool{}
		for i := 0; i < 9; i++ {
			w := tok.get("/api/v1/auth/whoami").want(t, "the PAT before the revocation", 200)
			seen[w.Replica()] = true
		}
		if len(seen) != 3 {
			t.Errorf("the PAT was used on %v before the revocation, want all three replicas", sortedKeys(seen))
		}
		b.do(http.MethodDelete, "/api/v1/tokens/"+c.ID, nil).want(t, "revoke the PAT", 204)
		start := time.Now()
		var lastOK time.Duration
		replicas := map[string]bool{}
		for time.Since(start) < 3*time.Second {
			time.Sleep(100 * time.Millisecond) // every 401 counts as a rejected credential (R-AUTH-038): do not spend the address's budget
			w := tok.get("/api/v1/auth/whoami")
			switch w.Status {
			case 200:
				lastOK = time.Since(start)
			case 401:
				replicas[w.Replica()] = true
			case 429:
				t.Fatalf("whoami with the revoked token = %s: the paced loop spent the failure budget", w)
			default:
				t.Fatalf("whoami with the revoked token = %s", w)
			}
		}
		t.Logf("the revoked PAT: last 200 %s after the revocation answered; 401 from %v", lastOK.Round(time.Millisecond), sortedKeys(replicas))
		if lastOK > 2*time.Second {
			t.Errorf("a replica still accepted the token %s after the revocation (auth.revocation_bound is 2 s)", lastOK)
		}
		if len(replicas) != 3 {
			t.Errorf("401 came from %v, want all three replicas", sortedKeys(replicas))
		}
		flushall() // nothing in Valkey keeps a revoked token alive, and a flush must not bring it back
		for i := 0; i < 9; i++ {
			if w := tok.get("/api/v1/auth/whoami"); w.Status != 401 {
				t.Errorf("after FLUSHALL the revoked token answers %s on %s, want 401", w, w.Replica())
			}
		}
	})

	t.Run(mode+" re-authentication, logout", func(t *testing.T) {
		b := newBrowser(t)
		b.oidcLogin("bob", bobPw)
		oldSession := b.cookie("__Host-trv_session")
		// A session that signed in two hours ago may not mint tokens (R-AUTH-011).
		sql(t, `UPDATE trove.ui_session SET authenticated_at = now() - interval '2 hours' WHERE id IN (SELECT s.id FROM trove.ui_session s JOIN trove.principal p ON p.id = s.principal_id WHERE p.kind = 'user' AND p.name = 'bob' AND s.ended_at IS NULL)`)
		time.Sleep(4 * time.Second) // the session caches of the replicas re-read within their poll
		tokenReq := map[string]any{"description": "e2e reauth", "ttlSeconds": 600, "scope": map[string]any{"inherit": true}}
		for i := 0; i < 3; i++ {
			r := b.do(http.MethodPost, "/api/v1/tokens", tokenReq)
			if r.Status != 401 || r.Code() != "reauthentication_required" {
				t.Fatalf("a token from a session of two hours ago, try %d on %s = %s, want 401 reauthentication_required", i, r.Replica(), r)
			}
		}
		// The silent round trip: the IdP's session is alive, so the flow ends without a password.
		trace := b.reauth(bobPw)
		if trace.SawLoginForm {
			t.Error("the re-authentication round trip asked for the password: the IdP session should have answered silently")
		}
		b.do(http.MethodPost, "/api/v1/tokens", tokenReq).want(t, "a token after the re-authentication", 201)
		sessionEnded(t, oldSession) // the session the re-authentication replaced
		// logout, then the cookie is dead on every replica
		sess := b.cookie("__Host-trv_session")
		b.do(http.MethodPost, "/auth/logout", nil).want(t, "logout", 200)
		sessionEnded(t, sess)
	})

	t.Run(fmt.Sprintf("%s %d browser logins", mode, logins), func(t *testing.T) {
		started := time.Now()
		sameReplica, failed := 0, 0
		seen := map[string]bool{}
		for i := 0; i < logins; i++ {
			tick := time.Now()
			if flush && i == logins/2 {
				valkeyPrimaryCLI(t, "flushall")
			}
			user, pw := "alice", alicePw
			if i%2 == 1 {
				user, pw = "bob", bobPw
			}
			b := newBrowser(t)
			tr := b.oidcLogin(user, pw)
			if tr.Final.Status != 200 {
				failed++
				t.Errorf("login %d (%s): the UI answered %s", i, user, tr.Final)
				continue
			}
			if tr.StartReply.Replica() == tr.CallbackReply.Replica() {
				sameReplica++
			}
			seen[tr.StartReply.Replica()] = true
			seen[tr.CallbackReply.Replica()] = true
			for k := 0; k < 2; k++ { // the session on the next requests, on other replicas
				if r, name := b.whoami(); r.Status != 200 || name != user {
					failed++
					t.Errorf("login %d (%s): whoami %d on %s = %s", i, user, k, r.Replica(), r)
				}
			}
			if left := 2200*time.Millisecond - time.Since(tick); left > 0 {
				time.Sleep(left)
			}
		}
		t.Logf("%d logins in %s, %d failed; start and callback on the same replica in %d, replicas seen %v", logins, time.Since(started).Round(time.Second), failed, sameReplica, sortedKeys(seen))
		if len(seen) != 3 {
			t.Errorf("the logins reached %v, want all three replicas", sortedKeys(seen))
		}
		if sameReplica == logins {
			t.Error("start and callback were always served by the same replica: nothing scattered")
		}
	})
}

// newBrowserOn is a browser whose connections go the way of the given client's route (the pod forwards).
func newBrowserOn(t testing.TB, via *api) *browser {
	return &browser{newAPI(t, withJar(), withRoute(via.rt))}
}

// reauth walks the silent re-authentication round trip of R-AUTH-011.
func (b *browser) reauth(password string) loginTrace {
	b.t.Helper()
	return b.walkLogin(b.base+"/auth/oidc/keycloak/start?purpose=reauth&return_to=%2Fui%2F", "bob", password)
}

// ── the hook for the browser journeys of R-UI-047 ────────────────────────────

// TestE1_11Journeys runs the Playwright journeys of R-UI-047 against the scatter profile when the suite exists
// (web/e2e/playwright.config.ts), with the edge URL, the dev CA and the users of the test IdP in the environment.
// The screens are being built; until the suite lands this test says so and skips, so the gap is in the log and
// not silent. The suite's contract: `npx playwright test` in web/e2e, base URL TROVE_E2E_EDGE_URL, the CA in
// NODE_EXTRA_CA_CERTS, passwords in TROVE_E2E_ALICE_PASSWORD, TROVE_E2E_BOB_PASSWORD and
// TROVE_E2E_BREAKGLASS_PASSWORD, the administrator token in TROVE_E2E_ADMIN_TOKEN.
func TestE1_11Journeys(t *testing.T) {
	dir := filepath.Join(cfg.repo, "web", "e2e")
	if _, err := os.Stat(filepath.Join(dir, "playwright.config.ts")); err != nil {
		t.Skip("R-UI-047 E1.11: the Playwright journeys are not part of the repository yet (web/e2e/playwright.config.ts); this test runs them as soon as they are")
	}
	useScatter(t)
	cmd := exec.Command("npx", "playwright", "test")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TROVE_E2E_EDGE_URL="+cfg.edgeURL,
		"NODE_EXTRA_CA_CERTS="+cfg.caFile,
		"TROVE_E2E_ALICE_PASSWORD="+secretValue(t, "trove-keycloak", "alice-password"),
		"TROVE_E2E_BOB_PASSWORD="+secretValue(t, "trove-keycloak", "bob-password"),
		"TROVE_E2E_BREAKGLASS_PASSWORD="+secretValue(t, "trove-break-glass", "password"),
		"TROVE_E2E_ADMIN_TOKEN="+adminToken(t),
	)
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(cfg.out, "playwright.log"), out, 0o644)
	if err != nil {
		t.Fatalf("playwright: %v\n%s", err, tail(string(out), 4000))
	}
}
