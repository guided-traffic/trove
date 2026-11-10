//go:build e2e

package e2e

// The loops that run under an upgrade (upgrade_test.go): `npm ci` and `dotnet restore` in the client pods, each through a
// virtual repository whose members are a remote (the counting test upstream) and a local repository, an `npm publish` into the
// local one, and a browser journey on the host. Every loop writes one record per iteration, so an iteration that the upgrade
// breaks is found in the records, and a `kubectl exec` that breaks is never counted as a failed iteration. The npm loops are
// the ones of the drain and failover tests (npm_load_test.go); the NuGet loop and the browser loop are the ones of this file.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	upgradeNPMLoops    = 3
	upgradeNuGetLoops  = 2
	upgradeLocalNuGet  = "Upgrade"
	upgradeNuGetRemote = nugetDepRoot
)

// upCmd is one iteration of any loop.
type upCmd struct {
	Loop  string    `json:"loop"`
	Kind  string    `json:"kind"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Code  int       `json:"code"`
	N     int       `json:"n"`
	Log   string    `json:"log,omitempty"`
	// Skew marks a browser iteration whose page load met a 404 for a hashed asset of the other release (Q-HA-99) and no
	// answer of 500 or above: an iteration that met a server error fails whatever else it met.
	Skew bool `json:"skew,omitempty"`
}

// upWindow is a time span the upgrade took, in which every loop must have run.
type upWindow struct {
	Name     string
	From, To time.Time
}

// upgradeFx is what the loops use, built on the installation that is running when it is built.
type upgradeFx struct {
	npm         *npmHAFixture
	nuget       *nugetFixture
	nugetAll    string // the virtual NuGet repository over the local one and the remote
	nugetRemote string
	nugetID     string // the package in the local repository
	nugetFeed   string
}

// buildUpgradeFixture creates the repositories, accounts and packages of the loops through the API of the installation
// that runs (the release before the upgrade). The repositories and accounts stay in the database and are served by the
// release after it: that is part of what the upgrade is tested for.
func buildUpgradeFixture(t *testing.T) *upgradeFx {
	t.Helper()
	markScatterProfile()
	bootstrapAdmin(t)
	if stubHost := os.Getenv("TROVE_E2E_NPMSTUB_HOST"); stubHost == "" || os.Getenv("TROVE_E2E_NUGETSTUB_HOST") == "" {
		t.Fatal("TROVE_E2E_NPMSTUB_HOST and TROVE_E2E_NUGETSTUB_HOST are not set: run the upgrade tests through `deploy/dev/e2e.sh upgrade`, which deploys the test upstreams")
	}
	fx := &upgradeFx{}
	npmFx, err := buildNPMFixture(t)
	if err != nil {
		t.Fatalf("npm fixture: %v", err)
	}
	fx.npm = buildNPMHAFixture(t, npmFx)

	fx.nuget, err = buildNuGetFixture(t)
	if err != nil {
		t.Fatalf("NuGet fixture: %v", err)
	}
	apiHost, contentHost := nugetStubHosts(t)
	fx.nugetRemote = "nuget-upgrade-" + fx.nuget.run + "-remote"
	fx.nugetAll = "nuget-upgrade-" + fx.nuget.run + "-all"
	adm := fx.nuget.adm
	adm.do(http.MethodPost, reposRoute, map[string]any{
		"key": fx.nugetRemote, "class": "remote", "format": "nuget", "requireAuthentication": true,
		"remote": map[string]any{"url": "https://" + apiHost + "/v3/index.json", "extraHosts": []map[string]any{{"pattern": contentHost, "sendCredentials": false}}},
	}).want(t, "create "+fx.nugetRemote, 201)
	adm.do(http.MethodPost, reposRoute, map[string]any{
		"key": fx.nugetAll, "class": "virtual", "format": "nuget", "requireAuthentication": true,
		"virtual": map[string]any{"members": []string{nugetAuthKey, fx.nugetRemote}, "deployTarget": nugetAuthKey},
	}).want(t, "create "+fx.nugetAll, 201)
	ensurePolicy(t, adm, "e2e-nuget-upgrade-"+fx.nuget.run, []string{fx.nugetRemote, fx.nugetAll}, [2]any{"sa:" + nugetPubSA, []string{"read"}})
	fx.nugetFeed = nugetFeed(t, "localhost", fx.nugetAll)
	// the policies are in force on every replica within R-AUTHZ-028's bound; the scatter edge spreads the probes
	waitFor(t, "the NuGet publisher to read the virtual repository on at least two replicas", 60*time.Second, 500*time.Millisecond, func() bool {
		seen := map[string]bool{}
		for range 12 {
			r := newAPI(t).withToken(fx.nuget.token).get(nugetAddr(fx.nugetAll) + "/v3/index.json")
			if r.Status != 200 {
				return false
			}
			seen[r.Replica()] = true
		}
		return len(seen) >= 2
	})

	// the local package of the restore loops, pushed through the virtual repository into its deployment target
	fx.nugetID = fx.nuget.pkgID(upgradeLocalNuGet)
	c := fx.nuget.client(t, 0, "upgrade-push")
	c.config(fx.nugetFeed, true)
	file := c.place(fx.nugetID, "1.0.0", fx.nuget.nugetPack(t, fx.nugetID, "1.0.0", "the package the restore loops of the upgrade tests read"))
	c.mustPush("push through the virtual repository", "dotnet", "nuget", "push", file, "--source", fx.nugetFeed, "--api-key", "az")
	return fx
}

// ── the NuGet restore loop ───────────────────────────────────────────────────

// nugetRestoreLoopScript is one dotnet restore loop: a project with a local and a remote package, restored with empty
// package and HTTP caches again and again until the file stop appears. dotnet restore exits 0 on some failures, so an
// iteration counts only when both packages were extracted. Each iteration writes `R restore <start ms> <end ms> <status> <n>`;
// the output of a failed one stays.
const nugetRestoreLoopScript = `#!/bin/sh
mkdir -p out
n=0
while [ ! -e stop ]; do
  n=$((n + 1))
  rm -rf "$NUGET_PACKAGES" "$NUGET_HTTP_CACHE_PATH" obj
  start="$(date +%%s%%3N)"
  dotnet restore loop.csproj --no-cache --nologo >"out/$n.log" 2>&1
  rc=$?
  end="$(date +%%s%%3N)"
  if [ "$rc" -eq 0 ]; then
    for d in %s; do
      [ -d "$NUGET_PACKAGES/$d" ] || { rc=97; echo "the restore extracted no $d" >>"out/$n.log"; }
    done
  fi
  [ "$rc" -eq 0 ] && rm -f "out/$n.log"
  echo "R restore $start $end $rc $n" >>records
done
echo finished >finished
`

type upNuGetLoop struct {
	name string
	c    *nugetClient
}

var nugetRecord = regexp.MustCompile(`^R restore (\d+) (\d+) (\d+) (\d+)$`)

func startNuGetLoops(t *testing.T, fx *upgradeFx, n int) []*upNuGetLoop {
	t.Helper()
	pods, err := strconv.Atoi(os.Getenv("TROVE_E2E_NUGET_CLIENTS_PODS"))
	if err != nil || pods < 1 {
		t.Fatal("TROVE_E2E_NUGET_CLIENTS_PODS is not set; run the upgrade tests through `deploy/dev/e2e.sh upgrade`")
	}
	low := strings.ToLower
	dirs := fmt.Sprintf("%s/1.0.0 %s/1.0.0", low(fx.nugetID), low(upgradeNuGetRemote))
	var loops []*upNuGetLoop
	for i := 0; i < n; i++ {
		c := fx.nuget.client(t, i%pods, fmt.Sprintf("upgrade-restore%d", i))
		c.config(fx.nugetFeed, true)
		c.write("loop.csproj", []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net10.0</TargetFramework><NuGetAudit>false</NuGetAudit></PropertyGroup>
  <ItemGroup>
    <PackageReference Include="`+fx.nugetID+`" Version="1.0.0" />
    <PackageReference Include="`+upgradeNuGetRemote+`" Version="1.0.0" />
  </ItemGroup>
</Project>
`))
		c.write("loop.sh", []byte(fmt.Sprintf(nugetRestoreLoopScript, dirs)))
		// the first restore runs before the loop does, so a failure to restore at all is not an iteration of the upgrade
		if out, err := c.try("dotnet", "restore", "loop.csproj", "--no-cache", "--nologo"); err != nil {
			t.Fatalf("the restore of the loop's project before the upgrade: %v\n%s", err, tailLines(out, 20))
		}
		loops = append(loops, &upNuGetLoop{name: fmt.Sprintf("nuget%d", i), c: c})
	}
	// registered before the first loop starts: a test that fails while the loops start leaves none running into the next scenario
	t.Cleanup(func() {
		for _, lp := range loops {
			lp.halt()
		}
	})
	for _, lp := range loops {
		// setsid and the redirections detach the loop from the exec session, which ends at once
		if out, err := lp.c.try("sh", "-c", "setsid nohup sh ./loop.sh </dev/null >loop.log 2>&1 &"); err != nil {
			t.Fatalf("starting the %s loop: %v\n%s", lp.name, err, out)
		}
	}
	return loops
}

func (lp *upNuGetLoop) halt() {
	_, _ = kubectl("-n", cfg.namespace, "exec", nugetClientPod(lp.c.t, lp.c.pod), "--", "touch", lp.c.dir()+"/stop")
}

func (lp *upNuGetLoop) records(t *testing.T) []upCmd {
	t.Helper()
	waitFor(t, "the "+lp.name+" loop to finish its iteration", 10*time.Minute, 2*time.Second, func() bool {
		_, err := lp.c.try("test", "-f", lp.c.dir()+"/finished")
		return err == nil
	})
	raw, err := lp.c.try("cat", lp.c.dir()+"/records")
	if err != nil {
		t.Errorf("the %s loop wrote no records: %v", lp.name, err)
		return nil
	}
	var out []upCmd
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		m := nugetRecord.FindStringSubmatch(sc.Text())
		if m == nil {
			t.Errorf("the %s loop wrote a record the test cannot read: %q", lp.name, sc.Text())
			continue
		}
		s, _ := strconv.ParseInt(m[1], 10, 64)
		e, _ := strconv.ParseInt(m[2], 10, 64)
		code, _ := strconv.Atoi(m[3])
		n, _ := strconv.Atoi(m[4])
		cmd := upCmd{Loop: lp.name, Kind: "restore", Start: time.UnixMilli(s), End: time.UnixMilli(e), Code: code, N: n}
		if code != 0 {
			log, _ := lp.c.try("cat", fmt.Sprintf("%s/out/%d.log", lp.c.dir(), n))
			cmd.Log = tailLines(log, 30)
		}
		out = append(out, cmd)
	}
	return out
}

// ── the browser loop ─────────────────────────────────────────────────────────

// upBrowserLoop runs the journey of web/e2e/upgrade, one Playwright run per iteration, on the host.
type upBrowserLoop struct {
	stop chan struct{}
	done chan struct{}
	mu   sync.Mutex
	cmds []upCmd
}

// startBrowserLoop starts the loop of one test (name: cut, rolling). A failed iteration keeps its trace, screenshot and
// error context in .out/playwright/upgrade/<name>/<n>, which the job uploads; a passed one leaves nothing. Each test owns its
// directory, so a later test does not remove the traces of an earlier one (F-64: the rolling test's start removed the
// cut test's trace of its failed iteration before the upload).
func startBrowserLoop(t *testing.T, name string) *upBrowserLoop {
	t.Helper()
	dir := filepath.Join(cfg.repo, "web", "e2e")
	if _, err := os.Stat(filepath.Join(cfg.repo, "web", "node_modules", ".bin", "playwright")); err != nil {
		t.Fatal("R-UI-047: the Playwright suite is not installed; run `npm ci` in web/ and `npx playwright install --with-deps chromium`")
	}
	// the other journeys sign in with the break-glass password and as alice and bob; this one needs the first only, and
	// support.ts reads all three at import
	env := append(os.Environ(),
		"TROVE_E2E_EDGE_URL="+cfg.edgeURL,
		"NODE_EXTRA_CA_CERTS="+cfg.caFile,
		"TROVE_E2E_ALICE_PASSWORD="+secretValue(t, "trove-keycloak", "alice-password"),
		"TROVE_E2E_BOB_PASSWORD="+secretValue(t, "trove-keycloak", "bob-password"),
		"TROVE_E2E_BREAKGLASS_PASSWORD="+secretValue(t, "trove-break-glass", "password"),
		"TROVE_E2E_ADMIN_TOKEN="+adminToken(t),
	)
	artifacts := filepath.Join(cfg.repo, ".out", "playwright", "upgrade", name)
	_ = os.RemoveAll(artifacts)
	l := &upBrowserLoop{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(l.done)
		for n := 1; ; n++ {
			select {
			case <-l.stop:
				return
			default:
			}
			out := filepath.Join(artifacts, strconv.Itoa(n))
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			cmd := exec.CommandContext(ctx, "npx", "playwright", "test", "--config", "playwright.upgrade.config.ts")
			cmd.Dir = dir
			cmd.Env = append(env, "UPGRADE_OUTPUT_DIR="+out)
			start := time.Now()
			raw, err := cmd.CombinedOutput()
			c := upCmd{Loop: "browser", Kind: "journey", Start: start, End: time.Now(), N: n}
			if err != nil {
				c.Code = 1
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					c.Code = ee.ExitCode()
				}
				c.Log = tailLines(string(raw), 40)
				// the journey names every answer of 500 or above on its standard output (SERVER-ERROR), which keeps the cause
				// of a failure in the job log when its trace is not at hand
				if errs := grepLines(string(raw), "SERVER-ERROR"); errs != "" {
					c.Log = errs + "\n" + c.Log
				} else if c.Skew = strings.Contains(string(raw), "ASSET-SKEW "); c.Skew {
					c.Log = grepLines(string(raw), "ASSET-SKEW") + "\n" + c.Log
				}
			} else {
				_ = os.RemoveAll(out)
			}
			cancel()
			l.mu.Lock()
			l.cmds = append(l.cmds, c)
			l.mu.Unlock()
			select {
			case <-l.stop:
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	return l
}

func (l *upBrowserLoop) halt() {
	select {
	case <-l.stop:
	default:
		close(l.stop)
	}
}

func (l *upBrowserLoop) records() []upCmd {
	l.halt()
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]upCmd(nil), l.cmds...)
}

// ── all loops ────────────────────────────────────────────────────────────────

// upLoops are the loops of one test.
type upLoops struct {
	name    string
	fx      *upgradeFx
	npm     *npmLoad
	nuget   []*upNuGetLoop
	browser *upBrowserLoop
	started time.Time
}

// startUpgradeLoops starts the npm ci loops and the publish loop, the NuGet restore loops and the browser loop.
func startUpgradeLoops(t *testing.T, fx *upgradeFx, name string) *upLoops {
	t.Helper()
	l := &upLoops{name: name, fx: fx, started: time.Now()}
	l.npm = startNPMLoad(t, fx.npm, name, npmLoadOpts{ciLoops: upgradeNPMLoops, publish: true})
	l.nuget = startNuGetLoops(t, fx, upgradeNuGetLoops)
	l.browser = startBrowserLoop(t, name)
	t.Cleanup(func() {
		// a test that fails before stop leaves no loop running into the next scenario
		for _, lp := range l.nuget {
			lp.halt()
		}
		l.browser.halt()
	})
	t.Logf("loops %s: %d npm ci, 1 npm publish, %d dotnet restore, 1 browser journey", name, upgradeNPMLoops, upgradeNuGetLoops)
	return l
}

// upResult is what the loops did.
type upResult struct {
	Name string  `json:"name"`
	Cmds []upCmd `json:"commands"`
	npm  npmLoadResult
	// started and stopped bound the span the loops ran in
	started, stopped time.Time
}

func (l *upLoops) stop(t *testing.T) upResult {
	t.Helper()
	r := upResult{Name: l.name, started: l.started, stopped: time.Now()}
	l.browser.halt()
	for _, lp := range l.nuget {
		lp.halt()
	}
	r.npm = l.npm.stop(t)
	for _, c := range r.npm.Commands {
		r.Cmds = append(r.Cmds, upCmd{Loop: c.Loop, Kind: c.Kind, Start: c.Start, End: c.End, Code: c.Code, N: c.N, Log: c.Log})
	}
	for _, lp := range l.nuget {
		r.Cmds = append(r.Cmds, lp.records(t)...)
	}
	r.Cmds = append(r.Cmds, l.browser.records()...)
	noteResult(t, "upgrade-loops-"+l.name, r)
	return r
}

// requireClean is the pass condition of both tests: no iteration of any loop failed after the client's own retries, and
// every loop ran an iteration during every window of the upgrade, so the upgrade met each of them. The publish loop runs one
// short command every 10 s and is not required to overlap a window; its overlap is reported. skewFrom names a release whose
// pods cannot serve the hashed UI assets of the release under test and cannot be changed any more (Q-HA-99): a journey
// iteration that failed on that 404 only is a finding of that release, counted and shown; with skewFrom empty it fails the
// test like any other failure (F-58).
func (r upResult) requireClean(t *testing.T, skewFrom string, windows ...upWindow) {
	t.Helper()
	type tally struct{ n, failed int }
	byKind := map[string]*tally{}
	loops := map[string]string{}
	var slowest time.Duration
	skewed := 0
	for _, c := range r.Cmds {
		k := byKind[c.Kind]
		if k == nil {
			k = &tally{}
			byKind[c.Kind] = k
		}
		k.n++
		loops[c.Loop] = c.Kind
		slowest = max(slowest, c.End.Sub(c.Start))
		if c.Code != 0 && c.Skew && skewFrom != "" {
			// Q-HA-99: the asset skew of the UI against the pods of skewFrom is a finding that is counted and shown, and no other
			// failure is let through
			skewed++
			t.Logf("FINDING (Q-HA-99): journey iteration %d met a 404 for a hashed asset between the release under test and %s and failed after %s, started %s:\n%s",
				c.N, skewFrom, c.End.Sub(c.Start).Round(100*time.Millisecond), c.Start.Format("15:04:05.000"), grepLines(c.Log, "ASSET-SKEW"))
			continue
		}
		if c.Code != 0 {
			k.failed++
			t.Errorf("%s iteration %d of the %s loop exited %d after %s, started %s:\n%s", c.Kind, c.N, c.Loop, c.Code,
				c.End.Sub(c.Start).Round(100*time.Millisecond), c.Start.Format("15:04:05.000"), c.Log)
		}
	}
	for _, want := range []string{"ci", "publish", "restore", "journey"} {
		if byKind[want] == nil || byKind[want].n == 0 {
			t.Errorf("the loops %s ran no %s iteration", r.Name, want)
		}
	}
	var names []string
	for k, v := range byKind {
		names = append(names, fmt.Sprintf("%s %d (failed %d)", k, v.n, v.failed))
	}
	sort.Strings(names)
	t.Logf("loops %s: iterations %s, slowest %s, journey iterations that met the asset skew of the UI (Q-HA-99) %d", r.Name, strings.Join(names, ", "), slowest.Round(100*time.Millisecond), skewed)
	for _, w := range windows {
		overlap := map[string]int{}
		for _, c := range r.Cmds {
			if c.Start.Before(w.To) && c.End.After(w.From) {
				overlap[c.Loop]++
			}
		}
		for loop, kind := range loops {
			if kind != "publish" && overlap[loop] == 0 {
				t.Errorf("the %s loop ran no iteration during %s (%s to %s): the upgrade did not meet it", loop, w.Name, w.From.Format("15:04:05"), w.To.Format("15:04:05"))
			}
		}
		t.Logf("iterations per loop during %s (%s, %s): %v", w.Name, w.From.Format("15:04:05"), w.To.Sub(w.From).Round(time.Second), overlap)
	}
}

// edgeNamespace is the namespace of the dev profile's edge (HAProxy Ingress, deploy/dev/haproxy-values.yaml), whose pods write
// one access line per request to their standard output.
const edgeNamespace = "ingress-controller"

// edgeGapLine matches an access line of the edge for a request it answered 503 because the backend of its route had no
// server: `<backend>/<NOSRV> Tq/Tw/Tc/Tr/Ta 503 …`. A 503 of trove itself names its server; a request the client gave up
// on before a server was chosen has the status -1.
var edgeGapLine = regexp.MustCompile(`\S+/<NOSRV> \S+ 503 `)

// requireEdgeServed is the edge's side of the pass condition: while the loops ran, the edge answered no request with 503 for
// want of a backend, because an upgrade without downtime never leaves a route without a ready server (R-HA-020, R-HA-064).
// The loops alone cannot tell: npm repeats an answer of 503 and passes, and a browser iteration meets a gap of a second or
// two only by chance (F-64: in step 2 of the cut the edge answered 509 requests with 503 within 1.4 s, every npm ci passed,
// and one journey iteration out of 14 met it).
func (r upResult) requireEdgeServed(t *testing.T) {
	t.Helper()
	pods, err := kubectl("-n", edgeNamespace, "get", "pods", "-o", "name")
	if err != nil || strings.TrimSpace(pods) == "" {
		t.Errorf("the edge's pods: %v %q", err, pods)
		return
	}
	var gaps []string
	read := 0
	for _, pod := range strings.Fields(pods) {
		out, err := kubectl("-n", edgeNamespace, "logs", pod, "--all-containers", "--timestamps", "--since-time", r.started.UTC().Format(time.RFC3339))
		if err != nil {
			t.Errorf("the access lines of %s: %v", pod, err)
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			ts, rest, ok := strings.Cut(line, " ")
			when, perr := time.Parse(time.RFC3339Nano, ts)
			if !ok || perr != nil || when.Before(r.started) || when.After(r.stopped) {
				continue
			}
			read++
			if edgeGapLine.MatchString(rest) {
				gaps = append(gaps, strings.TrimSpace(line))
			}
		}
	}
	if len(gaps) > 0 {
		t.Errorf("the edge answered %d requests with 503 for want of a backend while the loops %s ran (%s to %s; R-HA-020, R-HA-064), the first:\n%s",
			len(gaps), r.Name, r.started.Format("15:04:05"), r.stopped.Format("15:04:05"), strings.Join(gaps[:min(len(gaps), 10)], "\n"))
		return
	}
	t.Logf("the edge answered no request with 503 for want of a backend while the loops %s ran (%d lines of its pods read)", r.Name, read)
}

// publishedAreReadable is the write path's pass condition: every publish the registry acknowledged during the upgrade
// installs with its content, and every version it lists has a tarball that matches its integrity.
func (r upResult) publishedAreReadable(t *testing.T, fx *upgradeFx) {
	t.Helper()
	r.npm.installPublished(t, fx.npm)
	r.npm.committedTarballsReadable(t, fx.npm)
}

// grepLines keeps the lines of s that contain the word.
func grepLines(s, word string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, word) {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return strings.Join(out, "\n")
}
