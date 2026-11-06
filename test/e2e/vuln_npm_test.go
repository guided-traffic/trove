//go:build e2e

package e2e

// The npm tests of the vulnerability scanning (R-VULN-029, E4b.4, E4b.5): `npm audit`, `pnpm audit` and `yarn npm audit`
// through npm-local, npm-remote and npm-all from the gate rows of R-NPM-045, a blocked install (malicious, and in triage)
// from every row, and the audit endpoint's body checked on the wire. The rows run side by side in the client pods, in
// phases, so that the counters of the three upstreams can be read over the audits alone.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The reasons a row's audit is not applicable (R-NPM-045, Q-NPM-65): the matrix's own, so the two reports say the same.
const (
	vulnNAPnpm10 = "pnpm 10 speaks only npm 6's audit endpoints (audits/quick, audits), which trove answers with 501 and does not translate (Q-NPM-65)"
	vulnNAYarn1  = "yarn audit sends its request to registry.yarnpkg.com whatever the .npmrc says (verified with --verbose), so it never reaches the registry under test (Q-NPM-65)"
)

func vulnAuditNA(row string) string {
	switch row {
	case "yarn1":
		return vulnNAYarn1
	case "pnpm10":
		return vulnNAPnpm10
	}
	return ""
}

// eachRow runs f for the eight gate rows side by side as the subtests of `scenario`, which ends when every row has; the outcome
// of each is recorded in the matrix report of the run.
func (e *vulnEnv) eachRow(t *testing.T, run, scenario string, f func(t *testing.T, c *npmClient)) {
	t.Helper()
	pods := npmClientCount(t)
	t.Run(scenario, func(t *testing.T) {
		for i, row := range npmGateRows {
			t.Run(row, func(t *testing.T) {
				t.Parallel()
				defer func() { npmRep.record(run, row, scenario, t) }()
				c := newNPMClient(t, e.npm, row, i%pods)
				f(t, c)
			})
		}
	})
}

// audit runs the row's audit command in dir; Yarn 4 audits through `yarn npm audit`.
func (c *npmClient) audit(dir string) npmRun {
	c.t.Helper()
	if c.family() == "yarn4" {
		return c.run(dir, "npm", "audit")
	}
	return c.run(dir, "audit")
}

// seedAuditContent makes the packages the audits read: trove-test-alias@2.0.0 in npm-local (published once per cluster:
// npm-local says always, so it is matched), and @acme/ui in npm-internal, whose claim makes it an internal name.
func (e *vulnEnv) seedAuditContent(t *testing.T) {
	t.Helper()
	c := newNPMClient(t, e.npm, "npm11", 0)
	if r := e.npm.registryDoc(t, npmLocalKey, vulnNPMAlias); r.Status != 200 {
		c.publish(vulnNPMAlias, "2.0.0")
	}
	c.registryKey = vulnNPMInternal
	if r := e.npm.registryDoc(t, vulnNPMInternal, vulnAcme); r.Status != 200 {
		c.publish(vulnAcme, "1.0.0")
	}
}

// TestVuln_R_VULN_017_NPMAuditThroughEveryRepository is E4b.5 for npm: every audit-capable gate row installs a package of
// the public registry (cached in npm-remote through npm-all) and one that npm-local holds, then audits through npm-local,
// npm-remote and npm-all. The audit reports the advisories of both, and neither the test advisory source nor the npm test
// upstream saw a request while the audits ran. The rows of pnpm 10 and Yarn 1 are reported as not applicable, with the
// reasons of Q-NPM-65.
func TestVuln_R_VULN_017_NPMAuditThroughEveryRepository(t *testing.T) {
	e := vulnFx(t)
	requireScatter(t, e.npm)
	e.seedAuditContent(t)
	run := "vuln-audit" + vulnRunSuffix
	repos := []string{npmLocalKey, npmRemoteKey, npmAllKey}

	// What a row installs: one tree through npm-all (the version of the public registry cached in npm-remote, the one that npm-local
	// holds), audited through every repository. Yarn 4 fetches the document of each package it audits from the registry it audits
	// through (verified: YN0035, a 404 of npm-local for a package that only npm-remote holds), so its trees are per repository, each
	// of what that repository holds.
	held := map[string][]string{
		npmLocalKey:  {vulnNPMAlias + "@2.0.0"},
		npmRemoteKey: {vulnNPMVuln + "@1.0.0"},
		npmAllKey:    {vulnNPMVuln + "@1.0.0", vulnNPMAlias + "@2.0.0"},
	}
	e.eachRow(t, run, "install", func(t *testing.T, c *npmClient) {
		if vulnAuditNA(c.row) != "" {
			return
		}
		trees := map[string][]string{"vuln-audit": held[npmAllKey]}
		if c.family() == "yarn4" {
			trees = map[string][]string{}
			for _, key := range repos {
				trees["vuln-audit-"+key] = held[key]
			}
		}
		for name, specs := range trees {
			dir := c.project(name, nil)
			c.registryKey = npmAllKey
			if c.family() == "yarn4" {
				c.registryKey = strings.TrimPrefix(name, "vuln-audit-")
			}
			c.configure(dir)
			c.mustInstall(dir, specs...)
		}
	})
	// Warm the package documents the audits make the registries serve (npm asks for the document of every flagged package): through
	// each repository, for both names, a miss included (npm-remote holds no trove-test-alias; its negative answer is cached).
	// The caches are the replicas' own (INV-18), so every replica is asked, one by one.
	addrs, stopForwards := packagesForwards(t)
	for _, addr := range addrs {
		a := apiAtPod(t, addr, e.npm.token)
		for _, key := range repos {
			for _, name := range []string{vulnNPMVuln, vulnNPMAlias} {
				a.getOverForward("/api/npm/" + key + "/" + name)
			}
		}
	}
	stopForwards()
	stub(t).reset(t)
	osvStub(t).send(t, http.MethodPost, "/-/stub/reset", nil)

	e.eachRow(t, run, "audit", func(t *testing.T, c *npmClient) {
		if why := vulnAuditNA(c.row); why != "" {
			npmNotApplicable(t, why)
		}
		for _, key := range repos {
			dir := c.dir("vuln-audit")
			if c.family() == "yarn4" {
				dir = c.dir("vuln-audit-" + key)
			}
			c.registryKey = key
			c.configure(dir)
			r := c.audit(dir)
			// every client exits non-zero when it found an advisory
			if r.Code == 0 {
				t.Fatalf("%s audit through %s exited 0, want the advisories reported:\n%s", c.row, key, tailLines(r.Out, 40))
			}
			want := []struct{ id, summary string }{{"GHSA-aaaa-aaaa-0001", vulnNPMVuln}, {"GHSA-aaaa-bbbb-cccc", vulnNPMAlias}}
			if c.family() == "yarn4" {
				want = want[:0]
				for _, spec := range held[key] {
					if strings.HasPrefix(spec, vulnNPMVuln) {
						want = append(want, struct{ id, summary string }{"GHSA-aaaa-aaaa-0001", vulnNPMVuln})
					} else {
						want = append(want, struct{ id, summary string }{"GHSA-aaaa-bbbb-cccc", vulnNPMAlias})
					}
				}
			}
			for _, adv := range want {
				if !strings.Contains(r.Out, adv.id) && !strings.Contains(r.Out, adv.summary) {
					t.Fatalf("%s audit through %s does not report %s (%s):\n%s", c.row, key, adv.summary, adv.id, tailLines(r.Out, 40))
				}
			}
		}
	})

	t.Run("no request to an upstream during the audits", func(t *testing.T) {
		// R-VULN-017: the test source and every upstream see no request during the audit, of any kind. The package documents were
		// warmed above (npm fetches the document of each flagged package for its fix calculation), so a request here is a finding.
		if got := stub(t).counts(t); got.Total() != 0 {
			t.Errorf("the npm test upstream saw %d requests during the audits: %+v", got.Total(), got.Entries)
		}
		if got := len(stub(t).audits(t)); got != 0 {
			t.Errorf("the npm test upstream saw %d advisory requests during the audits", got)
		}
		if got := osvStub(t).osvCounts(t).Total; got != 0 {
			t.Errorf("the test advisory source saw %d requests during the audits", got)
		}
	})
	npmRep.flush(t, run)
}

// bulkRequest is what an npm client sends: the names and versions of a tree, gzip-coded JSON.
func bulkRequest(t testing.TB, body map[string][]string) []byte {
	t.Helper()
	raw, _ := json.Marshal(body)
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	return b.Bytes()
}

type bulkAdvisory struct {
	ID                 int64    `json:"id"`
	URL                string   `json:"url"`
	Title              string   `json:"title"`
	Severity           string   `json:"severity"`
	VulnerableVersions string   `json:"vulnerable_versions"`
	CWE                []string `json:"cwe"`
}

// TestVuln_R_VULN_017_NPMAuditBody reads the audit endpoint's answers off the wire: a tree with a claimed name and a malicious
// version answers the malicious advisory as critical with CWE-506, omits the internal name and says so in the notice; the same
// request through two replicas gives the same bytes, on every kind of repository; the legacy endpoints stay 501.
func TestVuln_R_VULN_017_NPMAuditBody(t *testing.T) {
	e := vulnFx(t)
	e.seedAuditContent(t)
	addrs, stop := packagesForwards(t)
	defer stop()
	tree := map[string][]string{vulnAcme: {"1.0.0"}, vulnNPMMal: {"1.0.0"}, vulnNPMVuln: {"1.0.0"}, vulnNPMAlias: {"2.0.0"}}
	req := bulkRequest(t, tree)
	stub(t).reset(t)
	osvStub(t).send(t, http.MethodPost, "/-/stub/reset", nil)
	for _, key := range []string{npmLocalKey, npmRemoteKey, npmAllKey} {
		var bodies [][]byte
		for i, addr := range addrs[:2] {
			a := apiAtPod(t, addr, e.npm.token)
			r := a.do(http.MethodPost, "/api/npm/"+key+"/-/npm/v1/security/advisories/bulk", req, "Content-Type", "application/json", "Content-Encoding", "gzip")
			r.want(t, fmt.Sprintf("audit of %s at replica %d", key, i), 200)
			bodies = append(bodies, r.Body)
			if i > 0 {
				continue
			}
			var got map[string][]bulkAdvisory
			r.JSON(t, &got)
			if _, ok := got[vulnAcme]; ok {
				t.Errorf("%s: the answer reports the internal name %s", key, vulnAcme)
			}
			mal := got[vulnNPMMal]
			if len(mal) != 1 || mal[0].Severity != "critical" || !contains(mal[0].CWE, "CWE-506") || mal[0].ID <= 0 {
				t.Errorf("%s: the malicious advisory of %s = %+v, want one, critical, CWE-506, a numeric id", key, vulnNPMMal, mal)
			}
			vuln := got[vulnNPMVuln]
			if len(vuln) != 1 || vuln[0].Severity != "high" || !strings.HasSuffix(vuln[0].URL, "/GHSA-aaaa-aaaa-0001") || vuln[0].VulnerableVersions == "" {
				t.Errorf("%s: the advisory of %s = %+v, want one, high, the GHSA url", key, vulnNPMVuln, vuln)
			}
			if n := r.Header.Get("npm-notice"); !strings.Contains(n, "1 internal name") {
				t.Errorf("%s: npm-notice %q does not name the withheld internal name", key, n)
			}
		}
		if !bytes.Equal(bodies[0], bodies[1]) {
			t.Errorf("%s: two replicas answered different bytes:\n%s\n%s", key, bodies[0], bodies[1])
		}
		// the legacy endpoints of npm 6 and pnpm 10 are answered 501 and are not translated
		a := apiAtPod(t, addrs[0], e.npm.token)
		for _, p := range []string{"/-/npm/v1/security/audits/quick", "/-/npm/v1/security/audits"} {
			a.do(http.MethodPost, "/api/npm/"+key+p, "{}", "Content-Type", "application/json").want(t, "legacy audit endpoint "+p+" at "+key, 501)
		}
	}
	if got := stub(t).counts(t).Total(); got != 0 {
		t.Errorf("the npm test upstream saw %d requests", got)
	}
	if got := osvStub(t).osvCounts(t).Total; got != 0 {
		t.Errorf("the test advisory source saw %d requests", got)
	}
}

// TestVuln_R_VULN_015_NPMBlockedInstallMalicious is E4b.4 for npm: `trove-test-mal@1.0.0` is refused through npm-all by every
// gate row with the 403 and trove's text, the npm test upstream sees no request for the tarball and nothing is stored; the
// refusal is the one of R-VULN-016 (403, no-store, vulnerability_blocked) on the wire.
func TestVuln_R_VULN_015_NPMBlockedInstallMalicious(t *testing.T) {
	e := vulnFx(t)
	requireScatter(t, e.npm)
	run := "vuln-mal" + vulnRunSuffix
	stub(t).reset(t)
	e.eachRow(t, run, "malicious", func(t *testing.T, c *npmClient) {
		dir := c.project("vuln-mal", nil)
		c.configure(dir)
		r := c.run(dir, c.addCmd(vulnNPMMal+"@1.0.0")...)
		if r.Code == 0 {
			t.Fatalf("%s installed the malicious %s:\n%s", c.row, vulnNPMMal, tailLines(r.Out, 30))
		}
		requireAll(t, c.row+" install of "+vulnNPMMal, r.Out, "403")
		if c.family() == "npm" {
			requireAll(t, c.row+" install of "+vulnNPMMal, r.Out, "E403", "trove blocked "+vulnNPMMal+"@1.0.0", "malicious package", "MAL-2026-1234")
		}
		if x := c.exec(dir, nil, "sh", "-c", "ls node_modules/"+vulnNPMMal+" 2>&1 || true"); !strings.Contains(x.Out, "No such file") {
			t.Fatalf("%s left %s in node_modules: %s", c.row, vulnNPMMal, x.Out)
		}
	})
	t.Run("the refusal on the wire, no tarball request upstream, nothing stored", func(t *testing.T) {
		a := e.adm.withToken(e.npm.token)
		r := a.get("/api/npm/"+npmRemoteKey+"/"+vulnNPMMal+"/-/"+vulnNPMMal+"-1.0.0.tgz").want(t, "tarball of the malicious version", 403)
		if cc := r.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("Cache-Control of the refusal = %q, want no-store", cc)
		}
		var body struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		r.JSON(t, &body)
		if body.Code != "vulnerability_blocked" || !strings.Contains(body.Error, "trove blocked "+vulnNPMMal+"@1.0.0 in "+npmRemoteKey+": malicious package (MAL-2026-1234)") {
			t.Errorf("the refusal = %+v, want code vulnerability_blocked and trove's text", body)
		}
		for _, m := range []string{http.MethodHead} {
			a.do(m, "/api/npm/"+npmAllKey+"/"+vulnNPMMal+"/-/"+vulnNPMMal+"-1.0.0.tgz", nil).want(t, m+" of the malicious version", 403)
		}
		if n := stub(t).counts(t).Tarballs(vulnNPMMal); n != 0 {
			t.Errorf("the npm test upstream served the tarball of %s %d times, want none", vulnNPMMal, n)
		}
		stored := sql(t, `SELECT count(*) FROM trove.items i JOIN trove.repositories r ON r.id = i.repo_id WHERE r.key = '`+npmRemoteKey+`' AND i.path LIKE '%`+vulnNPMMal+`-1.0.0.tgz'`)
		if stored != "0" {
			t.Errorf("%s: %s items of the malicious tarball are stored", npmRemoteKey, stored)
		}
	})
	npmRep.flush(t, run)
}

// TestVuln_R_VULN_015_NPMBlockedInstallInTriage is E4b.4 for npm, the other half: a version cached in npm-remote with a
// finding that is confirmed and blocked through the API is refused by `install` and by the lockfile install of every gate
// row, through whichever replica the edge picks, and downloads again after the block is lifted; the upstream is never asked
// for the tarball a second time.
func TestVuln_R_VULN_015_NPMBlockedInstallInTriage(t *testing.T) {
	e := vulnFx(t)
	requireScatter(t, e.npm)
	run := "vuln-triage" + vulnRunSuffix
	stub(t).reset(t)
	e.eachRow(t, run, "triage", func(t *testing.T, c *npmClient) {
		name := e.npmTri[c.row]
		spec := name + "@1.0.0"
		dir := c.project("vuln-triage", nil)
		c.configure(dir)
		c.mustInstall(dir, spec)
		f := e.finding(t, npmRemoteKey, name, "1.0.0")
		if f.Blocked || f.Status == "blocked" || f.Status == "dismissed" {
			t.Fatalf("the finding of %s starts as %+v, want an open finding that does not block", spec, f)
		}
		e.block(t, e.adm, f.ID)

		// the lockfile stays, the installed packages and every cache of the client go: the tarball is asked for again
		c.wipeAll(dir)
		for _, cmd := range [][]string{c.addCmd(spec), c.frozenInstall()} {
			r := c.run(dir, cmd...)
			if r.Code == 0 {
				t.Fatalf("%s %s succeeded while %s is blocked in triage:\n%s", c.row, strings.Join(cmd, " "), spec, tailLines(r.Out, 30))
			}
			requireAll(t, c.row+" "+strings.Join(cmd, " ")+" of a blocked version", r.Out, "403")
			if c.family() == "npm" {
				requireAll(t, c.row+" "+strings.Join(cmd, " "), r.Out, "trove blocked "+spec+" in "+npmAllKey+": blocked in triage ("+e.advisoryOf[name]+")")
				if retry := retryNotice.FindString(r.Out); retry != "" {
					t.Errorf("%s retried the refused request (%q): a 403 is final", c.row, retry)
				}
			}
		}
		e.lift(t, e.adm, f.ID)
		c.wipeAll(dir)
		c.mustInstall(dir, spec)
		c.assertLoads(t, dir, name)
		if n := stub(t).counts(t).Tarballs(name); n > 1 {
			t.Errorf("%s: the npm test upstream served the tarball of %s %d times, want at most once (the first fill)", c.row, name, n)
		}
	})
	npmRep.flush(t, run)
}

// wipeAll is wipeClientState and the store pnpm 11 and later keep below $HOME/.local whatever npm_config_store_dir says: without it
// a client that has the version installs it from its own store and never asks the registry.
func (c *npmClient) wipeAll(dir string) {
	c.t.Helper()
	c.wipeClientState(dir)
	c.sh(dir, `rm -rf "`+c.home()+`/.local"`)
}

// assertLoads checks that the package installed and loads.
func (c *npmClient) assertLoads(t *testing.T, dir, name string) {
	t.Helper()
	if r := c.exec(dir, nil, "node", "-e", fmt.Sprintf("require.resolve(%q)", name)); r.Code != 0 {
		t.Fatalf("%s: %s is not installed after the lift:\n%s", c.row, name, r.Out)
	}
}
