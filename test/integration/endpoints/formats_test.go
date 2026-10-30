//go:build integration

package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// Package formats on an endpoint with exposure rules (R-EP-018): a format without the exposure
// adapter is not exposable, and npm, which has one, is.

// opaqueFormat is a package format of this test without an exposure adapter (formats.Base
// returns none): one document route and one upload route. It is registered for the whole test
// binary, so the repository model accepts it and the dispatcher serves its prefix on every
// endpoint of every replica.
type opaqueFormat struct{ formats.Base }

func init() { formats.Register(opaqueFormat{}) }

type foldedNames struct{}

func (foldedNames) Normalize(name string) (string, bool) { return strings.ToLower(name), name != "" }

func (opaqueFormat) Descriptor() formats.Descriptor {
	return formats.Descriptor{
		Facts: catalog.Facts{
			ID: "opaque", DisplayName: "Opaque", Classes: []formats.Class{formats.Local}, Layout: "opaque",
			ProtocolBasePath: "/api/opaque/{key}/", PatternSubject: formats.SubjectName, PackageNames: true,
		},
		ContractVersion: formats.ContractVersion, Names: foldedNames{}, PropertyPrefix: "opaque.",
	}
}

func (f opaqueFormat) Routes() []formats.Route {
	return []formats.Route{
		{
			Name: "opaque.doc.get", Method: http.MethodGet, Template: "/{package}", Examples: []string{"left-pad"},
			Action: formats.ActionRead, Kind: formats.Metadata, Handler: f.document,
		},
		{
			Name: "opaque.doc.put", Method: http.MethodPut, Template: "/{package}", Examples: []string{"left-pad"},
			Action: formats.ActionDeploy, Kind: formats.Upload, Handler: f.upload,
		},
	}
}

func (f opaqueFormat) Match(m, p string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(f.Routes(), m, p)
}

func (opaqueFormat) Classify(p formats.Path) formats.PathInfo {
	return formats.PathInfo{Kind: formats.PathMetadata, Package: p.Path}
}

func (opaqueFormat) PackageDocumentPath(name string) (formats.Path, bool) {
	return formats.Path{Path: name}, true
}

// document lists everything of the package: a format without a filter has nothing it could
// leave out, which is why it is not served where rules apply.
func (opaqueFormat) document(_ context.Context, rq *formats.Request, w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]any{"name": rq.Params.Get("package"), "versions": []string{"1.0.0", "2.0.0-internal"}})
}

func (opaqueFormat) upload(_ context.Context, _ *formats.Request, w http.ResponseWriter) error {
	w.WriteHeader(http.StatusCreated)
	return nil
}

// WriteError is the format's own error shape: code and detail.
func (opaqueFormat) WriteError(w http.ResponseWriter, _ *http.Request, err error) {
	p, _ := errs.ProblemOf(err, "", "")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": p.Code, "detail": p.Detail})
}

// fieldCode is the code of the field error at a pointer of a 422 (R-API-009).
func fieldCode(t *testing.T, a answer, pointer string) string {
	t.Helper()
	var p struct {
		Errors []struct{ Pointer, Code string } `json:"errors"`
	}
	if err := json.Unmarshal(a.Body, &p); err != nil {
		t.Fatalf("%v: %s", err, a.Body)
	}
	for _, f := range p.Errors {
		if f.Pointer == pointer {
			return f.Code
		}
	}
	return ""
}

// TestR_EP_018_FormatsOnAnEndpointWithRules: a format without the adapter answers 404 on every
// read route on customers and 403 endpoint_read_only on a write (Q-EP-57), and a rule for one of
// its repositories is refused; npm declares its adapter, so its rules are accepted, its ranges
// are checked by its own syntax and the dry run judges its versions.
func TestR_EP_018_FormatsOnAnEndpointWithRules(t *testing.T) {
	e := newEnv(t, false)
	e.stack.Repository("opaque-local", "local", "opaque", "{}")
	e.stack.Repository("npm-customers", "local", "npm", "{}")
	acme := testenv.Person{ID: e.acmeID, Kind: "service_account"}
	e.stack.Policy("formats-read", []string{"read"}, []string{"opaque-local", "npm-customers"}, acme)
	r := e.start("a")

	t.Run("R-EP-018 the format is served on the default endpoint", func(t *testing.T) {
		if a := e.send(r.def, "trove.test", "GET", "/api/opaque/opaque-local/left-pad", e.opsPAT, nil); a.Status != http.StatusOK {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	var epoch string
	t.Run("R-EP-018 R-EP-012 a rule for a repository of a format without the adapter is 422 format_not_exposable", func(t *testing.T) {
		a := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(`{"name":"opaque","repository":"opaque-local"}`))
		if a.Status != http.StatusUnprocessableEntity || fieldCode(t, a, "/repository") != errs.CodeFormatNotExposable {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		// A rule for another repository moves the epoch, so the requests below are decided on
		// an index that holds every rule written so far.
		ok := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(`{"name":"files","repository":"generic-local","paths":{"include":["releases/**"]}}`))
		if ok.Status != http.StatusCreated {
			t.Fatalf("%d %s", ok.Status, ok.Body)
		}
		epoch = ok.Header.Get("Trove-Authz-Epoch")
	})
	t.Run("R-EP-018 every read route answers 404 on customers, byte-identical to a missing repository, for every subject", func(t *testing.T) {
		for _, tok := range []string{e.acmeSAT, e.botAdminSAT} {
			hidden := e.send(r.cust, customersHost, "GET", "/api/opaque/opaque-local/left-pad", tok, nil, "Trove-Authz-Min-Epoch", epoch)
			missing := e.send(r.cust, customersHost, "GET", "/api/opaque/no-such-repo/left-pad", tok, nil, "Trove-Authz-Min-Epoch", epoch)
			if hidden.Status != http.StatusNotFound || string(hidden.Body) != string(missing.Body) || strings.Contains(string(hidden.Body), "2.0.0-internal") {
				t.Fatalf("hidden %d %s, missing %d %s", hidden.Status, hidden.Body, missing.Status, missing.Body)
			}
		}
	})
	t.Run("R-EP-018 R-EP-013 Q-EP-57 a write is the read-only refusal of every write on the endpoint", func(t *testing.T) {
		a := e.send(r.cust, customersHost, "PUT", "/api/opaque/opaque-local/left-pad", e.botAdminSAT, []byte("{}"), "Trove-Authz-Min-Epoch", epoch)
		if a.Status != http.StatusForbidden || !strings.Contains(string(a.Body), errs.CodeEndpointReadOnly) {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	t.Run("R-EP-012 R-NPM-048 npm is exposable: its rules are accepted and its ranges checked strictly", func(t *testing.T) {
		bad := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(`{"name":"bad","repository":"npm-customers","versions":{"range":">=2.0.0 <"}}`))
		if bad.Status != http.StatusUnprocessableEntity || fieldCode(t, bad, "/versions/range") != errs.CodeInvalidVersionRange {
			t.Fatalf("%d %s", bad.Status, bad.Body)
		}
		rule := `{"name":"acme-sdk","repository":"npm-customers","packages":{"include":["@acme/sdk","@acme/sdk-*"],"exclude":["@acme/sdk-internal"]},` +
			`"versions":{"exclude":["2.0.1"],"range":">=2.0.0"},"prereleases":false,"distTags":{"include":["latest","lts"]}}`
		if a := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(rule)); a.Status != http.StatusCreated {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	t.Run("R-EP-016 R-NPM-048 the dry run judges npm versions with npm's syntax", func(t *testing.T) {
		a := e.api("POST", "/api/v1/endpoints/customers/check-exposure",
			[]byte(`{"repository":"npm-customers","package":"@acme/sdk","versions":["1.9.0","2.0.0","2.0.1","2.1.0-rc.1","2.1.0"]}`))
		var res struct {
			PackageExposed bool `json:"packageExposed"`
			Versions       []struct {
				Version string `json:"version"`
				Exposed bool   `json:"exposed"`
				Reason  string `json:"reason"`
			} `json:"versions"`
		}
		if a.Status != http.StatusOK || json.Unmarshal(a.Body, &res) != nil || !res.PackageExposed || len(res.Versions) != 5 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		want := map[string]string{"1.9.0": "out_of_range", "2.0.0": "exposed", "2.0.1": "version_excluded", "2.1.0-rc.1": "prerelease", "2.1.0": "exposed"}
		for _, v := range res.Versions {
			if want[v.Version] != v.Reason || v.Exposed != (v.Reason == "exposed") {
				t.Fatalf("%s: %+v (%s)", v.Version, v, a.Body)
			}
		}
	})
}
