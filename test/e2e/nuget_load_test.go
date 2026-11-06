//go:build e2e

package e2e

// The NuGet part of the load of R-HA-052 and R-HA-059 (Q-NUGET-73, Q-HA-100), which the drain and failover scenarios of
// nuget_ha_test.go run under next to the npm part (npm_load_test.go):
//
//   - dotnet restore loops (R-HA-059: four) of a project of 61 packages through the virtual repository nuget-ha-all,
//     whose members are nuget-ha-local (three packages of this run) and nuget-ha-remote (the corpus of the counting NuGet
//     test upstream), with trove's cache warm and an empty global packages folder per iteration
//     (`dotnet restore --no-cache --force` with a new NUGET_PACKAGES);
//   - a dotnet nuget push of a new version to nuget-ha-local every 10 s;
//   - the k6 NuGet requests of k6/nuget.js through the edge, the 20 % of R-HA-059's 150 requests/s.
//
// dotnet runs with its default retries. The loops run detached inside the client pods and write one record per command, as
// the npm loops do, so a kubectl exec that breaks during a fault is never counted as a failed client command, and the output
// of a failed command is kept. docs/developer/e2e.md#drain-and-failover-under-nuget-load describes the whole.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/nugetstub"
)

const (
	nugetHASA     = "e2e-nuget-ha"
	nugetHALocal  = "nuget-ha-local"
	nugetHARemote = "nuget-ha-remote"
	nugetHAAll    = "nuget-ha-all"
	// haRestoreLoops is R-HA-059's four parallel dotnet restore loops.
	haRestoreLoops = 4
	// haMinPackages is R-HA-059's "60 packages": the fixture's restore graph, the three local packages included, has at least
	// this many.
	haMinPackages = 60
	// nugetK6Rate is the NuGet share of R-HA-059's 150 requests/s: its 20 %. npm's 120 are haK6Rate.
	nugetK6Rate = 30
	// haPushPackages is the number of versions the push loop has ready: one every 10 s for an hour.
	haPushPackages = 360
)

// nugetHAFixture is what every scenario's NuGet load uses: the tokens, the restore project and the input of k6/nuget.js.
type nugetHAFixture struct {
	fx *nugetFixture
	// tokens: two per restore loop, then nugetHAK6Tokens of k6's, then the publisher's. As for npm (npmHAFixture) a loop
	// alternates its two tokens and k6 spreads its requests over its own, so the package_read budget of R-API-016 is not the
	// thing the load measures.
	tokens   []string
	csproj   []byte
	locals   []string // the IDs of the three local packages of this run, version 1.0.0
	packages int      // what one restore extracts
	closure  []string // the lowercase IDs of the remote packages the project reaches
	k6Input  []byte
}

const nugetHAK6Tokens = 3

func (h *nugetHAFixture) k6Tokens() []string {
	return h.tokens[2*haRestoreLoops : 2*haRestoreLoops+nugetHAK6Tokens]
}

func (h *nugetHAFixture) publisherToken() string { return h.tokens[2*haRestoreLoops+nugetHAK6Tokens] }

func (h *nugetHAFixture) feed(t testing.TB, key string) string { return nugetFeed(t, "localhost", key) }

var (
	nugetHAFxOnce sync.Once
	nugetHAFxVal  *nugetHAFixture
	nugetHAFxErr  error
)

// nugetHAFx builds the fixture once per process, in the scatter profile of the NuGet tests.
func nugetHAFx(t *testing.T) *nugetHAFixture {
	t.Helper()
	fx := nugetFx(t)
	nugetHAFxOnce.Do(func() {
		nugetHAFxErr = fmt.Errorf("the fixture did not finish (see the first failure)")
		nugetHAFxVal = buildNuGetHAFixture(t, fx)
		nugetHAFxErr = nil
	})
	if nugetHAFxErr != nil {
		t.Fatalf("NuGet HA fixture: %v", nugetHAFxErr)
	}
	return nugetHAFxVal
}

// haRoots picks the root of the restore project from the corpus of the test upstream: the first generated package, in the order
// of the corpus, whose dependency closure and the local packages reach haMinPackages. The corpus chains its packages (each
// depends on a few of the nine before it), so one root reaches a closure of about the size asked for; a stride over several
// roots, as ReferenceRoots takes for E3.2, overshoots (the last generated package alone reaches 174).
func haRoots(local int) (roots, closure []string) {
	c := nugetstub.Default()
	for _, k := range c.IDs() {
		p, _ := c.Get(k)
		if !strings.HasPrefix(k, "contoso.") || k == strings.ToLower(nugetstub.ManyVersionsID) || k == strings.ToLower(nugetstub.UtilID) {
			continue
		}
		if closure = c.Closure(p.ID); len(closure)+local >= haMinPackages {
			return []string{p.ID}, closure
		}
	}
	panic("the corpus of the test upstream has no package whose closure reaches the size of the restore project")
}

func buildNuGetHAFixture(t *testing.T, fx *nugetFixture) *nugetHAFixture {
	start := time.Now()
	h := &nugetHAFixture{fx: fx}
	adm := fx.adm
	apiHost, contentHost := nugetStubHosts(t)
	for _, body := range []map[string]any{
		{"key": nugetHALocal, "class": "local", "format": "nuget", "requireAuthentication": true},
		{
			"key": nugetHARemote, "class": "remote", "format": "nuget", "requireAuthentication": true,
			"remote": map[string]any{"url": "https://" + apiHost + "/v3/index.json", "extraHosts": []map[string]any{{"pattern": contentHost, "sendCredentials": false}}},
		},
		{
			"key": nugetHAAll, "class": "virtual", "format": "nuget", "requireAuthentication": true,
			"virtual": map[string]any{"members": []string{nugetHALocal, nugetHARemote}, "deployTarget": nugetHALocal},
		},
	} {
		r, err := adm.send(http.MethodPost, reposRoute, body)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != 201 && r.Status != 409 {
			t.Fatalf("create %v: %s", body["key"], r)
		}
	}
	ensureSA(t, adm, nugetHASA)
	ensurePolicy(t, adm, nugetHASA, []string{nugetHALocal}, [2]any{"sa:" + nugetHASA, []string{"read", "deploy", "delete"}})
	ensurePolicy(t, adm, nugetHASA+"-read", []string{nugetHARemote, nugetHAAll}, [2]any{"sa:" + nugetHASA, []string{"read"}})
	h.tokens = pacedSATokens(t, adm, nugetHASA, 2*haRestoreLoops+nugetHAK6Tokens+1, 6*3600)
	nugetReadableOnEveryReplica(t, h.tokens[0], nugetHALocal, nugetHARemote, nugetHAAll)

	// the three local packages of the project
	pub := fx.client(t, 0, "ha-setup")
	pub.token = h.publisherToken()
	pub.config(h.feed(t, nugetHALocal), true)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("E2e.NugetHa%s.Lib%d", fx.run, i)
		file := pub.place(id, "1.0.0", nugetstub.Nupkg(&nugetstub.Package{ID: id, Description: "trove HA load " + id}, nugetstub.Version{Version: "1.0.0"}))
		pub.mustPush("push of "+id, "dotnet", "nuget", "push", file, "--source", h.feed(t, nugetHALocal), "--api-key", "az")
		h.locals = append(h.locals, id)
	}

	// the project: the corpus roots and the local packages
	roots, closure := haRoots(len(h.locals))
	h.closure = closure
	var refs strings.Builder
	for _, id := range append(append([]string{}, roots...), h.locals...) {
		refs.WriteString(`    <PackageReference Include="` + id + `" Version="1.0.0" />` + "\n")
	}
	h.csproj = []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net10.0</TargetFramework><NuGetAudit>false</NuGetAudit></PropertyGroup>
  <ItemGroup>
` + refs.String() + `  </ItemGroup>
</Project>
`)

	// one restore through nuget-all fills trove's cache: the loops find it warm
	warm := fx.client(t, 0, "ha-warm")
	warm.token = h.tokens[0]
	warm.config(h.feed(t, nugetHAAll), true)
	warm.write("fixture.csproj", h.csproj)
	warm.exec("dotnet", "restore", "fixture.csproj", "--no-cache", "--force")
	h.packages = warm.restoredPackages()
	if want := len(closure) + len(h.locals); h.packages != want || h.packages < haMinPackages {
		t.Fatalf("the fixture's restore extracts %d packages, want %d (at least %d)", h.packages, want, haMinPackages)
	}

	// k6's requests: the documents and packages of the local packages and of the first of the project's remote ones. The
	// weak ETag of a rendered document is the same on every replica (R-NUGET-030): the test reads it once.
	reader := adm.withToken(h.tokens[0])
	in := struct {
		Registration []string          `json:"registration"`
		Flat         []string          `json:"flat"`
		ETags        map[string]string `json:"etags"`
		Nupkgs       []map[string]any  `json:"nupkgs"`
	}{ETags: map[string]string{}}
	base := nugetAddr(nugetHAAll) + "/v3/"
	ids := append(append([]string{}, h.locals...), closure[:min(17, len(closure))]...)
	for i, id := range ids {
		lower := strings.ToLower(id)
		for _, doc := range []struct {
			path string
			list *[]string
		}{{base + "registration/" + lower + "/index.json", &in.Registration}, {base + "flatcontainer/" + lower + "/index.json", &in.Flat}} {
			r := reader.get(doc.path).want(t, "k6 document "+doc.path, 200)
			*doc.list = append(*doc.list, doc.path)
			in.ETags[doc.path] = r.Header.Get("ETag")
		}
		if i < 3 || i%2 == 0 {
			path := base + "flatcontainer/" + lower + "/1.0.0/" + lower + ".1.0.0.nupkg"
			r := reader.get(path).want(t, "k6 package "+path, 200)
			sum := sha512.Sum512(r.Body)
			in.Nupkgs = append(in.Nupkgs, map[string]any{"path": path, "size": len(r.Body), "integrity": base64.StdEncoding.EncodeToString(sum[:])})
		}
	}
	h.k6Input, _ = json.Marshal(in)
	t.Logf("NuGet HA fixture: %d packages per restore (%d from nuget-ha-local, %d remote root), %d k6 documents of each kind and %d packages, %d tokens, built in %s",
		h.packages, len(h.locals), len(roots), len(in.Registration), len(in.Nupkgs), len(h.tokens), time.Since(start).Round(time.Second))
	return h
}

// ── the loops ────────────────────────────────────────────────────────────────

// restoreLoopScript is one dotnet restore loop: the project of the fixture with an empty global packages folder and no HTTP
// cache, again and again until the file stop appears, on the loop's two tokens in turn. Each command writes
// `R restore <start ms> <end ms> <exit status> <n>`; the output of a failed one stays. dotnet exits 0 on some failures
// (docs/developer/e2e.md#the-nuget-clients), so a restore that exits 0 but extracted other than $EXPECT packages counts as
// failed (exit status 96).
const restoreLoopScript = `#!/bin/sh
mkdir -p out iter
first="$TROVE_TOKEN"
n=0
while [ ! -e stop ]; do
  n=$((n + 1))
  if [ $((n % 2)) -eq 0 ]; then TROVE_TOKEN="$TROVE_TOKEN_ALT"; else TROVE_TOKEN="$first"; fi
  export TROVE_TOKEN
  d="$(mktemp -d -p "$PWD/iter")"
  start="$(date +%s%3N)"
  NUGET_PACKAGES="$d/packages" NUGET_HTTP_CACHE_PATH="$d/http" NUGET_PLUGINS_CACHE_PATH="$d/plugins" \
    dotnet restore fixture.csproj --no-cache --force >"out/$n.log" 2>&1
  rc=$?
  if [ "$rc" -eq 0 ]; then
    got="$(find "$d/packages" -name '*.nupkg' | wc -l)"
    if [ "$got" -ne "$EXPECT" ]; then rc=96; echo "the restore extracted $got packages, want $EXPECT" >>"out/$n.log"; fi
  fi
  end="$(date +%s%3N)"
  rm -rf "$d" obj
  [ "$rc" -eq 0 ] && rm -f "out/$n.log"
  echo "R restore $start $end $rc $n" >>records
done
echo finished >finished
`

// pushLoopScript pushes version 1.0.<n> of $PKGID to $FEED every 10 s, counted from the start of the previous push, from the
// archives pkgs/<id>.1.0.<n>.nupkg the test put there: `R push <start ms> <end ms> <exit status> <n> <version>`. dotnet
// answers "Your package was pushed." on success; an exit status 0 without it counts as failed (97), a missing archive as 95.
const pushLoopScript = `#!/bin/sh
mkdir -p out
n=0
while [ ! -e stop ]; do
  n=$((n + 1))
  v="1.0.$n"
  f="pkgs/$PKGID.$v.nupkg"
  start="$(date +%s%3N)"
  if [ -f "$f" ]; then
    dotnet nuget push "$f" --source "$FEED" --api-key az >"out/$n.log" 2>&1
    rc=$?
    if [ "$rc" -eq 0 ] && ! grep -qi 'your package was pushed' "out/$n.log"; then rc=97; fi
  else
    rc=95; echo "no archive $f" >"out/$n.log"
  fi
  end="$(date +%s%3N)"
  [ "$rc" -eq 0 ] && rm -f "out/$n.log"
  echo "R push $start $end $rc $n $v" >>records
  while [ ! -e stop ] && [ $(( $(date +%s%3N) - start )) -lt 10000 ]; do sleep 0.2; done
done
echo finished >finished
`

type nugetLoadOpts struct {
	restoreLoops int  // dotnet restore loops; R-HA-059: four
	push         bool // a dotnet nuget push of a new version to nuget-ha-local every 10 s
	k6           bool // the k6 NuGet requests at nugetK6Rate
}

// nugetFullLoad is the NuGet part of R-HA-059's load.
func nugetFullLoad() nugetLoadOpts {
	return nugetLoadOpts{restoreLoops: haRestoreLoops, push: true, k6: true}
}

type nugetLoop struct {
	name string
	c    *nugetClient
}

type nugetLoad struct {
	name    string
	h       *nugetHAFixture
	loops   []*nugetLoop // the restore loops, then the push loop
	pubPkg  string
	hashes  map[string]string // version -> the base64 sha512 of the archive the push loop pushes
	k6      *k6run
	started time.Time
}

// nugetClientStdin runs a command in clients pod i with the bytes on its standard input.
func nugetClientStdin(t testing.TB, i int, stdin []byte, argv ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	args := append([]string{"--context", cfg.kubeContext, "-n", cfg.namespace, "exec", "-i", nugetClientPod(t, i), "--"}, argv...)
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdin = bytes.NewReader(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", argv, err, out)
	}
}

// pushArchives builds the archives the push loop pushes (one per version, all of one package ID) as a gzip-compressed tar,
// and the hash of each as the restore later reports it.
func pushArchives(id string) ([]byte, map[string]string) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hashes := map[string]string{}
	for n := 1; n <= haPushPackages; n++ {
		v := fmt.Sprintf("1.0.%d", n)
		pkg := nugetstub.Nupkg(&nugetstub.Package{ID: id, Description: "trove HA load"}, nugetstub.Version{Version: v})
		_ = tw.WriteHeader(&tar.Header{Name: id + "." + v + ".nupkg", Mode: 0o644, Size: int64(len(pkg))})
		_, _ = tw.Write(pkg)
		hashes[v] = nugetstub.PackageHash(pkg)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes(), hashes
}

// startNuGetLoad starts the loops in the client pods (spread over them) and k6, and returns once each has begun.
func startNuGetLoad(t *testing.T, h *nugetHAFixture, name string, o nugetLoadOpts) *nugetLoad {
	t.Helper()
	l := &nugetLoad{name: name, h: h, pubPkg: fmt.Sprintf("E2e.NugetHa%s.%s.Pub", h.fx.run, name)}
	pods := nugetClientCount(t)
	loopEnv := func(c *nugetClient, extra ...string) []string {
		return append([]string{"TROVE_TOKEN=" + c.token}, extra...)
	}
	var envs [][]string
	for i := 0; i < o.restoreLoops; i++ {
		c := h.fx.client(t, i%pods, fmt.Sprintf("ha-%s-restore%d", name, i))
		c.token = h.tokens[2*i]
		c.exec("sh", "-c", `rm -rf "$1" && mkdir -p "$1"`, "sh", c.dir())
		c.config(h.feed(t, nugetHAAll), true)
		c.write("fixture.csproj", h.csproj)
		c.write("loop.sh", []byte(restoreLoopScript))
		l.loops = append(l.loops, &nugetLoop{name: fmt.Sprintf("restore%d", i), c: c})
		envs = append(envs, loopEnv(c, "TROVE_TOKEN_ALT="+h.tokens[2*i+1], "EXPECT="+strconv.Itoa(h.packages)))
	}
	if o.push {
		c := h.fx.client(t, 0, fmt.Sprintf("ha-%s-push", name))
		c.token = h.publisherToken()
		c.exec("sh", "-c", `rm -rf "$1" && mkdir -p "$1/pkgs"`, "sh", c.dir())
		c.config(h.feed(t, nugetHALocal), true)
		c.write("loop.sh", []byte(pushLoopScript))
		var tarball []byte
		tarball, l.hashes = pushArchives(l.pubPkg)
		nugetClientStdin(t, c.pod, tarball, "tar", "-xzf", "-", "-C", c.dir()+"/pkgs")
		l.loops = append(l.loops, &nugetLoop{name: "push", c: c})
		envs = append(envs, loopEnv(c, "PKGID="+l.pubPkg, "FEED="+h.feed(t, nugetHALocal)))
	}
	l.started = time.Now()
	for i, lp := range l.loops {
		// setsid and the redirections detach the loop from the exec session, which ends at once
		args := append([]string{"env", "-C", lp.c.dir()}, envs[i]...)
		args = append(args, "sh", "-c", "setsid nohup sh ./loop.sh </dev/null >loop.log 2>&1 &")
		nugetClientExec(t, lp.c.pod, args...)
	}
	if o.k6 {
		l.k6 = startK6Script(t, "nuget-"+name, "nuget.js", time.Hour, k6Opts{rate: nugetK6Rate, prefix: "G ", token: strings.Join(h.k6Tokens(), "\n"), files: map[string][]byte{"nuget-fixture.json": h.k6Input}})
	}
	t.Cleanup(func() {
		// a test that fails before stop leaves no loop running into the next scenario
		for _, lp := range l.loops {
			touchStop(nugetClientPod(t, lp.c.pod), lp.c.dir()+"/stop")
		}
	})
	t.Logf("NuGet load %s: %d dotnet restore loops, push %v, k6 %v", name, o.restoreLoops, o.push, o.k6)
	return l
}

type nugetLoadResult struct {
	Name      string
	Commands  []npmCommand // the same record as an npm command: Kind is restore or push
	Published []string     // the versions whose push exited 0
	PubPkg    string
	Hashes    map[string]string
	K6        *npmK6Result
}

var (
	nugetRecordLine = regexp.MustCompile(`^R (restore|push) (\d+) (\d+) (\d+) (\d+)(?: (\S+))?$`)
	nugetK6Pattern  = regexp.MustCompile(`^G (\d+) (\d+) (\S+) (\d+) (registration|flat|nupkg) (ok|bad) (\S+)$`)
)

// stop ends k6, lets every loop finish the command it is running, and collects the records.
func (l *nugetLoad) stop(t *testing.T) nugetLoadResult {
	t.Helper()
	res := nugetLoadResult{Name: l.name, PubPkg: l.pubPkg, Hashes: l.hashes}
	if l.k6 != nil {
		k := parseK6Lines(t, l.k6, nugetK6Pattern)
		res.K6 = &k
	}
	for _, lp := range l.loops {
		nugetClientExec(t, lp.c.pod, "touch", lp.c.dir()+"/stop")
	}
	for _, lp := range l.loops {
		// a restore that retries waits between attempts, and a hung request ends at the client's timeout of 100 s
		waitFor(t, "the "+lp.name+" loop of "+l.name+" to finish its command", 10*time.Minute, 2*time.Second, func() bool {
			_, err := nugetClientTry(t, lp.c.pod, "test", "-e", lp.c.dir()+"/finished")
			return err == nil
		})
		records := nugetClientExec(t, lp.c.pod, "cat", lp.c.dir()+"/records")
		sc := bufio.NewScanner(strings.NewReader(records))
		for sc.Scan() {
			m := nugetRecordLine.FindStringSubmatch(sc.Text())
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
				out, _ := nugetClientTry(t, lp.c.pod, "cat", fmt.Sprintf("%s/out/%d.log", lp.c.dir(), n))
				cmd.Log = tailLines(out, 30)
			} else if cmd.Kind == "push" {
				res.Published = append(res.Published, cmd.Version)
			}
			res.Commands = append(res.Commands, cmd)
		}
	}
	noteResult(t, "nuget-load-"+l.name, res)
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
	t.Logf("NuGet load %s: commands (count, failed) %v, slowest %s, %d pushes acknowledged", l.name, byKind, slowest.Round(100*time.Millisecond), len(res.Published))
	return res
}

// allExitZero is R-HA-059's and R-HA-051's client condition: every dotnet command exits 0 with dotnet's default retries.
func (r nugetLoadResult) allExitZero(t *testing.T) {
	t.Helper()
	if len(r.Commands) == 0 {
		t.Errorf("the NuGet load %s ran no command", r.Name)
	}
	for _, c := range r.Commands {
		if c.Code != 0 {
			t.Errorf("dotnet %s (%s, command %d %s) exited %d after %s, started %s:\n%s", c.Kind, c.Loop, c.N, c.Version, c.Code,
				c.End.Sub(c.Start).Round(100*time.Millisecond), c.Start.Format("15:04:05.000"), c.Log)
		}
	}
}

// loopsSpan requires that every dotnet restore loop ran a command that overlapped [from, to]; as for npm, the push loop's
// overlap is reported only (a fault of a second may fall between two pushes).
func (r nugetLoadResult) loopsSpan(t *testing.T, from, to time.Time) {
	t.Helper()
	overlap := map[string]int{}
	loops := map[string]bool{}
	for _, c := range r.Commands {
		if c.Kind == "restore" {
			loops[c.Loop] = true
		}
		if c.Start.Before(to) && c.End.After(from) {
			overlap[c.Loop]++
		}
	}
	if len(loops) == 0 {
		t.Errorf("the NuGet load %s ran no dotnet restore command", r.Name)
	}
	for loop := range loops {
		if overlap[loop] == 0 {
			t.Errorf("the %s loop ran no command between %s and %s: the fault did not meet it", loop, from.Format("15:04:05"), to.Format("15:04:05"))
		}
	}
	t.Logf("NuGet commands per loop that overlapped %s to %s: %v", from.Format("15:04:05"), to.Format("15:04:05"), overlap)
}

// count says how many commands of a kind the load ran.
func (r nugetLoadResult) count(kind string) int {
	n := 0
	for _, c := range r.Commands {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// k6Holds is the k6 condition of R-HA-059 for the NuGet requests: only 2xx and 304, no connection error, no wrong package, the
// arrival rate held.
func (r nugetLoadResult) k6Holds(t *testing.T) {
	t.Helper()
	r.K6.holds(t, nugetK6Rate, "registration 200", "registration 304", "flat 200", "flat 304", "nupkg 200")
}

// installPublished is R-HA-059's "every acknowledged publish installs afterwards" for NuGet: one dotnet restore through
// nuget-ha-all of every acknowledged version (a PackageDownload of the exact version each), and every extracted archive has
// the hash of the bytes the loop pushed.
func (r nugetLoadResult) installPublished(t *testing.T, h *nugetHAFixture) {
	t.Helper()
	if len(r.Published) == 0 {
		t.Errorf("the NuGet load %s acknowledged no push", r.Name)
		return
	}
	c := h.fx.client(t, 0, fmt.Sprintf("ha-%s-verify", r.Name))
	c.token = h.tokens[0]
	c.exec("sh", "-c", `rm -rf "$1" && mkdir -p "$1"`, "sh", c.dir())
	c.config(h.feed(t, nugetHAAll), true)
	// one item with every version: NuGet reads the versions of a PackageDownload from a semicolon-separated Version, and of
	// several items of one ID it restores the first only
	exact := make([]string, len(r.Published))
	for i, v := range r.Published {
		exact[i] = "[" + v + "]"
	}
	items := `    <PackageDownload Include="` + r.PubPkg + `" Version="` + strings.Join(exact, ";") + `" />` + "\n"
	c.write("verify.csproj", []byte(`<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net10.0</TargetFramework><NuGetAudit>false</NuGetAudit></PropertyGroup>
  <ItemGroup>
`+items+`  </ItemGroup>
</Project>
`))
	c.exec("dotnet", "restore", "verify.csproj", "--no-cache", "--force")
	lower := strings.ToLower(r.PubPkg)
	var bad []string
	for _, v := range r.Published {
		got, err := c.try("cat", c.packages()+"/"+lower+"/"+v+"/"+lower+"."+v+".nupkg.sha512")
		if err != nil || strings.TrimSpace(got) != r.Hashes[v] {
			bad = append(bad, fmt.Sprintf("%s: %q (%v), want %s", v, strings.TrimSpace(got), err, r.Hashes[v]))
		}
	}
	if len(bad) != 0 {
		t.Errorf("acknowledged pushes of %s that do not install with their content: %s", r.PubPkg, strings.Join(bad, "; "))
	}
	t.Logf("%d acknowledged pushes of %s installed with their content", len(r.Published), r.PubPkg)
}
