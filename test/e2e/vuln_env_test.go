//go:build e2e

package e2e

// The harness of the vulnerability tests (R-VULN-029, docs/developer/e2e.md#the-vulnerability-tests): the cluster in the
// `vuln` profile (three api and three packages replicas, two worker replicas and two of the pool vuln, the scatter edge, the scanning reading the test advisory source of
// deploy/dev/osvstub.yaml), the repositories over the two counting test upstreams, a claimed scope, one package per client
// row that only that row's triage test changes, the control clients of the three test upstreams, the synchronization of
// the advisory data and what the findings API says. The tests are in vuln_*_test.go.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/osvstub"
)

// The repositories of the vulnerability tests. npm-local, npm-remote and npm-all are the ones of the npm tests (npmFixture);
// the NuGet ones are new.
const (
	vulnNPMInternal     = "npm-vuln-internal" // the holder of the claimed scope; not npm-internal, which the CI tests define differently
	vulnNuGetLocal      = "nuget-local"
	vulnNuGetRemote     = "nuget-remote"
	vulnNuGetRemoteOpen = "nuget-remote-open"
	vulnNuGetAll        = "nuget-all"
	vulnAcme            = "@acme/ui"
)

// The fixture packages of the test advisory source, with the versions its records name. They are only read by the tests:
// every package a test changes (a triage status) is one of its own and carries the run.
const (
	vulnNPMVuln  = osvstub.NPMVuln  // 1.0.0 affected (GHSA-aaaa-aaaa-0001, high), served by the npm test upstream once published
	vulnNPMMal   = osvstub.NPMMal   // 1.0.0 malicious (MAL-2026-1234)
	vulnNPMAlias = osvstub.NPMAlias // 2.0.0 affected, published to npm-local
	vulnNuGetHi  = osvstub.NuGetHigh
	vulnNuGetBad = osvstub.NuGetMal
)

// vulnEnv is the installation the vulnerability tests share: created once per run, idempotent over runs.
type vulnEnv struct {
	adm   *api
	npm   *npmFixture
	nuget *nugetFixture
	run   string // six hex digits, part of every name and identifier this run creates
	// tri are the packages of the triage tests: one per npm gate row and per .NET SDK, and the ones of the replica test.
	npmTri   map[string]string
	nugetTri map[string]string
	// advisory identifiers of the triage packages, by package
	advisoryOf map[string]string
	// records are the advisories this run added to the test source, for a test that resets the source
	records []vulnRecord
}

type vulnRecord struct {
	ecosystem string
	advisory  osvstub.Advisory
}

var (
	vulnOnce sync.Once
	vulnVal  *vulnEnv
	vulnErr  error
)

// vulnFx puts the cluster into the vuln profile and builds the installation of the tests.
func vulnFx(t testing.TB) *vulnEnv {
	t.Helper()
	vulnOnce.Do(func() {
		vulnErr = fmt.Errorf("the environment did not finish (see the first failure)")
		vulnVal, vulnErr = buildVulnEnv(t)
	})
	if vulnErr != nil {
		t.Fatalf("vulnerability environment: %v", vulnErr)
	}
	return vulnVal
}

// ── the test upstreams ───────────────────────────────────────────────────────

var (
	osvOnce sync.Once
	osvVal  *stubControl
)

// osvStub is the control client of the test advisory source (docs/developer/e2e.md#the-test-advisory-source).
func osvStub(t testing.TB) *stubControl {
	t.Helper()
	host := os.Getenv("TROVE_E2E_OSVSTUB_HOST")
	if host == "" {
		t.Fatal("TROVE_E2E_OSVSTUB_HOST is not set; run the vulnerability sets of deploy/dev/e2e.sh")
	}
	osvOnce.Do(func() { osvVal = newStubControl(t, "svc/osvstub", host, "trove-osvstub", 5*time.Minute) })
	return osvVal
}

func (s *stubControl) osvCounts(t testing.TB) osvstub.Counts {
	t.Helper()
	var c osvstub.Counts
	if err := json.Unmarshal(s.send(t, http.MethodGet, "/-/stub/requests?log=false", nil), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// addRecord adds or replaces a record of the test source; the next sync reads it.
func (s *stubControl) addRecord(t testing.TB, ecosystem string, a osvstub.Advisory) {
	t.Helper()
	s.send(t, http.MethodPost, "/-/stub/records", map[string]any{"ecosystem": ecosystem, "record": json.RawMessage(a.JSON())})
}

// triageRecord is the advisory of a package of a triage test: version 1.0.0 is affected (high), 1.0.1 fixes it.
func triageRecord(id, ecosystem, name string) osvstub.Advisory {
	rangeType := "SEMVER"
	if ecosystem == "NuGet" {
		rangeType = "ECOSYSTEM"
	}
	return osvstub.Advisory{
		SchemaVersion: osvstub.SchemaVersion, ID: id, Published: "2026-09-01T00:00:00Z",
		Summary:  name + " before 1.0.1",
		Details:  "Synthetic record of an end-to-end test.",
		Severity: []osvstub.Severity{{Type: "CVSS_V3", Score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N"}},
		Affected: []osvstub.Affected{{
			Package: osvstub.Package{Ecosystem: ecosystem, Name: name},
			Ranges:  []osvstub.Range{{Type: rangeType, Events: []map[string]string{osvstub.Introduced("0"), osvstub.Fixed("1.0.1")}}},
		}},
		References:       []osvstub.Reference{{Type: "ADVISORY", URL: "https://advisories.example.org/osvstub/" + id}},
		DatabaseSpecific: map[string]any{"github_reviewed": true},
	}
}

// ── the environment ──────────────────────────────────────────────────────────

func buildVulnEnv(t testing.TB) (*vulnEnv, error) {
	if err := runUp("vuln"); err != nil {
		return nil, err
	}
	waitServingSettled(t)
	waitFor(t, "four ready worker pods (the main worker and the pool vuln)", 5*time.Minute, 2*time.Second, func() bool {
		n := 0
		for _, p := range livePods(t, selWorker) {
			if p.Ready && !p.Deleting {
				n++
			}
		}
		return n == 4
	})
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	e := &vulnEnv{run: hex.EncodeToString(b), npmTri: map[string]string{}, nugetTri: map[string]string{}, advisoryOf: map[string]string{}}
	e.adm = adminAPI(t)

	// The three upstreams start clean: the dataset of the advisory source back to its fixture, the counters and the
	// modes of the npm and NuGet upstreams.
	osv := osvStub(t)
	osv.send(t, http.MethodPost, "/-/stub/reset?all=true", nil)
	stub(t).resetAll(t)
	nugetStub(t).resetAll(t)

	npmFx, err := buildNPMFixture(t)
	if err != nil {
		return nil, err
	}
	e.npm = npmFx
	e.buildRepositories(t)
	e.nuget = &nugetFixture{adm: e.adm, run: e.run}
	e.buildNuGetPublisher(t)

	// One package per row, with an advisory of its own, in the two upstreams; the records go into the source in one go
	// and one synchronization brings them in.
	for _, row := range npmGateRows {
		e.addNPMTriage(t, row)
	}
	for _, label := range []string{"replicas", "valkey"} {
		e.addNPMTriage(t, label)
	}
	// the package of the publish test is not in the upstream: a version that exists there cannot be published through npm-all
	e.addNPMRecord(t, "pub")
	for _, label := range []string{"mal", "malv"} {
		e.addNPMMalicious(t, label)
	}
	for _, sdk := range []string{"8", "9", "10", "replicas", "valkey"} {
		e.addNuGetTriage(t, sdk)
	}
	// The packages of the fixture a test only reads: the npm upstream serves them once they are published to it.
	for _, pv := range [][2]string{{vulnNPMVuln, "1.0.0"}, {vulnNPMMal, "1.0.0"}, {vulnNPMVuln, "1.0.1"}} {
		stub(t).publish(t, pv[0], pv[1], nil)
	}
	// A first synchronization from the stub's fixture plus these records. A full one, with the shrink guard lifted: an
	// earlier run on this cluster may have left a larger dataset behind (the drain run of the long set).
	e.syncFull(t, true)
	e.awaitComplete(t)
	return e, nil
}

// buildRepositories creates (or brings to the settings the tests need) the repositories of the npm side.
func (e *vulnEnv) buildRepositories(t testing.TB) {
	t.Helper()
	apiHost, contentHost := nugetStubHosts(t)
	// npm-local holds public packages (vendored copies): its claims serve routing only (matchPublicAdvisories: always).
	e.ensureRepo(t, npmLocalKey, nil, map[string]any{"vulnerabilities": map[string]any{"matchPublicAdvisories": "always"}})
	// npm-internal is the holder of the claimed scope: its names are internal, public advisories do not apply to them.
	e.ensureRepo(t, vulnNPMInternal, map[string]any{"key": vulnNPMInternal, "class": "local", "format": "npm", "requireAuthentication": true}, nil)
	g := e.adm.get("/api/v1/repositories/"+vulnNPMInternal+"/claims").want(t, "read the claims of "+vulnNPMInternal, 200)
	e.adm.do(http.MethodPut, "/api/v1/repositories/"+vulnNPMInternal+"/claims",
		map[string]any{"claims": []map[string]any{{"pattern": "@acme/*", "scope": "global"}}}, "If-Match", g.Header.Get("ETag")).want(t, "claim @acme/*", 200)
	ensurePolicy(t, e.adm, "e2e-npm-vuln", []string{vulnNPMInternal}, [2]any{"sa:" + npmPublisher, []string{"read", "deploy", "delete"}})

	remote := map[string]any{"url": "https://" + apiHost + "/v3/index.json", "extraHosts": []map[string]any{{"pattern": contentHost, "sendCredentials": false}}}
	e.ensureRepo(t, vulnNuGetLocal, map[string]any{"key": vulnNuGetLocal, "class": "local", "format": "nuget", "requireAuthentication": true},
		map[string]any{"vulnerabilities": map[string]any{"matchPublicAdvisories": "always"}})
	// nuget-remote serves no vulnerability information itself (R-VULN-018: the data of a restore through it comes from nuget-local).
	e.ensureRepo(t, vulnNuGetRemote, map[string]any{"key": vulnNuGetRemote, "class": "remote", "format": "nuget", "requireAuthentication": true, "remote": remote},
		map[string]any{"nuget": map[string]any{"vulnerabilityInfo": "off"}})
	// nuget-remote-open restores a malicious version: it does not block it (R-VULN-002 blockMalicious), so that the audit has
	// a version to warn about; its vulnerability information is the default.
	e.ensureRepo(t, vulnNuGetRemoteOpen, map[string]any{"key": vulnNuGetRemoteOpen, "class": "remote", "format": "nuget", "requireAuthentication": true, "remote": remote},
		map[string]any{"vulnerabilities": map[string]any{"blockMalicious": false}})
	e.ensureRepo(t, vulnNuGetAll, map[string]any{
		"key": vulnNuGetAll, "class": "virtual", "format": "nuget", "requireAuthentication": true,
		"virtual": map[string]any{"members": []string{vulnNuGetLocal, vulnNuGetRemote}, "deployTarget": vulnNuGetLocal},
	}, map[string]any{"virtual": map[string]any{"members": []string{vulnNuGetLocal, vulnNuGetRemote}, "deployTarget": vulnNuGetLocal}})
	// npm-all and nuget-all are keys of the CI tests as well (a different list of members): on a cluster that ran them, the
	// members are set again here; a CI run of this suite has a cluster of its own.
	e.ensureRepo(t, npmAllKey, nil, map[string]any{"virtual": map[string]any{"members": []string{npmLocalKey, npmRemoteKey}, "deployTarget": npmLocalKey}})
}

// ensureRepo creates the repository (body nil: it exists, the npm fixture made it) and applies the settings by a merge patch.
func (e *vulnEnv) ensureRepo(t testing.TB, key string, body map[string]any, patch map[string]any) {
	t.Helper()
	if body != nil {
		r, err := e.adm.send(http.MethodPost, "/api/v1/repositories", body)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != 201 && r.Status != 409 {
			t.Fatalf("create %s: %s", key, r)
		}
	}
	if patch == nil {
		return
	}
	g := e.adm.get("/api/v1/repositories/"+key).want(t, "read "+key, 200)
	e.adm.do(http.MethodPatch, "/api/v1/repositories/"+key, patch, "If-Match", g.Header.Get("ETag"), "Content-Type", "application/merge-patch+json").
		want(t, "settings of "+key, 200)
}

func (e *vulnEnv) buildNuGetPublisher(t testing.TB) {
	t.Helper()
	ensureSA(t, e.adm, nugetPubSA)
	// a virtual repository accepts only read and manage (R-AUTHZ-009): a push through it is authorized on its deployment target
	ensurePolicy(t, e.adm, "e2e-nuget-vuln-write", []string{vulnNuGetLocal}, [2]any{"sa:" + nugetPubSA, []string{"read", "deploy", "delete"}})
	ensurePolicy(t, e.adm, "e2e-nuget-vuln-read", []string{vulnNuGetRemote, vulnNuGetRemoteOpen, vulnNuGetAll}, [2]any{"sa:" + nugetPubSA, []string{"read"}})
	e.nuget.token = freshSAToken(t, e.adm, nugetPubSA, 4*3600, map[string]any{"inherit": true}).Token
	nugetReadableOnEveryReplica(t, e.nuget.token, vulnNuGetLocal, vulnNuGetRemote, vulnNuGetRemoteOpen, vulnNuGetAll)
	awaitReadableOnEveryReplica(t, e.npm.token, vulnNPMInternal, npmLocalKey, npmRemoteKey, npmAllKey)
}

// addNPMTriage makes the package of a label: published to the npm test upstream, one advisory in the test source.
func (e *vulnEnv) addNPMTriage(t testing.TB, label string) {
	t.Helper()
	stub(t).publish(t, e.addNPMRecord(t, label), "1.0.0", nil)
}

// addRecord adds an advisory to the test source and remembers it.
func (e *vulnEnv) addRecord(t testing.TB, ecosystem string, a osvstub.Advisory) {
	t.Helper()
	osvStub(t).addRecord(t, ecosystem, a)
	e.records = append(e.records, vulnRecord{ecosystem, a})
}

// addNPMRecord makes the advisory of a package of a label, without the package in the upstream.
func (e *vulnEnv) addNPMRecord(t testing.TB, label string) string {
	t.Helper()
	name := fmt.Sprintf("e2e-vtri-%s-%s", e.run, label)
	id := fmt.Sprintf("GHSA-e2e-%s-%s", e.run, label)
	e.addRecord(t, "npm", triageRecord(id, "npm", name))
	e.npmTri[label] = name
	e.advisoryOf[name] = id
	return name
}

// addNPMMalicious makes a package whose version 1.0.0 is malicious: published to the npm test upstream, one MAL- record.
func (e *vulnEnv) addNPMMalicious(t testing.TB, label string) {
	t.Helper()
	name := fmt.Sprintf("e2e-vmal-%s-%s", e.run, label)
	id := fmt.Sprintf("MAL-2026-e%s%s", e.run, label)
	stub(t).publish(t, name, "1.0.0", nil)
	e.addRecord(t, "npm", maliciousRecord(id, name))
	e.npmTri[label] = name
	e.advisoryOf[name] = id
}

// addNuGetTriage makes the package of a label for NuGet; the version list of the upstream has 1.0.0 only.
func (e *vulnEnv) addNuGetTriage(t testing.TB, label string) {
	t.Helper()
	name := fmt.Sprintf("E2e.Vtri%s.L%s", e.run, label)
	id := fmt.Sprintf("GHSA-e2e-%s-n%s", e.run, label)
	nugetStub(t).nugetPublish(t, name, "1.0.0")
	e.addRecord(t, "NuGet", triageRecord(id, "NuGet", name))
	e.nugetTri[label] = name
	e.advisoryOf[name] = id
}

// ── the synchronization ──────────────────────────────────────────────────────

// task starts a maintenance task through the API and returns the id of its job; a run that is going on (409) is answered
// with running set and no id.
func (e *vulnEnv) task(t testing.TB, name string, params any) (id string, running bool) {
	t.Helper()
	var r reply
	for range 20 {
		r = e.adm.do(http.MethodPost, "/api/v1/system/tasks/"+name+"/run", params)
		if r.Status != http.StatusTooManyRequests {
			break
		}
		time.Sleep(2 * time.Second)
	}
	r.want(t, "run "+name, 202, 409)
	if r.Status == http.StatusConflict {
		return "", true
	}
	var j struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	r.JSON(t, &j)
	if j.Job.ID == "" {
		t.Fatalf("run %s: the answer names no job: %s", name, r)
	}
	return j.Job.ID, false
}

// jobState reads the state of a job from the table (the job API of the tests of the phase is not needed for a state).
func jobState(t testing.TB, id string) string {
	t.Helper()
	return sql(t, "SELECT state FROM trove.job WHERE id = '"+id+"'")
}

func (e *vulnEnv) waitJob(t testing.TB, id string, within time.Duration) string {
	t.Helper()
	var st string
	waitFor(t, "job "+id+" to end", within, time.Second, func() bool {
		st = jobState(t, id)
		return st == "succeeded" || st == "failed" || st == "cancelled"
	})
	return st
}

// syncOnce runs the task kind and waits for its job and for the evaluation it causes; a run that was going on is waited out and
// the task started again, so that the run that counts has read the dataset as the test left it.
func (e *vulnEnv) syncOnce(t testing.TB, kind string, params any) string {
	t.Helper()
	for range 4 {
		id, running := e.task(t, kind, params)
		if running {
			waitFor(t, "the running synchronization to end", 10*time.Minute, time.Second, func() bool {
				return sql(t, `SELECT count(*) FROM trove.job WHERE kind LIKE 'vuln-sync-osv%' AND state IN ('queued', 'running', 'cancelling')`) == "0"
			})
			continue
		}
		st := e.waitJob(t, id, 10*time.Minute)
		if st != "succeeded" {
			t.Fatalf("%s ended %s: %s", kind, st, sql(t, "SELECT coalesce(error::text, '') FROM trove.job WHERE id = '"+id+"'"))
		}
		e.awaitEvaluated(t)
		return id
	}
	t.Fatalf("%s: a run was always going on", kind)
	return ""
}

func (e *vulnEnv) syncIncremental(t testing.TB) { e.syncOnce(t, "vuln-sync-osv", nil) }

func (e *vulnEnv) syncFull(t testing.TB, acceptShrink bool) {
	var params any
	if acceptShrink {
		params = map[string]any{"acceptShrink": true}
	}
	e.syncOnce(t, "vuln-sync-osv-full", params)
}

// awaitEvaluated waits until nothing is queued or running that evaluates and no advisory waits for its evaluation.
func (e *vulnEnv) awaitEvaluated(t testing.TB) {
	t.Helper()
	waitFor(t, "the evaluation of the advisory data to finish", 5*time.Minute, time.Second, func() bool {
		return sql(t, `SELECT (SELECT count(*) FROM trove.vuln_pending_evaluation)
			+ (SELECT count(*) FROM trove.job WHERE kind IN ('vuln-evaluate', 'vuln-rescan') AND state IN ('queued', 'running', 'cancelling'))
			+ (SELECT count(*) FROM trove.vuln_source_state WHERE full_evaluation_requested_at IS NOT NULL)`) == "0"
	})
}

// awaitComplete waits until both ecosystems of the source are complete.
func (e *vulnEnv) awaitComplete(t testing.TB) {
	t.Helper()
	waitFor(t, "the advisory data of npm and NuGet to be complete", 5*time.Minute, time.Second, func() bool {
		return sql(t, `SELECT count(*) FROM trove.vuln_source_state WHERE source = 'osv' AND state = 'complete' AND ecosystem IN ('npm', 'NuGet')`) == "2"
	})
}

// ── findings ─────────────────────────────────────────────────────────────────

type vulnFinding struct {
	ID          string  `json:"id"`
	Repository  string  `json:"repository"`
	Package     string  `json:"package"`
	Version     string  `json:"version"`
	Advisory    string  `json:"advisory"`
	Kind        string  `json:"kind"`
	Severity    string  `json:"severity"`
	Status      string  `json:"status"`
	Blocked     bool    `json:"blocked"`
	BlockReason *string `json:"blockReason"`
}

// findings lists the findings of a query of the API (status excludes the resolved ones by default).
func (e *vulnEnv) findings(t testing.TB, query url.Values) []vulnFinding {
	t.Helper()
	return e.findingsVia(t, e.adm, query)
}

// findingsVia is findings through the given client: e.adm reaches any replica through the edge, a client of one pod reaches
// that replica alone.
func (e *vulnEnv) findingsVia(t testing.TB, adm *api, query url.Values) []vulnFinding {
	t.Helper()
	var all []vulnFinding
	next := "/api/v1/vulnerabilities/findings?" + query.Encode()
	for next != "" {
		r := adm.get(next).want(t, "list findings", 200)
		var page struct {
			Items []vulnFinding `json:"items"`
		}
		r.JSON(t, &page)
		all = append(all, page.Items...)
		next = ""
		if l := r.Header.Get("Link"); l != "" {
			for _, part := range strings.Split(l, ",") {
				if strings.Contains(part, `rel="next"`) {
					if i, j := strings.Index(part, "<"), strings.Index(part, ">"); i >= 0 && j > i {
						next = part[i+1 : j]
					}
				}
			}
			if strings.HasPrefix(next, "http") {
				u, _ := url.Parse(next)
				next = u.RequestURI()
			}
		}
	}
	return all
}

// finding returns the one finding of the repository, package and version, waiting for it.
func (e *vulnEnv) finding(t testing.TB, repo, pkg, version string) vulnFinding {
	t.Helper()
	return e.findingVia(t, e.adm, repo, pkg, version)
}

// findingVia is finding through the given client. The block of a finding is decided with the repository settings of the
// replica that answers, and a replica learns of a settings change within cluster.epoch_poll_interval when Valkey is down
// (R-VULN-027): a test that waited for one replica to have the change reads the finding from that replica.
func (e *vulnEnv) findingVia(t testing.TB, adm *api, repo, pkg, version string) vulnFinding {
	t.Helper()
	var got []vulnFinding
	waitFor(t, "the finding of "+pkg+"@"+version+" in "+repo, 2*time.Minute, time.Second, func() bool {
		got = e.findingsVia(t, adm, url.Values{"repository": {repo}, "package": {pkg}, "version": {version}})
		return len(got) >= 1
	})
	if len(got) != 1 {
		t.Fatalf("%s@%s in %s: %d findings, want 1: %+v", pkg, version, repo, len(got), got)
	}
	return got[0]
}

// setStatus triages a finding (JSON merge patch with If-Match) through the given client; an extra pair adds a member.
func (e *vulnEnv) setStatus(t testing.TB, a *api, id, status string) reply {
	t.Helper()
	g := a.get("/api/v1/vulnerabilities/findings/"+id).want(t, "read finding "+id, 200)
	return a.do(http.MethodPatch, "/api/v1/vulnerabilities/findings/"+id, map[string]any{"status": status},
		"If-Match", g.Header.Get("ETag"), "Content-Type", "application/merge-patch+json").want(t, "set "+status, 200)
}

// block confirms a finding and sets the download block on it (R-VULN-013); lift undoes the block by confirming it again.
func (e *vulnEnv) block(t testing.TB, a *api, id string) {
	t.Helper()
	e.setStatus(t, a, id, "confirmed")
	e.setStatus(t, a, id, "blocked")
}

func (e *vulnEnv) lift(t testing.TB, a *api, id string) {
	t.Helper()
	e.setStatus(t, a, id, "confirmed")
}

// ── small helpers ────────────────────────────────────────────────────────────

// requireAll fails the test with out when a text is missing from out.
func requireAll(t testing.TB, what, out string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		if !strings.Contains(out, s) {
			t.Fatalf("%s does not mention %q:\n%s", what, s, tailLines(out, 40))
		}
	}
}
