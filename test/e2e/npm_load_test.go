//go:build e2e

package e2e

// The npm part of the load of R-HA-052 and R-HA-059 (WP2.8, E2.11), which the drain and failover scenarios of
// npm_ha_test.go run under:
//
//   - npm ci loops (R-HA-059: eight) of a lockfile of at least 400 packages through npm-all, whose members are
//     npm-local (three packages of this run) and npm-remote (the corpus of the counting test upstream), with trove's
//     cache warm and an empty client cache per iteration (`npm ci --cache <new directory>`);
//   - an npm publish of a new version to npm-local every 10 s;
//   - the k6 npm requests of k6/npm.js through the edge.
//
// npm runs with its default retries. The loops run detached inside the client pods and write one record per command,
// so a kubectl exec that breaks during a fault is never counted as a failed client command, and the output of a
// failed command is kept. docs/developer/e2e.md#drain-and-failover-under-npm-load describes the whole.

import (
	"bufio"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

const (
	haSA        = "e2e-npm-ha"
	haCILoops   = 8   // R-HA-059: eight parallel npm ci loops
	haMinLocked = 400 // R-HA-059: a fixture with 400 locked dependencies
	// haK6Rate is the npm share of R-HA-059's 150 requests/s: its 50 % documents and 30 % tarballs (Q-HA-65); the
	// 20 % of NuGet requests join with M3.
	haK6Rate = 120
	haRow    = "npm11"
)

// npmHAFixture is what every scenario's load uses: the tokens, the lockfile project and the input of k6/npm.js.
type npmHAFixture struct {
	fx *npmFixture
	// tokens: two per ci loop, then k6's, then the publisher's. The package_read class of R-API-016 budgets 200
	// requests/s per token with a burst of 1,000 in 5 s; one loop asks for 475 tarballs every 2.5 s, so on one token
	// it met the budget in four of ten commands (429, then npm's 10 s retry). A loop alternates its two tokens.
	tokens  []string
	pkgJSON string
	lock    string
	locked  int
	locals  []string
	k6Input []byte
	// for the load scripts of R-ARCH-027 (npm_perf_test.go): a 1 MiB and a 50 KiB tarball and the package names
	blob1MiB, small50KiB string
	blobPkg, smallPkg    string
	remoteRoots          []string
}

func (h *npmHAFixture) k6Token() string        { return h.tokens[2*haCILoops] }
func (h *npmHAFixture) publisherToken() string { return h.tokens[2*haCILoops+1] }

var (
	haFxOnce sync.Once
	haFxVal  *npmHAFixture
	haFxErr  error
)

// npmHAFx builds the fixture once per process, in the scatter profile of the npm tests.
func npmHAFx(t *testing.T) *npmHAFixture {
	t.Helper()
	fx := npmFx(t)
	requireScatter(t, fx)
	haFxOnce.Do(func() {
		haFxErr = fmt.Errorf("the fixture did not finish (see the first failure)")
		haFxVal = buildNPMHAFixture(t, fx)
		haFxErr = nil
	})
	if haFxErr != nil {
		t.Fatalf("npm HA fixture: %v", haFxErr)
	}
	return haFxVal
}

type haTarball struct {
	Path      string `json:"path"`
	Size      int    `json:"size"`
	Integrity string `json:"integrity"`
}

func buildNPMHAFixture(t *testing.T, fx *npmFixture) *npmHAFixture {
	start := time.Now()
	h := &npmHAFixture{fx: fx}
	adm := fx.adm
	ensureSA(t, adm, haSA)
	ensurePolicy(t, adm, haSA, []string{npmLocalKey}, [2]any{"sa:" + haSA, []string{"read", "deploy", "delete"}})
	ensurePolicy(t, adm, haSA+"-read", []string{npmRemoteKey, npmAllKey}, [2]any{"sa:" + haSA, []string{"read"}})
	h.tokens = pacedSATokens(t, adm, haSA, 2*haCILoops+2, 6*3600) // 18 of the 20 an account may hold (auth.tokens.max_active_per_principal)
	// the policies are in force on every replica within R-AUTHZ-028's bound; the scatter edge spreads the probes
	waitFor(t, "the load's token to read npm-local and npm-all on every replica", 30*time.Second, 500*time.Millisecond, func() bool {
		for i := 0; i < 6; i++ {
			for _, key := range []string{npmLocalKey, npmAllKey} {
				if r := adm.withToken(h.tokens[0]).get("/api/npm/" + key + "/-/whoami"); r.Status != 200 {
					return false
				}
			}
		}
		return true
	})

	c := newNPMClient(t, fx, haRow, 0)
	c.token = h.tokens[0]
	c.tag = "ha-" + fx.run + "-setup"
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("@e2e/ha-%s-lib%d", fx.run, i)
		c.publish(name, "1.0.0")
		h.locals = append(h.locals, name)
	}

	// The tarballs of k6's 30 %: nine versions of one local package, 1 MiB to 5 MiB of random bytes, so the
	// tarball is as large as its content.
	blob := fmt.Sprintf("@e2e/ha-%s-blob", fx.run)
	for i := 0; i < 9; i++ {
		version := fmt.Sprintf("1.0.%d", i)
		dir := c.publishDir(blob, version, "marker:"+blob+"@"+version)
		if r := c.sh(dir, fmt.Sprintf("head -c %d /dev/urandom > data.bin", 1<<20+i*(1<<19))); r.Code != 0 {
			t.Fatalf("writing the content of %s@%s: %s", blob, version, r.Out)
		}
		c.must(dir, c.publishCmd(version)...)
	}
	_, _, doc := fx.versionsOf(t, npmAllKey, blob)
	var tarballs []haTarball
	versions, _ := doc["versions"].(map[string]any)
	for v, raw := range versions {
		dist, _ := raw.(map[string]any)["dist"].(map[string]any)
		u, err := url.Parse(fmt.Sprint(dist["tarball"]))
		if err != nil {
			t.Fatalf("%s@%s: tarball URL %v: %v", blob, v, dist["tarball"], err)
		}
		r := adm.withToken(h.k6Token()).get(u.EscapedPath()).want(t, "tarball of "+blob+"@"+v, 200)
		sum := sha512.Sum512(r.Body)
		integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
		if integrity != dist["integrity"] {
			t.Fatalf("%s@%s: the tarball's sha512 is %s, the document says %v", blob, v, integrity, dist["integrity"])
		}
		tarballs = append(tarballs, haTarball{Path: u.EscapedPath(), Size: len(r.Body), Integrity: integrity})
	}
	sort.Slice(tarballs, func(i, j int) bool { return tarballs[i].Size < tarballs[j].Size })
	if len(tarballs) != 9 || tarballs[0].Size < 1<<20 || tarballs[8].Size > 6<<20 {
		t.Fatalf("the tarballs of %s: %+v", blob, tarballs)
	}
	h.blobPkg, h.blob1MiB = blob, tarballs[0].Path
	h.smallPkg = fmt.Sprintf("@e2e/ha-%s-small", fx.run)
	sdir := c.publishDir(h.smallPkg, "1.0.0", "marker:"+h.smallPkg+"@1.0.0")
	if r := c.sh(sdir, fmt.Sprintf("head -c %d /dev/urandom > data.bin", 50<<10)); r.Code != 0 {
		t.Fatalf("writing the content of %s: %s", h.smallPkg, r.Out)
	}
	c.must(sdir, c.publishCmd("1.0.0")...)
	_, _, sdoc := fx.versionsOf(t, npmAllKey, h.smallPkg)
	sdist, _ := sdoc["versions"].(map[string]any)["1.0.0"].(map[string]any)["dist"].(map[string]any)
	su, err := url.Parse(fmt.Sprint(sdist["tarball"]))
	if err != nil {
		t.Fatalf("%s: tarball URL %v", h.smallPkg, sdist["tarball"])
	}
	h.small50KiB = su.EscapedPath()

	// The lockfile project: corpus roots whose closure has at least 400 names minus the three local packages, plus
	// those. `npm install` resolves it through npm-all, writes the lockfile and fills trove's cache: the loops find it
	// warm.
	roots := corpus.Default().Reference(haMinLocked - len(h.locals)).Dependencies
	deps := map[string]string{}
	for name, r := range roots {
		deps[name] = r
	}
	for _, l := range h.locals {
		deps[l] = "1.0.0"
	}
	dir := c.project("ha-fixture", map[string]any{"dependencies": deps})
	c.configure(dir)
	c.must(dir, "install")
	h.pkgJSON = c.read(dir + "/package.json")
	h.lock = c.read(dir + "/package-lock.json")
	var lock struct {
		Packages map[string]struct {
			Resolved string `json:"resolved"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(h.lock), &lock); err != nil {
		t.Fatalf("the fixture's lockfile: %v", err)
	}
	locals := 0
	for path, p := range lock.Packages {
		if path == "" {
			continue
		}
		h.locked++
		if strings.Contains(p.Resolved, "/@e2e/ha-"+fx.run+"-lib") {
			locals++
		}
	}
	if h.locked < haMinLocked || locals != len(h.locals) {
		t.Fatalf("the fixture's lockfile locks %d packages (%d of them from npm-local), want at least %d with %d local", h.locked, locals, haMinLocked, len(h.locals))
	}

	// k6's documents: the local packages and the first twenty roots of the remote, by name
	var docs []string
	for _, name := range append(append([]string{}, h.locals...), blob) {
		docs = append(docs, "/api/npm/"+npmAllKey+"/"+url.PathEscape(name))
	}
	names := sortedKeys(roots)
	h.remoteRoots = names[:min(20, len(names))]
	for _, name := range h.remoteRoots {
		docs = append(docs, "/api/npm/"+npmAllKey+"/"+url.PathEscape(name))
	}
	h.k6Input, _ = json.Marshal(map[string]any{"documents": docs, "tarballs": tarballs})
	t.Logf("npm HA fixture: %d locked packages (%d local), %d k6 documents, tarballs of %d to %d bytes, %d tokens, built in %s",
		h.locked, locals, len(docs), tarballs[0].Size, tarballs[8].Size, len(h.tokens), time.Since(start).Round(time.Second))
	return h
}

// pacedSATokens revokes the active tokens of a service account (an earlier run's) and mints n new ones. The
// administrator's token has the write budget of R-API-016 (burst 50), which revoking and minting 18 tokens after the
// fixture's other writes exceeds; a 429 is waited out for its Retry-After.
func pacedSATokens(t testing.TB, adm *api, sa string, n, ttl int) []string {
	t.Helper()
	send := func(method, path string, body any, want ...int) reply {
		for range 30 {
			r, err := adm.send(method, path, body)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			if r.Status != http.StatusTooManyRequests {
				return r.want(t, method+" "+path, want...)
			}
			wait, _ := strconv.Atoi(r.Header.Get("Retry-After"))
			time.Sleep(time.Duration(max(wait, 1)) * time.Second)
		}
		t.Fatalf("%s %s: still rate limited after 30 attempts", method, path)
		return reply{}
	}
	var l struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	send(http.MethodGet, "/api/v1/tokens?owner=sa:"+sa+"&state=active&limit=500", nil, 200).JSON(t, &l)
	for _, it := range l.Items {
		send(http.MethodDelete, "/api/v1/tokens/"+it.ID, nil, 200, 204, 404)
	}
	var out []string
	for len(out) < n {
		var c createdToken
		body := map[string]any{"description": "end-to-end test", "ttlSeconds": ttl, "scope": map[string]any{"inherit": true}}
		send(http.MethodPost, "/api/v1/service-accounts/"+sa+"/tokens", body, 201).JSON(t, &c)
		out = append(out, c.Token)
	}
	return out
}

// ── the loops ────────────────────────────────────────────────────────────────

// ciLoopScript is one npm ci loop: the lockfile install with an empty client cache, again and again until the file
// stop appears, on the loop's two tokens in turn. Each command writes `R ci <start ms> <end ms> <exit status> <n>`;
// the output of a failed one stays.
const ciLoopScript = `#!/bin/sh
mkdir -p out caches
first="$TROVE_TOKEN"
n=0
while [ ! -e stop ]; do
  n=$((n + 1))
  if [ $((n % 2)) -eq 0 ]; then TROVE_TOKEN="$TROVE_TOKEN_ALT"; else TROVE_TOKEN="$first"; fi
  export TROVE_TOKEN
  cache="$(mktemp -d -p "$PWD/caches")"
  start="$(date +%s%3N)"
  npm11 ci --cache "$cache" >"out/$n.log" 2>&1
  rc=$?
  end="$(date +%s%3N)"
  rm -rf "$cache" node_modules
  [ "$rc" -eq 0 ] && rm -f "out/$n.log"
  echo "R ci $start $end $rc $n" >>records
done
echo finished >finished
`

// publishLoopScript publishes version 1.0.<n> of $PKG to npm-local every 10 s, counted from the start of the
// previous publish: `R publish <start ms> <end ms> <exit status> <n> <version>`.
const publishLoopScript = `#!/bin/sh
mkdir -p out
n=0
while [ ! -e stop ]; do
  n=$((n + 1))
  v="1.0.$n"
  start="$(date +%s%3N)"
  printf '{"name":"%s","version":"%s","description":"trove HA load","main":"index.js","license":"MIT"}\n' "$PKG" "$v" >pkg/package.json
  printf 'module.exports = "marker:%s@%s";\n' "$PKG" "$v" >pkg/index.js
  (cd pkg && npm11 publish) >"out/$n.log" 2>&1
  rc=$?
  end="$(date +%s%3N)"
  [ "$rc" -eq 0 ] && rm -f "out/$n.log"
  echo "R publish $start $end $rc $n $v" >>records
  while [ ! -e stop ] && [ $(( $(date +%s%3N) - start )) -lt 10000 ]; do sleep 0.2; done
done
echo finished >finished
`

type npmLoadOpts struct {
	ciLoops int  // npm ci loops; R-HA-059: eight
	publish bool // a publish of a new version to npm-local every 10 s
	k6      bool // the k6 npm requests at haK6Rate
}

// haFullLoad is the npm part of R-HA-059's load; the pod-crash scenario of R-HA-052 runs without k6.
func haFullLoad() npmLoadOpts { return npmLoadOpts{ciLoops: haCILoops, publish: true, k6: true} }

type npmLoop struct {
	name string
	c    *npmClient
	dir  string
}

type npmLoad struct {
	name    string
	h       *npmHAFixture
	loops   []*npmLoop // the ci loops, then the publish loop
	pubPkg  string
	k6      *k6run
	started time.Time
}

// startNPMLoad starts the loops in the client pods (spread over them) and k6, and returns once each has begun.
func startNPMLoad(t *testing.T, h *npmHAFixture, name string, o npmLoadOpts) *npmLoad {
	t.Helper()
	l := &npmLoad{name: name, h: h, pubPkg: fmt.Sprintf("@e2e/ha-%s-%s-pub", h.fx.run, name)}
	pods := npmClientCount(t)
	for i := 0; i < o.ciLoops; i++ {
		c := newNPMClient(t, h.fx, haRow, i%pods)
		c.token = h.tokens[2*i]
		c.extraEnv = []string{"TROVE_TOKEN_ALT=" + h.tokens[2*i+1]}
		c.tag = fmt.Sprintf("ha-%s-%s-ci%d", h.fx.run, name, i)
		dir := c.dir("loop")
		c.sh("/work", `rm -rf "/work/`+c.tag+`" && mkdir -p "`+dir+`"`)
		c.write(dir+"/package.json", h.pkgJSON)
		c.write(dir+"/package-lock.json", h.lock)
		c.configure(dir)
		c.write(dir+"/loop.sh", ciLoopScript)
		l.loops = append(l.loops, &npmLoop{name: fmt.Sprintf("ci%d", i), c: c, dir: dir})
	}
	if o.publish {
		c := newNPMClient(t, h.fx, haRow, 0)
		c.token = h.publisherToken()
		c.registryKey = npmLocalKey
		c.tag = fmt.Sprintf("ha-%s-%s-publish", h.fx.run, name)
		c.extraEnv = []string{"PKG=" + l.pubPkg}
		dir := c.dir("loop")
		c.sh("/work", `rm -rf "/work/`+c.tag+`" && mkdir -p "`+dir+`/pkg"`)
		c.configure(dir + "/pkg")
		c.write(dir+"/loop.sh", publishLoopScript)
		l.loops = append(l.loops, &npmLoop{name: "publish", c: c, dir: dir})
	}
	l.started = time.Now()
	for _, lp := range l.loops {
		// setsid and the redirections detach the loop from the exec session, which ends at once
		if r := lp.c.sh(lp.dir, `setsid nohup sh ./loop.sh </dev/null >loop.log 2>&1 &`); r.Code != 0 {
			t.Fatalf("starting the %s loop: %s", lp.name, r.Out)
		}
	}
	if o.k6 {
		l.k6 = startK6Script(t, "npm-"+name, "npm.js", time.Hour, k6Opts{rate: haK6Rate, prefix: "N ", token: h.k6Token(), files: map[string][]byte{"npm-fixture.json": h.k6Input}})
	}
	t.Cleanup(func() {
		// a test that fails before stop leaves no loop running into the next scenario
		for _, lp := range l.loops {
			_, _ = kubectl("-n", cfg.namespace, "exec", npmClientPod(t, lp.c.pod), "--", "touch", lp.dir+"/stop")
		}
	})
	t.Logf("npm load %s: %d npm ci loops, publish %v, k6 %v", name, o.ciLoops, o.publish, o.k6)
	return l
}

// npmCommand is one command of a loop.
type npmCommand struct {
	Loop    string    `json:"loop"`
	Kind    string    `json:"kind"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Code    int       `json:"code"`
	N       int       `json:"n"`
	Version string    `json:"version,omitempty"`
	Log     string    `json:"log,omitempty"` // the tail of a failed command's output
}

type npmLoadResult struct {
	Name      string
	Commands  []npmCommand
	Published []string // the versions whose publish exited 0
	PubPkg    string
	K6        *npmK6Result
}

var recordLine = regexp.MustCompile(`^R (ci|publish) (\d+) (\d+) (\d+) (\d+)(?: (\S+))?$`)

// stop ends k6, lets every loop finish the command it is running, and collects the records.
func (l *npmLoad) stop(t *testing.T) npmLoadResult {
	t.Helper()
	res := npmLoadResult{Name: l.name, PubPkg: l.pubPkg}
	if l.k6 != nil {
		k := parseNPMK6(t, l.k6)
		res.K6 = &k
	}
	for _, lp := range l.loops {
		lp.c.sh(lp.dir, "touch stop")
	}
	for _, lp := range l.loops {
		// an npm command that retries waits up to a minute between attempts (fetch-retry-maxtimeout)
		waitFor(t, "the "+lp.name+" loop of "+l.name+" to finish its command", 10*time.Minute, 2*time.Second, func() bool {
			return lp.c.sh(lp.dir, "test -e finished").Code == 0
		})
		sc := bufio.NewScanner(strings.NewReader(lp.c.read(lp.dir + "/records")))
		for sc.Scan() {
			m := recordLine.FindStringSubmatch(sc.Text())
			if m == nil {
				t.Errorf("the %s loop wrote a record the test cannot read: %q", lp.name, sc.Text())
				continue
			}
			s, _ := strconv.ParseInt(m[2], 10, 64)
			e, _ := strconv.ParseInt(m[3], 10, 64)
			code, _ := strconv.Atoi(m[4])
			n, _ := strconv.Atoi(m[5])
			cmd := npmCommand{Loop: lp.name, Kind: m[1], Start: time.UnixMilli(s), End: time.UnixMilli(e), Code: code, N: n, Version: m[6]}
			if code != 0 {
				cmd.Log = tailLines(lp.c.read(fmt.Sprintf("%s/out/%d.log", lp.dir, n)), 30)
			} else if cmd.Kind == "publish" {
				res.Published = append(res.Published, cmd.Version)
			}
			res.Commands = append(res.Commands, cmd)
		}
	}
	noteResult(t, "npm-load-"+l.name, res)
	byKind := map[string][2]int{}
	var slowest time.Duration
	for _, c := range res.Commands {
		k := byKind[c.Kind]
		k[0]++
		if c.Code != 0 {
			k[1]++
		}
		byKind[c.Kind] = k
		slowest = max(slowest, c.End.Sub(c.Start))
	}
	t.Logf("npm load %s: commands (count, failed) %v, slowest %s, %d publishes acknowledged", l.name, byKind, slowest.Round(100*time.Millisecond), len(res.Published))
	return res
}

// allExitZero is R-HA-059's and R-HA-051's client condition: every npm command exits 0 with npm's default retries.
func (r npmLoadResult) allExitZero(t *testing.T) {
	t.Helper()
	if len(r.Commands) == 0 {
		t.Errorf("the npm load %s ran no command", r.Name)
	}
	for _, c := range r.Commands {
		if c.Code != 0 {
			t.Errorf("npm %s (%s, command %d %s) exited %d after %s, started %s:\n%s", c.Kind, c.Loop, c.N, c.Version, c.Code,
				c.End.Sub(c.Start).Round(100*time.Millisecond), c.Start.Format("15:04:05.000"), c.Log)
		}
	}
}

// loopsSpan requires that every npm ci loop ran a command that overlapped [from, to]: the fault happened during the
// npm ci loops, not between them. The publish loop runs one short command every 10 s, which a fault of a second
// (a Sentinel failover) may fall between; its overlap is reported.
func (r npmLoadResult) loopsSpan(t *testing.T, from, to time.Time) {
	t.Helper()
	overlap := map[string]int{}
	loops := map[string]bool{}
	for _, c := range r.Commands {
		if c.Kind == "ci" {
			loops[c.Loop] = true
		}
		if c.Start.Before(to) && c.End.After(from) {
			overlap[c.Loop]++
		}
	}
	if len(loops) == 0 {
		t.Errorf("the npm load %s ran no npm ci command", r.Name)
	}
	for loop := range loops {
		if overlap[loop] == 0 {
			t.Errorf("the %s loop ran no command between %s and %s: the fault did not meet it", loop, from.Format("15:04:05"), to.Format("15:04:05"))
		}
	}
	t.Logf("commands per loop that overlapped %s to %s: %v", from.Format("15:04:05"), to.Format("15:04:05"), overlap)
}

// installPublished is R-HA-059's "every acknowledged publish installs afterwards": one npm install through npm-all of
// every acknowledged version under an alias, then each alias loads with the marker of its version.
func (r npmLoadResult) installPublished(t *testing.T, h *npmHAFixture) {
	t.Helper()
	if len(r.Published) == 0 {
		t.Errorf("the npm load %s acknowledged no publish", r.Name)
		return
	}
	c := newNPMClient(t, h.fx, haRow, 0)
	c.token = h.tokens[0]
	c.tag = fmt.Sprintf("ha-%s-%s-verify", h.fx.run, r.Name)
	deps, want := map[string]string{}, map[string]string{}
	for _, v := range r.Published {
		alias := "v-" + strings.ReplaceAll(v, ".", "-")
		deps[alias] = "npm:" + r.PubPkg + "@" + v
		want[alias] = "marker:" + r.PubPkg + "@" + v
	}
	dir := c.project("installed", map[string]any{"dependencies": deps})
	c.configure(dir)
	c.must(dir, "install")
	raw, _ := json.Marshal(want)
	js := `const want = JSON.parse(process.argv[1]); const bad = [];
for (const [k, v] of Object.entries(want)) { let got; try { got = String(require(k)); } catch (e) { got = 'error ' + e.message; } if (got !== v) bad.push(k + ': ' + got); }
process.stdout.write(bad.length ? bad.join('\n') : 'ok');`
	if out := c.exec(dir, nil, "node", "-e", js, string(raw)); out.Code != 0 || strings.TrimSpace(out.Out) != "ok" {
		t.Errorf("acknowledged publishes of %s that do not install with their content: %s", r.PubPkg, out.Out)
	}
	t.Logf("%d acknowledged publishes of %s installed with their content", len(r.Published), r.PubPkg)
}

// committedTarballsReadable reads every version the registry lists for the publish loop's package and downloads its
// tarball against its integrity: an item row without its object would answer an error here. It stands in for
// `trove admin storage verify` (R-HA-052's pod-crash condition), which M5 builds.
func (r npmLoadResult) committedTarballsReadable(t *testing.T, h *npmHAFixture) {
	t.Helper()
	versions, _, doc := h.fx.versionsOf(t, npmLocalKey, r.PubPkg)
	acked := map[string]bool{}
	for _, v := range r.Published {
		acked[v] = true
	}
	listed := map[string]bool{}
	vs, _ := doc["versions"].(map[string]any)
	for _, v := range versions {
		listed[v] = true
		dist, _ := vs[v].(map[string]any)["dist"].(map[string]any)
		u, err := url.Parse(fmt.Sprint(dist["tarball"]))
		if err != nil {
			t.Errorf("%s@%s: tarball URL %v", r.PubPkg, v, dist["tarball"])
			continue
		}
		got := h.fx.adm.withToken(h.tokens[0]).get(u.EscapedPath())
		sum := sha512.Sum512(got.Body)
		if got.Status != 200 || "sha512-"+base64.StdEncoding.EncodeToString(sum[:]) != dist["integrity"] {
			t.Errorf("%s@%s is listed but its tarball answers %s with sha512 %x…, want 200 and %v", r.PubPkg, v, got, sum[:6], dist["integrity"])
		}
	}
	for v := range acked {
		if !listed[v] {
			t.Errorf("the acknowledged publish %s@%s is not listed by npm-local", r.PubPkg, v)
		}
	}
	t.Logf("%d versions of %s listed, every tarball read with its integrity (%d acknowledged)", len(versions), r.PubPkg, len(acked))
}

// ── k6 ───────────────────────────────────────────────────────────────────────

type npmK6Line struct {
	At      time.Time
	Status  int
	Replica string
	MS      int
	Kind    string
	Note    string
	Path    string
}

type npmK6Result struct {
	Total       int
	Status      map[string]int // "document 200", "document 304", "tarball 200", ...
	Replicas    map[string]int
	Failures    []npmK6Line
	Bad         int
	Interrupted int
	Lines       []npmK6Line
	First, Last time.Time
	Dropped     float64
}

var npmK6Pattern = regexp.MustCompile(`^N (\d+) (\d+) (\S+) (\d+) (document|tarball) (ok|bad) (\S+)$`)

// parseNPMK6 ends the k6 run with SIGINT and reads its lines. As parseK6 does, a request cut by the harness's own stop
// (status 0, "context canceled", in the last two seconds) is set aside; every other status 0 is a failure.
func parseNPMK6(t *testing.T, k *k6run) npmK6Result {
	t.Helper()
	stopK6(t, k)
	r := npmK6Result{Status: map[string]int{}, Replicas: map[string]int{}}
	f, err := os.Open(filepath.Join(k.dir, "console.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var all []npmK6Line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		m := npmK6Pattern.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		ms, _ := strconv.ParseInt(m[1], 10, 64)
		status, _ := strconv.Atoi(m[2])
		dur, _ := strconv.Atoi(m[4])
		l := npmK6Line{At: time.UnixMilli(ms), Status: status, Replica: m[3], MS: dur, Kind: m[5], Note: m[6], Path: m[7]}
		all = append(all, l)
		if r.First.IsZero() || l.At.Before(r.First) {
			r.First = l.At
		}
		if l.At.After(r.Last) {
			r.Last = l.At
		}
	}
	for _, l := range all {
		if l.Status == 0 && strings.HasSuffix(l.Replica, ":context_canceled") && l.At.After(r.Last.Add(-2*time.Second)) {
			r.Interrupted++
			continue
		}
		r.Lines = append(r.Lines, l)
		r.Total++
		r.Status[fmt.Sprintf("%s %d", l.Kind, l.Status)]++
		ok := (l.Status >= 200 && l.Status < 300 || l.Status == 304) && l.Note == "ok"
		if l.Note == "bad" {
			r.Bad++
		}
		if ok {
			r.Replicas[l.Replica]++
		} else {
			r.Failures = append(r.Failures, l)
		}
	}
	if b, err := os.ReadFile(filepath.Join(k.dir, "summary.json")); err == nil {
		var s struct {
			Dropped float64 `json:"dropped_iterations"`
		}
		_ = json.Unmarshal(b, &s)
		r.Dropped = s.Dropped
	} else {
		t.Errorf("k6 %s wrote no summary.json: %v", k.name, err)
	}
	p99, _ := r.p99(r.First, r.Last.Add(time.Millisecond))
	t.Logf("k6 %s: %d requests over %s, status %v, replicas %v, %d wrong tarballs, %d failures, p99 %s, %d in flight at the stop set aside, dropped iterations %v",
		k.name, r.Total, r.Last.Sub(r.First).Round(time.Second), r.Status, r.Replicas, r.Bad, len(r.Failures), p99, r.Interrupted, r.Dropped)
	return r
}

// stopK6 ends a k6 run with SIGINT, which makes it write its summary.
func stopK6(t *testing.T, k *k6run) {
	t.Helper()
	_ = exec.Command("docker", "kill", "--signal=INT", k.container).Run()
	select {
	case <-k.done:
	case <-time.After(2 * time.Minute):
		t.Fatalf("k6 %s did not stop", k.name)
	}
}

// only2xx304 is the k6 condition of R-HA-059 and R-HA-057: no answer other than 2xx and 304, no connection error
// (k6 does not retry), no tarball with other bytes than were published, and the arrival rate held.
func (r npmK6Result) only2xx304(t *testing.T) {
	t.Helper()
	secs := r.Last.Sub(r.First).Seconds()
	if secs < 20 {
		t.Errorf("k6 ran for %.0f s, too short to say anything", secs)
	}
	if want := int(0.95 * haK6Rate * secs); r.Total < want {
		t.Errorf("k6 sent %d requests in %.0f s, want at least %d", r.Total, secs, want)
	}
	if len(r.Failures) != 0 {
		first := r.Failures
		if len(first) > 8 {
			first = first[:8]
		}
		t.Errorf("%d of %d k6 requests were not 2xx or 304 with the published bytes: %v; first: %+v", len(r.Failures), r.Total, r.Status, first)
	}
	if r.Dropped != 0 {
		t.Errorf("k6 dropped %v iterations: the load fell behind its rate", r.Dropped)
	}
	if r.Status["document 200"] == 0 || r.Status["document 304"] == 0 || r.Status["tarball 200"] == 0 {
		t.Errorf("k6 did not exercise every kind of request: %v", r.Status)
	}
}

// p99 is the 99th percentile of the duration of the requests started in [from, to), and their number.
func (r npmK6Result) p99(from, to time.Time) (time.Duration, int) {
	var d []time.Duration
	for _, l := range r.Lines {
		if !l.At.Before(from) && l.At.Before(to) {
			d = append(d, time.Duration(l.MS)*time.Millisecond)
		}
	}
	if len(d) == 0 {
		return 0, 0
	}
	return percentile(d, 0.99), len(d)
}

// ── misc ─────────────────────────────────────────────────────────────────────

// troveSettled waits until the release has exactly its web and worker pods, all ready and none terminating, so a
// scenario does not start inside the recovery of the one before.
func troveSettled(t *testing.T, web, worker int) {
	t.Helper()
	waitFor(t, "the trove pods to settle", 5*time.Minute, 2*time.Second, func() bool {
		ps := podsOf(t, selTrove)
		w, k := 0, 0
		for _, p := range ps {
			if !p.Ready || p.Deleting {
				return false
			}
			if strings.Contains(p.Name, "-web-") {
				w++
			} else {
				k++
			}
		}
		return w == web && k == worker
	})
}

// replicaMetric reads one series of every trove pod (web and worker).
func replicaMetric(t *testing.T, series string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, p := range livePods(t, selTrove) {
		out[p.Name] = metricsOf(t, p.Name)[series]
	}
	return out
}

// deleteWebPod deletes a web pod with its grace period, as an operator or a node upgrade does, and returns its name.
func deleteWebPod(t *testing.T) string {
	t.Helper()
	webs := readyWebPods(t)
	if len(webs) == 0 {
		t.Fatal("no ready web pod to delete")
	}
	kn(t, "delete", "pod", webs[0], "--wait=false")
	return webs[0]
}
