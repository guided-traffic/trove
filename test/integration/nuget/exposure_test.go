//go:build integration

package nuget

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// NuGet on an endpoint with exposure rules (R-NUGET-034, R-EP-018), on the fixture of the spec's
// acceptance criteria: Acme.Sdk 1.9.0, 2.0.0, 2.1.0-beta.1 and 2.1.0 in nuget-customers, a second
// endpoint customers with a listener and a host of its own, and a rule that exposes Acme.Sdk from
// 2.0 (the bare notation: 2.0 or higher) without pre-releases.

// customersHost shares no label with the default endpoint's host, so a body that names the default
// endpoint is found by a substring search.
const customersHost = "registry.acme.test"

const customersEndpoint = `{"customers":{"listen":"127.0.0.1:0","base_url":"https://` + customersHost + `","surface":["packages"]}}`

// sdkRule exposes Acme.Sdk (in the spelling of the package, the rule folds names with the rule of
// NuGet) from 2.0 up; Acme.Sdk.Internal and Acme.Billing match no include.
const sdkRule = `{"name":"acme-sdk","repository":"nuget-customers","enabled":true,
	"packages":{"include":["Acme.Sdk"],"exclude":[]},
	"versions":{"include":[],"exclude":[],"range":"2.0"},"prereleases":false}`

type exposureEnv struct {
	t    *testing.T
	st   *testenv.Stack
	reps []*testenv.Replica
	// opsPAT administers through the default endpoint, devPAT deploys, acmeSAT reads and is
	// bound to customers.
	opsPAT, devPAT, acmeSAT string
	// epoch is the authz epoch of the last rule write.
	epoch string
}

func newExposureEnv(t *testing.T, replicas int) *exposureEnv {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "nugetexp"})
	x := &exposureEnv{t: t, st: st}
	ops, dev, acme := st.User("ops"), st.User("dev"), st.ServiceAccount("customer-acme")
	st.Bind("admin", ops)
	st.Repository("nuget-customers", "local", "nuget", "{}")
	st.Policy("customers-publish", []string{"read", "deploy", "delete"}, []string{"nuget-customers"}, dev)
	st.Policy("customers-read", []string{"read"}, []string{"nuget-customers"}, acme)
	x.opsPAT = st.Token(ops, scope.Scope{Inherit: true, Admin: true})
	x.devPAT = st.Token(dev, scope.Default())
	x.acmeSAT = st.Token(acme, scope.Scope{Inherit: true, Endpoints: []string{"customers"}})
	for i := range replicas {
		x.reps = append(x.reps, st.Start(t, testenv.ReplicaOptions{Name: fmt.Sprintf("web-%c", 'a'+i), Endpoints: customersEndpoint}))
	}
	for _, p := range []pkgSpec{
		{id: "Acme.Sdk", version: "1.9.0"},
		{id: "Acme.Sdk", version: "2.0.0"},
		{id: "Acme.Sdk", version: "2.1.0-beta.1"},
		{id: "Acme.Sdk", version: "2.1.0"},
		{id: "Acme.Sdk.Internal", version: "1.0.0"},
		{id: "Acme.Billing", version: "3.0.0"},
	} {
		body, ct := form(t, nupkg(t, p))
		if a := st.Do(x.reps[0], "PUT", "/api/nuget/nuget-customers/v3/package", x.devPAT, body, "Content-Type", ct); a.Status != http.StatusCreated {
			t.Fatalf("push %s %s: %d %s", p.id, p.version, a.Status, a.Body)
		}
	}
	a := x.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(sdkRule))
	if a.Status != http.StatusCreated {
		t.Fatalf("create the rule: %d %s", a.Status, a.Body)
	}
	x.epoch = a.Header.Get("Trove-Authz-Epoch")
	return x
}

func (x *exposureEnv) api(method, path string, body any, hdr ...string) testenv.Answer {
	x.t.Helper()
	return x.st.Do(x.reps[0], method, path, x.opsPAT, body, hdr...)
}

// onCustomers sends a request below nuget-customers to a replica's customers listener with the
// bound token, at the epoch of the last rule write.
func (x *exposureEnv) onCustomers(r *testenv.Replica, method, path string, body any, hdr ...string) testenv.Answer {
	x.t.Helper()
	hdr = append(hdr, "Trove-Authz-Min-Epoch", x.epoch)
	return x.st.DoAt(r.Addr("customers"), customersHost, method, "/api/nuget/nuget-customers/"+path, x.acmeSAT, body, hdr...)
}

// onDefault sends a request below nuget-customers to the default endpoint as the developer.
func (x *exposureEnv) onDefault(r *testenv.Replica, method, path string, body any, hdr ...string) testenv.Answer {
	x.t.Helper()
	return x.st.Do(r, method, "/api/nuget/nuget-customers/"+path, x.devPAT, body, hdr...)
}

func jsonOf(t *testing.T, a testenv.Answer, into any) {
	t.Helper()
	if a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if err := json.Unmarshal(a.Body, into); err != nil {
		t.Fatalf("%v: %s", err, a.Body)
	}
}

func listed(t *testing.T, a testenv.Answer) []string {
	t.Helper()
	var d struct {
		Versions []string `json:"versions"`
	}
	jsonOf(t, a, &d)
	return d.Versions
}

// registered lists the versions of the leaves of a registration index, fetching the pages that
// are not inlined.
func (x *exposureEnv) registered(r *testenv.Replica, hive, id string) []string {
	x.t.Helper()
	var d struct {
		Items []struct {
			ID    string `json:"@id"`
			Items []struct {
				Entry struct {
					Version string `json:"version"`
				} `json:"catalogEntry"`
			} `json:"items"`
		} `json:"items"`
	}
	jsonOf(x.t, x.onCustomers(r, "GET", "v3/"+hive+"/"+id+"/index.json", nil), &d)
	var out []string
	for _, p := range d.Items {
		for _, l := range p.Items {
			out = append(out, l.Entry.Version)
		}
	}
	return out
}

// TestR_NUGET_034_DocumentsOnAnEndpointWithRules: what a NuGet client sees on customers, through
// the whole stack: the dispatcher, the bound token, the decision point with its ceiling and the
// data port on PostgreSQL.
func TestR_NUGET_034_DocumentsOnAnEndpointWithRules(t *testing.T) {
	x := newExposureEnv(t, 1)
	r := x.reps[0]

	t.Run("R-NUGET-034 the version list, the registrations and the search list the exposed versions; default lists all", func(t *testing.T) {
		if got := listed(t, x.onCustomers(r, "GET", "v3/flatcontainer/acme.sdk/index.json", nil)); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
			t.Fatalf("customers version list %v", got)
		}
		if got := listed(t, x.onDefault(r, "GET", "v3/flatcontainer/acme.sdk/index.json", nil)); !slices.Equal(got, []string{"1.9.0", "2.0.0", "2.1.0-beta.1", "2.1.0"}) {
			t.Fatalf("default version list %v", got)
		}
		for _, hive := range []string{"registration", "registration-gz", "registration-semver2"} {
			if got := x.registered(r, hive, "acme.sdk"); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
				t.Fatalf("%s lists %v", hive, got)
			}
		}
		var s struct {
			TotalHits int `json:"totalHits"`
			Data      []struct {
				ID       string `json:"id"`
				Version  string `json:"version"`
				Versions []struct {
					Version string `json:"version"`
				} `json:"versions"`
			} `json:"data"`
		}
		jsonOf(t, x.onCustomers(r, "GET", "v3/search?q=acme&prerelease=true", nil), &s)
		if s.TotalHits != 1 || len(s.Data) != 1 || s.Data[0].ID != "Acme.Sdk" || s.Data[0].Version != "2.1.0" || len(s.Data[0].Versions) != 2 {
			t.Fatalf("search on customers = %+v", s)
		}
		jsonOf(t, x.onDefault(r, "GET", "v3/search?q=acme&prerelease=true", nil), &s)
		if s.TotalHits != 3 {
			t.Fatalf("search on default = %+v", s)
		}
	})

	t.Run("R-NUGET-034 R-EP-013 a hidden version and a hidden package answer like missing ones", func(t *testing.T) {
		same := func(t *testing.T, what string, hidden, missing testenv.Answer) {
			t.Helper()
			if hidden.Status != http.StatusNotFound || missing.Status != http.StatusNotFound || string(hidden.Body) != string(missing.Body) {
				t.Fatalf("%s: hidden %d %s, missing %d %s", what, hidden.Status, hidden.Body, missing.Status, missing.Body)
			}
			for _, h := range []string{"Content-Type", "Cache-Control", "WWW-Authenticate"} {
				if hidden.Header.Get(h) != missing.Header.Get(h) {
					t.Fatalf("%s: %s %q and %q", what, h, hidden.Header.Get(h), missing.Header.Get(h))
				}
			}
		}
		for _, hive := range []string{"registration", "registration-gz", "registration-semver2"} {
			same(t, hive+" leaf", x.onCustomers(r, "GET", "v3/"+hive+"/acme.sdk/1.9.0.json", nil), x.onCustomers(r, "GET", "v3/"+hive+"/acme.sdk/9.9.9.json", nil))
		}
		same(t, "pre-release leaf", x.onCustomers(r, "GET", "v3/registration-semver2/acme.sdk/2.1.0-beta.1.json", nil), x.onCustomers(r, "GET", "v3/registration-semver2/acme.sdk/9.9.9.json", nil))
		same(t, ".nupkg", x.onCustomers(r, "GET", "v3/flatcontainer/acme.sdk/1.9.0/acme.sdk.1.9.0.nupkg", nil), x.onCustomers(r, "GET", "v3/flatcontainer/acme.sdk/9.9.9/acme.sdk.9.9.9.nupkg", nil))
		same(t, ".nuspec", x.onCustomers(r, "GET", "v3/flatcontainer/acme.sdk/2.1.0-beta.1/acme.sdk.nuspec", nil), x.onCustomers(r, "GET", "v3/flatcontainer/acme.sdk/9.9.9/acme.sdk.nuspec", nil))
		same(t, "excluded package", x.onCustomers(r, "GET", "v3/flatcontainer/acme.sdk.internal/index.json", nil), x.onCustomers(r, "GET", "v3/flatcontainer/acme.nothing/index.json", nil))
		same(t, "package without a rule", x.onCustomers(r, "GET", "v3/registration/acme.billing/index.json", nil), x.onCustomers(r, "GET", "v3/registration/acme.nothing/index.json", nil))
		if a := x.onCustomers(r, "GET", "v3/flatcontainer/acme.sdk/2.0.0/acme.sdk.2.0.0.nupkg", nil); a.Status != http.StatusOK {
			t.Fatalf("an exposed .nupkg: %d %s", a.Status, a.Body)
		}
		if a := x.onCustomers(r, "GET", "v3/flatcontainer/ACME.Sdk/2.0.0.0/Acme.Sdk.2.0.0.0.nupkg", nil); a.Status != http.StatusOK {
			t.Fatalf("an exposed .nupkg in another spelling: %d %s", a.Status, a.Body)
		}
	})

	t.Run("R-NUGET-034 R-EP-018 every URL names the endpoint's host and the ETag differs per endpoint", func(t *testing.T) {
		a := x.onCustomers(r, "GET", "v3/registration-semver2/acme.sdk/index.json", nil)
		if !strings.Contains(string(a.Body), "https://"+customersHost+"/api/nuget/nuget-customers/v3/") || strings.Contains(string(a.Body), testenv.DefaultHost) {
			t.Fatalf("the URLs of the document: %s", a.Body)
		}
		for k, v := range a.Header {
			if strings.Contains(strings.Join(v, " "), testenv.DefaultHost) {
				t.Fatalf("header %s names the default endpoint: %v", k, v)
			}
		}
		d := x.onDefault(r, "GET", "v3/registration-semver2/acme.sdk/index.json", nil)
		if a.Header.Get("ETag") == "" || a.Header.Get("ETag") == d.Header.Get("ETag") {
			t.Fatalf("ETag customers %q default %q", a.Header.Get("ETag"), d.Header.Get("ETag"))
		}
	})

	t.Run("R-NUGET-034 the service index of customers lists no publish resource", func(t *testing.T) {
		c, d := x.onCustomers(r, "GET", "v3/index.json", nil), x.onDefault(r, "GET", "v3/index.json", nil)
		if c.Status != http.StatusOK || d.Status != http.StatusOK {
			t.Fatalf("%d and %d", c.Status, d.Status)
		}
		for _, res := range []string{"PackagePublish", "SymbolPackagePublish"} {
			if strings.Contains(string(c.Body), res) || !strings.Contains(string(d.Body), res) {
				t.Fatalf("%s: customers %s, default %s", res, c.Body, d.Body)
			}
		}
		if !strings.Contains(string(c.Body), "PackageBaseAddress/3.0.0") || !strings.Contains(string(c.Body), "SearchQueryService/3.5.0") {
			t.Fatalf("customers lacks the read resources: %s", c.Body)
		}
	})

	t.Run("R-NUGET-034 R-EP-013 push, delete and relist answer 403 endpoint_read_only and change nothing", func(t *testing.T) {
		body, ct := form(t, nupkg(t, pkgSpec{id: "Acme.Sdk", version: "2.2.0"}))
		for _, c := range []struct {
			what, method, path string
			body               any
			hdr                []string
		}{
			{"push with the token as the API key", "PUT", "v3/package", body, []string{"Content-Type", ct, "X-NuGet-ApiKey", x.acmeSAT}},
			{"push as a bearer token", "PUT", "v3/package", body, []string{"Content-Type", ct}},
			{"unlist", "DELETE", "v3/package/acme.sdk/2.0.0", nil, []string{"X-NuGet-ApiKey", x.acmeSAT}},
			{"relist", "POST", "v3/package/acme.sdk/2.0.0", nil, []string{"X-NuGet-ApiKey", x.acmeSAT}},
		} {
			a := x.onCustomers(r, c.method, c.path, c.body, c.hdr...)
			if a.Status != http.StatusForbidden || code(t, a) != "endpoint_read_only" {
				t.Fatalf("%s: %d %s", c.what, a.Status, a.Body)
			}
		}
		if n := x.st.Number(`SELECT count(*) FROM nuget_version WHERE lower_id = 'acme.sdk'`); n != 4 {
			t.Fatalf("a write through customers changed the package: %d versions", n)
		}
		if got := listed(t, x.onCustomers(r, "GET", "v3/flatcontainer/acme.sdk/index.json", nil)); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
			t.Fatalf("version list after the refused writes %v", got)
		}
	})
}

// TestR_EP_018_NuGetRuleChangeFailsTheEarlierValidatorOnEveryReplica: a conditional request with
// the validator of a document that listed 2.0.0 answers 200 without it on every replica once the
// rule narrows, and the replicas shared the validator before.
func TestR_EP_018_NuGetRuleChangeFailsTheEarlierValidatorOnEveryReplica(t *testing.T) {
	x := newExposureEnv(t, 2)
	path := "v3/flatcontainer/acme.sdk/index.json"
	first := x.onCustomers(x.reps[0], "GET", path, nil)
	if got := listed(t, first); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
		t.Fatalf("version list %v", got)
	}
	etag := first.Header.Get("ETag")
	for _, r := range x.reps {
		if a := x.onCustomers(r, "GET", path, nil, "If-None-Match", etag); a.Status != http.StatusNotModified {
			t.Fatalf("%s: the validator of another replica answered %d", r.Name, a.Status)
		}
	}
	g := x.api("GET", "/api/v1/endpoints/customers/exposure-rules/acme-sdk", nil)
	narrowed := strings.Replace(sdkRule, `"range":"2.0"`, `"range":"(2.0,)"`, 1)
	a := x.api("PUT", "/api/v1/endpoints/customers/exposure-rules/acme-sdk", []byte(narrowed), "If-Match", g.Header.Get("ETag"))
	if a.Status != http.StatusOK {
		t.Fatalf("narrow the rule: %d %s", a.Status, a.Body)
	}
	x.epoch = a.Header.Get("Trove-Authz-Epoch")
	for _, r := range x.reps {
		b := x.onCustomers(r, "GET", path, nil, "If-None-Match", etag)
		if got := listed(t, b); !slices.Equal(got, []string{"2.1.0"}) {
			t.Fatalf("%s: version list %v", r.Name, got)
		}
		if b.Header.Get("ETag") == etag {
			t.Fatalf("%s: the validator did not change", r.Name)
		}
		if a := x.onCustomers(r, "GET", "v3/flatcontainer/acme.sdk/2.0.0/acme.sdk.2.0.0.nupkg", nil); a.Status != http.StatusNotFound {
			t.Fatalf("%s: the file of the version the rule now hides: %d", r.Name, a.Status)
		}
	}
}
