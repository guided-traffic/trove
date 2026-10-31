//go:build e2e

package e2e

// npm on a customer endpoint (E2.12, R-NPM-048, R-EP-018, R-EP-019): the endpoint `customers` of the dev profile has
// its own Ingress and host (packages.trove.localhost) and exposes a repository only through rules. The clients reach it
// by that host, which their pods resolve to the loopback address of their forwarder (hostAliases in deploy/dev/e2e.sh),
// and use a token bound to the endpoint. The virtual repository of the test has a remote member, so the cache-fill rule
// of R-EP-019 is part of it.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

// endpointView lists the versions of a package as the row's client prints them for the registry of the endpoint.
func (c *npmClient) versionsOfPackage(dir, name string) []string {
	c.t.Helper()
	var r npmRun
	switch c.family() {
	case "yarn4":
		r = c.run(dir, "npm", "info", name, "--fields", "versions", "--json")
	case "yarn1":
		r = c.run(dir, "info", name, "versions", "--json")
	default:
		r = c.run(dir, "view", name, "versions", "--json")
	}
	if r.Code != 0 {
		c.t.Logf("%s could not list the versions of %s (exit %d):\n%s", c.row, name, r.Code, tailLines(r.Out, 12))
		return nil
	}
	out := r.Out
	if i := strings.IndexAny(out, "[{"); i >= 0 {
		out = out[i:]
	}
	var vs []string
	if json.Unmarshal([]byte(out), &vs) == nil {
		sort.Strings(vs)
		return vs
	}
	var wrapped struct {
		Data     []string `json:"data"`
		Versions []string `json:"versions"`
	}
	if json.Unmarshal([]byte(out), &wrapped) == nil {
		vs = append(wrapped.Data, wrapped.Versions...)
		sort.Strings(vs)
	}
	return vs
}

func TestNPMEndpoint(t *testing.T) {
	fx := npmFx(t)
	requireScatter(t, fx)
	t.Run("R-NPM-048 R-EP-018 R-EP-019 E2.12 npm on the customers endpoint", func(t *testing.T) {
		repos := fx.upstreamRepos(t, "e212", nil)
		s := stub(t)
		s.resetAll(t)
		defer s.resetAll(t)

		// the fixture of R-EP-012: 1.0.0, 1.9.0, 2.0.0, 2.1.0 and 2.1.0-rc.1 (tag next) in a local member
		sdk := "@acme" + fx.run + "/sdk"
		pub := fx.clientOn(t, "npm11", 0, repos.all, "e212pub")
		for _, v := range []string{"1.0.0", "1.9.0", "2.0.0", "2.1.0"} {
			pub.publish(sdk, v)
		}
		dir := pub.publishDir(sdk, "2.1.0-rc.1", "marker:rc")
		pub.must(dir, "publish", "--tag", "next")

		// the rule: versions from 2.0.0 on, no pre-releases
		rule := map[string]any{
			"name": "sdk-" + fx.run, "repository": repos.all,
			"packages": map[string]any{"include": []string{sdk}},
			"versions": map[string]any{"range": ">=2.0.0"},
		}
		fx.adm.do(http.MethodPost, "/api/v1/endpoints/customers/exposure-rules", rule).want(t, "create the rule", 201)
		defer func() {
			g := fx.adm.get("/api/v1/endpoints/customers/exposure-rules/sdk-" + fx.run)
			if g.Status == 200 {
				fx.adm.do(http.MethodDelete, "/api/v1/endpoints/customers/exposure-rules/sdk-"+fx.run, nil, "If-Match", g.Header.Get("ETag"))
			}
		}()

		ensureSA(t, fx.adm, "e2e-npm-cust")
		ensurePolicy(t, fx.adm, "e2e-npm-cust-"+fx.run, []string{repos.all, repos.remote, npmLocalKey}, [2]any{"sa:e2e-npm-cust", []string{"read"}})
		bound := freshSAToken(t, fx.adm, "e2e-npm-cust", 3600, map[string]any{"inherit": true, "endpoints": []string{"customers"}}).Token

		exposed := []string{"2.0.0", "2.1.0"}
		for i, row := range npmGateRows {
			t.Run(row, func(t *testing.T) {
				c := fx.clientOn(t, row, i%npmClientCount(t), repos.all, "e212")
				c.token, c.host = bound, custHost
				d := c.project("customers", nil)
				c.configure(d)
				got := c.versionsOfPackage(d, sdk)
				if strings.Join(got, ",") != strings.Join(exposed, ",") {
					t.Fatalf("%s lists %v for %s on customers, want exactly %v", row, got, sdk, exposed)
				}
				c.mustInstall(d, sdk)
				if v := c.read(d + "/node_modules/" + sdk + "/package.json"); !strings.Contains(v, `"version": "2.1.0"`) {
					t.Fatalf("the install on customers did not take the highest exposed release:\n%s", v)
				}
				d2 := c.project("hidden", nil)
				c.configure(d2)
				// a hidden version is a missing one: the client fails the install of 1.9.0 exactly as it fails the install of a
				// version that no registry holds (npm: ETARGET, pnpm: NO_MATCHING_VERSION, Yarn: YN0016 or its resolution error)
				hidden := c.run(d2, c.addCmd(sdk+"@1.9.0")...)
				missing := c.run(d2, c.addCmd(sdk+"@9.9.9")...)
				if hidden.Code == 0 || missing.Code == 0 {
					t.Fatalf("the install of the hidden 1.9.0 exited %d and that of the missing 9.9.9 exited %d, want both to fail:\n%s", hidden.Code, missing.Code, tailLines(hidden.Out, 12))
				}
				if h, m := failureLine(hidden.Out, "1.9.0"), failureLine(missing.Out, "9.9.9"); h == "" || h != m {
					t.Fatalf("the hidden 1.9.0 fails with %q and the missing 9.9.9 with %q, want the same failure:\n%s\n---\n%s", h, m, tailLines(hidden.Out, 8), tailLines(missing.Out, 8))
				}
			})
		}

		// the bodies of the endpoint name only the endpoint: tarball URLs carry its host and no member's key appears
		api := customersAPI(t, bound)
		doc := api.get("/api/npm/"+repos.all+"/"+url.PathEscape(sdk)).want(t, "document on customers", 200)
		body := string(doc.Body)
		if !strings.Contains(body, "https://"+custHost+":"+edgePort(t)+"/") {
			t.Fatalf("tarball URLs on customers do not name the endpoint's host:\n%s", tailLines(body, 6))
		}
		// the tarball of a hidden version is the 404 of R-EP-013, and no tarball URL names anything but the endpoint
		api.get("/api/npm/"+repos.all+"/"+url.PathEscape(sdk)+"/-/sdk-1.9.0.tgz").want(t, "the tarball of a hidden version", 404)
		for _, member := range []string{npmLocalKey, repos.remote} {
			if strings.Contains(body, member) {
				t.Fatalf("the document on customers names the member %s", member)
			}
		}
		if tags := api.get("/api/npm/"+repos.all+"/-/package/"+url.PathEscape(sdk)+"/dist-tags").want(t, "dist-tags on customers", 200); !strings.Contains(string(tags.Body), `"latest":"2.1.0"`) {
			t.Fatalf("dist-tags on customers: %s, want latest on 2.1.0", tags)
		}

		// a narrowed rule makes every replica answer the old validator with 200
		etag := doc.Header.Get("ETag")
		g := fx.adm.get("/api/v1/endpoints/customers/exposure-rules/sdk-"+fx.run).want(t, "read the rule", 200)
		fx.adm.do(http.MethodPatch, "/api/v1/endpoints/customers/exposure-rules/sdk-"+fx.run, `{"versions":{"range":">=2.1.0"}}`,
			"Content-Type", "application/merge-patch+json", "If-Match", g.Header.Get("ETag")).want(t, "narrow the rule", 200)
		pods, stop := customersOnPods(t, bound)
		defer stop()
		for i, p := range pods {
			waitFor(t, fmt.Sprintf("replica %d to answer the earlier ETag with 200", i), 10*time.Second, 200*time.Millisecond, func() bool {
				x := p.get("/api/npm/"+repos.all+"/"+url.PathEscape(sdk), "If-None-Match", etag)
				return x.Status == 200 && !strings.Contains(string(x.Body), `"2.0.0"`)
			})
		}

		// R-EP-019: a version that is not cached in the remote member is not there on customers, and no request goes to
		// the upstream; the same install through default fills the cache and then customers has it
		const cobalt = "@cobalt/eager-lantern"
		// a rule of its own: the package is exposed without a version range, so what keeps it from customers is the cache
		fx.adm.do(http.MethodPost, "/api/v1/endpoints/customers/exposure-rules", map[string]any{
			"name": "cobalt-" + fx.run, "repository": repos.all, "packages": map[string]any{"include": []string{cobalt}},
		}).want(t, "create the rule of the remote package", 201)
		defer func() {
			g := fx.adm.get("/api/v1/endpoints/customers/exposure-rules/cobalt-" + fx.run)
			if g.Status == 200 {
				fx.adm.do(http.MethodDelete, "/api/v1/endpoints/customers/exposure-rules/cobalt-"+fx.run, nil, "If-Match", g.Header.Get("ETag"))
			}
		}()
		for i, p := range pods {
			waitFor(t, fmt.Sprintf("replica %d to know the rule", i), 10*time.Second, 200*time.Millisecond, func() bool {
				return p.get("/api/npm/"+repos.all+"/-/ping").Status == 200
			})
		}
		s.reset(t)
		api.get("/api/npm/"+repos.all+"/"+url.PathEscape(cobalt)).want(t, "an uncached remote package on customers", 404)
		if n := s.counts(t).Documents(cobalt) + s.counts(t).Tarballs(cobalt); n != 0 {
			t.Fatalf("customers made %d requests to the upstream for %s, want none (cache_fill false)", n, cobalt)
		}
		// the same install through default fills the cache: the document alone does not, because a version that is not
		// cached in the remote member is not exposed (R-EP-019), so the client downloads the package
		fill := fx.clientOn(t, "npm11", 0, repos.all, "e212fill")
		fd := fill.project("fill", nil)
		fill.configure(fd)
		fill.mustInstall(fd, cobalt)
		if s.counts(t).Documents(cobalt) < 1 || s.counts(t).Tarballs(cobalt) < 1 {
			t.Fatalf("the install through default did not fill the cache for %s: %s", cobalt, s.counts(t).Summary())
		}
		api.get("/api/npm/"+repos.all+"/"+url.PathEscape(cobalt)).want(t, "the package on customers after the fill", 200)
		_ = npmstub.KindDocument
	})
}

// failureRe finds the line of a client's output that states why the install failed: npm's and pnpm's error codes, Yarn
// 4's YN code, Yarn 1's "error" line.
var failureRe = regexp.MustCompile(`(?i)ETARGET|ERR_PNPM_[A-Z_]+|YN\d{4}|^error |No matching version|Failed to resolve`)

// failureLine is the first such line with the version taken out, so that the failure of one version and the failure
// of another compare equal when the client says the same thing about both.
func failureLine(out, version string) string {
	for _, l := range strings.Split(out, "\n") {
		if failureRe.MatchString(strings.TrimSpace(l)) {
			l = strings.ReplaceAll(l, version, "<version>")
			return strings.Join(strings.Fields(l), " ")
		}
	}
	return ""
}
