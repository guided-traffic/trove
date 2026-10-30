//go:build integration

package npm

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// npm on an endpoint with exposure rules (R-NPM-048, R-EP-018), on the fixture of R-EP-012: a
// second endpoint customers with a listener of its own, a host that shares nothing with the
// default endpoint's, and the example rule acme-sdk on npm-customers. The npm format has the local
// class only in this build, so npm-customers is the local repository that holds the fixture;
// the virtual repository of the fixture, its members and its remote (R-EP-019) join with the
// remote and virtual classes of npm.

// customersHost is the host of the endpoint customers: it shares no label with trove.test, so
// a body that names the default endpoint's host is found by a substring search.
const customersHost = "registry.acme.test"

// customersEndpoint is server.endpoints of every replica: customers with the defaults of a
// further endpoint (exposure rules, no cache fill, bound service-account tokens only).
const customersEndpoint = `{"customers":{"listen":"127.0.0.1:0","base_url":"https://` + customersHost + `","surface":["packages"]}}`

// acmeRule is the example rule of R-EP-012.
const acmeRule = `{"name":"acme-sdk","repository":"npm-customers","enabled":true,
	"packages":{"include":["@acme/sdk","@acme/sdk-*"],"exclude":["@acme/sdk-internal"]},
	"versions":{"include":[],"exclude":["2.0.1"],"range":">=2.0.0"},"prereleases":false,
	"distTags":{"include":["latest","lts"],"exclude":[]}}`

// exposureEnv is the installation of the fixture with its replicas.
type exposureEnv struct {
	*env
	reps []*testenv.Replica
	// opsPAT administers through the default endpoint; acmeSAT is customer-acme's token,
	// bound to customers.
	opsPAT, acmeSAT string
	// epoch is the authz epoch of the last rule write.
	epoch string
}

// newExposureEnv seeds the fixture, starts n replicas and creates the rule.
func newExposureEnv(t *testing.T, n int) *exposureEnv {
	t.Helper()
	x := &exposureEnv{env: newEnv(t)}
	st := x.st
	st.Repository("npm-customers", "local", "npm", `{"local":{"packageOwnership":"enforce"}}`)
	ops, acme := st.User("ops"), st.ServiceAccount("customer-acme")
	st.Bind("admin", ops)
	st.Policy("customers-publish", []string{"read", "deploy", "delete"}, []string{"npm-customers"}, x.alice)
	st.Policy("customers-read", []string{"read"}, []string{"npm-customers"}, acme)
	x.opsPAT = st.Token(ops, scope.Scope{Inherit: true, Admin: true})
	x.acmeSAT = st.Token(acme, scope.Scope{Inherit: true, Endpoints: []string{"customers"}})
	for i := range n {
		x.reps = append(x.reps, st.Start(t, testenv.ReplicaOptions{Name: fmt.Sprintf("web-%c", 'a'+i), Endpoints: customersEndpoint}))
	}
	r := x.reps[0]
	for _, v := range []string{"1.9.0", "2.0.0", "2.0.1", "2.1.0"} {
		x.mustPublish(r, "npm-customers", x.tokA, pkg{Name: "@acme/sdk", Version: v})
	}
	x.mustPublish(r, "npm-customers", x.tokA, pkg{Name: "@acme/sdk", Version: "2.1.0-rc.1", Tag: "next"})
	x.mustPublish(r, "npm-customers", x.tokA, pkg{Name: "@acme/sdk-internal", Version: "1.0.0"})
	x.mustPublish(r, "npm-customers", x.tokA, pkg{Name: "@acme/billing", Version: "3.0.0"})
	a := x.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(acmeRule))
	if a.Status != http.StatusCreated {
		t.Fatalf("create the rule: %d %s", a.Status, a.Body)
	}
	x.epoch = a.Header.Get("Trove-Authz-Epoch")
	return x
}

// api sends a management API request through the first replica's default endpoint.
func (x *exposureEnv) api(method, path string, body any, hdr ...string) testenv.Answer {
	x.t.Helper()
	return x.st.Do(x.reps[0], method, path, x.opsPAT, body, hdr...)
}

// onCustomers sends a request below npm-customers to a replica's customers listener, with
// customer-acme's token, at the epoch of the last rule write.
func (x *exposureEnv) onCustomers(r *testenv.Replica, method, path string, body any, hdr ...string) testenv.Answer {
	x.t.Helper()
	hdr = append(hdr, "Trove-Authz-Min-Epoch", x.epoch)
	return x.st.DoAt(r.Addr("customers"), customersHost, method, "/api/npm/npm-customers/"+path, x.acmeSAT, body, hdr...)
}

// onDefault sends a request below npm-customers to a replica's default endpoint as alice.
func (x *exposureEnv) onDefault(r *testenv.Replica, method, path string, body any, hdr ...string) testenv.Answer {
	x.t.Helper()
	return x.st.Do(r, method, "/api/npm/npm-customers/"+path, x.tokA, body, hdr...)
}

var urlPattern = regexp.MustCompile(`https?://[^"\s]+`)

// TestR_NPM_048_DocumentsOnAnEndpointWithRules: what the npm client sees on customers.
func TestR_NPM_048_DocumentsOnAnEndpointWithRules(t *testing.T) {
	x := newExposureEnv(t, 1)
	r := x.reps[0]

	t.Run("R-NPM-048 R-EP-018 the versions of the full and the abbreviated document; default lists every version", func(t *testing.T) {
		for _, accept := range []string{"application/json", abbreviatedAccept} {
			doc := document(t, x.onCustomers(r, "GET", "@acme%2fsdk", nil, "Accept", accept))
			if got := versionNames(doc); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
				t.Fatalf("%s: versions %v", accept, got)
			}
			if tags := tagsOfDoc(doc); len(tags) != 1 || tags["latest"] != "2.1.0" {
				t.Fatalf("%s: dist-tags %v", accept, tags)
			}
		}
		all := document(t, x.onDefault(r, "GET", "@acme%2fsdk", nil))
		if got := versionNames(all); len(got) != 5 {
			t.Fatalf("default lists %v", got)
		}
		if tags := tagsOfDoc(all); tags["latest"] != "2.1.0" || tags["next"] != "2.1.0-rc.1" {
			t.Fatalf("default dist-tags %v", tags)
		}
	})

	t.Run("R-NPM-048 R-EP-018 every URL names the endpoint's host, and nothing names the default endpoint's", func(t *testing.T) {
		a := x.onCustomers(r, "GET", "@acme%2fsdk", nil)
		urls := urlPattern.FindAllString(string(a.Body), -1)
		if len(urls) != 2 {
			t.Fatalf("tarball URLs %v", urls)
		}
		for _, u := range urls {
			if !strings.HasPrefix(u, "https://"+customersHost+"/api/npm/npm-customers/@acme/sdk/-/sdk-") {
				t.Fatalf("URL %s", u)
			}
		}
		for k, v := range a.Header {
			if strings.Contains(strings.Join(v, " "), testenv.DefaultHost) {
				t.Fatalf("header %s names the default endpoint: %v", k, v)
			}
		}
		if strings.Contains(string(a.Body), testenv.DefaultHost) {
			t.Fatalf("the body names the default endpoint: %s", a.Body)
		}
		if tgz := x.onCustomers(r, "GET", "@acme%2fsdk/-/sdk-2.1.0.tgz", nil); tgz.Status != http.StatusOK {
			t.Fatalf("a listed tarball: %d %s", tgz.Status, tgz.Body)
		}
	})

	t.Run("R-EP-018 the ETag of the document differs per endpoint", func(t *testing.T) {
		c := x.onCustomers(r, "GET", "@acme%2fsdk", nil).Header.Get("ETag")
		d := x.onDefault(r, "GET", "@acme%2fsdk", nil).Header.Get("ETag")
		if c == "" || c == d {
			t.Fatalf("ETag customers %q default %q", c, d)
		}
	})

	t.Run("R-NPM-048 R-EP-013 a hidden version answers like a missing one, a hidden package like a missing package", func(t *testing.T) {
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
		same(t, "version", x.onCustomers(r, "GET", "@acme%2fsdk/1.9.0", nil), x.onCustomers(r, "GET", "@acme%2fsdk/9.9.9", nil))
		same(t, "pre-release", x.onCustomers(r, "GET", "@acme%2fsdk/2.1.0-rc.1", nil), x.onCustomers(r, "GET", "@acme%2fsdk/9.9.9", nil))
		same(t, "dropped tag", x.onCustomers(r, "GET", "@acme%2fsdk/next", nil), x.onCustomers(r, "GET", "@acme%2fsdk/nosuchtag", nil))
		same(t, "tarball", x.onCustomers(r, "GET", "@acme%2fsdk/-/sdk-1.9.0.tgz", nil), x.onCustomers(r, "GET", "@acme%2fsdk/-/sdk-9.9.9.tgz", nil))
		same(t, "excluded package", x.onCustomers(r, "GET", "@acme%2fsdk-internal", nil), x.onCustomers(r, "GET", "@acme%2fnothing", nil))
		same(t, "package without a rule", x.onCustomers(r, "GET", "@acme%2fbilling", nil), x.onCustomers(r, "GET", "@acme%2fnothing", nil))
		same(t, "dist-tags of a hidden package", x.onCustomers(r, "GET", "-/package/@acme%2fbilling/dist-tags", nil), x.onCustomers(r, "GET", "-/package/@acme%2fnothing/dist-tags", nil))
		if v := x.onCustomers(r, "GET", "@acme%2fsdk/2.0.0", nil); v.Status != http.StatusOK {
			t.Fatalf("an exposed version: %d %s", v.Status, v.Body)
		}
	})

	t.Run("R-NPM-048 latest moved to a hidden version on default stays on the highest exposed release on customers", func(t *testing.T) {
		if a := x.onDefault(r, "PUT", "-/package/@acme%2fsdk/dist-tags/latest", `"1.9.0"`); a.Status != http.StatusOK {
			t.Fatalf("dist-tag add: %d %s", a.Status, a.Body)
		}
		if tags := x.tags("npm-customers", "@acme/sdk"); tags["latest"] != "1.9.0" {
			t.Fatalf("stored tags %v", tags)
		}
		for _, accept := range []string{"application/json", abbreviatedAccept} {
			if tags := tagsOfDoc(document(t, x.onCustomers(r, "GET", "@acme%2fsdk", nil, "Accept", accept))); tags["latest"] != "2.1.0" || len(tags) != 1 {
				t.Fatalf("%s: dist-tags on customers %v", accept, tags)
			}
		}
		a := x.onCustomers(r, "GET", "-/package/@acme%2fsdk/dist-tags", nil)
		if a.Status != http.StatusOK || a.JSON(t)["latest"] != "2.1.0" {
			t.Fatalf("dist-tags route: %d %s", a.Status, a.Body)
		}
		if v := x.onCustomers(r, "GET", "@acme%2fsdk/latest", nil); v.Status != http.StatusOK || v.JSON(t)["version"] != "2.1.0" {
			t.Fatalf("version route latest: %d %s", v.Status, v.Body)
		}
		if tags := tagsOfDoc(document(t, x.onDefault(r, "GET", "@acme%2fsdk", nil))); tags["latest"] != "1.9.0" {
			t.Fatalf("default dist-tags %v", tags)
		}
	})

	t.Run("R-NPM-048 R-EP-013 the read of a write and the writes answer 403 endpoint_read_only", func(t *testing.T) {
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"GET", "@acme%2fsdk?write=true", nil},
			{"PUT", "-/package/@acme%2fsdk/dist-tags/lts", `"2.0.0"`},
			{"PUT", escapeName("@acme/sdk"), body(t, pkg{Name: "@acme/sdk", Version: "2.2.0"})},
		} {
			a := x.onCustomers(r, c.method, c.path, c.body)
			if a.Status != http.StatusForbidden || code(t, a) != errs.CodeEndpointReadOnly {
				t.Fatalf("%s %s: %d %s", c.method, c.path, a.Status, a.Body)
			}
		}
		if x.versions("npm-customers", "@acme/sdk") != 5 {
			t.Fatal("a write through customers changed the package")
		}
	})
}

// TestR_EP_018_ARuleChangeFailsTheEarlierValidatorOnEveryReplica: a conditional request with
// the ETag of a document that listed 2.0.0 answers 200 without it on every replica once a rule
// update hides 2.0.0, and the replicas shared the validator before.
func TestR_EP_018_ARuleChangeFailsTheEarlierValidatorOnEveryReplica(t *testing.T) {
	x := newExposureEnv(t, 3)
	first := x.onCustomers(x.reps[0], "GET", "@acme%2fsdk", nil)
	if got := versionNames(document(t, first)); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
		t.Fatalf("versions %v", got)
	}
	etag := first.Header.Get("ETag")
	for _, r := range x.reps {
		if a := x.onCustomers(r, "GET", "@acme%2fsdk", nil, "If-None-Match", etag); a.Status != http.StatusNotModified {
			t.Fatalf("%s: the validator of another replica answered %d", r.Name, a.Status)
		}
	}
	g := x.api("GET", "/api/v1/endpoints/customers/exposure-rules/acme-sdk", nil)
	narrowed := strings.Replace(acmeRule, `"exclude":["2.0.1"]`, `"exclude":["2.0.1","2.0.0"]`, 1)
	a := x.api("PUT", "/api/v1/endpoints/customers/exposure-rules/acme-sdk", []byte(narrowed), "If-Match", g.Header.Get("ETag"))
	if a.Status != http.StatusOK {
		t.Fatalf("narrow the rule: %d %s", a.Status, a.Body)
	}
	x.epoch = a.Header.Get("Trove-Authz-Epoch")
	for _, r := range x.reps {
		b := x.onCustomers(r, "GET", "@acme%2fsdk", nil, "If-None-Match", etag)
		if got := versionNames(document(t, b)); !slices.Equal(got, []string{"2.1.0"}) {
			t.Fatalf("%s: versions %v", r.Name, got)
		}
		if b.Header.Get("ETag") == etag {
			t.Fatalf("%s: the validator did not change", r.Name)
		}
		if tgz := x.onCustomers(r, "GET", "@acme%2fsdk/-/sdk-2.0.0.tgz", nil); tgz.Status != http.StatusNotFound {
			t.Fatalf("%s: the tarball of a version hidden since: %d", r.Name, tgz.Status)
		}
	}
}
