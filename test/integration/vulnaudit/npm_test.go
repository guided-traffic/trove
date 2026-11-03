//go:build integration

package vulnaudit

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const bulk = "-/npm/v1/security/advisories/bulk"

// seedNPM inserts the npm advisories of the tests and marks the data complete.
func (e *env) seedNPM() {
	e.t.Helper()
	semver := func(events ...string) string { return rangeJSON("SEMVER", events...) }
	e.advisory("npm", "GHSA-aaaa-aaaa-0001", "GHSA-aaaa-aaaa-0001", "vulnerability", "high", "Test vulnerability", []string{"CVE-2026-0002"}, false,
		aff{name: "trove-test-vuln", ranges: semver(ev("introduced", "1.0.0"), ev("fixed", "1.0.1"))})
	e.advisory("npm", "GHSA-aaaa-aaaa-0003", "GHSA-aaaa-aaaa-0003", "vulnerability", "medium", "left-pad has no fix", nil, false,
		aff{name: "left-pad", ranges: semver(ev("introduced", "0"), ev("last_affected", "1.3.0"))})
	e.advisory("npm", "MAL-2026-1234", "MAL-2026-1234", "malicious", "critical", "", nil, false, aff{name: "trove-test-mal", versions: []string{"1.0.0"}})
	e.advisory("npm", "MAL-2026-1235", "MAL-2026-1235", "malicious", "critical", "", nil, false, aff{name: "@acme/ui", versions: []string{"1.0.0"}})
	e.advisory("npm", "GHSA-aaaa-aaaa-0020", "GHSA-aaaa-aaaa-0020", "vulnerability", "low", "Vendored copy", nil, false, aff{name: "@vendored/lib", every: true})
	e.advisory("npm", "GHSA-aaaa-aaaa-0009", "GHSA-aaaa-aaaa-0009", "vulnerability", "high", "Withdrawn", nil, true, aff{name: "trove-test-vuln", every: true})
	e.advisory("npm", "GHSA-aaaa-aaaa-0030", "GHSA-aaaa-aaaa-0030", "vulnerability", "high", "Beta inside the range", nil, false,
		aff{name: "beta-pkg", ranges: semver(ev("introduced", "1.0.0"), ev("fixed", "2.0.0"))})
	e.data("npm", true, 1, 0)
}

// answer is the parsed answer of the bulk endpoint.
type answer map[string][]struct {
	ID                 int64    `json:"id"`
	URL                string   `json:"url"`
	Title              string   `json:"title"`
	Severity           string   `json:"severity"`
	VulnerableVersions string   `json:"vulnerable_versions"`
	CWE                []string `json:"cwe"`
}

func parse(t *testing.T, a testenv.Answer) answer {
	t.Helper()
	if a.Status != http.StatusOK {
		t.Fatalf("status %d: %s", a.Status, a.Body)
	}
	var out answer
	if err := json.Unmarshal(a.Body, &out); err != nil {
		t.Fatalf("%v: %s", err, a.Body)
	}
	return out
}

const body = `{"trove-test-vuln":["1.0.0","1.0.1"],"left-pad":["1.3.0"],"trove-test-mal":["1.0.0"],"@acme/ui":["1.0.0"],"@vendored/lib":["2.0.0"],"lodash":["4.17.21"]}`

// TestR_VULN_017_TheAuditIsAnsweredFromTrovesDataOnEveryClass: local, remote and virtual repositories answer in mode local
// with the same bytes, on two replicas; withdrawn advisories and advisories of other versions are not listed; an internal
// name is withheld, a name of a repository that declares public packages is not.
func TestR_VULN_017_TheAuditIsAnsweredFromTrovesDataOnEveryClass(t *testing.T) {
	e := newEnv(t)
	e.seedNPM()
	a, b := e.replica("web-a"), e.replica("web-b")

	first := e.post(a, npmRemote, bulk, e.tokReader, body)
	got := parse(t, first)
	if len(got) != 4 {
		t.Fatalf("names %v: %s", got, first.Body)
	}
	vuln := got["trove-test-vuln"]
	if len(vuln) != 1 || vuln[0].VulnerableVersions != ">=1.0.0 <1.0.1" || vuln[0].Severity != "high" ||
		vuln[0].URL != "https://github.com/advisories/GHSA-aaaa-aaaa-0001" || vuln[0].ID != advisory.NPMAuditID("GHSA-aaaa-aaaa-0001", 0) {
		t.Errorf("trove-test-vuln: %+v", vuln)
	}
	if lp := got["left-pad"]; len(lp) != 1 || lp[0].VulnerableVersions != "<=1.3.0" {
		t.Errorf("left-pad: %+v", lp)
	}
	if mal := got["trove-test-mal"]; len(mal) != 1 || mal[0].Severity != "critical" || mal[0].Title != "Malicious code in trove-test-mal" ||
		!slices.Equal(mal[0].CWE, []string{"CWE-506"}) {
		t.Errorf("trove-test-mal: %+v", mal)
	}
	if _, ok := got["@acme/ui"]; ok {
		t.Error("the internal name @acme/ui is answered")
	}
	if v := got["@vendored/lib"]; len(v) != 1 {
		t.Errorf("a name of a repository that holds public packages is withheld: %+v", v)
	}
	if want := "trove audited 5 of 6 packages from its own advisory data; 1 internal names were not matched against public advisories"; first.Header.Get("npm-notice") != want {
		t.Errorf("npm-notice %q", first.Header.Get("npm-notice"))
	}
	if first.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", first.Header.Get("Cache-Control"))
	}

	for _, c := range []struct {
		name string
		rep  *testenv.Replica
		repo string
	}{{"npm-local", a, npmLocal}, {"npm-all", a, npmAll}, {"npm-all on the other replica", b, npmAll}, {"npm-remote on the other replica", b, npmRemote}} {
		other := e.post(c.rep, c.repo, bulk, e.tokReader, body)
		if other.Status != 200 || string(other.Body) != string(first.Body) || other.Header.Get("npm-notice") != first.Header.Get("npm-notice") {
			t.Errorf("%s: %d, the answer differs:\n%s\n%s", c.name, other.Status, first.Body, other.Body)
		}
	}
	// A body of names without an advisory is {}.
	if none := e.post(a, npmRemote, bulk, e.tokReader, `{"lodash":["4.17.21"]}`); none.Status != 200 || strings.TrimSpace(string(none.Body)) != "{}" {
		t.Errorf("%d %s", none.Status, none.Body)
	}
}

// TestR_VULN_017_APrereleaseInsideAnAffectedRange is the pre-release criterion: 2.0.0-beta.1 lies in >=1.0.0 <2.0.0 in OSV
// order, and the text carries it so that the client's own range test reports it.
func TestR_VULN_017_APrereleaseInsideAnAffectedRange(t *testing.T) {
	e := newEnv(t)
	e.seedNPM()
	a := e.replica("web-a")
	got := parse(t, e.post(a, npmAll, bulk, e.tokReader, `{"beta-pkg":["2.0.0-beta.1","1.5.0"]}`))
	if len(got["beta-pkg"]) != 1 || got["beta-pkg"][0].VulnerableVersions != ">=1.0.0 <2.0.0 || =2.0.0-beta.1" {
		t.Errorf("%+v", got)
	}
}

// TestR_VULN_017_ADataChangeIsInForceOnEveryReplica: an advisory that arrives with an epoch change and a claim that is added
// are in the next answer of every replica.
func TestR_VULN_017_AClaimChangesWhatIsWithheld(t *testing.T) {
	e := newEnv(t)
	e.seedNPM()
	a, b := e.replica("web-a"), e.replica("web-b")
	ask := `{"trove-test-mal":["1.0.0"]}`
	if got := parse(t, e.post(a, npmAll, bulk, e.tokReader, ask)); len(got["trove-test-mal"]) != 1 {
		t.Fatalf("%+v", got)
	}
	e.claim(npmLocal, "trove-test-*")
	e.changed("claims", `{}`, a, b)
	for _, r := range []*testenv.Replica{a, b} {
		x := e.post(r, npmAll, bulk, e.tokReader, ask)
		if x.Status != 200 || strings.TrimSpace(string(x.Body)) != "{}" || !strings.Contains(x.Header.Get("npm-notice"), "1 internal names") {
			t.Errorf("after the claim: %d %s %q", x.Status, x.Body, x.Header.Get("npm-notice"))
		}
	}
}

// TestR_VULN_017_WhileTheDataIsNotComplete is the 503 of an npm import that has not finished.
func TestR_VULN_017_WhileTheDataIsNotComplete(t *testing.T) {
	e := newEnv(t)
	e.seedNPM()
	e.data("npm", false, 0, 0)
	a := e.replica("web-a")
	x := e.post(a, npmRemote, bulk, e.tokReader, body)
	if x.Status != http.StatusServiceUnavailable || npmCode(t, x) != "vulnerability_data_unavailable" || !strings.Contains(string(x.Body), "not imported yet") {
		t.Fatalf("%d %s", x.Status, x.Body)
	}
}

// TestR_VULN_017_WhereTheAuditStaysRefused: the cache view, a repository whose mode is disabled, npm 6's endpoints, the
// scanning switched off, and a caller without read.
func TestR_VULN_017_WhereTheAuditStaysRefused(t *testing.T) {
	e := newEnv(t)
	e.seedNPM()
	a := e.replica("web-a")

	for _, c := range []struct{ name, repo, path string }{
		{"npm-off (mode disabled)", npmOff, bulk},
		{"npm 6's quick endpoint", npmRemote, "-/npm/v1/security/audits/quick"},
		{"npm 6's endpoint on a local repository", npmLocal, "-/npm/v1/security/audits"},
		{"the cache view", npmRemote + "~cache", bulk},
	} {
		x := e.post(a, c.repo, c.path, e.tokReader, body)
		if x.Status != http.StatusNotImplemented || npmCode(t, x) != "npm_audit_unavailable" {
			t.Errorf("%s: %d %s", c.name, x.Status, x.Body)
		}
	}
	if x := e.post(a, npmRemote, "-/npm/v1/security/audits/quick", e.tokReader, body); !strings.Contains(string(x.Body), "bulk advisory endpoint") {
		t.Errorf("npm 6's endpoint does not name the bulk endpoint where it is answered: %s", x.Body)
	}
	if x := e.post(a, npmRemote, bulk, e.tokNobody, body); x.Status < 400 || strings.Contains(string(x.Body), "GHSA-") {
		t.Errorf("a caller without read: %d %s", x.Status, x.Body)
	}
	if x := e.post(a, npmRemote, bulk, "", body); x.Status != http.StatusUnauthorized && x.Status != http.StatusForbidden && x.Status != http.StatusNotFound {
		t.Errorf("an anonymous caller: %d", x.Status)
	}
	if x := e.post(a, npmRemote, bulk, e.tokReader, `[1]`); x.Status != http.StatusBadRequest {
		t.Errorf("a body that is no object: %d", x.Status)
	}

	// An installation that switched the scanning off answers audits like disabled.
	e.st.Exec(`UPDATE repositories SET config = '{"npm":{"audit":{"local":true}}}'::jsonb || (config - 'npm') WHERE key = $1`, npmRemote)
	off := e.replica("web-off", "TROVE_VULNERABILITIES_ENABLED=false")
	if x := e.post(off, npmRemote, bulk, e.tokReader, body); x.Status != http.StatusNotImplemented {
		t.Errorf("local with the scanning disabled: %d %s", x.Status, x.Body)
	}
}
