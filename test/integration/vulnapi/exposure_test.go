//go:build integration

package vulnapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_VULN_028_AVersionRuleOfAnEndpointNarrowsEveryAnswerByVersion: the scope of the findings report is decided per package for
// a caller whose decision does not look at the version (Q-VULN-81), and per version for an endpoint with exposure rules, whose
// rule may (R-EP-014, R-EP-018). The endpoint `customers` serves the api surface and exposes @acme/* of npm-local from 2.0.0:
// a caller with an unrestricted read grant sees through it the finding of @acme/sdk 2.1.0 and not the one of 1.9.0, in the list, the
// summary, the export, by id and in a bulk status change (the hidden id is not_found), and through `default` both.
func TestR_VULN_028_AVersionRuleOfAnEndpointNarrowsEveryAnswerByVersion(t *testing.T) {
	e := newEnv(t)
	// A service account: the endpoint serves service accounts' tokens (R-AUTH-022). It holds the triage capability and an
	// unrestricted read grant on npm-local.
	ivy := e.st.ServiceAccount("ivy")
	e.st.Bind("security-admin", ivy)
	e.st.Policy("acme-readers", []string{"read"}, []string{"npm-local"}, ivy)
	system := []string{"vulnerabilities:triage"}
	tokDefault := e.st.Token(ivy, scope.Scope{Inherit: true, System: system})
	tokCustomers := e.st.Token(ivy, scope.Scope{Inherit: true, System: system, Endpoints: []string{"customers"}})
	old := e.addFinding(finding{Repo: "npm-local", Package: "@acme/sdk", Version: "1.9.0"})
	cur := e.addFinding(finding{Repo: "npm-local", Package: "@acme/sdk", Version: "2.1.0"})

	const host = "packages.trove.test"
	cust := e.st.Start(t, testenv.ReplicaOptions{
		Name: "customers", Env: []string{"TROVE_API_RATE_LIMITS_ENABLED=false"},
		Endpoints: `{"customers":{"listen":"127.0.0.1:0","base_url":"https://` + host + `","surface":["api"]}}`,
	})
	rule := e.st.Do(cust, "POST", "/api/v1/endpoints/customers/exposure-rules", e.tokRita,
		[]byte(`{"name":"acme","repository":"it/npm-local","packages":{"include":["@acme/*"]},"versions":{"range":">=2.0.0"}}`),
		"Content-Type", "application/json")
	if rule.Status != http.StatusCreated {
		t.Fatalf("rule: %d %s", rule.Status, rule.Body)
	}
	epoch := rule.Header.Get("Trove-Authz-Epoch")
	through := func(method, path, tok string, body []byte) testenv.Answer {
		t.Helper()
		if tok == tokDefault {
			return e.st.Do(cust, method, path, tok, body, "Content-Type", "application/json", "Trove-Authz-Min-Epoch", epoch)
		}
		return e.st.DoAt(cust.Addr("customers"), host, method, path, tok, body, "Content-Type", "application/json", "Trove-Authz-Min-Epoch", epoch)
	}
	const base = "/api/v1/vulnerabilities/"

	for _, tc := range []struct {
		name    string
		tok     string
		visible map[string]bool
	}{
		{"through customers only 2.1.0 is visible", tokCustomers, map[string]bool{old: false, cur: true}},
		{"through default both are", tokDefault, map[string]bool{old: true, cur: true}},
	} {
		wantCount := 0
		for _, v := range tc.visible {
			if v {
				wantCount++
			}
		}
		t.Run("R-VULN-028 R-EP-014 Q-VULN-81 "+tc.name, func(t *testing.T) {
			a := through("GET", base+"findings?limit=50", tc.tok, nil)
			if a.Status != 200 {
				t.Fatalf("list: %d %s", a.Status, a.Body)
			}
			var p page
			if err := json.Unmarshal(a.Body, &p); err != nil {
				t.Fatal(err)
			}
			if len(p.Items) != wantCount {
				t.Errorf("list holds %d findings, want %d: %s", len(p.Items), wantCount, a.Body)
			}
			for _, it := range p.Items {
				if !tc.visible[it.ID] {
					t.Errorf("the list shows %s %s@%s", it.ID, it.Package, it.Version)
				}
			}

			s := through("GET", base+"summary", tc.tok, nil)
			var sum struct{ NeedsTriage int64 }
			if s.Status != 200 || json.Unmarshal(s.Body, &sum) != nil || sum.NeedsTriage != int64(wantCount) {
				t.Errorf("summary: %d, needsTriage %d, want %d: %s", s.Status, sum.NeedsTriage, wantCount, s.Body)
			}

			x := through("GET", base+"findings/export", tc.tok, nil)
			if x.Status != 200 || strings.Contains(string(x.Body), "1.9.0") == !tc.visible[old] || !strings.Contains(string(x.Body), "2.1.0") {
				t.Errorf("export: %d %s", x.Status, x.Body)
			}

			for id, visible := range tc.visible {
				g := through("GET", base+"findings/"+id, tc.tok, nil)
				if visible && g.Status != 200 || !visible && g.Status != http.StatusNotFound {
					t.Errorf("by id %s: %d %s, visible %v", id, g.Status, g.Body, visible)
				}
			}

			b := through("POST", base+"findings/set-status", tc.tok, []byte(`{"ids":["`+old+`","`+cur+`"],"status":"confirmed"}`))
			var res struct {
				Results []struct {
					ID      string `json:"id"`
					Outcome string `json:"outcome"`
					Status  int    `json:"status"`
				} `json:"results"`
			}
			if b.Status != 200 || json.Unmarshal(b.Body, &res) != nil || len(res.Results) != 2 {
				t.Fatalf("set-status: %d %s", b.Status, b.Body)
			}
			for _, r := range res.Results {
				if tc.visible[r.ID] && r.Outcome != "changed" || !tc.visible[r.ID] && r.Status != http.StatusNotFound {
					t.Errorf("set-status %s: %+v, visible %v", r.ID, r, tc.visible[r.ID])
				}
			}
			// The next case starts from the stored status.
			e.st.Exec(`UPDATE vuln_finding SET status = 'needs_triage' WHERE id IN ($1::uuid, $2::uuid)`, old, cur)
		})
	}
	if st := e.st.Text(`SELECT status FROM vuln_finding WHERE id = $1::uuid`, old); st != "needs_triage" {
		t.Errorf("the hidden finding is %s", st)
	}
}
