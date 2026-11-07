//go:build integration

package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
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
			ProtocolBasePath: "/api/opaque/{project}/{key}/", PatternSubject: formats.SubjectName, PackageNames: true,
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
	e.stack.Repository("npm-virtual", "virtual", "npm", "{}")
	acme := testenv.Person{ID: e.acmeID, Kind: "service_account"}
	e.stack.Policy("formats-read", []string{"read"}, []string{"opaque-local", "npm-customers", "npm-virtual"}, acme)
	// plat is a repository-admin (endpoints:read, no content bypass) who may read npm-customers only for @platform/*.
	plat := e.stack.User("plat")
	e.stack.Bind("repository-admin", plat)
	e.stack.Policy("platform-read", []string{"read"}, []string{"npm-customers"}, plat)
	e.stack.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern)
		SELECT id, 'package', 'include', '@platform/*' FROM authz_policy WHERE name = 'platform-read'`)
	platPAT := e.stack.Token(plat, scope.Scope{Inherit: true, System: []string{"endpoints:read"}})
	r := e.start("a")

	t.Run("R-EP-018 the format is served on the default endpoint", func(t *testing.T) {
		if a := e.send(r.def, "trove.test", "GET", "/api/opaque/it/opaque-local/left-pad", e.opsPAT, nil); a.Status != http.StatusOK {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	var epoch string
	t.Run("R-EP-018 R-EP-012 a rule for a repository of a format without the adapter is 422 format_not_exposable", func(t *testing.T) {
		a := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(`{"name":"opaque","repository":"it/opaque-local"}`))
		if a.Status != http.StatusUnprocessableEntity || fieldCode(t, a, "/repository") != errs.CodeFormatNotExposable {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		// A rule for another repository moves the epoch, so the requests below are decided on
		// an index that holds every rule written so far.
		ok := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(`{"name":"files","repository":"it/generic-local","paths":{"include":["releases/**"]}}`))
		if ok.Status != http.StatusCreated {
			t.Fatalf("%d %s", ok.Status, ok.Body)
		}
		epoch = ok.Header.Get("Trove-Authz-Epoch")
	})
	t.Run("R-EP-018 every read route answers 404 on customers, byte-identical to a missing repository, for every subject", func(t *testing.T) {
		for _, tok := range []string{e.acmeSAT, e.botAdminSAT} {
			hidden := e.send(r.cust, customersHost, "GET", "/api/opaque/it/opaque-local/left-pad", tok, nil, "Trove-Authz-Min-Epoch", epoch)
			missing := e.send(r.cust, customersHost, "GET", "/api/opaque/it/no-such-repo/left-pad", tok, nil, "Trove-Authz-Min-Epoch", epoch)
			if hidden.Status != http.StatusNotFound || string(hidden.Body) != string(missing.Body) || strings.Contains(string(hidden.Body), "2.0.0-internal") {
				t.Fatalf("hidden %d %s, missing %d %s", hidden.Status, hidden.Body, missing.Status, missing.Body)
			}
		}
	})
	t.Run("R-EP-018 R-EP-013 Q-EP-57 a write is the read-only refusal of every write on the endpoint", func(t *testing.T) {
		a := e.send(r.cust, customersHost, "PUT", "/api/opaque/it/opaque-local/left-pad", e.botAdminSAT, []byte("{}"), "Trove-Authz-Min-Epoch", epoch)
		if a.Status != http.StatusForbidden || !strings.Contains(string(a.Body), errs.CodeEndpointReadOnly) {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	t.Run("R-EP-012 R-NPM-048 npm is exposable: its rules are accepted and its ranges checked strictly", func(t *testing.T) {
		bad := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(`{"name":"bad","repository":"it/npm-customers","versions":{"range":">=2.0.0 <"}}`))
		if bad.Status != http.StatusUnprocessableEntity || fieldCode(t, bad, "/versions/range") != errs.CodeInvalidVersionRange {
			t.Fatalf("%d %s", bad.Status, bad.Body)
		}
		rule := `{"name":"acme-sdk","repository":"it/npm-customers","packages":{"include":["@acme/sdk","@acme/sdk-*"],"exclude":["@acme/sdk-internal"]},` +
			`"versions":{"exclude":["2.0.1"],"range":">=2.0.0"},"prereleases":false,"distTags":{"include":["latest","lts"]}}`
		if a := e.api("POST", "/api/v1/endpoints/customers/exposure-rules", []byte(rule)); a.Status != http.StatusCreated {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	t.Run("R-EP-016 R-NPM-048 the dry run judges npm versions with npm's syntax", func(t *testing.T) {
		a := e.api("POST", "/api/v1/endpoints/customers/check-exposure",
			[]byte(`{"repository":"it/npm-customers","package":"@acme/sdk","versions":["1.9.0","2.0.0","2.0.1","2.1.0-rc.1","2.1.0"]}`))
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
	t.Run("R-EP-016 Q-EP-61 a check without versions judges the stored versions, with their dist-tags", func(t *testing.T) {
		const repo = `(SELECT id FROM repositories WHERE key = 'npm-customers')`
		e.stack.Exec(`INSERT INTO packages (repo_id, name, display_name) VALUES (` + repo + `, '@acme/sdk', '@acme/sdk')`)
		pkg := `(SELECT id FROM packages WHERE repo_id = ` + repo + ` AND name = '@acme/sdk')`
		for _, v := range []string{"1.9.0", "2.0.0", "2.0.1", "2.1.0-rc.1", "2.1.0", "2.2.0", "3.0.0"} {
			e.stack.Exec(`INSERT INTO package_versions (package_id, version, display_version) VALUES (`+pkg+`, $1, $1)`, v)
		}
		e.stack.Exec(`UPDATE package_versions SET trashed_at = now() WHERE version = '2.2.0' AND package_id = ` + pkg)
		e.stack.Exec(`INSERT INTO npm_tombstones (repo_id, name, version, integrity, source, unpublished_at, unpublished_by)
			VALUES (` + repo + `, '@acme/sdk', '3.0.0', 'sha512-x', 'unpublish', now(), gen_random_uuid())`)
		e.stack.Exec(`INSERT INTO npm_packages (package_id, repo_id, name_fold, dist_tags) VALUES (` + pkg + `, ` + repo + `, '@acme/sdk',
			'{"latest":"2.1.0","lts":"1.9.0","next":"2.1.0-rc.1"}')`)

		type verdict struct {
			Version string `json:"version"`
			Exposed bool   `json:"exposed"`
			Reason  string `json:"reason"`
		}
		var res struct {
			Versions []verdict `json:"versions"`
			DistTags []struct {
				Tag     string `json:"tag"`
				Version string `json:"version"`
				Visible bool   `json:"visible"`
			} `json:"distTags"`
			Warnings []string `json:"warnings"`
		}
		for _, body := range []string{`{"repository":"it/npm-customers","package":"@acme/sdk"}`, `{"repository":"it/npm-customers","package":"@acme/sdk","versions":[]}`} {
			a := e.api("POST", "/api/v1/endpoints/customers/check-exposure", []byte(body))
			res.Versions, res.DistTags = nil, nil
			if a.Status != http.StatusOK || json.Unmarshal(a.Body, &res) != nil {
				t.Fatalf("%d %s", a.Status, a.Body)
			}
			// Newest first. The trashed 2.2.0 and the tombstoned 3.0.0 are not stored versions any more.
			want := []verdict{
				{"2.1.0", true, "exposed"},
				{"2.1.0-rc.1", false, "prerelease"},
				{"2.0.1", false, "version_excluded"},
				{"2.0.0", true, "exposed"},
				{"1.9.0", false, "out_of_range"},
			}
			if len(res.Versions) != len(want) {
				t.Fatalf("%s: %s", body, a.Body)
			}
			for i, w := range want {
				if res.Versions[i] != w {
					t.Fatalf("%s: version %d = %+v, want %+v (%s)", body, i, res.Versions[i], w, a.Body)
				}
			}
			// latest stays: the rule admits the tag and 2.1.0 is exposed. lts is admitted by name but points
			// at a version out of range, and next is not in the include list.
			wantTags := map[string]bool{"latest": true, "lts": false, "next": false}
			if len(res.DistTags) != 3 || res.DistTags[0].Tag != "latest" || res.DistTags[1].Tag != "lts" || res.DistTags[2].Tag != "next" {
				t.Fatalf("%s: distTags %+v", body, res.DistTags)
			}
			for _, tg := range res.DistTags {
				if tg.Visible != wantTags[tg.Tag] {
					t.Fatalf("%s: tag %s visible = %v (%s)", body, tg.Tag, tg.Visible, a.Body)
				}
			}
		}
		// Versions named in the request are judged as sent, and the tags are still answered.
		a := e.api("POST", "/api/v1/endpoints/customers/check-exposure", []byte(`{"repository":"it/npm-customers","package":"@acme/sdk","versions":["2.0.0"]}`))
		res.Versions, res.DistTags = nil, nil
		if a.Status != http.StatusOK || json.Unmarshal(a.Body, &res) != nil || len(res.Versions) != 1 || len(res.DistTags) != 3 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		// A virtual repository stores nothing of its own: the answer says it cannot list, instead of
		// passing an empty list off as "nothing is stored".
		a = e.api("POST", "/api/v1/endpoints/customers/check-exposure", []byte(`{"repository":"it/npm-virtual","package":"@acme/sdk"}`))
		res.Versions, res.DistTags, res.Warnings = nil, nil, nil
		if a.Status != http.StatusOK || json.Unmarshal(a.Body, &res) != nil || len(res.Versions) != 0 || len(res.Warnings) != 1 || res.Warnings[0] != "stored_versions_not_listed" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		// A package the repository does not hold lists nothing.
		a = e.api("POST", "/api/v1/endpoints/customers/check-exposure", []byte(`{"repository":"it/npm-customers","package":"@acme/none"}`))
		res.Versions, res.DistTags = nil, nil
		if a.Status != http.StatusOK || json.Unmarshal(a.Body, &res) != nil || len(res.Versions) != 0 || len(res.DistTags) != 0 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	t.Run("R-EP-016 Q-EP-62 a check lists the stored versions and tags only of packages the caller may read", func(t *testing.T) {
		const repo = `(SELECT id FROM repositories WHERE key = 'npm-customers')`
		e.stack.Exec(`INSERT INTO packages (repo_id, name, display_name) VALUES (` + repo + `, '@platform/sdk', '@platform/sdk')`)
		pkg := `(SELECT id FROM packages WHERE repo_id = ` + repo + ` AND name = '@platform/sdk')`
		e.stack.Exec(`INSERT INTO package_versions (package_id, version, display_version) VALUES (` + pkg + `, '1.0.0', '1.0.0')`)
		e.stack.Exec(`INSERT INTO npm_packages (package_id, repo_id, name_fold, dist_tags) VALUES (` + pkg + `, ` + repo + `, '@platform/sdk', '{"latest":"1.0.0"}')`)
		// @acme/sdk, seeded above with versions and tags, is a package the caller may not read.
		check := func(pkgName string) (versions, tags int, body string) {
			a := e.send(r.def, "trove.test", "POST", "/api/v1/endpoints/customers/check-exposure", platPAT,
				[]byte(`{"repository":"it/npm-customers","package":"`+pkgName+`"}`))
			var res struct {
				Versions []json.RawMessage `json:"versions"`
				DistTags []json.RawMessage `json:"distTags"`
			}
			if a.Status != http.StatusOK || json.Unmarshal(a.Body, &res) != nil {
				t.Fatalf("%s: %d %s", pkgName, a.Status, a.Body)
			}
			return len(res.Versions), len(res.DistTags), strings.ReplaceAll(string(a.Body), pkgName, "PKG")
		}
		if v, tg, body := check("@platform/sdk"); v != 1 || tg != 1 {
			t.Fatalf("the readable package lists %d versions and %d tags: %s", v, tg, body)
		}
		vh, th, held := check("@acme/sdk")
		vm, tm, missing := check("@acme/sdk-none") // admitted by the same rule, held by no one
		if vh != 0 || th != 0 || vm != 0 || tm != 0 || held != missing {
			t.Fatalf("a package the caller may not read must answer as one the repository does not hold:\n%s\n%s", held, missing)
		}
	})
}
