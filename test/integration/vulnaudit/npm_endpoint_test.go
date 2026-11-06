//go:build integration

package vulnaudit

import (
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// customersHost and customersEndpoint are the fixture endpoint of R-EP-012: a second endpoint with exposure rules.
const (
	customersHost     = "registry.acme.test"
	customersEndpoint = `{"customers":{"listen":"127.0.0.1:0","base_url":"https://` + customersHost + `","surface":["packages"]}}`
	acmeRule          = `{"name":"acme-sdk","repository":"npm-customers","enabled":true,
	"packages":{"include":["@acme/sdk"],"exclude":[]},"versions":{"include":[],"exclude":[],"range":""},"prereleases":false,
	"distTags":{"include":["latest"],"exclude":[]}}`
)

// TestR_VULN_017_AnEndpointWithExposureRulesKeepsThe501 is the criterion of the fixture endpoint customers: the audit fails
// with 501 in mode local, because the withheld names would tell a caller there which names the installation treats as
// internal, also names the endpoint hides (Q-VULN-19); the default endpoint answers.
func TestR_VULN_017_AnEndpointWithExposureRulesKeepsThe501(t *testing.T) {
	e := newEnv(t)
	e.seedNPM()
	e.st.Repository(npmCustomer, "local", "npm", `{}`)
	ops, acme := e.st.User("ops"), e.st.ServiceAccount("customer-acme")
	e.st.Bind("admin", ops)
	e.st.Policy("customers-read", []string{"read"}, []string{npmCustomer}, acme)
	opsPAT := e.st.Token(ops, scope.Scope{Inherit: true, Admin: true})
	acmeSAT := e.st.Token(acme, scope.Scope{Inherit: true, Endpoints: []string{"customers"}})
	r := e.st.Start(t, testenv.ReplicaOptions{Name: "web-a", Endpoints: customersEndpoint})
	rule := e.st.Do(r, "POST", "/api/v1/endpoints/customers/exposure-rules", opsPAT, []byte(acmeRule))
	if rule.Status != http.StatusCreated {
		t.Fatalf("create the rule: %d %s", rule.Status, rule.Body)
	}
	x := e.st.DoAt(r.Addr("customers"), customersHost, "POST", "/api/npm/it/"+npmCustomer+"/"+bulk, acmeSAT, []byte(body),
		"Trove-Authz-Min-Epoch", rule.Header.Get("Trove-Authz-Epoch"))
	if x.Status != http.StatusNotImplemented || npmCode(t, x) != "npm_audit_unavailable" {
		t.Errorf("on customers: %d %s", x.Status, x.Body)
	}
	if d := e.post(r, npmRemote, bulk, e.tokReader, body); d.Status != http.StatusOK {
		t.Errorf("on the default endpoint: %d %s", d.Status, d.Body)
	}
}
