//go:build integration

// Package vulnfindings tests the evaluation of stored versions against advisory data and the finding lifecycle
// (R-VULN-009 to R-VULN-014, and the evaluation criteria of R-VULN-001, R-VULN-002, R-VULN-010 and R-VULN-011) against the
// integration stack: PostgreSQL, the object store and an in-process replica started through the composition root
// (internal/app), so the real commit hook, the real jobs and the real repository hook run. Advisory rows are inserted
// directly: the synchronization that writes them is another work package's.
package vulnfindings

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/formats/npm"
	"github.com/guided-traffic/trove/internal/formats/nuget"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// env is one installation: an administrator, the repositories the tests ask about and one replica that runs the web
// role and the worker role (the jobs poll every 200 ms).
type env struct {
	t   *testing.T
	st  *testenv.Stack
	rep *testenv.Replica
	adm string
}

// remoteCfg is the canonical document of a remote repository of the given upstream: every member present, as the repository
// service stores it, so that a later merge patch validates.
func remoteCfg(url, vulnerabilities string) string {
	doc := map[string]any{"remote": map[string]any{
		"url": url, "extraHosts": []any{}, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": 600, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
	}}
	if vulnerabilities != "" {
		doc["vulnerabilities"] = json.RawMessage(vulnerabilities)
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

func newEnv(t *testing.T, extra ...string) *env { return newEnvRole(t, "all", extra...) }

// newEnvRole is newEnv with a replica of the given role: a web replica claims no job.
func newEnvRole(t *testing.T, role string, extra ...string) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "vulnfind"})
	rita := st.User("rita")
	st.Bind("admin", rita)
	adm := st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	st.Repository("npm-vendor", "local", "npm", `{"vulnerabilities":{"matchPublicAdvisories":"always"}}`)
	st.Repository("npm-local", "local", "npm", `{}`)
	st.Repository("npm-noclaim", "local", "npm", `{"local":{"autoClaim":false}}`)
	st.Repository("npm-remote", "remote", "npm", remoteCfg("https://registry.npmjs.org/", ""))
	st.Repository("npm-mirror", "remote", "npm", remoteCfg("https://npm-mirror.corp.example/", ""))
	st.Repository("nuget-remote", "remote", "nuget", remoteCfg("https://api.nuget.org/", ""))
	st.Repository("nuget-local", "local", "nuget", `{"vulnerabilities":{"matchPublicAdvisories":"always"}}`)
	st.Repository("files-internal", "local", "generic", `{}`)
	rep := st.Start(t, testenv.ReplicaOptions{Name: "rep-a", Role: role, Env: append([]string{"TROVE_JOBS_POLL_INTERVAL=200ms"}, extra...)})
	return &env{t: t, st: st, rep: rep, adm: adm}
}

// addAdvisory inserts one OSV record of an ecosystem. canonical is the identifier findings carry; the severity is the level.
func (e *env) addAdvisory(eco, id, canonical, kind, severity string) {
	e.t.Helper()
	if canonical == "" {
		canonical = id
	}
	e.st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, aliases, summary, severity, severity_source, record_sha256)
		VALUES ($1, $2, $3, now(), $4, $5, 'a test advisory', $6, 'cvss', '\x00')`, eco, id, canonical, kind, aliasesOf(id, canonical), severity)
}

func aliasesOf(id, canonical string) []string {
	if id == canonical {
		return []string{}
	}
	return []string{canonical}
}

// addAffected inserts one affected entry: ranges is the JSON of the ranges, versions the explicit versions.
func (e *env) addAffected(eco, advisoryID string, ord int, name, ranges string, versions []string, every bool, fixed []string) {
	e.t.Helper()
	if versions == nil {
		versions = []string{}
	}
	if fixed == nil {
		fixed = []string{}
	}
	e.st.Exec(`INSERT INTO vuln_affected (ecosystem, advisory_id, ord, name, ranges, versions, every_version, fixed)
		VALUES ($1, $2, $3, $4, $5::text::jsonb, $6, $7, $8)`, eco, advisoryID, ord, name, ranges, versions, every, fixed)
}

// fixedRange is the ranges JSON of one range that introduces at 0 and fixes at v.
func fixedRange(typ, v string) string {
	return fmt.Sprintf(`[{"type":%q,"events":[{"introduced":"0"},{"fixed":%q}]}]`, typ, v)
}

// setComplete marks the advisory data of an ecosystem as completely imported.
func (e *env) setComplete(eco string, complete bool) {
	e.t.Helper()
	state := "empty"
	if complete {
		state = "complete"
	}
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, synced_at) VALUES ('osv', $1, $2, now())
		ON CONFLICT (source, ecosystem) DO UPDATE SET state = EXCLUDED.state`, eco, state)
}

// queue marks an advisory as changed: the next vuln-evaluate run evaluates the stored versions of its names.
func (e *env) queue(eco string, ids ...string) {
	e.t.Helper()
	for _, id := range ids {
		e.st.Exec(`INSERT INTO vuln_pending_evaluation (ecosystem, advisory_id) VALUES ($1, $2)
			ON CONFLICT (ecosystem, advisory_id) DO UPDATE SET enqueued_at = now()`, eco, id)
	}
}

// addVersion inserts a stored version into a repository without going through a format: the state a fill leaves.
func (e *env) addVersion(repo, name, version string) {
	e.t.Helper()
	e.st.Exec(`INSERT INTO packages (repo_id, name, display_name) SELECT id, $2, $2 FROM repositories WHERE key = $1
		ON CONFLICT (repo_id, name) DO NOTHING`, repo, name)
	e.st.Exec(`INSERT INTO package_versions (package_id, version, display_version)
		SELECT p.id, $3, $3 FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1 AND p.name = $2`, repo, name, version)
}

// run enqueues a job of a kind and waits until it ended; it fails the test when the job did not succeed.
func (e *env) run(kind, key string, args any) jobs.Job {
	e.t.Helper()
	res, err := e.rep.App.Queue().EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: kind, Args: args, DedupKey: key})
	if err != nil {
		e.t.Fatalf("enqueue %s: %v", kind, err)
	}
	return e.waitJob(res.Job.ID, kind)
}

func (e *env) waitJob(id, kind string) jobs.Job {
	e.t.Helper()
	var j jobs.Job
	testenv.WaitFor(e.t, "the job "+kind, 60*time.Second, func() bool {
		got, ok, err := e.rep.App.Queue().Get(context.Background(), id)
		if err != nil || !ok {
			return false
		}
		j = got
		return got.State == "succeeded" || got.State == "failed" || got.State == "dead"
	})
	if j.State != "succeeded" {
		e.t.Fatalf("the job %s ended %s: %s", kind, j.State, j.Error)
	}
	return j
}

// evaluate runs vuln-evaluate to the end.
func (e *env) evaluate() jobs.Job {
	e.t.Helper()
	return e.run(vuln.KindEvaluate, vuln.DedupEvaluate, nil)
}

// rescan runs vuln-rescan of a repository to the end.
func (e *env) rescan(repo string) jobs.Job {
	e.t.Helper()
	id := e.st.Number(`SELECT id FROM repositories WHERE key = $1`, repo)
	return e.run(vuln.KindRescan, vuln.RescanKey(id), vuln.RescanArgs{Repository: repo, RepositoryID: id})
}

// findings counts the findings of a repository (any status) for a package version and advisory; "" matches any.
func (e *env) findings(repo, name, version, advisory string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM vuln_finding f JOIN repositories r ON r.id = f.repo_id
		WHERE r.key = $1 AND ($2 = '' OR f.package_name = $2) AND ($3 = '' OR f.version = $3) AND ($4 = '' OR f.advisory_id = $4)`,
		repo, name, version, advisory)
}

// status is the status of the one finding of a repository, package and version, "" for none.
func (e *env) status(repo, name, version string) string {
	e.t.Helper()
	return e.st.Text(`SELECT COALESCE((SELECT f.status FROM vuln_finding f JOIN repositories r ON r.id = f.repo_id
		WHERE r.key = $1 AND f.package_name = $2 AND f.version = $3), '')`, repo, name, version)
}

// history lists the changes of a finding's events in order.
func (e *env) history(repo, name, version string) string {
	e.t.Helper()
	return e.st.Text(`SELECT COALESCE(string_agg(v.change, ',' ORDER BY v.seq), '') FROM vuln_finding_event v JOIN vuln_finding f ON f.id = v.finding_id
		JOIN repositories r ON r.id = f.repo_id WHERE r.key = $1 AND f.package_name = $2 AND f.version = $3`, repo, name, version)
}

// events counts the audit events of a type.
func (e *env) events(typ string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM audit_event WHERE action = $1`, typ)
}

// patch sends a merge patch to a repository.
func (e *env) patch(repo, body string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.rep, "PATCH", "/api/v1/repositories/"+repo, e.adm, body, "Content-Type", "application/merge-patch+json", "If-Match", "*")
}

// publish publishes an npm package version and requires 201.
func (e *env) publish(repo, name, version string) {
	e.t.Helper()
	p := pkg{Name: name, Version: version}
	if a := e.st.Do(e.rep, "PUT", pubPath(repo, name), e.adm, body(e.t, p)); a.Status != 201 {
		e.t.Fatalf("publish %s@%s to %s: %d %s", name, version, repo, a.Status, a.Body)
	}
}

// unpublish removes the tarball of a version through /files/, which unpublishes the version the same way.
func (e *env) unpublish(repo, name, version string) {
	e.t.Helper()
	base := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		base = name[i+1:]
	}
	path := fmt.Sprintf("/files/%s/%s/-/%s-%s.tgz", repo, name, base, version)
	if a := e.st.Do(e.rep, "DELETE", path, e.adm, nil); a.Status != 204 {
		e.t.Fatalf("delete %s: %d %s", path, a.Status, a.Body)
	}
}

// waitSnapshot waits until a condition that reads the replica's repository snapshot holds: a change of a repository reaches the
// snapshot within the change-log bound (R-ARCH-018), not at once.
func waitSnapshot(t *testing.T, _ *env, cond func() bool) {
	t.Helper()
	testenv.WaitFor(t, "the repository snapshot", 20*time.Second, cond)
}

// ecosystems are the ecosystems of the two formats with the adapter, as the composition root builds them.
func ecosystems() []evaluate.Ecosystem {
	return []evaluate.Ecosystem{
		{Name: "NuGet", Formats: []string{"nuget"}, Adapter: nuget.New()},
		{Name: "npm", Formats: []string{"npm"}, Adapter: npm.New()},
	}
}
