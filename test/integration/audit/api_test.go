//go:build integration

package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/repository"
	itstack "github.com/guided-traffic/trove/test/integration/stack"
)

// The audit operations over HTTP (R-OPS-017) on three web replicas that share one
// database, and exit criterion E1.5: the events the access core produces are found
// through GET /api/v1/audit/events and appear in the log stream. The login and
// token events are produced by the identity and login areas, whose producers are
// not part of this stack; their event types are covered by their own areas' tests.

const events = "/api/v1/audit/events"

type apiFx struct {
	e              *itstack.Env
	a, b, c        *itstack.Replica
	cl             *itstack.Cluster
	admin, auditor itstack.Person
	bob            itstack.Person
	val            *apitest.Validator
}

func newAPIFx(t *testing.T, o itstack.Options) *apiFx {
	t.Helper()
	e := itstack.New(t)
	f := &apiFx{e: e, val: apitest.New(t, v1.EmbeddedYAML())}
	f.admin, f.auditor, f.bob = e.User("sec-alice"), e.User("audrey"), e.User("bob")
	e.Admin(f.admin)
	e.Exec("INSERT INTO authz_role_binding (role, principal_id) VALUES ('auditor', $1::uuid)", f.auditor.ID)
	e.Repo("npm-internal", "local", "npm")
	e.Repo("npm-other", "local", "npm")
	o.Validator = f.val
	f.a, f.b, f.c = e.NewReplica("web-a", o), e.NewReplica("web-b", o), e.NewReplica("web-c", o)
	f.cl = itstack.NewCluster(f.a, f.b, f.c)
	return f
}

func (f *apiFx) inLog(id string) bool {
	for _, r := range []*itstack.Replica{f.a, f.b, f.c} {
		if r.Log.Has(id) {
			return true
		}
	}
	return false
}

func subject(p itstack.Person) authz.Subject {
	return authz.Subject{Kind: p.Kind, ID: p.ID, Name: p.Name}
}

func deploy(repo, path string) authz.Coordinate {
	return authz.Coordinate{Repository: repo, Path: path, Kind: authz.KindWrite, Actions: []authz.Action{authz.Deploy}}
}

func want(t *testing.T, r itstack.Response, status int, code string) {
	t.Helper()
	if r.Status != status || (code != "" && r.Code() != code) {
		t.Fatalf("answered %d %q, want %d %q\n%s", r.Status, r.Code(), status, code, r.Body)
	}
}

// find polls the query until at least n events match, because the asynchronous
// events reach the table with the next batch.
func (f *apiFx) find(t *testing.T, query string, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := f.cl.Do(f.auditor.Name, "GET", events+"?"+query, nil)
		want(t, r, 200, "")
		if items := r.Items(t); len(items) >= n || time.Now().After(deadline) {
			if len(items) < n {
				t.Fatalf("%s: %d events, want at least %d", query, len(items), n)
			}
			return items
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func detail(ev map[string]any, key string) any {
	d, _ := ev["details"].(map[string]any)
	return d[key]
}

const policiesPath = "/api/v1/permission-policies"

func policy(name string) map[string]any {
	return map[string]any{
		"name": name, "resources": map[string]any{"repositories": []string{"npm-internal"}},
		"entries": []any{map[string]any{"principal": "user:bob", "actions": []string{"read"}}},
	}
}

// TestE1_5_PolicyChangesAndDeniedDeploysAreFoundAndLogged: every policy change and
// every denied deploy produces an event that the query finds and that appears in
// the log stream.
func TestE1_5_PolicyChangesAndDeniedDeploysAreFoundAndLogged(t *testing.T) {
	f := newAPIFx(t, itstack.Options{})
	me := f.admin.Name

	created := f.a.Do(me, "POST", policiesPath, policy("p1"))
	want(t, created, 201, "")
	want(t, f.b.Do(me, "PUT", policiesPath+"/p1", policy("p1"), "If-Match", `"1"`, "Trove-Authz-Min-Epoch", fmt.Sprint(created.Epoch())), 200, "")
	want(t, f.c.Do(me, "PUT", "/api/v1/role-bindings/webhook-admin/user:bob", nil), 201, "")
	want(t, f.a.Do(me, "DELETE", policiesPath+"/p1", nil, "Trove-Authz-Min-Epoch", fmt.Sprint(f.e.AuthzEpoch())), 204, "")
	want(t, f.a.Do(me, "DELETE", "/api/v1/role-bindings/webhook-admin/user:bob", nil, "Trove-Authz-Min-Epoch", fmt.Sprint(f.e.AuthzEpoch())), 204, "")

	t.Run("R-OPS-017 every policy and role change is found, newest first, and is in the log stream", func(t *testing.T) {
		items := f.find(t, "action=authz.*&limit=100", 5)
		got := map[string]int{}
		for _, ev := range items {
			got[ev["action"].(string)]++
			if ev["category"] != "authz" || ev["outcome"] != "success" {
				t.Errorf("%v", ev)
			}
			if a := ev["actor"].(map[string]any); a["type"] != "user" || a["name"] != "sec-alice" {
				t.Errorf("actor %v", a)
			}
			if !f.inLog(ev["id"].(string)) {
				t.Errorf("%s %v is not in the log stream", ev["action"], ev["id"])
			}
			if detail(ev, "epoch") == nil {
				t.Errorf("%s carries no epoch: %v", ev["action"], ev["details"])
			}
		}
		for action, n := range map[string]int{"authz.policy.created": 1, "authz.policy.updated": 1, "authz.policy.deleted": 1, "authz.role.bound": 1, "authz.role.unbound": 1} {
			if got[action] != n {
				t.Errorf("%s: %d events, want %d (%v)", action, got[action], n, got)
			}
		}
		for i := 1; i < len(items); i++ {
			if items[i-1]["occurredAt"].(string) < items[i]["occurredAt"].(string) {
				t.Fatalf("not newest first at %d", i)
			}
		}
		// A single event by its ID.
		one := items[0]
		g := f.cl.Do(f.auditor.Name, "GET", events+"/"+one["id"].(string), nil)
		want(t, g, 200, "")
		if g.JSON(t)["action"] != one["action"] {
			t.Fatalf("%v", g.JSON(t))
		}
	})
	t.Run("E1.5 a denied deploy is found by actor, action and outcome and is in the log stream", func(t *testing.T) {
		d := f.b.Engine.Decide(context.Background(), subject(f.bob), deploy("npm-internal", "a/b.zip"))
		if d.Outcome != authz.DenyVisible {
			t.Fatalf("%+v", d)
		}
		items := f.find(t, "action=authz.denied&outcome=denied&actor=bob", 1)
		ev := items[0]
		if ev["repository"] != "npm-internal" || ev["path"] != "a/b.zip" || detail(ev, "required_action") != "deploy" || ev["reason"] != "deny.no-grant" {
			t.Fatalf("%v", ev)
		}
		if !f.inLog(ev["id"].(string)) {
			t.Fatal("not in the log stream")
		}
		// The same by the actor's ID and by the repository filter.
		f.find(t, "actor="+f.bob.ID+"&repository=npm-internal&category=authz", 1)
	})
	t.Run("E1.5 R-OPS-017 a refused management operation is found by the trace ID the client received", func(t *testing.T) {
		r := f.b.Do(f.bob.Name, "POST", policiesPath, policy("nope"))
		want(t, r, 403, "forbidden")
		trace := r.Header.Get("X-Request-Id")
		if trace == "" {
			t.Fatal("no X-Request-Id")
		}
		items := f.find(t, "traceId="+trace, 1)
		ev := items[0]
		src, _ := ev["source"].(map[string]any)
		if ev["action"] != "authz.denied" || detail(ev, "permission") != "policies:write" || src["ip"] != "127.0.0.1" {
			t.Fatalf("%v", ev)
		}
	})
}

// TestR_OPS_017_PermissionAndFilters: the capability audit:read guards the three
// operations (H-123), and the filters are validated.
func TestR_OPS_017_PermissionAndFilters(t *testing.T) {
	f := newAPIFx(t, itstack.Options{})
	want(t, f.cl.Do(f.admin.Name, "POST", policiesPath, policy("p")), 201, "")
	id := f.cl.Do(f.admin.Name, "GET", events, nil).Items(t)[0]["id"].(string)
	from, to := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	t.Run("R-OPS-017 H-123 a caller without audit:read is refused on every audit operation", func(t *testing.T) {
		for _, p := range []string{events, events + "/" + id, events + "/export?from=" + from + "&to=" + to} {
			want(t, f.cl.Do(f.bob.Name, "GET", p, nil), 403, "forbidden")
			want(t, f.cl.Do("", "GET", p, nil), 401, "")
		}
		// The auditor and the administrator hold it.
		for _, who := range []string{f.auditor.Name, f.admin.Name} {
			want(t, f.cl.Do(who, "GET", events, nil), 200, "")
		}
	})
	t.Run("R-OPS-017 a refusal is itself an event of the refused caller", func(t *testing.T) {
		items := f.find(t, "action=authz.denied&actor=bob", 1)
		if detail(items[0], "permission") != "audit:read" {
			t.Fatalf("%v", items[0])
		}
	})
	t.Run("R-OPS-017 the filters are validated", func(t *testing.T) {
		for _, q := range []string{"category=nope", "outcome=nope", "from=yesterday", "to=tomorrow", "limit=0", "limit=x", "unknown=1", "action=a&action=b"} {
			want(t, f.cl.Do(f.auditor.Name, "GET", events+"?"+q, nil), 400, "")
		}
		want(t, f.cl.Do(f.auditor.Name, "GET", events+"?from="+to+"&to="+from, nil), 400, "invalid_audit_range")
		want(t, f.cl.Do(f.auditor.Name, "GET", events+"?cursor=bm90LWEtY3Vyc29y", nil), 400, "invalid_cursor")
		want(t, f.cl.Do(f.auditor.Name, "GET", events+"/not-an-id", nil), 404, "not_found")
		want(t, f.cl.Do(f.auditor.Name, "GET", events+"/01933b1e-7c2a-7f4e-9d1b-6a2f0c9e8b11", nil), 404, "not_found")
	})
	t.Run("R-OPS-017 the prefix of an action matches whole segments", func(t *testing.T) {
		if n := len(f.cl.Do(f.auditor.Name, "GET", events+"?action=authz.policy.*", nil).Items(t)); n != 1 {
			t.Fatalf("authz.policy.*: %d", n)
		}
		if n := len(f.cl.Do(f.auditor.Name, "GET", events+"?action=authz.pol.*", nil).Items(t)); n != 0 {
			t.Fatalf("authz.pol.*: %d", n)
		}
		if n := len(f.cl.Do(f.auditor.Name, "GET", events+"?action=authz.policy.created", nil).Items(t)); n != 1 {
			t.Fatalf("exact: %d", n)
		}
	})
}

// TestR_OPS_017_PaginationWalkAcrossReplicas: a walk with limit=1 visits every
// event once while another replica keeps writing, each page answered by the next
// replica.
func TestR_OPS_017_PaginationWalkAcrossReplicas(t *testing.T) {
	f := newAPIFx(t, itstack.Options{})
	me := f.admin.Name
	for i := range 6 {
		want(t, f.cl.Do(me, "POST", policiesPath, policy(fmt.Sprintf("walk-%d", i))), 201, "")
	}
	all := f.cl.Do(me, "GET", events+"?limit=1000", nil).Items(t)
	if len(all) != 6 {
		t.Fatalf("%d events", len(all))
	}
	var seen []string
	next := events + "?limit=1"
	for page := 0; page < 20; page++ {
		r := f.cl.Do(me, "GET", next, nil)
		want(t, r, 200, "")
		for _, it := range r.Items(t) {
			seen = append(seen, it["id"].(string))
		}
		if page == 2 {
			// A write between two pages is newer than the cursor and neither repeats nor skips an event.
			want(t, f.c.Do(me, "POST", policiesPath, policy("late")), 201, "")
		}
		c, _ := r.JSON(t)["nextCursor"].(string)
		if c == "" {
			if r.Header.Get("Link") != "" {
				t.Fatal("the last page has a Link header")
			}
			break
		}
		if !strings.Contains(r.Header.Get("Link"), `rel="next"`) {
			t.Fatalf("Link = %q", r.Header.Get("Link"))
		}
		next = events + "?limit=1&cursor=" + url.QueryEscape(c)
	}
	if len(seen) != 6 {
		t.Fatalf("walked %d events, want 6: %v", len(seen), seen)
	}
	for i, ev := range all {
		if seen[i] != ev["id"] {
			t.Fatalf("position %d: %s, the full page has %v", i, seen[i], ev["id"])
		}
	}
	// A cursor of another filter is refused.
	r := f.cl.Do(me, "GET", events+"?limit=1", nil)
	c := r.JSON(t)["nextCursor"].(string)
	want(t, f.cl.Do(me, "GET", events+"?limit=1&action=authz.*&cursor="+url.QueryEscape(c), nil), 400, "invalid_cursor")
}

// TestR_OPS_017_DenialSummariesStayCorrect: the producer sends every refusal and
// the audit writer alone collapses them (Q-AUTHZ-24), so a summary carries the
// exact count and repeat_of; different keys are not collapsed; an anonymous caller
// is keyed by client address (H-101).
func TestR_OPS_017_DenialSummariesStayCorrect(t *testing.T) {
	f := newAPIFx(t, itstack.Options{Audit: config.Audit{DenialDedupWindow: 1500 * time.Millisecond, RecordAnonymousDenials: true}})
	bob := subject(f.bob)
	ctx := context.Background()
	for range 5 {
		f.a.Engine.Decide(ctx, bob, deploy("npm-internal", "x.zip"))
	}
	// Another path, another repository and another action are other keys.
	f.a.Engine.Decide(ctx, bob, deploy("npm-internal", "y.zip"))
	f.a.Engine.Decide(ctx, bob, deploy("npm-other", "x.zip"))
	f.a.Engine.Decide(ctx, bob, authz.Coordinate{Repository: "npm-internal", Path: "x.zip", Kind: authz.KindWrite, Actions: []authz.Action{authz.Delete}})

	t.Run("R-OPS-015 five refusals of one key are one event and one summary of four", func(t *testing.T) {
		// first events of four keys, then the summary of the first one
		items := f.find(t, "action=authz.denied&actor=bob&limit=100", 5)
		var first, summary map[string]any
		for _, ev := range items {
			if ev["path"] == "x.zip" && ev["repository"] == "npm-internal" && detail(ev, "required_action") == "deploy" {
				if detail(ev, "repeat_of") != nil {
					summary = ev
				} else {
					first = ev
				}
			}
		}
		if first == nil || summary == nil {
			t.Fatalf("first %v summary %v among %d events", first, summary, len(items))
		}
		if detail(first, "count") != float64(1) || detail(summary, "count") != float64(4) || detail(summary, "repeat_of") != first["id"] {
			t.Fatalf("first %v\nsummary %v", first["details"], summary["details"])
		}
		if len(items) != 5 {
			t.Fatalf("%d events, want 4 first events and 1 summary", len(items))
		}
		sum := 0.0
		for _, ev := range items {
			if ev["path"] == "x.zip" && ev["repository"] == "npm-internal" && detail(ev, "required_action") == "deploy" {
				sum += detail(ev, "count").(float64)
			}
		}
		if sum != 5 {
			t.Fatalf("the counts of the key add up to %v, want 5", sum)
		}
	})
	t.Run("H-101 anonymous refusals are keyed by client address, not by operation", func(t *testing.T) {
		for _, p := range []string{policiesPath, "/api/v1/role-bindings", events} {
			want(t, f.a.Do("", "GET", p, nil), 401, "")
		}
		items := f.find(t, "action=authz.denied&limit=100", 7)
		var anon []map[string]any
		for _, ev := range items {
			if ev["actor"].(map[string]any)["type"] == "anonymous" {
				anon = append(anon, ev)
			}
		}
		if len(anon) != 2 {
			t.Fatalf("%d anonymous events, want a first event and one summary: %v", len(anon), anon)
		}
		total := 0.0
		for _, ev := range anon {
			total += detail(ev, "count").(float64)
			if ev["source"].(map[string]any)["ip"] != "127.0.0.1" {
				t.Errorf("source = %v", ev["source"])
			}
		}
		if total != 3 {
			t.Fatalf("counts add up to %v, want 3", total)
		}
	})
}

// TestR_OPS_017_Export streams the range in both formats, oldest first and complete
// over several batches, records audit.exported before the first byte, holds no
// transaction while the client reads, and refuses a bad range.
func TestR_OPS_017_Export(t *testing.T) {
	f := newAPIFx(t, itstack.Options{})
	me := f.admin.Name
	const bulk = 2300 // more than two batches of 1000
	f.e.Exec(`INSERT INTO audit_event (id, occurred_at, action, category, outcome, actor_type, actor_name, user_agent, details)
		SELECT gen_random_uuid(), now() - (g * interval '1 millisecond') - interval '1 minute', 'bulk.generated', 'system', 'success',
		       'system', 'generator', CASE WHEN g = 7 THEN '=cmd|calc' ELSE 'it' END, jsonb_build_object('n', g, 'pad', repeat('x', 2000))
		  FROM generate_series(1, $1::int) g`, bulk)
	from, to := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	exportURL := events + "/export?from=" + from + "&to=" + to

	t.Run("R-OPS-017 the JSON Lines export holds each event once, oldest first, over several batches", func(t *testing.T) {
		r := f.a.Do(f.auditor.Name, "GET", exportURL+"&action=bulk.generated", nil)
		want(t, r, 200, "")
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
			t.Fatalf("Content-Type = %q", ct)
		}
		sc := bufio.NewScanner(bytes.NewReader(r.Body))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		seen, prev := map[string]bool{}, ""
		for sc.Scan() {
			var ev map[string]any
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				t.Fatalf("line %q: %v", sc.Text(), err)
			}
			id, at := ev["id"].(string), ev["occurredAt"].(string)
			if seen[id] {
				t.Fatalf("%s twice", id)
			}
			seen[id] = true
			if at < prev {
				t.Fatalf("%s after %s: not oldest first", at, prev)
			}
			prev = at
		}
		if len(seen) != bulk {
			t.Fatalf("%d events, want %d", len(seen), bulk)
		}
	})
	t.Run("R-OPS-017 every export is recorded as audit.exported before it streams", func(t *testing.T) {
		n := f.e.Number("SELECT count(*) FROM audit_event WHERE action = 'audit.exported'")
		if n != 1 {
			t.Fatalf("%d audit.exported events after one export", n)
		}
		ev := f.find(t, "action=audit.exported", 1)[0]
		fl, _ := detail(ev, "filters").(map[string]any)
		if detail(ev, "format") != "jsonl" || fl["action"] != "bulk.generated" || ev["actor"].(map[string]any)["name"] != "audrey" || ev["outcome"] != "success" {
			t.Fatalf("%v", ev)
		}
		if !f.inLog(ev["id"].(string)) {
			t.Fatal("not in the log stream")
		}
	})
	t.Run("R-OPS-017 the CSV export has the header, defuses a formula cell and quotes", func(t *testing.T) {
		r := f.b.Do(f.auditor.Name, "GET", exportURL+"&action=bulk.generated&format=csv", nil)
		want(t, r, 200, "")
		rows, err := csv.NewReader(bytes.NewReader(r.Body)).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != bulk+1 || rows[0][0] != "id" || rows[0][len(rows[0])-1] != "details" {
			t.Fatalf("%d rows, header %v", len(rows), rows[0])
		}
		formula := 0
		for _, row := range rows[1:] {
			if len(row) != len(rows[0]) {
				t.Fatalf("row %v", row)
			}
			if row[11] == "'=cmd|calc" {
				formula++
			}
		}
		if formula != 1 {
			t.Fatalf("%d defused cells, want 1", formula)
		}
	})
	t.Run("R-OPS-017 a slow reader holds no transaction and no connection between batches", func(t *testing.T) {
		req, err := itstack.NewRequest("GET", f.a.Server.URL+exportURL+"&action=bulk.generated", f.auditor.Name, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		br := bufio.NewReader(res.Body)
		for range 5 {
			if _, err := br.ReadBytes('\n'); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Second) // the server has written (and is blocked on) the next batch
		idle := f.e.Number(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state LIKE 'idle in transaction%'`)
		if idle != 0 {
			t.Fatalf("%d connections idle in transaction while the client reads", idle)
		}
		_, _ = io.Copy(io.Discard, br)
	})
	t.Run("R-OPS-017 a missing, reversed or oversized range is 400 invalid_audit_range", func(t *testing.T) {
		for _, q := range []string{
			"", "?from=" + from, "?to=" + to, "?from=" + to + "&to=" + from,
			"?from=2024-01-01T00:00:00Z&to=2026-01-01T00:00:00Z",
		} {
			want(t, f.cl.Do(me, "GET", events+"/export"+q, nil), 400, "invalid_audit_range")
		}
		want(t, f.cl.Do(me, "GET", exportURL+"&format=xml", nil), 400, "")
		want(t, f.cl.Do(me, "GET", exportURL+"&cursor=x", nil), 400, "")
		if n := f.e.Number("SELECT count(*) FROM audit_event WHERE action = 'audit.exported'"); n != 3 {
			t.Fatalf("%d audit.exported events: refused requests must record none", n)
		}
	})
	t.Run("R-OPS-017 an export that cannot be recorded does not stream", func(t *testing.T) {
		f.e.Exec("ALTER TABLE audit_event RENAME TO audit_event_away")
		defer f.e.Exec("ALTER TABLE audit_event_away RENAME TO audit_event")
		r := f.cl.Do(me, "GET", exportURL, nil)
		if r.Status < 500 || strings.Contains(string(r.Body), "bulk.generated") {
			t.Fatalf("answered %d: %s", r.Status, r.Body)
		}
	})
}

// TestR_AUTHZ_031_RepositoryUpdateRecordsSettingsChanged: the repository update
// writes authz.settings.changed, with a journal entry, in its own transaction when
// an authorization setting changed, and nothing for another change.
func TestR_AUTHZ_031_RepositoryUpdateRecordsSettingsChanged(t *testing.T) {
	f := newAPIFx(t, itstack.Options{})
	ctx := context.Background()
	caller := repository.Caller{Actor: audit.Actor{Kind: "user", ID: f.admin.ID, Name: f.admin.Name}}
	st, err := f.a.Repos.Create(ctx, caller, []byte(`{"key":"generic-hook","class":"local","format":"generic"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Repos.Patch(ctx, caller, "generic-hook", repository.MatchVersion(st.Repository.Version), v1.MergePatcher([]byte(`{"description":"nothing of authorization"}`))); err != nil {
		t.Fatal(err)
	}
	if n := f.e.Number("SELECT count(*) FROM audit_event WHERE action = 'authz.settings.changed'"); n != 0 {
		t.Fatalf("%d events for a change of the description", n)
	}
	if _, err := f.a.Repos.Patch(ctx, caller, "generic-hook", repository.MatchAny(), v1.MergePatcher([]byte(`{"requireAuthentication":true}`))); err != nil {
		t.Fatal(err)
	}
	ev := f.find(t, "action=authz.settings.changed", 1)[0]
	before, _ := detail(ev, "before").(map[string]any)
	after, _ := detail(ev, "after").(map[string]any)
	if ev["repository"] != "generic-hook" || before["requireAuthentication"] != false || after["requireAuthentication"] != true || ev["actor"].(map[string]any)["name"] != "sec-alice" {
		t.Fatalf("%v", ev)
	}
	// The journal row of the revocation journal was written with the event.
	if n := f.e.Number("SELECT count(*) FROM revocation_outbox WHERE event = 'authz.settings.changed' AND id = $1::uuid", ev["id"]); n != 1 {
		t.Fatalf("%d journal rows for the event", n)
	}
	if !f.inLog(ev["id"].(string)) {
		t.Fatal("not in the log stream")
	}
}
