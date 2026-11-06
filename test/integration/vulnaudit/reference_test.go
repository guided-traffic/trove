//go:build integration

package vulnaudit

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/osvstub"
)

// The reference comparison of R-VULN-017 (roadmap exit criterion E4b.5): trove's own advisory data, imported from the test
// advisory source with the OSV records recorded on the day the public answers were taken (test/osvstub/testdata/reference),
// answers the bulk body of the reference lockfile with the GHSA identifiers and severities of the public registry's answer
// recorded on that day. A second body of real packages with known advisories (the control, control-request.json) is compared
// the same way: the reference lockfile resolves from the synthetic corpus only, and the public registry has no advisory for
// any of its names, so on its own the comparison is one of two empty answers.

// ghsaOf is the identifier an advisory entry of the answer names in its url.
func ghsaOf(url string) string {
	const prefix = "https://github.com/advisories/"
	return strings.TrimPrefix(url, prefix)
}

// entries is an answer reduced to what the criterion compares: per name, per GHSA identifier, the set of severities.
type entries map[string]map[string]map[string]bool

func reduce(t *testing.T, label string, raw []byte) entries {
	t.Helper()
	var a answer
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("%s: %v\n%.300s", label, err, raw)
	}
	out := entries{}
	for name, advs := range a {
		for _, x := range advs {
			if out[name] == nil {
				out[name] = map[string]map[string]bool{}
			}
			id := ghsaOf(x.URL)
			if out[name][id] == nil {
				out[name][id] = map[string]bool{}
			}
			out[name][id][x.Severity] = true
		}
	}
	return out
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func set(m map[string]bool) string { return strings.Join(keys(m), "|") }

// compare lists every difference between the recorded public answer and trove's, per name and identifier.
func compare(public, trove entries) (diffs []string, names, ids, matches int) {
	all := map[string]bool{}
	for n := range public {
		all[n] = true
	}
	for n := range trove {
		all[n] = true
	}
	for _, name := range keys(all) {
		names++
		for _, id := range keys(union(public[name], trove[name])) {
			ids++
			p, okP := public[name][id]
			g, okT := trove[name][id]
			switch {
			case okP && !okT:
				diffs = append(diffs, fmt.Sprintf("%s: %s is in the public answer (%s) and not in trove's", name, id, set(p)))
			case !okP && okT:
				diffs = append(diffs, fmt.Sprintf("%s: %s is in trove's answer (%s) and not in the public one", name, id, set(g)))
			case set(p) != set(g):
				diffs = append(diffs, fmt.Sprintf("%s: %s has severity %s in the public answer and %s in trove's", name, id, set(p), set(g)))
			default:
				matches++
			}
		}
	}
	return diffs, names, ids, matches
}

func union(a, b map[string]map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// lockfileBody is the bulk body npm builds from the reference lockfile: every installed package with a version.
func lockfileBody(t *testing.T) []byte {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "..", "perf", "npm", "reference", "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Packages map[string]struct {
			Version string `json:"version"`
			Link    bool   `json:"link"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	set := map[string]map[string]bool{}
	for path, p := range doc.Packages {
		i := strings.LastIndex(path, "node_modules/")
		if path == "" || i < 0 || p.Link || p.Version == "" {
			continue
		}
		name := path[i+len("node_modules/"):]
		if set[name] == nil {
			set[name] = map[string]bool{}
		}
		set[name][p.Version] = true
	}
	body := map[string][]string{}
	for name, vs := range set {
		body[name] = keys(vs)
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// gz compresses a body as npm 7 and later send it.
func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestR_VULN_017_TheReferenceLockfileMatchesThePublicAnswer is the first acceptance criterion of R-VULN-017 (E4b.5).
func TestR_VULN_017_TheReferenceLockfileMatchesThePublicAnswer(t *testing.T) {
	ref := osvstub.ReferenceAnswers()
	if want := lockfileBody(t); !bytes.Equal(bytes.TrimSpace(ref.PublicRequest), want) {
		t.Fatalf("the recorded request of %s is not the bulk body of perf/npm/reference/package-lock.json any more: run `make osv-reference-refresh`", ref.Taken)
	}

	// The test advisory source serves the reference records; trove imports them through the task API.
	osv := osvstub.New(osvstub.Config{})
	if err := osv.Toggle(osvstub.ToggleReference, true); err != nil {
		t.Fatal(err)
	}
	// The synthetic records of the fixture (among them a left-pad advisory, which the public registry does not have) are not
	// part of the comparison: the source serves exactly the recorded records.
	recorded := map[string]bool{}
	for _, r := range osvstub.ReferenceRecords() {
		recorded[r.ID] = true
	}
	for _, id := range osv.IDs("npm") {
		if !recorded[id] {
			if err := osv.Remove("npm", id); err != nil {
				t.Fatal(err)
			}
		}
	}
	osvCert, err := osvstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	osvSrv := httptest.NewUnstartedServer(osv)
	osvSrv.TLS = osvCert.ServerConfig()
	osvSrv.StartTLS()
	t.Cleanup(osvSrv.Close)

	up, err := npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	upCert, err := npmstub.NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	run, err := up.Start("127.0.0.1:0", upCert.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })

	dir := t.TempDir()
	var cas []string
	for name, pem := range map[string][]byte{"osv-ca.pem": osvCert.CAPEM, "npm-ca.pem": upCert.CAPEM} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, pem, 0o600); err != nil {
			t.Fatal(err)
		}
		cas = append(cas, strconv.Quote(p))
	}
	st := testenv.NewStack(t, testenv.StackOptions{Name: "vulnauditref", Env: []string{
		"TROVE_PROXY_OUTBOUND_TLS_CA_FILES=[" + strings.Join(cas, ",") + "]",
		`TROVE_PROXY_OUTBOUND_ADDRESS_POLICY_ALLOW_CIDRS=["127.0.0.1/32","::1/128"]`,
		"TROVE_VULNERABILITIES_SOURCES_OSV_BASE_URL=" + osvSrv.URL + "/",
		`TROVE_VULNERABILITIES_SOURCES_OSV_ECOSYSTEMS=["npm"]`,
		"TROVE_VULNERABILITIES_SOURCES_KEV_MODE=off",
		`TROVE_MAINTENANCE_TASKS={"vuln-sync-osv":{"enabled":false,"schedule":"*/15 * * * *"},"vuln-sync-osv-full":{"enabled":false,"schedule":"17 3 * * 0"},` +
			`"vuln-sync-kev":{"enabled":false,"schedule":"41 */6 * * *"}}`,
		"TROVE_CLUSTER_EPOCH_POLL_INTERVAL=500ms", "TROVE_JOBS_POLL_INTERVAL=200ms",
	}})
	rita, reader := st.User("rita"), st.User("reader")
	st.Bind("admin", rita)
	adm := st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	read := st.Token(reader, scope.Default())
	// npm-all is virtual with a local and a remote member; the remote's upstream is the npm test upstream.
	st.Repository(npmLocal, "local", "npm", `{}`)
	remote := strings.Replace(remoteDoc("npm", ""), `"url":"https://upstream.invalid/"`, `"url":"`+run.URL+`/"`, 1)
	st.Repository(npmRemote, "remote", "npm", remote)
	st.Repository(npmAll, "virtual", "npm", `{}`)
	for i, m := range []string{npmLocal, npmRemote} {
		st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id) SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, npmAll, i, m)
	}
	st.Policy("readers", []string{"read"}, []string{npmLocal, npmRemote, npmAll}, reader)
	web := st.Start(t, testenv.ReplicaOptions{Name: "web", Role: "web"})
	st.Start(t, testenv.ReplicaOptions{Name: "worker", Role: "worker"})

	if a := st.Do(web, "POST", "/api/v1/system/tasks/vuln-sync-osv/run", adm, nil); a.Status != http.StatusAccepted {
		t.Fatalf("run vuln-sync-osv: %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the first import of npm", 120*time.Second, func() bool {
		return st.Text(`SELECT COALESCE((SELECT state FROM vuln_source_state WHERE source = 'osv' AND ecosystem = 'npm'), 'none')`) == "complete"
	})
	if got := st.Number(`SELECT count(*) FROM vuln_advisory WHERE ecosystem = 'npm'`); int(got) != len(recorded) {
		t.Fatalf("%d records imported, the source serves the %d recorded ones", got, len(recorded))
	}

	osv.ResetCounts()
	npmBefore := up.Counts().Total()

	bodies := []struct {
		label           string
		request, public []byte
	}{
		{"the reference lockfile", ref.PublicRequest, ref.PublicAnswer},
		{"the control of real packages", ref.ControlRequest, ref.ControlAnswer},
	}
	for _, b := range bodies {
		public := reduce(t, b.label+" (public)", b.public)
		var compact bytes.Buffer
		if err := json.Compact(&compact, b.request); err != nil {
			t.Fatal(err)
		}
		for _, repo := range []string{npmAll, npmLocal, npmRemote} {
			a := st.Do(web, "POST", "/api/npm/it/"+repo+"/"+bulk, read, gz(t, compact.Bytes()),
				"Content-Type", "application/json", "Content-Encoding", "gzip")
			if a.Status != http.StatusOK {
				t.Fatalf("%s through %s: %d %s", b.label, repo, a.Status, a.Body)
			}
			diffs, names, ids, matches := compare(public, reduce(t, b.label+" ("+repo+")", a.Body))
			t.Logf("%s through %s, public answer of %s: %d names with an advisory in either answer, %d advisory identifiers, %d match, %d differ",
				b.label, repo, ref.Taken, names, ids, matches, len(diffs))
			for _, d := range diffs {
				t.Errorf("%s through %s: %s", b.label, repo, d)
			}
		}
		if b.label == bodies[1].label && len(public) == 0 {
			t.Errorf("the control has no advisory in the recorded public answer: the comparison would be vacuous")
		}
	}

	if got := osv.Counts().Total; got != 0 {
		t.Errorf("the test advisory source saw %d requests during the audits, want none: %v", got, osv.Counts().SortedPaths())
	}
	if got := up.Counts().Total(); got != npmBefore {
		t.Errorf("the npm test upstream saw %d requests during the audits, want none", got-npmBefore)
	}
}
