//go:build e2e

package e2e

// The edge tests (exit criterion E5.5, docs/developer/e2e.md#the-edge-and-alert-tests): R-HA-039's requirements for large and long transfers, and
// R-HA-038's criteria, through the chart's Ingress (HAProxy Ingress) and through the chart's HTTPRoute on the platform Gateway `shared` (Envoy
// Gateway), in the profile `edge` (deploy/dev/up.sh, values-edge.yaml, gateway.yaml).
//
// The clients are the ones the criteria name. curl runs in the NuGet clients pod and reaches an edge by name with --connect-to: the request still
// says https://localhost:<port>, so the certificate is verified for `localhost` and the base URL is the one the setup snippets print, while the
// connection goes to the Service of the edge under test. `dotnet nuget push` and `npm publish` need the forwarder of their pod, which the test
// points at one edge after the other (the base URL they get back from trove is the same for both).
//
// One difference from the criteria's wording: the slow download reads a 2 GiB object, not the 6 GiB one. At 2 MiB/s a 6 GiB object takes 51
// minutes; 2 GiB take 17, which is the "longer than 15 minutes" the criterion asks for, and the 6 GiB object is downloaded at full speed and
// compared by its SHA-256 (Q-HA-260).

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const edgeIngressTarget = "haproxy-ingress.ingress-controller.svc.cluster.local:443"

// The sizes of R-HA-039 and R-HA-038. TROVE_E2E_EDGE_SMOKE=1 shrinks them for a run of the harness while it is being developed (the files, the
// time of the slow read and the floor it must exceed); the set `edge` never sets it, and a run with it proves nothing about the criteria.
var (
	edgeBigMiB    = edgeSized(6*1024, 192)      // R-HA-039: above the 5 GiB single-request limit of S3
	edgeSlowMiB   = edgeSized(2*1024, 24)       // 2 GiB at 2 MiB/s: 17 minutes
	edgeNuGetMiB  = edgeSized(1024, 48)         // R-HA-039: a package of 1 GiB
	edgeNpmMiB    = edgeSized(200, 20)          // R-HA-038: a package of 200 MB
	edgeSlowFloor = edgeFloor(15 * time.Minute) // longer than 15 minutes
)

func edgeSized(full, smoke int) int {
	if os.Getenv("TROVE_E2E_EDGE_SMOKE") == "1" {
		return smoke
	}
	return full
}

func edgeFloor(full time.Duration) time.Duration {
	if os.Getenv("TROVE_E2E_EDGE_SMOKE") == "1" {
		return 5 * time.Second
	}
	return full
}

// edgeRoute is one way into the cluster: the Service the clients connect to.
type edgeRoute struct {
	name   string // ingress, gateway
	target string // host:port of the Service
	envoy  bool   // the proxy adds its x-envoy-* headers (enableEnvoyHeaders of the ClientTrafficPolicy)
}

type edgeEnv struct {
	routes  []edgeRoute
	token   string
	run     string
	port    string
	nugetPd string // the NuGet clients pod: curl, dotnet
	npmPd   string // the npm clients pod
}

var (
	edgeOnce sync.Once
	edgeVal  *edgeEnv
	edgeErr  error
)

// edgeFx puts the cluster into the profile `edge`, finds the Service of the Gateway's proxy, waits until both edges answer and creates the
// repositories the transfers use.
func edgeFx(t testing.TB) *edgeEnv {
	t.Helper()
	edgeOnce.Do(func() {
		edgeErr = fmt.Errorf("the edge fixture did not finish (see the first failure)")
		edgeVal, edgeErr = buildEdgeFixture(t)
	})
	if edgeErr != nil {
		t.Fatalf("edge fixture: %v", edgeErr)
	}
	return edgeVal
}

func buildEdgeFixture(t testing.TB) (*edgeEnv, error) {
	switchProfile(t, "edge")
	for _, d := range []string{"trove-api", "trove-packages", "trove-worker"} {
		kn(t, "rollout", "status", "deployment/"+d, "--timeout=5m")
	}
	waitServingSettled(t)
	waitFor(t, "the HTTPRoute trove to be accepted by the Gateway", 3*time.Minute, 3*time.Second, func() bool {
		out, err := kubectl("-n", cfg.namespace, "get", "httproute", "trove", "-o",
			`jsonpath={.status.parents[0].conditions[?(@.type=="Accepted")].status}/{.status.parents[0].conditions[?(@.type=="ResolvedRefs")].status}`)
		return err == nil && out == "True/True"
	})
	svc, err := kubectl("-n", "envoy-gateway-system", "get", "svc", "-l",
		"gateway.envoyproxy.io/owning-gateway-name=shared,gateway.envoyproxy.io/owning-gateway-namespace=gateway-system",
		"-o", "jsonpath={.items[0].metadata.name}")
	if err != nil || svc == "" {
		return nil, fmt.Errorf("no Service of the Gateway's proxy: %w", err)
	}
	fx := &edgeEnv{
		routes: []edgeRoute{
			{name: "ingress", target: edgeIngressTarget},
			{name: "gateway", target: svc + ".envoy-gateway-system.svc.cluster.local:443", envoy: true},
		},
		port:    edgePort(t),
		run:     strconv.FormatInt(time.Now().Unix(), 36),
		nugetPd: nugetClientPod(t, 0),
		npmPd:   npmClientPod(t, 0),
	}
	adm := adminAPI(t)
	fx.token = adminToken(t)
	for _, r := range fx.routes {
		for _, body := range []map[string]any{
			{"key": "generic-edge-" + r.name, "class": "local", "format": "generic"},
			{"key": "nuget-edge-" + r.name, "class": "local", "format": "nuget", "requireAuthentication": true},
			{"key": "npm-edge-" + r.name, "class": "local", "format": "npm", "requireAuthentication": true},
		} {
			adm.do(http.MethodPost, reposRoute, body).want(t, "create repository "+body["key"].(string), 201, 409)
		}
	}
	for _, r := range fx.routes {
		waitFor(t, "the "+r.name+" to answer", 3*time.Minute, 3*time.Second, func() bool {
			code, _, err := fx.head(r, "/api/v1/system/ping")
			return err == nil && code == "200"
		})
	}
	return fx, nil
}

// execIn runs argv in a pod of the namespace and returns both streams and the error of a non-zero exit.
func execIn(pod string, within time.Duration, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	args := append([]string{"--context", cfg.kubeContext, "-n", cfg.namespace, "exec", pod, "--"}, argv...)
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// sh runs a script in the NuGet clients pod and fails the test when it fails.
func (fx *edgeEnv) sh(t testing.TB, script string) string {
	t.Helper()
	out, err := execIn(fx.nugetPd, 9*time.Minute, "sh", "-c", script)
	if err != nil {
		t.Fatalf("in the clients pod: %v\n%s\n%s", err, script, tail(out, 2000))
	}
	return out
}

// curlBase is the curl invocation that reaches the edge by name: https://localhost:<port> is connected to the Service of the route.
func (fx *edgeEnv) curlBase(r edgeRoute) string {
	return fmt.Sprintf("curl -sS --cacert /etc/trove-ca/ca.crt --connect-to localhost:%s:%s -H 'Authorization: Bearer %s'", fx.port, r.target, fx.token)
}

func (fx *edgeEnv) url(path string) string { return "https://localhost:" + fx.port + path }

// head returns the status code of a request through the route and whether the answer carries the x-envoy-upstream-service-time header, which only
// Envoy adds.
func (fx *edgeEnv) head(r edgeRoute, path string) (code string, viaEnvoy bool, err error) {
	out, err := execIn(fx.nugetPd, 2*time.Minute, "sh", "-c", fx.curlBase(r)+` -o /dev/null -D - -w 'CODE=%{http_code}\n' '`+fx.url(path)+`'`)
	if err != nil {
		return "", false, fmt.Errorf("%w: %s", err, tail(out, 300))
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "CODE=") {
			code = strings.TrimPrefix(l, "CODE=")
		}
		if strings.HasPrefix(strings.ToLower(l), "x-envoy-upstream-service-time:") {
			viaEnvoy = true
		}
	}
	return code, viaEnvoy, nil
}

// detached runs a script in the NuGet clients pod so that it outlives the exec session (a transfer of 17 minutes must not depend on one
// connection to the API server), waits for its exit code and returns its output and the code. A failure to start or to finish within the time
// is reported as the code -1 and the reason in the output, so that a goroutine can call it.
func (fx *edgeEnv) detached(name, script string, within time.Duration) (string, int) {
	b64 := base64.StdEncoding.EncodeToString([]byte(script))
	start := fmt.Sprintf(`rm -f /work/%[1]s.exit /work/%[1]s.out; echo %[2]s | base64 -d >/work/%[1]s.sh; nohup sh -c 'sh /work/%[1]s.sh >/work/%[1]s.out 2>&1; echo $? >/work/%[1]s.exit' >/dev/null 2>&1 &`, name, b64)
	if out, err := execIn(fx.nugetPd, 2*time.Minute, "sh", "-c", start); err != nil {
		return err.Error() + ": " + out, -1
	}
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		time.Sleep(15 * time.Second)
		out, err := execIn(fx.nugetPd, 2*time.Minute, "sh", "-c", "cat /work/"+name+".exit 2>/dev/null")
		if err == nil && strings.TrimSpace(out) != "" {
			code, _ := strconv.Atoi(strings.TrimSpace(out))
			res, _ := execIn(fx.nugetPd, 2*time.Minute, "sh", "-c", "cat /work/"+name+".out")
			return strings.TrimSpace(res), code
		}
	}
	return "did not finish within " + within.String(), -1
}

// streamUp is the script of an upload whose bytes are made while they are sent: random bytes from /dev/urandom through tee, which hands them to
// sha256sum, into curl -T - (chunked). No file of 6 GiB is written to the runner's disk beside the spool and the blob store that the transfer
// itself fills, and the rate of curl (150 MB/s) keeps the writes of the cluster below what a shared runner's disk takes. The SHA-256 of what was
// sent is in /work/<name>.sha afterwards.
func (fx *edgeEnv) streamUp(r edgeRoute, name string, mib int, obj string) string {
	return fmt.Sprintf(`set -o pipefail
rm -f /work/%[1]s.sha
dd if=/dev/urandom bs=1M count=%[2]d status=none | tee >(sha256sum | cut -d' ' -f1 >/work/%[1]s.sha) | %[3]s --fail --limit-rate 150M -T - -o /dev/null -w "%%{http_code}" %[4]s
rc=$?
for i in $(seq 1 60); do test -s /work/%[1]s.sha && break; sleep 1; done
exit $rc`, name, mib, fx.curlBase(r), fx.url(obj))
}

// bash runs the script of streamUp: process substitution needs it.
func bashScript(script string) string {
	return "bash -c '" + strings.ReplaceAll(script, "'", `'\''`) + "'"
}

// uploaded says that a curl -T answered 200 or 201 (the output is the status code).
func uploaded(out string) bool { return strings.HasSuffix(out, "200") || strings.HasSuffix(out, "201") }

// slowResult is what the 17-minute download reported.
type slowResult struct {
	code     string
	size     int64
	duration time.Duration
	exit     int
	out      string
}

// TestEdge_R_HA_039_TransfersThroughTheIngressAndTheGateway (R-HA-039, R-HA-038, exit criterion E5.5): through the chart's Ingress and through the
// chart's HTTPRoute, a 6 GiB upload with curl -T and its download compared by SHA-256, a download at 2 MiB/s that runs longer than 15 minutes and
// completes, a push of a 1 GiB package with dotnet, and a 200 MB npm package with a scoped name and its packument. The slow downloads of the two
// edges run side by side; the uploads run one after the other, because a packages pod spools one of 6 GiB.
func TestEdge_R_HA_039_TransfersThroughTheIngressAndTheGateway(t *testing.T) {
	fx := edgeFx(t)

	// each route is the edge it says it is: the Gateway's proxy adds its x-envoy headers, the Ingress's controller does not
	for _, r := range fx.routes {
		t.Run("R-HA-038 R-HA-039 the "+r.name+" is the edge under test", func(t *testing.T) {
			_, viaEnvoy, err := fx.head(r, "/api/v1/system/ping")
			if err != nil {
				t.Fatal(err)
			}
			if viaEnvoy != r.envoy {
				t.Fatalf("through the %s the answer carries x-envoy-upstream-service-time: %v, want %v", r.name, viaEnvoy, r.envoy)
			}
		})
	}

	// the slow downloads: the 2 GiB object goes up through its edge first, then it is read at 2 MiB/s while the rest of the test runs
	slow := map[string]*slowResult{}
	var slowMu sync.Mutex
	var slowWG sync.WaitGroup
	for _, r := range fx.routes {
		obj := fmt.Sprintf("/files/%s/generic-edge-%s/slow-%s.bin", e2eProject, r.name, fx.run)
		out, code := fx.detached("slow-up-"+r.name, bashScript(fx.streamUp(r, "slow-"+r.name, edgeSlowMiB, obj)), 30*time.Minute)
		if code != 0 || !uploaded(out) {
			t.Fatalf("the upload of the 2 GiB object through the %s: exit %d: %s", r.name, code, out)
		}
		slowWG.Add(1)
		go func() {
			defer slowWG.Done()
			start := time.Now()
			out, exit := fx.detached("slow-"+r.name, fx.curlBase(r)+` --fail --limit-rate 2M -o /dev/null -w "%{http_code} %{size_download}" `+fx.url(obj), 50*time.Minute)
			res := &slowResult{exit: exit, out: out, duration: time.Since(start)}
			if f := strings.Fields(out); len(f) >= 2 {
				res.code = f[len(f)-2]
				res.size, _ = strconv.ParseInt(f[len(f)-1], 10, 64)
			}
			slowMu.Lock()
			slow[r.name] = res
			slowMu.Unlock()
		}()
	}

	for _, r := range fx.routes {
		obj := fmt.Sprintf("/files/%s/generic-edge-%s/big-%s.bin", e2eProject, r.name, fx.run)
		t.Run("R-HA-039 curl -T of 6 GiB through the "+r.name+" succeeds and the download has the same SHA-256", func(t *testing.T) {
			start := time.Now()
			out, code := fx.detached("big-up-"+r.name, bashScript(fx.streamUp(r, "big-"+r.name, edgeBigMiB, obj)), 40*time.Minute)
			if code != 0 || !uploaded(out) {
				t.Fatalf("upload through the %s: curl exit %d: %s", r.name, code, out)
			}
			t.Logf("the upload of %d MiB through the %s took %s", edgeBigMiB, r.name, time.Since(start).Round(time.Second))
			start = time.Now()
			out, code = fx.detached("big-down-"+r.name, bashScript(`set -o pipefail; `+fx.curlBase(r)+` --fail --limit-rate 150M -o - `+fx.url(obj)+` | sha256sum | cut -d' ' -f1`), 40*time.Minute)
			bigSum := strings.TrimSpace(fx.sh(t, "cat /work/big-"+r.name+".sha"))
			if code != 0 || out != bigSum {
				t.Fatalf("download through the %s: exit %d, SHA-256 %q, want %q", r.name, code, out, bigSum)
			}
			t.Logf("the download through the %s took %s", r.name, time.Since(start).Round(time.Second))
			fx.sh(t, fx.curlBase(r)+` -X DELETE -o /dev/null `+fx.url(obj)+` || true`)
		})
	}

	for _, r := range fx.routes {
		t.Run("R-HA-039 dotnet nuget push of a package of 1 GiB through the "+r.name+" succeeds", func(t *testing.T) { fx.nugetPush(t, r) })
		t.Run("R-HA-038 R-HA-039 npm publish of 200 MB, npm view and the packument of a scoped name through the "+r.name, func(t *testing.T) { fx.npmScoped(t, r) })
	}
	fx.retarget(t, "")

	slowWG.Wait()
	for _, r := range fx.routes {
		t.Run("R-HA-039 curl --limit-rate 2M through the "+r.name+" runs longer than 15 minutes and completes", func(t *testing.T) {
			res := slow[r.name]
			if res == nil {
				t.Fatal("the download did not report")
			}
			want := int64(edgeSlowMiB) * 1024 * 1024
			if res.exit != 0 || res.code != "200" || res.size != want {
				t.Fatalf("exit %d, HTTP %s, %d bytes (want %d): %s", res.exit, res.code, res.size, want, res.out)
			}
			if res.duration <= edgeSlowFloor {
				t.Fatalf("the download took %s, want more than %s", res.duration, edgeSlowFloor)
			}
			t.Logf("the slow download through the %s took %s", r.name, res.duration.Round(time.Second))
		})
	}
}

// retarget points the forwarders of the NuGet and the npm pod at an edge (empty: the Ingress, their default).
func (fx *edgeEnv) retarget(t testing.TB, target string) {
	t.Helper()
	nugetForwarderTarget(t, 0, target)
	npmForwarderTarget(t, 0, target)
}

func (fx *edgeEnv) nugetPush(t *testing.T, r edgeRoute) {
	fx.retarget(t, r.target)
	c := &nugetClient{t: t, pod: 0, tag: "edge-" + r.name + "-" + fx.run, token: fx.token}
	feed := nugetFeed(t, "localhost", "nuget-edge-"+r.name)
	c.config(feed, true)
	id, version := "E2e.Edge.Big", "1.0.0-edge."+strconv.FormatInt(time.Now().Unix(), 10)
	// The token goes as the API key, which authenticates the first request: with the credentials of nuget.config alone the client sends its body before it
	// knows the feed wants them, the 401 closes the connection under a body of 1 GiB, and the client reports a broken pipe and not the status.
	// a package of 1 GiB: a content file of random bytes in a package that dotnet packs
	c.write("pack.csproj", []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net10.0</TargetFramework><PackageId>`+id+`</PackageId><Version>`+version+`</Version><Authors>trove end-to-end</Authors><Description>1 GiB</Description><NuGetAudit>false</NuGetAudit></PropertyGroup>
  <ItemGroup><None Include="blob.bin" Pack="true" PackagePath="content/" /></ItemGroup>
</Project>
`))
	c.write("Marker.cs", []byte(`namespace E2e { public static class Marker { public const string Id = "edge"; } }`))
	if out, err := c.try("sh", "-c", fmt.Sprintf("dd if=/dev/urandom of=blob.bin bs=1M count=%d status=none && dotnet pack pack.csproj -c Release -o out --nologo", edgeNuGetMiB)); err != nil {
		t.Fatalf("dotnet pack: %v\n%s", err, tail(out, 1500))
	}
	start := time.Now()
	c.mustPush("dotnet nuget push of 1 GiB through the "+r.name, "dotnet", "nuget", "push", "out/"+id+"."+version+".nupkg", "--source", feed, "--api-key", fx.token)
	t.Logf("the push through the %s took %s", r.name, time.Since(start).Round(time.Second))
	_, _ = c.try("rm", "-rf", c.dir())
}

func (fx *edgeEnv) npmScoped(t *testing.T, r edgeRoute) {
	fx.retarget(t, r.target)
	registry := registryURL(t, "localhost", "npm-edge-"+r.name)
	dir := "/work/edge-npm-" + r.name + "-" + fx.run
	version := "1.0." + strconv.FormatInt(time.Now().Unix(), 10)
	hostPath := strings.TrimPrefix(registry, "https:")
	setup := fmt.Sprintf(`rm -rf %[1]s && mkdir -p %[1]s/home && cd %[1]s && dd if=/dev/urandom of=blob.bin bs=1M count=%[2]d status=none \
 && printf '{"name":"@acme/ui","version":"%[3]s","files":["blob.bin"]}' >package.json \
 && printf 'registry=%[4]s\n%[5]s:_authToken=${TROVE_TOKEN}\n' >.npmrc`, dir, edgeNpmMiB, version, registry, hostPath)
	if out, err := execIn(fx.npmPd, 9*time.Minute, "sh", "-c", setup); err != nil {
		t.Fatalf("npm project: %v\n%s", err, tail(out, 1000))
	}
	env := []string{
		"env", "-C", dir, "HOME=" + dir + "/home", "NODE_EXTRA_CA_CERTS=/etc/trove-ca/ca.crt", "TROVE_TOKEN=" + fx.token,
		"npm_config_update_notifier=false", "npm_config_fund=false", "npm_config_audit=false",
	}
	start := time.Now()
	if out, err := execIn(fx.npmPd, 12*time.Minute, append(env, "npm11", "publish")...); err != nil {
		t.Fatalf("npm publish of 200 MB through the %s: %v\n%s", r.name, err, tail(out, 1500))
	}
	t.Logf("the publish through the %s took %s", r.name, time.Since(start).Round(time.Second))
	if out, err := execIn(fx.npmPd, 3*time.Minute, append(env, "npm11", "view", "@acme/ui", "version")...); err != nil || !strings.Contains(out, version) {
		t.Fatalf("npm view @acme/ui through the %s: %v\n%s", r.name, err, tail(out, 800))
	}
	// the packument by the path of the criterion, with the encoded slash, served by a packages pod
	body := fx.sh(t, fx.curlBase(r)+` -D /work/h.txt `+fx.url("/api/npm/"+repoRef("npm-edge-"+r.name)+"/@acme%2fui")+`; echo; grep -i '^x-trove-replica' /work/h.txt`)
	if !strings.Contains(body, `"@acme/ui"`) || !strings.Contains(body, version) {
		t.Fatalf("the packument through the %s does not name @acme/ui %s:\n%s", r.name, version, tail(body, 800))
	}
	packages := map[string]bool{}
	for _, p := range podsOf(t, selPackages) {
		packages[p.Name] = true
	}
	if rep := strings.TrimSpace(body[strings.LastIndex(body, ":")+1:]); !packages[rep] {
		t.Fatalf("the packument was served by %q, not by a packages pod %v", rep, packages)
	}
	// the UI loads from an api pod
	ui := fx.sh(t, fx.curlBase(r)+` -L -o /dev/null -D - -w 'CODE=%{http_code}\n' `+fx.url("/ui/"))
	apis := map[string]bool{}
	for _, p := range podsOf(t, selAPI) {
		apis[p.Name] = true
	}
	served := ""
	for _, l := range strings.Split(ui, "\n") {
		if strings.HasPrefix(strings.ToLower(l), "x-trove-replica:") {
			served = strings.TrimSpace(l[len("x-trove-replica:"):])
		}
	}
	if !strings.Contains(ui, "CODE=200") || !apis[served] {
		t.Fatalf("the UI through the %s: served by %q (api pods %v):\n%s", r.name, served, apis, tail(ui, 600))
	}
	_, _ = execIn(fx.npmPd, 2*time.Minute, "rm", "-rf", dir)
}
