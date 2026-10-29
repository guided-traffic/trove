//go:build integration

package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func mode(valkey bool) string {
	if valkey {
		return "valkey"
	}
	return "valkey-stopped"
}

// metrics reads a replica's ops listener.
func metrics(t *testing.T, r *replica) string {
	t.Helper()
	res, err := client.Get("http://" + r.ops + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}

func metricValue(text, series string) float64 {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, series+" ") {
			v, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, series)), 64)
			return v
		}
	}
	return -1
}

const releasesRule = `{"name":"releases","description":"Released files for customers","repository":"generic-local","paths":{"include":["releases/**"]}}`

// TestE1_14_CustomerEndpoint is exit criterion E1.14 with three replicas behind a
// dispatcher, once with Valkey running and once with Valkey unreachable: the
// credential rules, the bound tokens, a rule created, narrowed and deleted
// through the API on every replica within the bound of R-AUTHZ-028, hidden equal
// to missing, the administrator bound by the ceiling, the audit of every change,
// and the rate-limit class.
func TestE1_14_CustomerEndpoint(t *testing.T) {
	for _, valkey := range []bool{true, false} {
		t.Run(mode(valkey), func(t *testing.T) {
			e := newEnv(t, valkey)
			for i := range 3 {
				e.start(fmt.Sprintf("ep-%s-%d", mode(valkey), i))
			}
			ctx := context.Background()
			file := "/files/generic-local/releases/1.0/app.zip"

			t.Run("R-EP-010 a PAT on customers is 401 invalid_token without a token-table query", func(t *testing.T) {
				// The query counter is a lock: while api_token is locked exclusively,
				// a lookup would wait for it, so an answer that arrives at once made
				// none. The unbound SAT, never used before, does wait: its refusal
				// comes after the lookup.
				lock, err := pgx.Connect(ctx, testenv.PgDSN(t, e.name))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close(ctx)
				tx, err := lock.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, "LOCK TABLE trove.api_token IN ACCESS EXCLUSIVE MODE"); err != nil {
					t.Fatal(err)
				}
				for _, r := range e.reps {
					start := time.Now()
					a := e.onCustomers(r, file, e.alicePAT)
					if a.status != 401 || a.json(t)["code"] != errs.CodeInvalidToken || time.Since(start) > 2*time.Second {
						t.Fatalf("%s: %d %s after %s", r.name, a.status, a.body, time.Since(start))
					}
				}
				done := make(chan answer, 1)
				go func() { done <- e.onCustomers(e.reps[0], file, e.acmeUnbound2) }()
				select {
				case a := <-done:
					t.Fatalf("the unbound SAT was answered while the token table was locked: %d %s", a.status, a.body)
				case <-time.After(700 * time.Millisecond):
				}
				_ = tx.Rollback(ctx)
				if a := <-done; a.status != 401 || a.json(t)["code"] != errs.CodeInvalidToken {
					t.Fatalf("unbound SAT after the lookup: %d %s", a.status, a.body)
				}
			})

			t.Run("R-EP-011 a SAT bound to customers reaches the endpoint and is 401 on the default endpoint", func(t *testing.T) {
				for _, r := range e.reps {
					// No rule yet: the bound token is authenticated and the ceiling
					// hides the file (404), where a refused credential is 401.
					if a := e.onCustomers(r, file, e.acmeSAT); a.status != 404 {
						t.Fatalf("%s customers: %d %s", r.name, a.status, a.body)
					}
					a := e.send(r.def, "trove.test", "GET", file, e.acmeSAT, nil)
					if a.status != 401 || a.json(t)["code"] != errs.CodeInvalidToken {
						t.Fatalf("%s default: %d %s", r.name, a.status, a.body)
					}
					if a := e.onCustomers(r, file, e.acmeUnbound); a.status != 401 || a.json(t)["code"] != errs.CodeInvalidToken {
						t.Fatalf("%s unbound: %d %s", r.name, a.status, a.body)
					}
					if a := e.onCustomers(r, file, ""); a.status != 401 || a.json(t)["code"] != errs.CodeCredentialsRequired {
						t.Fatalf("%s anonymous: %d %s", r.name, a.status, a.body)
					}
				}
				m := metrics(t, e.reps[0])
				for _, reason := range []string{"token_type", "token_binding", "anonymous"} {
					if v := metricValue(m, `trove_endpoint_credentials_refused_total{endpoint="customers",reason="`+reason+`"}`); v < 1 {
						t.Errorf("trove_endpoint_credentials_refused_total{reason=%q} = %v", reason, v)
					}
				}
				if v := metricValue(m, `trove_endpoint_credentials_refused_total{endpoint="default",reason="token_binding"}`); v < 1 {
					t.Errorf("the bound token on default is not counted: %v", v)
				}
				waitFor(t, "token.rejected with reason endpoint_not_accepted", 10*time.Second, func() bool {
					return e.number(`SELECT count(*) FROM audit_event WHERE action = 'token.rejected' AND reason = 'endpoint_not_accepted' AND endpoint = 'customers'`) > 0
				})
			})

			t.Run("R-EP-005 with a bound SAT, a path outside the surface and an unknown path answer the same 404", func(t *testing.T) {
				r := e.pick()
				want := e.onCustomers(r, "/does-not-exist", e.acmeSAT)
				if want.status != 404 {
					t.Fatalf("%d %s", want.status, want.body)
				}
				for _, p := range []string{"/ui/", "/", "/auth/oidc/corp/start", "/api/v1/system/ping", "/api/v1/endpoints", "/v2/", "/.well-known/x"} {
					if a := e.onCustomers(r, p, e.acmeSAT); a.status != 404 || a.problem(t) != want.problem(t) {
						t.Errorf("%s: %d %s, want %s", p, a.status, a.body, want.body)
					}
				}
			})

			var epoch string
			t.Run("R-EP-012 R-EP-016 a rule created through the API takes effect on every replica within the bound, at once with the epoch", func(t *testing.T) {
				a := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(releasesRule), "Idempotency-Key", "releases-rule-create")
				if a.status != 201 || a.header.Get("ETag") != `"1"` || a.header.Get("Location") != "http://trove.test/api/v1/endpoints/customers/exposure-rules/releases" {
					t.Fatalf("%d %v %s", a.status, a.header, a.body)
				}
				if epoch = a.header.Get("Trove-Authz-Epoch"); epoch == "" {
					t.Fatal("no Trove-Authz-Epoch")
				}
				// R-API-014, Q-API-28: a replay carries the epoch of the first answer (Outcome.Finish).
				if again := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(releasesRule), "Idempotency-Key", "releases-rule-create"); again.status != 201 ||
					again.header.Get("Idempotency-Replayed") != "true" || again.header.Get("Trove-Authz-Epoch") != epoch {
					t.Fatalf("replay: %d replayed=%q epoch=%q, want epoch %q", again.status, again.header.Get("Idempotency-Replayed"), again.header.Get("Trove-Authz-Epoch"), epoch)
				}
				for _, r := range e.reps {
					if a := e.onCustomers(r, file, e.acmeSAT, "Trove-Authz-Min-Epoch", epoch); a.status != 200 || string(a.body) != "app 1.0" {
						t.Fatalf("%s with the epoch: %d %s", r.name, a.status, a.body)
					}
				}
				for _, r := range e.reps {
					took := waitFor(t, r.name+" serving the exposed file", propagation, func() bool { return e.onCustomers(r, file, e.acmeSAT2).status == 200 })
					t.Logf("%s: %s", r.name, took)
				}
			})

			t.Run("R-EP-014 R-EP-013 a file outside the rule answers the 404 of a missing file", func(t *testing.T) {
				r := e.pick()
				hidden := e.onCustomers(r, "/files/generic-local/internal/x.bin", e.acmeSAT)
				if hidden.status != 404 {
					t.Fatalf("%d %s", hidden.status, hidden.body)
				}
				for _, p := range []string{"/files/generic-local/releases/9.9/none.zip", "/files/generic-local/internal/none.bin", "/files/ghost/releases/1.0/app.zip", "/files/generic-other/releases/1.0/app.zip"} {
					if a := e.onCustomers(r, p, e.acmeSAT); a.status != 404 || a.problem(t) != hidden.problem(t) {
						t.Errorf("%s: %d %s, want the bytes of %s", p, a.status, a.body, hidden.body)
					}
				}
				// R-EP-009: the refused read is audited with the endpoint and the reason.
				waitFor(t, "authz.denied with deny.exposure on customers", 10*time.Second, func() bool {
					return e.number(`SELECT count(*) FROM audit_event WHERE action = 'authz.denied' AND reason = 'deny.exposure' AND endpoint = 'customers'`) > 0
				})
				// Through the default endpoint the same token is refused; an unbound
				// token of the same account reads the internal file there.
				if a := e.send(r.def, "trove.test", "GET", "/files/generic-local/internal/x.bin", e.acmeUnbound, nil); a.status != 200 {
					t.Fatalf("default: %d %s", a.status, a.body)
				}
			})

			t.Run("R-EP-013 Q-EP-12 an administrator token bound to customers sees no more than the rules expose", func(t *testing.T) {
				for _, r := range e.reps {
					if a := e.onCustomers(r, file, e.botAdminSAT); a.status != 200 {
						t.Fatalf("%s exposed: %d %s", r.name, a.status, a.body)
					}
					if a := e.onCustomers(r, "/files/generic-local/internal/x.bin", e.botAdminSAT); a.status != 404 {
						t.Fatalf("%s hidden: %d %s", r.name, a.status, a.body)
					}
					// The listing of the files operation holds what the rules expose
					// and no more: the folder of the exposed files lists them, the
					// folder outside the rules is the 404 of a missing one.
					list := e.onCustomers(r, "/files/generic-local/releases/", e.botAdminSAT)
					var page struct {
						Items []struct {
							Path string `json:"path"`
						} `json:"items"`
					}
					if list.status != 200 || json.Unmarshal(list.body, &page) != nil {
						t.Fatalf("%s listing: %d %s", r.name, list.status, list.body)
					}
					var got []string
					for _, it := range page.Items {
						got = append(got, it.Path)
					}
					if !slices.Equal(got, []string{"1.0", "2.0"}) {
						t.Fatalf("%s listing: %v, want the two exposed folders: %s", r.name, got, list.body)
					}
					if a := e.onCustomers(r, "/files/generic-local/internal/", e.botAdminSAT); a.status != 404 {
						t.Fatalf("%s hidden listing: %d %s", r.name, a.status, a.body)
					}
				}
				if a := e.send(e.pick().def, "trove.test", "GET", "/files/generic-local/releases/", e.botAdminSAT, nil); a.status != 401 {
					t.Fatalf("the bound administrator token on default: %d", a.status)
				}
				if v := metricValue(metrics(t, e.reps[0]), `trove_exposure_hidden_total{endpoint="customers",kind="request"}`); v < 1 {
					t.Errorf("trove_exposure_hidden_total = %v", v)
				}
			})

			t.Run("R-EP-016 a narrowed rule hides the rest on every replica within the bound", func(t *testing.T) {
				g := e.api("GET", "/api/v1/endpoints/customers/exposure-rules/releases", nil)
				doc := g.json(t)
				doc["paths"] = map[string]any{"include": []string{"releases/1.0/**"}, "exclude": []string{}}
				a := e.api("PUT", "/api/v1/endpoints/customers/exposure-rules/releases", doc, "If-Match", g.header.Get("ETag"))
				if a.status != 200 || a.header.Get("ETag") != `"2"` || a.header.Get("Trove-Authz-Epoch") == "" {
					t.Fatalf("%d %v %s", a.status, a.header, a.body)
				}
				for _, r := range e.reps {
					waitFor(t, r.name+" hiding releases/2.0", propagation, func() bool {
						return e.onCustomers(r, "/files/generic-local/releases/2.0/app.zip", e.acmeSAT2).status == 404
					})
					if a := e.onCustomers(r, file, e.acmeSAT2); a.status != 200 {
						t.Fatalf("%s releases/1.0: %d", r.name, a.status)
					}
				}
			})

			t.Run("R-EP-016 a deleted rule exposes nothing on every replica within the bound", func(t *testing.T) {
				g := e.api("GET", "/api/v1/endpoints/customers/exposure-rules/releases", nil)
				a := e.api("DELETE", "/api/v1/endpoints/customers/exposure-rules/releases", nil, "If-Match", g.header.Get("ETag"))
				if a.status != 204 || a.header.Get("Trove-Authz-Epoch") == "" {
					t.Fatalf("%d %v %s", a.status, a.header, a.body)
				}
				for _, r := range e.reps {
					waitFor(t, r.name+" hiding everything", propagation, func() bool { return e.onCustomers(r, file, e.acmeSAT2).status == 404 })
				}
			})

			t.Run("R-EP-016 R-EP-020 every change is an audit event; the narrowing ones carry the journal", func(t *testing.T) {
				for _, ev := range []string{"endpoint.exposure.created", "endpoint.exposure.updated", "endpoint.exposure.deleted"} {
					if n := e.number(`SELECT count(*) FROM audit_event WHERE action = $1 AND details->>'endpoint' = 'customers' AND details->>'name' = 'releases'`, ev); n != 1 {
						t.Errorf("%s: %d events", ev, n)
					}
				}
				if n := e.number(`SELECT count(*) FROM revocation_outbox WHERE event IN ('endpoint.exposure.updated', 'endpoint.exposure.deleted') AND entry->>'name' = 'releases' AND entry->>'endpoint' = 'customers'`); n != 2 {
					t.Errorf("%d journal entries, want 2", n)
				}
				if s := e.text(`SELECT entry::text FROM revocation_outbox WHERE event = 'endpoint.exposure.updated'`); !strings.Contains(s, `"releases/1.0/**"`) || !strings.Contains(s, `"repositoryId"`) {
					t.Errorf("journal entry %s", s)
				}
			})

			t.Run("R-EP-015 1,000 requests of one token within 2 s are admitted at most 300 times; another token and another endpoint are not slowed", func(t *testing.T) {
				// Wait until every replica counts the others serving its pool, which
				// the fallback share divides by.
				time.Sleep(1500 * time.Millisecond)
				var admitted, limited atomic.Int64
				var wg sync.WaitGroup
				start := time.Now()
				for w := range 25 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for i := range 40 {
							r := e.reps[(w+i)%len(e.reps)]
							a := e.onCustomers(r, file, e.acmeSAT)
							switch a.status {
							case 429:
								if a.header.Get("Retry-After") == "" || !strings.Contains(a.header.Get("RateLimit-Policy"), `"endpoint:external"`) {
									t.Errorf("a 429 without its fields: %v", a.header)
								}
								limited.Add(1)
							default:
								admitted.Add(1)
							}
						}
					}()
				}
				wg.Wait()
				took := time.Since(start)
				limit := 200 + int64(50*took.Seconds()+1)
				t.Logf("%d admitted, %d limited in %s (bound %d)", admitted.Load(), limited.Load(), took, limit)
				if admitted.Load() > limit || limited.Load() == 0 {
					t.Fatalf("%d of 1000 admitted in %s, at most %d allowed", admitted.Load(), took, limit)
				}
				if took <= 2*time.Second && admitted.Load() > 300 {
					t.Fatalf("%d admitted within 2 s", admitted.Load())
				}
				for _, r := range e.reps {
					if a := e.onCustomers(r, file, e.acmeSAT2); a.status == 429 {
						t.Fatalf("%s: the second token was slowed down", r.name)
					}
					if a := e.send(r.partner, partnersHost, "GET", file, e.acmeSAT, nil); a.status == 429 {
						t.Fatalf("%s: the first token was slowed down on partners", r.name)
					}
				}
				for _, r := range e.reps {
					want := 0.0
					if !valkey {
						want = 1
					}
					if v := metricValue(metrics(t, r), `trove_ratelimit_fallback{limiter="endpoint_external"}`); v != want {
						t.Errorf("%s: trove_ratelimit_fallback{limiter=\"endpoint_external\"} = %v, want %v", r.name, v, want)
					}
				}
			})
		})
	}
}

// TestR_EP_016_EndpointAndExposureAPI runs the convention tests on the exposure
// rules and covers every operation of the area with its answers validated against
// the document.
func TestR_EP_016_EndpointAndExposureAPI(t *testing.T) {
	e := newEnv(t, true)
	// Rows written straight into the tables reach a replica's grant index only
	// with its first build, so they come before the replicas start.
	auditor := e.principalRow("user", "auditor")
	e.exec("INSERT INTO authz_role_binding (role, principal_id) VALUES ('auditor', $1::uuid)", auditor)
	// A token exercises the capabilities its scope names that the owner's roles
	// grant (R-AUTHZ-013): endpoints:read from the auditor role, endpoints:write
	// from none.
	auditorPAT := e.tokenRow(token.TypePAT, auditor, token.Scope{Inherit: true, System: []string{"endpoints:read", "endpoints:write"}})
	// A rule of an endpoint that is no longer configured (H-58).
	e.exec(`INSERT INTO endpoint_exposure_rule (id, endpoint, name, repo_id) SELECT gen_random_uuid(), 'retired', 'old', id FROM repositories WHERE key = 'generic-local'`)
	e.start("api-0")
	e.start("api-1")

	t.Run("R-EP-016 the exposure rules follow the API conventions (E1.6)", func(t *testing.T) {
		contract(t, e)
	})

	t.Run("R-EP-016 the endpoints with their configuration, rule counts and the inert names", func(t *testing.T) {
		a := e.api("GET", "/api/v1/endpoints", nil)
		var list struct {
			Items []struct {
				Name          string         `json:"name"`
				Inert         bool           `json:"inert"`
				RuleCount     int64          `json:"ruleCount"`
				Configuration map[string]any `json:"configuration"`
			} `json:"items"`
		}
		if a.status != 200 || json.Unmarshal(a.body, &list) != nil {
			t.Fatalf("%d %s", a.status, a.body)
		}
		var names []string
		for _, it := range list.Items {
			names = append(names, it.Name)
		}
		if !slices.Equal(names, []string{"default", "customers", "partners", "retired"}) {
			t.Fatalf("%v", names)
		}
		cust, retired := list.Items[1], list.Items[3]
		auth, _ := cust.Configuration["auth"].(map[string]any)
		rl, _ := cust.Configuration["rateLimit"].(map[string]any)
		if cust.Configuration["exposure"] != "rules" || auth["boundTokensOnly"] != true || rl["class"] != "external" || cust.RuleCount == 0 {
			t.Fatalf("customers: %+v", cust)
		}
		if !retired.Inert || retired.Configuration != nil || retired.RuleCount != 1 {
			t.Fatalf("retired: %+v", retired)
		}
		if a := e.api("GET", "/api/v1/endpoints?limit=1", nil); a.status != 200 || a.json(t)["nextCursor"] == nil {
			t.Fatalf("a page of one: %d %s", a.status, a.body)
		}
		if a := e.api("GET", "/api/v1/endpoints/customers", nil); a.status != 200 || a.json(t)["name"] != "customers" {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if a := e.api("GET", "/api/v1/endpoints/nowhere", nil); a.status != 404 {
			t.Fatalf("an unknown endpoint: %d", a.status)
		}
	})

	t.Run("R-EP-016 filters of the rule list", func(t *testing.T) {
		for _, b := range []string{
			`{"name":"f-one","repository":"generic-local","paths":{"include":["f/**"]}}`,
			`{"name":"f-two","repository":"generic-other","enabled":false}`,
		} {
			if a := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(b)); a.status != 201 {
				t.Fatalf("%d %s", a.status, a.body)
			}
		}
		names := func(q string) []string {
			a := e.api("GET", "/api/v1/endpoints/customers/exposure-rules?limit=500&"+q, nil)
			var l struct {
				Items []struct{ Name string } `json:"items"`
			}
			if a.status != 200 || json.Unmarshal(a.body, &l) != nil {
				t.Fatalf("%s: %d %s", q, a.status, a.body)
			}
			var out []string
			for _, it := range l.Items {
				if strings.HasPrefix(it.Name, "f-") {
					out = append(out, it.Name)
				}
			}
			return out
		}
		if got := names("repository=generic-other"); !slices.Equal(got, []string{"f-two"}) {
			t.Errorf("repository: %v", got)
		}
		if got := names("enabled=false"); !slices.Equal(got, []string{"f-two"}) {
			t.Errorf("enabled: %v", got)
		}
		if got := names("package=anything&sort=-name"); !slices.Equal(got, []string{"f-two", "f-one"}) {
			t.Errorf("package and descending: %v", got)
		}
	})

	t.Run("R-EP-012 validation answers 422 with the code of the first problem and every field", func(t *testing.T) {
		for _, c := range []struct {
			body, code, pointer string
		}{
			{`{"name":"v1","repository":"generic-local","packages":{"include":["x"]}}`, errs.CodeDimensionNotSupported, "/packages"},
			{`{"name":"v2","repository":"generic-local","versions":{"range":">=1.0.0"}}`, errs.CodeDimensionNotSupported, "/versions"},
			{`{"name":"v3","repository":"ghost"}`, errs.CodeUnknownRepository, "/repository"},
			{`{"name":"v4","repository":"generic-local","paths":{"include":["a//b"]}}`, errs.CodeInvalidPattern, "/paths/include/0"},
			{`{"name":"Bad Name","repository":"generic-local"}`, "pattern", "/name"},
		} {
			a := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(c.body))
			m := a.json(t)
			fields, _ := m["errors"].([]any)
			if a.status != 422 || len(fields) == 0 || fields[0].(map[string]any)["pointer"] != c.pointer || fields[0].(map[string]any)["code"] != c.code {
				t.Errorf("%s: %d %s", c.body, a.status, a.body)
			}
		}
		if a := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(`{"name":"f-one","repository":"generic-local"}`)); a.status != 409 || a.json(t)["code"] != errs.CodeAlreadyExists {
			t.Errorf("a taken name: %d %s", a.status, a.body)
		}
		if a := e.api("POST", "/api/v1/endpoints/nowhere/exposure-rules", []byte(`{"name":"x","repository":"generic-local"}`)); a.status != 404 {
			t.Errorf("an unknown endpoint: %d %s", a.status, a.body)
		}
		if a := e.api("POST", "/api/v1/endpoints/retired/exposure-rules", []byte(`{"name":"x","repository":"generic-local"}`)); a.status != 409 || a.json(t)["code"] != errs.CodeEndpointNotConfigured {
			t.Errorf("an inert endpoint: %d %s", a.status, a.body)
		}
		a := e.api("POST", "/api/v1/endpoints/default/exposure-rules", []byte(`{"name":"on-default","repository":"generic-local"}`))
		if w, _ := a.json(t)["warnings"].([]any); a.status != 201 || len(w) != 1 || w[0] != "endpoint_not_in_rules_mode" {
			t.Errorf("a rule on an endpoint in mode all: %d %s", a.status, a.body)
		}
	})

	t.Run("R-EP-016 R-EP-017 check-exposure reports the admitting rule or the reason, also for a draft", func(t *testing.T) {
		check := func(body string) answer {
			return e.api("POST", "/api/v1/endpoints/customers/check-exposure", []byte(body))
		}
		a := check(`{"repository":"generic-local","paths":["f/x.zip","g/y.zip"]}`)
		var res struct {
			Mode           string `json:"mode"`
			PackageExposed bool   `json:"packageExposed"`
			Paths          []struct {
				Path    string  `json:"path"`
				Exposed bool    `json:"exposed"`
				Rule    *string `json:"rule"`
				Reason  string  `json:"reason"`
			} `json:"paths"`
			Fingerprint string `json:"exposureFingerprint"`
		}
		if a.status != 200 || json.Unmarshal(a.body, &res) != nil || res.Mode != "rules" || !res.PackageExposed || len(res.Paths) != 2 {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if !res.Paths[0].Exposed || res.Paths[0].Rule == nil || *res.Paths[0].Rule != "f-one" || res.Paths[1].Exposed || res.Paths[1].Reason == "exposed" {
			t.Fatalf("%s", a.body)
		}
		draft := check(`{"repository":"generic-local","paths":["g/y.zip"],"rule":{"name":"draft","repository":"generic-local","paths":{"include":["g/**"]}}}`)
		if !strings.Contains(string(draft.body), `"rule":"draft"`) || draft.json(t)["exposureFingerprint"] != res.Fingerprint {
			t.Fatalf("a draft: %d %s", draft.status, draft.body)
		}
		if off := check(`{"repository":"generic-other","paths":["a"]}`); !strings.Contains(string(off.body), `"reason":"rule_disabled"`) {
			t.Fatalf("a disabled rule: %s", off.body)
		}
		if bad := check(`{"repository":"generic-local","versions":["1.0.0"]}`); bad.status != 422 || bad.json(t)["code"] != errs.CodeValidation || !strings.Contains(string(bad.body), `"code":"`+errs.CodeDimensionNotSupported+`"`) {
			t.Fatalf("versions on generic: %d %s", bad.status, bad.body)
		}
		if missing := check(`{"repository":"ghost"}`); missing.status != 404 {
			t.Fatalf("a missing repository: %d %s", missing.status, missing.body)
		}
		if all := e.api("POST", "/api/v1/endpoints/default/check-exposure", []byte(`{"repository":"generic-local","paths":["anything"]}`)); !strings.Contains(string(all.body), `"mode":"all"`) || !strings.Contains(string(all.body), `"exposed":true`) {
			t.Fatalf("mode all: %s", all.body)
		}
	})

	t.Run("R-EP-016 an auditor reads and gets 403 on writes; an inert endpoint's rules are read and deleted", func(t *testing.T) {
		r := e.pick()
		if a := e.send(r.def, "trove.test", "GET", "/api/v1/endpoints/customers/exposure-rules", auditorPAT, nil); a.status != 200 {
			t.Fatalf("auditor read: %d %s", a.status, a.body)
		}
		if a := e.send(r.def, "trove.test", "POST", "/api/v1/endpoints/customers/exposure-rules", auditorPAT, []byte(`{"name":"x","repository":"generic-local"}`)); a.status != 403 {
			t.Fatalf("auditor write: %d %s", a.status, a.body)
		}
		if a := e.send(r.def, "trove.test", "GET", "/api/v1/endpoints", e.alicePAT, nil); a.status != 403 {
			t.Fatalf("a user without endpoints:read: %d", a.status)
		}
		if a := e.api("GET", "/api/v1/endpoints/retired/exposure-rules/old", nil); a.status != 200 || a.json(t)["inert"] != true {
			t.Fatalf("inert read: %d %s", a.status, a.body)
		}
		if a := e.api("DELETE", "/api/v1/endpoints/retired/exposure-rules/old", nil); a.status != 204 {
			t.Fatalf("inert delete: %d %s", a.status, a.body)
		}
		if a := e.api("GET", "/api/v1/endpoints/retired", nil); a.status != 404 {
			t.Fatalf("a name without rules and configuration: %d", a.status)
		}
		// H-58: every replica warned about the inert rules at start-up.
		for _, r := range e.reps {
			if logs := r.logs.String(); !strings.Contains(logs, "are inert") || !strings.Contains(logs, `"endpoint":"retired"`) {
				t.Errorf("%s logged no warning for the inert endpoint", r.name)
			}
		}
	})

	if missing := e.val.Uncovered("listEndpoints", "getEndpoint", "listExposureRules", "createExposureRule", "getExposureRule",
		"replaceExposureRule", "patchExposureRule", "deleteExposureRule", "checkExposure"); len(missing) > 0 {
		t.Errorf("operations no answer reached: %v", missing)
	}
}
