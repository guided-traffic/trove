//go:build e2e

package e2e

// npm on a customer endpoint (E2.12, R-NPM-048, R-EP-018, R-EP-019): the endpoint `customers` of the dev profile has
// its own Ingress and host (packages.trove.localhost) and exposes a repository only through rules. The clients reach it
// by that host, which their pods resolve to the loopback address of their forwarder (hostAliases in deploy/dev/e2e.sh),
// and use a token bound to the endpoint. The test waits for WP2.9 (the exposure adapter of npm) and WP2.5 (a remote
// member): until the rule API refuses an npm repository it ends as pending on WP2.9 (npmPending).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
			"packages": map[string]any{"include": []string{sdk, "@cobalt/*"}},
			"versions": map[string]any{"range": ">=2.0.0"},
		}
		r := fx.adm.do(http.MethodPost, "/api/v1/endpoints/customers/exposure-rules", rule)
		if r.Status == 422 {
			npmPending(t, "WP2.9", "the exposure adapter of npm: creating a rule for an npm repository answered "+r.String())
		}
		r.want(t, "create the rule", 201)
		defer func() {
			g := fx.adm.get("/api/v1/endpoints/customers/exposure-rules/sdk-" + fx.run)
			if g.Status == 200 {
				fx.adm.do(http.MethodDelete, "/api/v1/endpoints/customers/exposure-rules/sdk-"+fx.run, nil, "If-Match", g.Header.Get("ETag"))
			}
		}()

		ensureSA(t, fx.adm, "e2e-npm-cust")
		ensurePolicy(t, fx.adm, "e2e-npm-cust-"+fx.run, []string{repos.all}, [2]any{"sa:e2e-npm-cust", []string{"read"}})
		bound := newSAToken(t, fx.adm, "e2e-npm-cust", 3600, map[string]any{"inherit": true, "endpoints": []string{"customers"}}).Token

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
				if hr := c.run(d2, c.addCmd(sdk+"@1.9.0")...); hr.Code == 0 || !strings.Contains(hr.Out, "404") {
					t.Fatalf("an install of the hidden 1.9.0 on customers exited %d, want a 404 as for a missing version:\n%s", hr.Code, tailLines(hr.Out, 12))
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
		s.reset(t)
		api.get("/api/npm/"+repos.all+"/"+url.PathEscape(cobalt)).want(t, "an uncached remote package on customers", 404)
		if n := s.counts(t).Documents(cobalt) + s.counts(t).Tarballs(cobalt); n != 0 {
			t.Fatalf("customers made %d requests to the upstream for %s, want none (cache_fill false)", n, cobalt)
		}
		fx.adm.withToken(fx.token).get("/api/npm/"+repos.all+"/"+url.PathEscape(cobalt)).want(t, "the same package through default", 200)
		if s.counts(t).Documents(cobalt) < 1 {
			t.Fatalf("default did not fill the cache for %s", cobalt)
		}
		api.get("/api/npm/"+repos.all+"/"+url.PathEscape(cobalt)).want(t, "the package on customers after the fill", 200)
		_ = npmstub.KindDocument
	})
}
