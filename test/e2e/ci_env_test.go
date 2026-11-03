//go:build e2e

package e2e

// The harness of the GitLab CI OIDC tests (M4, R-CI-028, roadmap E4.1 to E4.7): the issuer double of test/oidcstub in
// the cluster (deploy/dev/oidcstub.yaml), the reference setup of R-CI-028 (the repositories npm-internal, npm-all,
// nuget-internal and nuget-all, the policy ci-read-internal, the CI issuer and the mapping of the specification's
// Examples) and the jobs of the reference pipeline, run with the real npm and .NET clients of the client pods.
//
// The pipeline is not re-typed here: docs/spec/gitlab-ci-oidc.md is the source. specExamples reads the Examples (the
// issuer and mapping bodies, the .gitlab-ci.yml and the nuget.config), swaps the example host for the edge, and a job
// runs the steps of `.trove` (before_script) and of its own script exactly as the file writes them, in one shell with
// -e -o pipefail, as a GitLab runner does. What the harness adds is what the runner is: the job's variables, the ID token
// (minted by the double with the claim set of a recorded fixture, test/oidcstub/testdata), a working directory with the
// repository's files and, after the script, a copy of the CI token into a file of the pod so that a test can use the
// token after the job revoked it. The token never reaches the job's log.
//
// The scenarios are in ci_pipeline_test.go (E4.1, E4.4, E4.5, E4.6, the client criteria) and ci_ha_test.go (E4.7).

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/guided-traffic/trove/test/oidcstub"
)

const (
	ciIssuerName = "gitlab-com" // the name the Examples give the issuer
	ciNpmInt     = "npm-internal"
	ciNpmAll     = "npm-all"
	ciNugetInt   = "nuget-internal"
	ciNugetAll   = "nuget-all"
	ciPolicyName = "ci-read-internal"
	ciSetupSA    = "e2e-ci-setup"
	// The dev CA, which signs the double's certificate, as the pods of trove see it (deploy/dev/values-dev.yaml mounts it for
	// proxy.outbound.tls.ca_files). The issuer names it as its ca_bundle_ref, the way an instance behind a private CA is
	// registered (R-CI-001, Q-CI-31).
	ciCABundleRef = "file:/var/run/secrets/trove-dev/keycloak-ca.crt"

	ciNpmVersionTag = "v1.4.0" // the protected tag of the reference pipeline
)

// ── the issuer double ────────────────────────────────────────────────────────

// oidcDouble is the client of the control API of one double (docs/developer/e2e.md, "Control API"): a port-forward to the
// Service's control port, the token of its Secret, the certificate verified for the Service name.
type oidcDouble struct {
	stub string // oidcstub or oidcstub-single
	host string // <stub>.<ns>.svc, the issuer is https://<host>
	tok  string
	c    *http.Client
}

var (
	doubleMu sync.Mutex
	doubles  = map[string]*oidcDouble{}
)

// double returns the control client of the double stub (host from the environment variable hostEnv).
func double(t testing.TB, stub, hostEnv string) *oidcDouble {
	t.Helper()
	host := os.Getenv(hostEnv)
	if host == "" {
		t.Fatalf("%s is not set; run the ci sets of deploy/dev/e2e.sh", hostEnv)
	}
	doubleMu.Lock()
	defer doubleMu.Unlock()
	if d, ok := doubles[stub]; ok {
		return d
	}
	addr := portForward(t, "svc/"+stub, 8444).addr
	var dialer net.Dialer
	d := &oidcDouble{
		stub: stub, host: host, tok: secretValue(t, "trove-"+stub, "control-token"),
		c: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialForward(ctx, &dialer, network, addr)
			},
			TLSClientConfig: &tls.Config{RootCAs: caPool(t), MinVersion: tls.VersionTLS12, ServerName: host},
		}},
	}
	doubles[stub] = d
	return d
}

func (d *oidcDouble) issuer() string { return "https://" + d.host }

func (d *oidcDouble) send(t testing.TB, method, path string, body any) []byte {
	t.Helper()
	raw, err := d.trySend(method, path, body)
	if err != nil {
		t.Fatalf("issuer double %s %s: %v", method, path, err)
	}
	return raw
}

// trySend is send for a goroutine that must not call t.Fatal.
func (d *oidcDouble) trySend(method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "https://"+d.host+":8444"+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.tok)
	resp, err := d.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// tryMint is mint for a goroutine that must not call t.Fatal.
func (d *oidcDouble) tryMint(claims map[string]any) (string, error) {
	raw, err := d.trySend(http.MethodPost, "/mint", map[string]any{"claims": claims})
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("mint: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("mint: the answer holds no token")
	}
	return out.Token, nil
}

// resetAll heals the faults, restores the caching and replaces the keys by a fresh default key set.
func (d *oidcDouble) resetAll(t testing.TB) {
	t.Helper()
	d.send(t, http.MethodPost, "/reset?all=true", nil)
}

// mint returns an ID token of the double: the claims override the defaults by name, remove drops claims.
func (d *oidcDouble) mint(t testing.TB, claims map[string]any, remove []string) string {
	t.Helper()
	body := map[string]any{"claims": claims}
	if len(remove) > 0 {
		body["remove"] = remove
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(d.send(t, http.MethodPost, "/mint", body), &out); err != nil || out.Token == "" {
		t.Fatalf("issuer double mint: %v %q", err, out.Token)
	}
	return out.Token
}

// ── the specification's Examples ─────────────────────────────────────────────

// ciSpec is what the tests take from the Examples of docs/spec/gitlab-ci-oidc.md, with the example host replaced by the
// edge's address.
type ciSpec struct {
	issuerBody  map[string]any // POST /api/v1/ci/issuers, lowerCamelCase
	mappingBody map[string]any // POST /api/v1/ci/issuers/gitlab-com/mappings, lowerCamelCase
	pipeline    ciPipeline
	nugetConfig string
}

// ciPipeline is the .gitlab-ci.yml of the Examples.
type ciPipeline struct {
	Variables map[string]string
	Audience  string // aud of the ID token the hidden job requests
	TokenVar  string // the variable that holds it
	Before    []string
	Jobs      map[string]ciPipelineJob
}

type ciPipelineJob struct {
	Script []string
	Rules  []string // the `if` expressions
}

var fenced = regexp.MustCompile("(?s)```([a-z]+)\n(.*?)\n```")

// specExamples reads the Examples section of the specification.
func specExamples(t testing.TB, base string) *ciSpec {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cfg.repo, "docs", "spec", "gitlab-ci-oidc.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	i := strings.Index(text, "\n## Examples\n")
	j := strings.Index(text, "\n## Deferred\n")
	if i < 0 || j < i {
		t.Fatal("docs/spec/gitlab-ci-oidc.md has no Examples section")
	}
	text = text[i:j]
	host := strings.TrimPrefix(base, "https://")
	text = strings.ReplaceAll(text, "packages.example.com", host)
	spec := &ciSpec{}
	for _, m := range fenced.FindAllStringSubmatch(text, -1) {
		lang, body := m[1], m[2]
		switch {
		case lang == "json" && strings.Contains(body, `"match"`):
			spec.mappingBody = camelBody(t, body)
		case lang == "json" && strings.Contains(body, `"kind": "gitlab"`):
			spec.issuerBody = camelBody(t, body)
		case lang == "yaml" && strings.Contains(body, "stages: [publish]"):
			spec.pipeline = parsePipeline(t, body)
		case lang == "xml" && strings.Contains(body, "packageSourceCredentials"):
			spec.nugetConfig = body + "\n"
		}
	}
	if spec.issuerBody == nil || spec.mappingBody == nil || len(spec.pipeline.Jobs) == 0 || spec.nugetConfig == "" {
		t.Fatalf("the Examples of the specification are not what the harness reads: issuer %v, mapping %v, jobs %d, nuget.config %v",
			spec.issuerBody != nil, spec.mappingBody != nil, len(spec.pipeline.Jobs), spec.nugetConfig != "")
	}
	return spec
}

// camelBody parses a JSON example of the specification and spells its member names lowerCamelCase, the wire form of the API
// (R-CI-002); claim names are values and stay as they are.
func camelBody(t testing.TB, s string) map[string]any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("example JSON: %v", err)
	}
	return camelize(v).(map[string]any)
}

func camelize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			parts := strings.Split(k, "_")
			for i := 1; i < len(parts); i++ {
				parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
			}
			out[strings.Join(parts, "")] = camelize(e)
		}
		return out
	case []any:
		for i := range x {
			x[i] = camelize(x[i])
		}
		return x
	}
	return v
}

func parsePipeline(t testing.TB, src string) ciPipeline {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf(".gitlab-ci.yml of the Examples: %v", err)
	}
	strs := func(v any) []string {
		var out []string
		for _, e := range v.([]any) {
			out = append(out, e.(string))
		}
		return out
	}
	p := ciPipeline{Variables: map[string]string{}, Jobs: map[string]ciPipelineJob{}}
	for k, v := range doc["variables"].(map[string]any) {
		p.Variables[k] = v.(string)
	}
	hidden := doc[".trove"].(map[string]any)
	for name, v := range hidden["id_tokens"].(map[string]any) {
		p.TokenVar, p.Audience = name, v.(map[string]any)["aud"].(string)
	}
	p.Before = strs(hidden["before_script"])
	for name, v := range doc {
		job, ok := v.(map[string]any)
		if !ok || strings.HasPrefix(name, ".") || job["script"] == nil {
			continue
		}
		if job["extends"] != ".trove" {
			t.Fatalf("job %s of the Examples does not extend .trove", name)
		}
		j := ciPipelineJob{Script: strs(job["script"])}
		for _, r := range job["rules"].([]any) {
			j.Rules = append(j.Rules, r.(map[string]any)["if"].(string))
		}
		p.Jobs[name] = j
	}
	return p
}

// ruleHolds evaluates the `if` expressions of the Examples: `$VAR` (set and not empty) and `$VAR == "x"` joined by &&.
func ruleHolds(t testing.TB, expr string, vars map[string]string) bool {
	t.Helper()
	for _, term := range strings.Split(expr, "&&") {
		term = strings.TrimSpace(term)
		if name, want, ok := strings.Cut(term, "=="); ok {
			if vars[strings.TrimPrefix(strings.TrimSpace(name), "$")] != strings.Trim(strings.TrimSpace(want), `"`) {
				return false
			}
			continue
		}
		if !strings.HasPrefix(term, "$") || vars[term[1:]] == "" {
			return false
		}
	}
	return true
}

// ── the fixture ──────────────────────────────────────────────────────────────

// ciFixture is the installation of R-CI-028: created afresh once per run (the repositories, the issuer and its mappings of an
// earlier run are deleted first, so that a version published before never meets this run's publish).
type ciFixture struct {
	adm      *api
	run      string
	base     string // TROVE_URL: https://localhost:<edge port>
	host     string // localhost:<edge port>
	dbl      *oidcDouble
	spec     *ciSpec
	setupTok string // the token of the setup account: read and deploy on the four repositories
	jobBase  int64
	jobs     atomic.Int64
	lock     string // package-lock.json of the npm project, written once
}

var (
	ciFxOnce sync.Once
	ciFxVal  *ciFixture
	ciFxErr  error
)

// ciFx puts the cluster into the scatter profile and builds the reference setup.
func ciFx(t testing.TB) *ciFixture {
	t.Helper()
	useScatter(t)
	ciFxOnce.Do(func() {
		ciFxErr = fmt.Errorf("the fixture did not finish (see the first failure)")
		ciFxVal, ciFxErr = buildCIFixture(t)
	})
	if ciFxErr != nil {
		t.Fatalf("CI fixture: %v", ciFxErr)
	}
	return ciFxVal
}

func buildCIFixture(t testing.TB) (*ciFixture, error) {
	adm := adminAPI(t)
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	fx := &ciFixture{adm: adm, run: hex.EncodeToString(b)}
	fx.host = "localhost:" + edgePort(t)
	fx.base = "https://" + fx.host
	// IDs of this run's jobs: a rerun on the same cluster never meets an issuance of an earlier run
	n := int64(0)
	for _, c := range b {
		n = n<<8 | int64(c)
	}
	fx.jobBase = 1_000_000 + n
	fx.dbl = double(t, "oidcstub", "TROVE_E2E_OIDCSTUB_HOST")
	fx.dbl.resetAll(t)
	fx.spec = specExamples(t, fx.base)

	ciCleanSlate(t, adm)

	// the repositories of R-CI-028; the virtual ones are npm-all and nuget-all
	for _, body := range []map[string]any{
		{"key": ciNpmInt, "class": "local", "format": "npm", "requireAuthentication": true},
		{"key": ciNugetInt, "class": "local", "format": "nuget"},
		{
			"key": ciNpmAll, "class": "virtual", "format": "npm", "requireAuthentication": true,
			"virtual": map[string]any{"members": []string{ciNpmInt}, "deployTarget": ciNpmInt},
		},
		{
			"key": ciNugetAll, "class": "virtual", "format": "nuget",
			"virtual": map[string]any{"members": []string{ciNugetInt}, "deployTarget": ciNugetInt},
		},
	} {
		adm.do(http.MethodPost, "/api/v1/repositories", body).want(t, fmt.Sprint("create ", body["key"]), 201)
	}
	// the policy of the mapping: no entry yet, the save of a mapping that lists it adds its own (R-CI-013)
	adm.do(http.MethodPost, "/api/v1/permission-policies", map[string]any{
		"name": ciPolicyName, "description": "end-to-end tests: read on the internal repositories",
		"resources": map[string]any{"repositories": []string{ciNpmInt, ciNpmAll, ciNugetInt, ciNugetAll}}, "entries": []any{},
	}).want(t, "create the policy "+ciPolicyName, 201)
	// the setup account publishes the packages that the reference projects depend on
	ensureSA(t, adm, ciSetupSA)
	ensurePolicy(t, adm, ciSetupSA, []string{ciNpmInt, ciNugetInt}, [2]any{"sa:" + ciSetupSA, []string{"read", "deploy", "delete"}})
	ensurePolicy(t, adm, ciSetupSA+"-read", []string{ciNpmAll, ciNugetAll}, [2]any{"sa:" + ciSetupSA, []string{"read"}})
	fx.setupTok = freshSAToken(t, adm, ciSetupSA, 4*3600, map[string]any{"inherit": true}).Token

	// the CI issuer of the Examples: the double instead of gitlab.com, registered with the CA of its certificate
	issuer := map[string]any{}
	for k, v := range fx.spec.issuerBody {
		issuer[k] = v
	}
	issuer["issuer"] = fx.dbl.issuer()
	issuer["caBundleRef"] = ciCABundleRef
	issuer["description"] = "end-to-end tests: the issuer double"
	adm.do(http.MethodPost, "/api/v1/ci/issuers", issuer).want(t, "register the issuer double", 201)
	// the mapping of the Examples, and the read-only mapping the prose next to it describes
	adm.do(http.MethodPost, ciMappingsPath(), fx.spec.mappingBody).want(t, "create the mapping of the Examples", 201)
	adm.do(http.MethodPost, ciMappingsPath(), map[string]any{
		"name": "acme-read", "description": "Every other pipeline of the group reads", "priority": 900,
		"match":  map[string]any{"all": []any{map[string]any{"claim": "namespace_id", "equals": "4711"}}},
		"grants": map[string]any{"policies": []string{ciPolicyName}}, "maxTtlSeconds": 1800,
	}).want(t, "create the mapping acme-read", 201)

	if err := fx.publishDependencies(t); err != nil {
		return nil, err
	}
	return fx, nil
}

func ciMappingsPath() string { return "/api/v1/ci/issuers/" + ciIssuerName + "/mappings" }

// ciCleanSlate removes what an earlier run left: the issuer with its mappings and tokens, the repositories (their keys are
// free again once the background job removed the rows) and the policy.
func ciCleanSlate(t testing.TB, adm *api) {
	t.Helper()
	if r := adm.do(http.MethodDelete, "/api/v1/ci/issuers/"+ciIssuerName+"?cascade=true", nil); r.Status != 204 && r.Status != 404 {
		t.Fatalf("delete the issuer of an earlier run: %s", r)
	}
	if r := adm.do(http.MethodDelete, "/api/v1/permission-policies/"+ciPolicyName, nil); r.Status != 204 && r.Status != 404 {
		t.Fatalf("delete the policy of an earlier run: %s", r)
	}
	for _, key := range []string{ciNpmAll, ciNugetAll, ciNpmInt, ciNugetInt} {
		deleteRepository(t, adm, key)
	}
	for _, key := range []string{ciNpmAll, ciNugetAll, ciNpmInt, ciNugetInt} {
		waitFor(t, "the repository key "+key+" to be free", 3*time.Minute, 2*time.Second, func() bool {
			return adm.get("/api/v1/repositories/"+key).Status == 404
		})
	}
}

// jobIDs returns the GitLab job and pipeline ID of the next job of the run.
func (fx *ciFixture) nextIDs() (jobID, pipelineID string) {
	n := fx.jobs.Add(1)
	return fmt.Sprint(fx.jobBase*100 + n), fmt.Sprint(fx.jobBase)
}

// ── pods ─────────────────────────────────────────────────────────────────────

// podExec runs argv in a pod with stdin and returns both output streams and the exit status (-1: did not start).
func podExec(t testing.TB, podName, stdin string, timeout time.Duration, argv ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{"--context", cfg.kubeContext, "-n", cfg.namespace, "exec"}
	if stdin != "" {
		args = append(args, "-i")
	}
	cmd := exec.CommandContext(ctx, "kubectl", append(append(args, podName, "--"), argv...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &ee):
		return out.String(), ee.ExitCode()
	default:
		return out.String() + "\n" + err.Error(), -1
	}
}

// putTree unpacks files below dir in the pod (one kubectl exec for any number of files).
func putTree(t testing.TB, podName, dir string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), ModTime: time.Unix(0, 0)}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	out, code := podExec(t, podName, buf.String(), 2*time.Minute, "sh", "-c", `mkdir -p "$1" && tar -x -C "$1" -f -`, "sh", dir)
	if code != 0 {
		t.Fatalf("unpack %d files below %s in %s: %d %s", len(files), dir, podName, code, out)
	}
}

// ── jobs ─────────────────────────────────────────────────────────────────────

// ciJob describes one job of the reference pipeline: where it runs, which claim set its ID token carries, the variables of
// the runner and the files of the repository it checks out.
type ciJob struct {
	kind   string // "npm" or "nuget"
	pod    int
	name   string
	claims *ciClaims
	vars   map[string]string // CI_* variables beyond what the claims give
	files  map[string]string // the repository
	steps  []string          // before_script, then script
	// tokenTTL asks the exchange for a lifetime (the extension parameter `ttl`); the steps then differ from the Examples
	// only in that the harness appends no flag: it is not used by the pipeline runs.
}

// ciJobResult is what a job left.
type ciJobResult struct {
	Out     string // the job log: standard output and error of the steps
	Code    int
	IDToken string
	Token   string // the CI token, captured by the harness after the script; empty when the exchange failed
	JobID   string
	Dur     time.Duration
	Dir     string
}

// runJob runs the job and returns its log. A failing step is a result: the scenarios of the 403 expect it.
func (fx *ciFixture) runJob(t testing.TB, j ciJob) ciJobResult {
	t.Helper()
	jobID, pipelineID := fx.nextIDs()
	c := j.claims.with(map[string]any{"aud": fx.base, "job_id": jobID, "pipeline_id": pipelineID})
	idToken := fx.dbl.mint(t, c.set, c.remove)
	payload := decodeJWTClaims(t, idToken)

	podName := npmClientPod(t, j.pod)
	if j.kind == "nuget" {
		podName = nugetClientPod(t, j.pod)
	}
	tag := fmt.Sprintf("%s-%s-%s", j.name, fx.run, jobID[len(jobID)-4:])
	root := "/work/" + tag
	repo := root + "/repo"
	vars := map[string]string{
		"TROVE_URL": fx.spec.pipeline.Variables["TROVE_URL"], "CI_JOB_ID": jobID, "CI_PIPELINE_ID": pipelineID, "CI_PROJECT_DIR": repo,
		"CI_COMMIT_SHA": str(payload["sha"]), "CI_COMMIT_REF_NAME": str(payload["ref"]), "CI_COMMIT_REF_PROTECTED": str(payload["ref_protected"]),
		"CI_PROJECT_ID": str(payload["project_id"]), "CI_PROJECT_PATH": str(payload["project_path"]),
	}
	if str(payload["ref_type"]) == "tag" {
		vars["CI_COMMIT_TAG"] = str(payload["ref"])
	}
	for k, v := range j.vars {
		vars[k] = v
	}
	// the ID token is a variable of the job, not a line of its log (id_tokens: of the hidden job)
	vars[fx.spec.pipeline.TokenVar] = idToken

	var env strings.Builder
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&env, "export %s=%s\n", k, shellQuote(vars[k]))
	}
	script := strings.Join(j.steps, "\n") + "\n"

	files := map[string]string{}
	for n, c := range j.files {
		files["repo/"+n] = c
	}
	files["job.env"] = env.String()
	files["job.sh"] = script
	files["bin/npm"] = "#!/bin/sh\nexec npm11 \"$@\"\n"
	_, _ = podExec(t, podName, "", time.Minute, "rm", "-rf", root)
	putTree(t, podName, root, files)
	_, _ = podExec(t, podName, "", time.Minute, "chmod", "+x", root+"/bin/npm")

	runner := `set -eo pipefail
cd "$CI_PROJECT_DIR_ROOT/repo"
. "$CI_PROJECT_DIR_ROOT/job.env"
rm -f "$CI_PROJECT_DIR_ROOT/job.env"
trap 'printf %s "${TROVE_TOKEN:-}" > "$CI_PROJECT_DIR_ROOT/token.out"' EXIT
. "$CI_PROJECT_DIR_ROOT/job.sh"
`
	argv := []string{"env", "CI_PROJECT_DIR_ROOT=" + root, "CURL_CA_BUNDLE=/etc/trove-ca/ca.crt"}
	switch j.kind {
	case "npm":
		argv = append(argv, "HOME="+root+"/home", "PATH="+root+"/bin:/usr/local/bin:/usr/bin:/bin", "NODE_EXTRA_CA_CERTS=/etc/trove-ca/ca.crt", "npm_config_update_notifier=false", "npm_config_fund=false",
			"npm_config_audit=false", "npm_config_cache="+root+"/home/npm-cache")
	case "nuget":
		argv = append(argv, "NUGET_PACKAGES="+root+"/packages", "NUGET_HTTP_CACHE_PATH="+root+"/httpcache", "NUGET_PLUGINS_CACHE_PATH="+root+"/plugins")
	}
	argv = append(argv, "bash", "-c", runner)
	start := time.Now()
	out, code := podExec(t, podName, "", 15*time.Minute, argv...)
	res := ciJobResult{Out: out, Code: code, IDToken: idToken, JobID: jobID, Dur: time.Since(start), Dir: root}
	if tok, c := podExec(t, podName, "", time.Minute, "cat", root+"/token.out"); c == 0 {
		res.Token = strings.TrimSpace(tok)
	}
	_ = os.WriteFile(filepath.Join(cfg.out, "ci-job-"+tag+".log"), []byte(out), 0o644)
	return res
}

// shellQuote single-quotes a value for the shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return fmt.Sprint(x)
	case float64:
		return fmt.Sprintf("%.0f", x)
	}
	return fmt.Sprint(v)
}

// decodeJWTClaims reads the payload of an ID token (the claim set the tests hand to the dry run).
func decodeJWTClaims(t testing.TB, tok string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %d parts", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// ── the claim sets of the recorded fixtures ──────────────────────────────────

// ciClaims is a claim set for the double's /mint: the claims that override the double's defaults and the default claims that
// the recorded case lacks.
type ciClaims struct {
	set    map[string]any
	remove []string
}

func (c *ciClaims) with(more map[string]any) *ciClaims {
	out := &ciClaims{set: map[string]any{}, remove: c.remove}
	for k, v := range c.set {
		out.set[k] = v
	}
	for k, v := range more {
		out.set[k] = v
	}
	return out
}

// fixtureClaims reads test/oidcstub/testdata/id-token-<name>.json, the claims GitLab 19.0.1 issued in that case, and fits them to
// the example installation: the instance's project and namespace IDs and paths become those of the Examples (the parent
// acme/billing/invoice-api is project 1234 in namespace 4711, the fork's namespace acme/partner is 4712 and its project
// 5678). The claims the double fixes itself (iss, aud, jti, iat, nbf, exp) are left to it.
func fixtureClaims(t testing.TB, name string) *ciClaims {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cfg.repo, "test", "oidcstub", "testdata", "id-token-"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Claims map[string]any `json:"claims"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	ids := map[string]map[string]string{
		"project_id": {"1": "1234", "2": "5678"}, "job_project_id": {"1": "1234", "2": "5678"},
		"namespace_id": {"3": "4711", "4": "4712"}, "job_namespace_id": {"3": "4711", "4": "4712"},
	}
	c := &ciClaims{set: map[string]any{}}
	for k, v := range fixture.Claims {
		switch k {
		case "iss", "aud", "jti", "iat", "nbf", "exp":
			continue
		}
		if s, ok := v.(string); ok {
			if m, ok := ids[k]; ok {
				if to, ok := m[s]; ok {
					s = to
				}
			}
			s = strings.ReplaceAll(s, "trove-oidc-validation/", "acme/")
			v = s
		}
		c.set[k] = v
	}
	for k := range oidcstub.DefaultClaims("https://x", nil, time.Now()) {
		if _, ok := c.set[k]; !ok && !slicesContains([]string{"iss", "aud", "jti", "iat", "nbf", "exp"}, k) {
			c.remove = append(c.remove, k)
		}
	}
	sort.Strings(c.remove)
	return c
}

func slicesContains(l []string, s string) bool {
	for _, e := range l {
		if e == s {
			return true
		}
	}
	return false
}

// protectedTag is the claim set of a protected tag pipeline: the recorded one with the tag the Examples use.
func protectedTag(t testing.TB, tag string) *ciClaims {
	return fixtureClaims(t, "push-protected-tag").with(map[string]any{
		"ref": tag, "ref_path": "refs/tags/" + tag,
		"sub":               "project_path:acme/billing/invoice-api:ref_type:tag:ref:" + tag,
		"ci_config_ref_uri": "gitlab.example.com/acme/billing/invoice-api//.gitlab-ci.yml@refs/tags/" + tag,
	})
}

// ── the projects the jobs check out ──────────────────────────────────────────

const (
	npmPackageName  = "@acme-billing/invoice-client"
	npmDependency   = "@acme-billing/shared-lib"
	nugetPackageID  = "Acme.Billing.InvoiceApi"
	nugetDependency = "Acme.Billing.Shared"
)

// npmRepoFiles is the repository of the npm job: package.json, the lockfile and one source file.
func (fx *ciFixture) npmRepoFiles() map[string]string {
	return map[string]string{
		"package.json": `{
  "name": "` + npmPackageName + `",
  "version": "0.0.0",
  "description": "end-to-end test package of the reference pipeline",
  "main": "index.js",
  "dependencies": {"` + npmDependency + `": "1.0.0"}
}
`,
		"package-lock.json": fx.lock,
		"index.js":          "module.exports = 'invoice-client';\n",
	}
}

// nugetRepoFiles is the repository of the NuGet job: nuget.config of the Examples, a solution file for the bare `dotnet restore`
// of the pipeline and the project that references the package of the other repository.
func (fx *ciFixture) nugetRepoFiles() map[string]string {
	return map[string]string{
		"nuget.config": fx.spec.nugetConfig,
		"Acme.Billing.slnx": `<Solution>
  <Project Path="src/Acme.Billing.InvoiceApi/Acme.Billing.InvoiceApi.csproj" />
</Solution>
`,
		"src/Acme.Billing.InvoiceApi/Acme.Billing.InvoiceApi.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net10.0</TargetFramework>
    <PackageId>` + nugetPackageID + `</PackageId>
    <Authors>trove end-to-end</Authors>
    <Description>end-to-end test package of the reference pipeline</Description>
    <NuGetAudit>false</NuGetAudit>
  </PropertyGroup>
  <ItemGroup><PackageReference Include="` + nugetDependency + `" Version="1.0.0" /></ItemGroup>
</Project>
`,
		"src/Acme.Billing.InvoiceApi/Invoice.cs": "namespace Acme.Billing { public static class Invoice { public const string Id = \"invoice\"; } }\n",
	}
}

// publishDependencies publishes the packages the two reference projects depend on, and writes the npm project's lockfile, with
// the setup account's token through the real clients (not CI tokens).
func (fx *ciFixture) publishDependencies(t testing.TB) error {
	t.Helper()
	// npm: shared-lib 1.0.0 into npm-internal, then the lockfile of the project from npm-all
	pod := npmClientPod(t, 0)
	root := "/work/ci-setup-" + fx.run
	_, _ = podExec(t, pod, "", time.Minute, "rm", "-rf", root)
	rc := func(key string) string {
		return fmt.Sprintf("registry=%s/api/npm/%s/\n//%s/api/npm/%s/:_authToken=${TROVE_TOKEN}\n", fx.base, key, fx.host, key)
	}
	putTree(t, pod, root, map[string]string{
		"lib/package.json": `{"name": "` + npmDependency + `", "version": "1.0.0", "main": "index.js"}` + "\n",
		"lib/index.js":     "module.exports = 'shared-lib';\n",
		"lib/.npmrc":       rc(ciNpmInt),
		"proj/.npmrc":      rc(ciNpmAll),
	})
	putTree(t, pod, root+"/proj", fx.npmRepoFiles())
	env := []string{
		"HOME=" + root + "/home", "NODE_EXTRA_CA_CERTS=/etc/trove-ca/ca.crt", "TROVE_TOKEN=" + fx.setupTok,
		"npm_config_update_notifier=false", "npm_config_fund=false", "npm_config_audit=false", "npm_config_cache=" + root + "/home/npm-cache",
	}
	if out, code := podExec(t, pod, "", 5*time.Minute, append(append([]string{"env", "-C", root + "/lib"}, env...), "npm11", "publish")...); code != 0 {
		return fmt.Errorf("publish %s 1.0.0: %d\n%s", npmDependency, code, tailLines(out, 30))
	}
	if out, code := podExec(t, pod, "", 5*time.Minute, append(append([]string{"env", "-C", root + "/proj"}, env...), "npm11", "install", "--package-lock-only")...); code != 0 {
		return fmt.Errorf("npm install --package-lock-only: %d\n%s", code, tailLines(out, 30))
	}
	lock, code := podExec(t, pod, "", time.Minute, "cat", root+"/proj/package-lock.json")
	if code != 0 || !strings.Contains(lock, npmDependency) {
		return fmt.Errorf("the lockfile does not resolve %s: %d\n%s", npmDependency, code, tailLines(lock, 30))
	}
	fx.lock = lock

	// NuGet: Acme.Billing.Shared 1.0.0 into nuget-internal
	pod = nugetClientPod(t, 0)
	root = "/work/ci-setup-nuget-" + fx.run
	_, _ = podExec(t, pod, "", time.Minute, "rm", "-rf", root)
	putTree(t, pod, root, map[string]string{
		"lib.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net10.0</TargetFramework><PackageId>` + nugetDependency + `</PackageId><Version>1.0.0</Version>
    <Authors>trove end-to-end</Authors><Description>end-to-end test package</Description><NuGetAudit>false</NuGetAudit></PropertyGroup>
</Project>
`,
		"Shared.cs":    "namespace Acme.Billing { public static class Shared { public const string Id = \"shared\"; } }\n",
		"nuget.config": `<?xml version="1.0" encoding="utf-8"?><configuration><packageSources><clear /></packageSources></configuration>` + "\n",
		// the push source needs credentials of its own: the service index is fetched before the API key is sent
		"push/nuget.config": `<?xml version="1.0" encoding="utf-8"?><configuration><packageSources><clear />
<add key="trove-internal" value="` + fx.base + `/api/nuget/` + ciNugetInt + `/v3/index.json" /></packageSources>
<packageSourceCredentials><trove-internal><add key="Username" value="ci" /><add key="ClearTextPassword" value="%TROVE_TOKEN%" /></trove-internal></packageSourceCredentials></configuration>` + "\n",
	})
	nenv := []string{
		"env", "-C", root, "NUGET_PACKAGES=" + root + "/packages", "NUGET_HTTP_CACHE_PATH=" + root + "/httpcache",
		"NUGET_PLUGINS_CACHE_PATH=" + root + "/plugins", "TROVE_TOKEN=" + fx.setupTok,
	}
	if out, code := podExec(t, pod, "", 10*time.Minute, append(append([]string{}, nenv...), "dotnet", "pack", "lib.csproj", "-c", "Release", "-o", "out", "--nologo")...); code != 0 {
		return fmt.Errorf("dotnet pack of %s: %d\n%s", nugetDependency, code, tailLines(out, 30))
	}
	source := fx.base + "/api/nuget/" + ciNugetInt + "/v3/index.json"
	if out, code := podExec(t, pod, "", 5*time.Minute, append(append([]string{}, nenv...), "sh", "-c", `cd push && dotnet nuget push ../out/`+nugetDependency+`.1.0.0.nupkg --source "$1" --api-key "$TROVE_TOKEN"`, "sh", source)...); code != 0 || !pushedRE.MatchString(out) {
		return fmt.Errorf("push %s 1.0.0: %d\n%s", nugetDependency, code, tailLines(out, 30))
	}
	return nil
}

// ── what the tests read back ─────────────────────────────────────────────────

// issuance is a record of GET /api/v1/ci/issuances (the members the tests read).
type issuance struct {
	TokenID      string         `json:"tokenId"`
	Issuer       string         `json:"issuer"`
	MappingNames []string       `json:"mappingNames"`
	ProjectID    string         `json:"projectId"`
	ProjectPath  string         `json:"projectPath"`
	JobID        string         `json:"jobId"`
	PipelineID   string         `json:"pipelineId"`
	Ref          string         `json:"ref"`
	RefType      string         `json:"refType"`
	RefProtected *bool          `json:"refProtected"`
	Sha          string         `json:"sha"`
	GrantedScope string         `json:"grantedScope"`
	Grants       map[string]any `json:"grants"`
	CrossProject bool           `json:"crossProject"`
	ExpiresAt    time.Time      `json:"expiresAt"`
	RevokedAt    *time.Time     `json:"revokedAt"`
	Active       bool           `json:"active"`
}

// issuancesOf lists the records of a job (R-CI-020).
func (fx *ciFixture) issuancesOf(t testing.TB, jobID string) []issuance {
	t.Helper()
	r := fx.adm.get("/api/v1/ci/issuances?jobId="+jobID).want(t, "list the issuances of job "+jobID, 200)
	var l struct {
		Items []issuance `json:"items"`
	}
	r.JSON(t, &l)
	return l.Items
}

// auditEvents lists audit events (the members the tests read).
type auditEvent struct {
	Action     string `json:"action"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason"`
	Repository string `json:"repository"`
	TraceID    string `json:"traceId"`
	Actor      struct {
		Type    string `json:"type"`
		TokenID string `json:"tokenId"`
	} `json:"actor"`
	Resource struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"resource"`
	Details map[string]any `json:"details"`
}

func (fx *ciFixture) audit(t testing.TB, query string) []auditEvent {
	t.Helper()
	r := fx.adm.get("/api/v1/audit/events?limit=500&"+query).want(t, "audit events "+query, 200)
	var l struct {
		Items []auditEvent `json:"items"`
	}
	r.JSON(t, &l)
	return l.Items
}

// exchange posts the subject token to the exchange endpoint (as the curl of the pipeline does) and returns the answer.
func (fx *ciFixture) exchange(t testing.TB, a *api, idToken string, extra map[string]string) reply {
	t.Helper()
	form := "grant_type=" + formEscape("urn:ietf:params:oauth:grant-type:token-exchange") +
		"&subject_token_type=" + formEscape("urn:ietf:params:oauth:token-type:id_token") + "&subject_token=" + formEscape(idToken)
	for k, v := range extra {
		form += "&" + k + "=" + formEscape(v)
	}
	r, err := a.send(http.MethodPost, "/api/v1/auth/oidc/token", form, "Content-Type", "application/x-www-form-urlencoded")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	return r
}

func formEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
