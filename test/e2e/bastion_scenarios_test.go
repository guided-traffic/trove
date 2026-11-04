//go:build e2e

package e2e

// The short scenarios of R-BAS-049 (docs/developer/e2e.md#the-bastion-tests), one test per row of the table. They run in the
// order of this file (Go runs the tests of a package in source order), the destructive ones last: the cut of the main
// installation, the interface mismatch and the floods, which block this machine's address at the Bastions.

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
)

// The periods of deploy/dev/bastion-chart-common.yaml.
const (
	basRevalidate   = 90 * time.Second // metadata_revalidate_after and artifact_revalidate_after
	basAnswerPeriod = 30 * time.Second
	basGrace        = 90 * time.Second
)

// TestBastion_R_BAS_049_Setup brings the Bastions up and checks that both have handshaken (R-BAS-010, R-BAS-007).
func TestBastion_R_BAS_049_Setup(t *testing.T) {
	env := basFx(t)
	for _, s := range env.sites() {
		cs := env.contacts(t, s)
		if len(cs) < 2 {
			t.Fatalf("%s: %d contacts", s.reg, len(cs))
		}
		live := map[string]bool{}
		for _, p := range basReadyPods(t, s) {
			live[p.Name] = true
		}
		for _, c := range cs {
			if !live[c.Replica] {
				continue // the last contact of a replica that is gone (the main installation keeps it)
			}
			if c.InterfaceVersion != 1 || c.BaseURL != s.baseURL(t) {
				t.Errorf("%s: replica %s reported interface %d and base URL %q, want 1 and %q", s.reg, c.Replica, c.InterfaceVersion, c.BaseURL, s.baseURL(t))
			}
		}
		for _, p := range basReadyPods(t, s) {
			m := basMetrics(t, p.Name)
			if m["trove_bastion_main_reachable"] != 1 || m["trove_bastion_interface_mismatch"] != 0 {
				t.Errorf("%s reachable %v mismatch %v", p.Name, m["trove_bastion_main_reachable"], m["trove_bastion_interface_mismatch"])
			}
		}
	}
}

// ── cold and warm cache ──────────────────────────────────────────────────────

// basPhase is what one run of a client cost the main installation.
type basPhase struct {
	artifacts, documents []mainReq
}

// TestBastion_R_BAS_049_ColdAndWarmCache: a cold `npm ci`, `pnpm install` and `yarn install` and `dotnet restore` with $FULL
// send every tarball and package to the main installation once; a second run with an empty client cache within the
// revalidation period sends nothing for a tarball, a .nupkg or a document; after it, only conditional requests that
// transfer no artifact (R-BAS-021, R-BAS-022, R-BAS-023, R-BAS-033, R-BAS-040).
func TestBastion_R_BAS_049_ColdAndWarmCache(t *testing.T) {
	env := basFx(t)
	for _, site := range env.sites() {
		t.Run(site.variant, func(t *testing.T) {
			type run struct {
				name    string
				cold    func() basPhase
				warm    func() basPhase
				after   func() basPhase
				artsCnt int
			}
			var runs []run
			measure := func(f func()) basPhase {
				w := openMainWindow(t)
				f()
				a, d := splitKinds(w.done())
				// a client may also ask for a package of its own (pnpm looks up `pnpm`): only the documents of the packages of
				// this run count
				var own []mainReq
				for _, r := range d {
					if strings.Contains(r.Path, env.run) {
						own = append(own, r)
					} else {
						t.Logf("a document request of the client itself: %s %s %d", r.Method, r.Path, r.Status)
					}
				}
				return basPhase{artifacts: a, documents: own}
			}
			for _, fam := range []string{"npm", "pnpm", "yarn4"} {
				set := env.npmSets[fam]
				c := env.npmClient(t, fam, 0, site.variant, env.full.Token)
				dir := env.basNPMProject(t, c, set, "cw-"+site.variant, site)
				install := func() basPhase {
					return measure(func() {
						c.basInstall(t, dir, set)
						if fam == "npm" {
							if r := c.run(dir, "view", set.root, "version"); r.Code != 0 || !strings.Contains(r.Out, "1.0.0") {
								t.Fatalf("npm view through the Bastion exited %d: %s", r.Code, tailLines(r.Out, 10))
							}
						}
					})
				}
				runs = append(runs, run{name: fam, cold: install, warm: install, after: install, artsCnt: set.tarballs()})
			}
			{
				c := env.nugetClient(t, 0, "cw-"+site.variant, env.full.Token)
				env.configureNuGet(t, c, site.reg)
				project := env.nuget.project(c, "consumer")
				restore := func() basPhase { return measure(func() { env.nuget.restore(t, c, project) }) }
				runs = append(runs, run{name: "dotnet", cold: restore, warm: restore, after: restore, artsCnt: len(env.nuget.ids)})
			}

			var hitsBefore float64
			hitCount := func() float64 {
				return basSum(t, site, "trove_bastion_requests_total", `class="artifact"`, `result="hit"`)
			}
			warmDocs := map[string]int{}
			for _, r := range runs {
				t.Run(r.name+" cold then warm", func(t *testing.T) {
					cold := r.cold()
					paths := map[string]int{}
					for _, a := range cold.artifacts {
						paths[a.Path]++
						if a.Status != 200 {
							t.Errorf("cold: %s %s answered %d", a.Method, a.Path, a.Status)
						}
					}
					if len(cold.artifacts) != r.artsCnt || len(paths) != r.artsCnt {
						t.Errorf("cold: %d artifact requests for %d distinct paths, want %d once each:\n%s", len(cold.artifacts), len(paths), r.artsCnt, describeReqs(cold.artifacts))
					}
					if len(cold.documents) == 0 && r.name == "dotnet" {
						t.Errorf("cold: dotnet restore asked the main installation for no document")
					}
					hitsBefore = hitCount()
					warm := r.warm()
					if len(warm.artifacts) != 0 || len(warm.documents) != 0 {
						t.Errorf("warm: %d artifact and %d document requests reached the main installation, want none:\n%s%s",
							len(warm.artifacts), len(warm.documents), describeReqs(warm.artifacts), describeReqs(warm.documents))
					}
					warmDocs[r.name] = len(warm.documents)
					// the Bastion served every artifact from its cache (a warm run that never asked would pass the check above by itself)
					got := hitCount() - hitsBefore
					if r.name == "npm" && got != float64(r.artsCnt) {
						t.Errorf("trove_bastion_requests_total{class=artifact,result=hit} rose by %v on a warm npm ci of %d tarballs, want %d (R-BAS-040)", got, r.artsCnt, r.artsCnt)
					}
					if got < float64(r.artsCnt) {
						t.Errorf("%s: the Bastion served %v artifacts from its cache on the warm run, want %d: the client did not ask for them", r.name, got, r.artsCnt)
					}
				})
			}
			t.Logf("waiting out the revalidation period of %s", basRevalidate)
			time.Sleep(basRevalidate + 5*time.Second)
			for _, r := range runs {
				t.Run(r.name+" after the period", func(t *testing.T) {
					after := r.after()
					if len(after.artifacts) != r.artsCnt {
						t.Errorf("after the period: %d artifact requests, want %d conditional ones (one per cached artifact):\n%s", len(after.artifacts), r.artsCnt, describeReqs(after.artifacts))
					}
					for _, a := range after.artifacts {
						if a.Status != 304 || a.RespBytes != 0 {
							t.Errorf("after the period: %s answered %d with %d bytes, want a 304 that transfers no artifact", a.Path, a.Status, a.RespBytes)
						}
					}
					for _, d := range after.documents {
						if d.Status != 304 && d.Status != 200 {
							t.Errorf("after the period: document %s answered %d", d.Path, d.Status)
						}
					}
				})
			}
		})
	}
}

// TestBastion_R_BAS_049_RoutingByPath (R-BAS-033): with the volume backend and cacheVolume.routeByPath, HAProxy Ingress pins a path to
// one replica, so a second request for any of the cached paths is a hit on the replica that holds it.
func TestBastion_R_BAS_049_RoutingByPath(t *testing.T) {
	env := basFx(t)
	site := env.vol
	pods := basReadyPods(t, site)
	if len(pods) != 2 {
		t.Fatalf("%d replicas of %s", len(pods), site.release)
	}
	// the tarballs of all three npm sets were cached by the cold runs; request each twice more and watch the hits per replica
	var paths []string
	for _, set := range env.npmSets {
		for _, n := range append([]string{set.root}, set.deps...) {
			paths = append(paths, npmDocPath(n)+"/-/"+n+"-1.0.0.tgz")
		}
	}
	before := map[string]float64{}
	for _, p := range pods {
		before[p.Name] = msum(basMetrics(t, p.Name), "trove_bastion_requests_total", `class="artifact"`, `result="hit"`)
	}
	w := openMainWindow(t)
	// from a client pod, with curl: the request of a package manager (the Go client of this machine asks for gzip, which is another variant)
	urls := make([]string, 0, 2*len(paths))
	for range 2 {
		for _, p := range paths {
			urls = append(urls, site.baseURL(t)+p)
		}
	}
	out := npmClientExec(t, 0, append([]string{
		"env", "TOK=" + env.full.Token, "sh", "-c",
		`for u in "$@"; do curl -s -o /dev/null -w '%{http_code}\n' --cacert /etc/trove-ca/ca.crt -H "Authorization: Bearer $TOK" "$u"; done`,
		"sh",
	}, urls...)...)
	for _, code := range strings.Fields(out) {
		if code != "200" {
			t.Fatalf("a tarball request answered %s: %s", code, out)
		}
	}
	reqs, _ := splitKinds(w.done())
	// a path may have been evicted by nothing; every request is a hit on the replica of the path
	if len(reqs) > len(paths) {
		t.Errorf("%d requests for %d paths requested twice reached the main installation (routing by path: at most one per path):\n%s", len(reqs), len(paths), describeReqs(reqs))
	}
	var total float64
	var served int
	for _, p := range pods {
		d := msum(basMetrics(t, p.Name), "trove_bastion_requests_total", `class="artifact"`, `result="hit"`) - before[p.Name]
		total += d
		if d > 0 {
			served++
		}
		t.Logf("%s served %v hits", p.Name, d)
	}
	if served != len(pods) {
		t.Errorf("only %d of %d replicas served paths: the hash does not spread them", served, len(pods))
	}
	if total < float64(len(paths)) {
		t.Errorf("%v hits for %d paths requested twice, want at least %d", total, len(paths), len(paths))
	}
}

// ── read-only ────────────────────────────────────────────────────────────────

var e403 = regexp.MustCompile(`(?i)E403|403`)

// TestBastion_R_BAS_049_ReadOnly (R-BAS-014): `npm publish` and `dotnet nuget push` pointing at a Bastion are refused in the
// client's shape and name the main installation; the upload body is not read.
func TestBastion_R_BAS_049_ReadOnly(t *testing.T) {
	env := basFx(t)
	for _, site := range env.sites() {
		t.Run(site.variant+" npm publish", func(t *testing.T) {
			c := env.npmClient(t, "npm", 1, "ro-"+site.variant, env.pub)
			name := "bas" + env.run + "-ro-" + site.variant
			dir := c.dir("publish")
			c.sh("/work", `rm -rf "`+dir+`" && mkdir -p "`+dir+`"`)
			c.write(dir+"/package.json", `{"name": "`+name+`", "version": "1.0.0", "description": "never stored", "main": "index.js", "license": "MIT"}`+"\n")
			c.write(dir+"/index.js", "module.exports = 1;\n")
			c.sh(dir, "head -c 1500000 /dev/urandom > blob.bin")
			c.configure(dir) // the main installation's snippet first, then the Bastion's over it
			env.configureNPM(t, c, dir, site.reg)
			var before float64
			for _, p := range basReadyPods(t, site) {
				before += msum(basMetrics(t, p.Name), "trove_http_received_bytes_total")
			}
			r := c.run(dir, "publish")
			if r.Code == 0 || !e403.MatchString(r.Out) {
				t.Fatalf("npm publish against the Bastion exited %d, want a failure with E403:\n%s", r.Code, tailLines(r.Out, 20))
			}
			if !strings.Contains(r.Out, cfg.edgeURL) {
				t.Errorf("the refusal does not name the main installation %s:\n%s", cfg.edgeURL, tailLines(r.Out, 20))
			}
			var after float64
			for _, p := range basReadyPods(t, site) {
				after += msum(basMetrics(t, p.Name), "trove_http_received_bytes_total")
			}
			if after-before >= 64*1024 {
				t.Errorf("the Bastion read %v bytes of an upload of 1.5 MB, want less than 64 KiB", after-before)
			}
			if r := env.adm.get("/api/npm/" + basNPMRepo + "/" + name); r.Status != 404 {
				t.Errorf("the refused package exists on the main installation: %s", r)
			}
		})
		t.Run(site.variant+" dotnet nuget push", func(t *testing.T) {
			id := "Bas" + env.run + ".Readonly" + strings.ToUpper(site.variant[:1])
			fx := &nugetFixture{adm: env.adm, run: env.run}
			c := env.nugetClient(t, 1, "ro-"+site.variant, env.pub)
			env.configureNuGet(t, c, site.reg)
			file := c.place(id, "1.0.0", fx.pack(t, 1, id, "1.0.0", "never stored", false).nupkg)
			feed := nugetFeed(t, site.host, basNuGetRepo)
			out, err := c.try("dotnet", "nuget", "push", file, "--source", feed, "--api-key", env.pub)
			if err == nil || pushedRE.MatchString(out) || !strings.Contains(out, "403") {
				t.Fatalf("dotnet nuget push against the Bastion: err %v, want a failure with 403:\n%s", err, tailLines(out, 20))
			}
			if r := env.adm.get("/api/nuget/" + basNuGetRepo + "/v3/flatcontainer/" + strings.ToLower(id) + "/index.json"); r.Status != 404 {
				t.Errorf("the refused package exists on the main installation: %s", r)
			}
		})
	}
}

// ── hidden content ───────────────────────────────────────────────────────────

// narrowPart rewrites the policy of $PART so that it reads packages matching include.
func (e *basEnv) narrowPart(t testing.TB, include ...string) {
	t.Helper()
	body := map[string]any{
		"name": basPartSA, "description": "end-to-end tests",
		"resources": map[string]any{"repositories": []string{basNPMRepo}, "packages": map[string]any{"include": include}},
		"entries":   []map[string]any{{"principal": "sa:" + basPartSA, "actions": []string{"read"}}},
	}
	g := e.adm.get("/api/v1/permission-policies/"+basPartSA).want(t, "read the policy of $PART", 200)
	e.adm.do(http.MethodPut, "/api/v1/permission-policies/"+basPartSA, body, "If-Match", g.Header.Get("ETag")).want(t, "narrow $PART", 200)
}

// TestBastion_R_BAS_049_HiddenContent (R-BAS-012, R-BAS-022, R-BAS-023): what the main installation hides from a caller by a
// permission is hidden at the Bastion, whether the Bastion holds it (cached for $FULL) or not, and a narrowing of the permission
// hides cached content within the revocation bound.
func TestBastion_R_BAS_049_HiddenContent(t *testing.T) {
	env := basFx(t)
	for _, site := range env.sites() {
		t.Run(site.variant, func(t *testing.T) {
			full, part := site.api(t, env.full.Token), site.api(t, env.part.Token)
			tgz := func(n string) string { return npmDocPath(n) + "/-/" + n + "-1.0.0.tgz" }
			// $FULL reads everything and the Bastion keeps it
			for _, n := range []string{env.open, env.hidden} {
				full.get(npmDocPath(n)).want(t, "$FULL reads the document of "+n, 200)
				full.get(tgz(n)).want(t, "$FULL reads the tarball of "+n, 200)
			}
			// the same two by $PART: the open one is served, the hidden one is a 404 as at the main installation, cached or not
			part.get(npmDocPath(env.open)).want(t, "$PART reads the open document", 200)
			part.get(tgz(env.open)).want(t, "$PART reads the open tarball", 200)
			for _, p := range []string{npmDocPath(env.hidden), tgz(env.hidden)} {
				got := part.get(p)
				want := newAPI(t).withToken(env.part.Token).get(p)
				if got.Status != 404 || got.Status != want.Status {
					t.Errorf("$PART at the Bastion for %s: %s; at the main installation: %s; want the same 404", p, got, want)
				}
				if strings.Contains(string(got.Body), "Trove-Bastion") {
					t.Errorf("the refusal carries the marking: %s", got)
				}
			}
			// a hidden package the Bastion never held
			late := "bas" + env.run + "-secret-late-" + site.variant
			env.publishNPM(t, basNPMRepo, late, "1.0.0", nil)
			part.get(npmDocPath(late)).want(t, "$PART asks for a package it may not read and the Bastion does not hold", 404)
			part.get(tgz(late)).want(t, "$PART asks for the tarball of such a package", 404)
			// the permission is narrowed: the open package is hidden from $PART within the revocation bound
			env.narrowPart(t, "bas"+env.run+"-nothing*")
			t.Cleanup(func() { env.narrowPart(t, "bas"+env.run+"-open*") })
			start := time.Now()
			waitFor(t, "the narrowed permission to hide the cached open package", 20*time.Second, 500*time.Millisecond, func() bool {
				return part.get(npmDocPath(env.open)).Status == 404 && part.get(tgz(env.open)).Status == 404
			})
			t.Logf("the narrowing reached %s in %s", site.release, time.Since(start).Round(100*time.Millisecond))
			full.get(tgz(env.open)).want(t, "$FULL still reads it", 200)
		})
	}
}

// ── token revoked ────────────────────────────────────────────────────────────

// TestBastion_R_BAS_049_TokenRevoked (R-BAS-015, R-BAS-026): a client loop is refused within 5 s of the revocation at the main
// installation, on both replicas.
func TestBastion_R_BAS_049_TokenRevoked(t *testing.T) {
	env := basFx(t)
	site := env.bkt
	tok := newSATokenWaiting(t, env.adm, basFullSA, 3600, map[string]any{"inherit": true})
	doc := npmDocPath(env.open)
	c := site.api(t, tok.Token)
	// warm: the Bastion holds the document and the answers of the token
	for range 4 {
		c.get(doc).want(t, "the token reads before the revocation", 200)
	}
	pods := basReadyPods(t, site)
	refusedBefore := map[string]float64{}
	for _, p := range pods {
		refusedBefore[p.Name] = msum(basMetrics(t, p.Name), "trove_bastion_requests_total", `class="refused"`) + msum(basMetrics(t, p.Name), "trove_bastion_refused_total")
	}
	type result struct {
		at     time.Time
		status int
	}
	stop := make(chan struct{})
	done := make(chan []result, 1)
	go func() {
		var rs []result
		a := site.api(t, tok.Token)
		for {
			select {
			case <-stop:
				done <- rs
				return
			default:
			}
			r, err := a.send(http.MethodGet, doc, nil)
			if err == nil {
				rs = append(rs, result{time.Now(), r.Status})
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	time.Sleep(2 * time.Second)
	revoked := time.Now()
	env.adm.do(http.MethodDelete, "/api/v1/tokens/"+tok.ID, nil).want(t, "revoke the token", 204, 200)
	revokedDone := time.Now()
	time.Sleep(8 * time.Second)
	close(stop)
	rs := <-done
	var lastOK, firstRefused time.Time
	for _, r := range rs {
		if r.status == 200 && r.at.After(lastOK) {
			lastOK = r.at
		}
		if r.status == 401 && (firstRefused.IsZero() || r.at.Before(firstRefused)) {
			firstRefused = r.at
		}
		if r.at.After(revokedDone.Add(5*time.Second)) && r.status != 401 {
			t.Errorf("a request %s after the revocation answered %d, want 401", r.at.Sub(revoked).Round(time.Millisecond), r.status)
		}
	}
	if firstRefused.IsZero() {
		t.Fatalf("the loop was never refused (%d requests)", len(rs))
	}
	t.Logf("revocation returned after %s; last 200 at +%s, first 401 at +%s (%d requests)", revokedDone.Sub(revoked).Round(time.Millisecond),
		lastOK.Sub(revoked).Round(time.Millisecond), firstRefused.Sub(revoked).Round(time.Millisecond), len(rs))
	if lastOK.Sub(revokedDone) > 5*time.Second {
		t.Errorf("the token was served %s after the revocation returned, want at most 5 s", lastOK.Sub(revokedDone).Round(time.Millisecond))
	}
	// both replicas took part: each served requests of the loop, each refused some afterwards
	for _, p := range pods {
		m := basMetrics(t, p.Name)
		now := msum(m, "trove_bastion_requests_total", `class="refused"`) + msum(m, "trove_bastion_refused_total")
		if now <= refusedBefore[p.Name] {
			t.Errorf("replica %s refused nothing after the revocation: the loop did not reach it", p.Name)
		}
	}
}

// ── failed-authentication timeout ────────────────────────────────────────────

// TestBastion_R_BAS_049_FailedAuthenticationTimeout (R-BAS-036): 21 random well-formed tokens put the address into the timeout;
// a known token from it keeps working.
func TestBastion_R_BAS_049_FailedAuthenticationTimeout(t *testing.T) {
	env := basFx(t)
	site := env.bkt
	doc := npmDocPath(env.open)
	// from a client pod: its address is not the one of the token loop of the scenario before, whose failures count too
	pod := func(tok string) (int, string, string) {
		out := npmClientExec(t, 1, "curl", "-sS", "-i", "--cacert", "/etc/trove-ca/ca.crt", "-H", "Authorization: Bearer "+tok, site.baseURL(t)+doc)
		status, _ := strconv.Atoi(strings.Fields(strings.SplitN(out, "\n", 2)[0] + " 0")[1])
		var code string
		if k := strings.Index(out, `"code":"`); k >= 0 {
			code = strings.SplitN(out[k+8:], `"`, 2)[0]
		}
		return status, code, out
	}
	if st, _, out := pod(env.full.Token); st != 200 {
		t.Fatalf("$FULL is known before the timeout: %d %s", st, out)
	}
	codes := map[int]int{}
	var timeoutAt int
	for i := 1; i <= 21; i++ {
		tk, err := token.Generate(token.TypePAT)
		if err != nil {
			t.Fatal(err)
		}
		st, code, out := pod(tk)
		codes[st]++
		if st == 429 && timeoutAt == 0 {
			timeoutAt = i
			if code != "auth_timeout" || !strings.Contains(strings.ToLower(out), "retry-after") {
				t.Errorf("token %d: %d %s, want 429 auth_timeout with Retry-After", i, st, code)
			}
		}
	}
	t.Logf("statuses of 21 random tokens: %v, the timeout began with token %d", codes, timeoutAt)
	if codes[401] != 20 || timeoutAt != 21 {
		t.Errorf("want 20 refusals by the main installation and the 21st token in the timeout, got %v (timeout at %d)", codes, timeoutAt)
	}
	// later unknown tokens are refused without a request to the main installation
	w := openMainWindow(t)
	for range 5 {
		tk, _ := token.Generate(token.TypePAT)
		if st, code, _ := pod(tk); st != 429 || code != "auth_timeout" {
			t.Errorf("a later unknown token: %d %s, want 429 auth_timeout", st, code)
		}
	}
	for _, r := range fromBastion(t, mainLog(t), w.before) {
		if strings.HasPrefix(r.Path, "/api/v1/bastion/check") {
			t.Errorf("a token in the timeout was checked at the main installation: %s %s", r.Method, r.Path)
		}
	}
	if st, _, out := pod(env.full.Token); st != 200 {
		t.Errorf("$FULL from the address in the timeout: %d %s, want 200", st, out)
	}
}

// ── network policy and chart ─────────────────────────────────────────────────

// TestBastion_R_BAS_049_NetworkPolicy (R-BAS-047): from a Bastion pod, the connection to the PostgreSQL Service of the main
// installation and to an external registry is refused by the policy, while the Bastion works.
func TestBastion_R_BAS_049_NetworkPolicy(t *testing.T) {
	env := basFx(t)
	site := env.bkt
	pod := basReadyPods(t, site)[0]
	probe := basDebugContainer(t, pod.Name)
	check := func(host string, port string, wantOpen bool, what string) {
		t.Helper()
		out, _ := kbTry("exec", pod.Name, "-c", probe, "--", "node", "-e",
			`const s=require('net').connect({host:process.argv[1],port:+process.argv[2],timeout:4000});
s.on('connect',()=>{console.log('open');process.exit(0)});
s.on('timeout',()=>{console.log('timeout');process.exit(0)});
s.on('error',e=>{console.log('error '+e.code);process.exit(0)});`, host, port)
		got := strings.TrimSpace(out)
		if (got == "open") != wantOpen {
			t.Errorf("%s: connection to %s:%s from the Bastion pod: %q, want open=%v", what, host, port, got, wantOpen)
		}
	}
	check("trove-api.trove.svc", "8080", true, "the main installation's api Service is allowed")
	check("trove-packages.trove.svc", "8080", true, "the main installation's packages Service is allowed")
	check("trove-db-rw.trove.svc", "5432", false, "the PostgreSQL Service of the main installation")
	check("trove-db-rw.trove.svc", "5432", false, "the PostgreSQL Service of the main installation, again")
	check("trove-s3.trove.svc", "9000", true, "the cache bucket is allowed with the bucket backend")
	check("trove-keycloak.trove.svc", "8443", false, "another service of the main namespace")
	check("registry.npmjs.org", "443", false, "an external registry")
	// the same two targets are reachable from outside the policy (a client pod), so the refusal is the policy's
	out := npmClientExec(t, 0, "node", "-e", `const s=require('net').connect({host:process.argv[1],port:+process.argv[2],timeout:6000});
s.on('connect',()=>{console.log('open');process.exit(0)});s.on('timeout',()=>{console.log('timeout');process.exit(0)});s.on('error',e=>{console.log('error '+e.code);process.exit(0)});`, "trove-db-rw.trove.svc", "5432")
	if strings.TrimSpace(out) != "open" {
		t.Errorf("the PostgreSQL Service is not reachable from a client pod either (%q): the check proves nothing", strings.TrimSpace(out))
	}
	out = npmClientExec(t, 0, "node", "-e", `const s=require('net').connect({host:process.argv[1],port:+process.argv[2],timeout:6000});
s.on('connect',()=>{console.log('open');process.exit(0)});s.on('timeout',()=>{console.log('timeout');process.exit(0)});s.on('error',e=>{console.log('error '+e.code);process.exit(0)});`, "registry.npmjs.org", "443")
	if strings.TrimSpace(out) != "open" {
		t.Errorf("registry.npmjs.org:443 is not reachable from a client pod (%q): the cluster has no internet access, so the policy's refusal cannot be told from it", strings.TrimSpace(out))
	}
	// and the Bastion itself keeps working
	site.get(t, env.full.Token, npmDocPath(env.open)).want(t, "the Bastion serves while the policy is in force", 200)
}

// basDebugContainer adds an ephemeral container with a shell-less Node.js (the clients image) to the pod, so a connection can
// be made from inside the pod's network identity, and returns its name.
func basDebugContainer(t testing.TB, podName string) string {
	t.Helper()
	const name = "e2e-probe"
	if out, err := kbTry("get", "pod", podName, "-o", "jsonpath={.spec.ephemeralContainers[*].name}"); err == nil && strings.Contains(out, name) {
		return name
	}
	custom := `{"imagePullPolicy":"IfNotPresent","securityContext":{"runAsNonRoot":true,"runAsUser":1000,"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"seccompProfile":{"type":"RuntimeDefault"}}}`
	path := t.TempDir() + "/custom.json"
	if err := writeFile(path, custom); err != nil {
		t.Fatal(err)
	}
	if out, err := kbTry("debug", podName, "--image=trove-e2e-clients:dev", "-c", name, "--profile=restricted", "--custom="+path, "--", "sleep", "3600"); err != nil {
		t.Fatalf("kubectl debug: %v\n%s", err, out)
	}
	waitFor(t, "the probe container", 90*time.Second, 2*time.Second, func() bool {
		out, _ := kbTry("get", "pod", podName, "-o", "jsonpath={.status.ephemeralContainerStatuses[?(@.name=='"+name+"')].state.running.startedAt}")
		return strings.TrimSpace(out) != ""
	})
	return name
}
