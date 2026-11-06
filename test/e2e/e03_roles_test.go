//go:build e2e

package e2e

// The cut into roles on the dev profile (phase 6a, E6a.5, R-ARCH-001, R-HA-002, R-HA-037): every role is a Deployment of its own
// with several replicas, the edge splits the host by path between the role Services, a request lands on the pods of its family
// only, and a route of the other role's family answers the 404 of an unknown path at the pod.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// roleDeployments are the Deployments the dev profile renders (deploy/dev/values-dev.yaml) and their replicas.
var roleDeployments = map[string]int{"trove-api": 2, "trove-packages": 3, "trove-worker": 2, "trove-worker-vuln": 2}

// serviceBackends returns the pods behind a Service, from its EndpointSlices.
func serviceBackends(t testing.TB, service string) []string {
	t.Helper()
	raw := kn(t, "get", "endpointslices", "-l", "kubernetes.io/service-name="+service, "-o", "json")
	var l struct {
		Items []struct {
			Endpoints []struct {
				TargetRef struct{ Name string }
			}
		}
	}
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range l.Items {
		for _, e := range s.Endpoints {
			out = append(out, e.TargetRef.Name)
		}
	}
	sort.Strings(out)
	return out
}

func names(ps []pod) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

// requestLines returns, per pod, the paths of the request log lines of the pods of a selector since a time.
func requestLines(t testing.TB, selector string, since time.Time) map[string][]string {
	t.Helper()
	out, err := kubectl("-n", cfg.namespace, "logs", "-l", selector, "--since-time="+since.UTC().Format(time.RFC3339), "--tail=-1", "--max-log-requests=16", "--prefix=true")
	if err != nil {
		t.Fatalf("the logs of %s: %v", selector, err)
	}
	prefix := regexp.MustCompile(`^\[pod/([^/]+)/[^\]]+\] (.*)$`)
	res := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		m := prefix.FindStringSubmatch(line)
		if m == nil || !strings.Contains(m[2], `"log":"request"`) {
			continue
		}
		var r struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(m[2]), &r) == nil && r.Path != "" {
			res[m[1]] = append(res[m[1]], r.Path)
		}
	}
	return res
}

var (
	packageFamily = []string{"/api/npm/", "/api/nuget/", "/files/", "/v2/", "/.well-known/"}
	apiFamily     = []string{"/api/v1/", "/ui/", "/auth/"}
)

func hasPrefixAny(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func TestE03Roles(t *testing.T) {
	t.Run("R-HA-002 R-ARCH-001 R-HA-062 E6a.5 every role is a Deployment of its own with its replicas ready, the registry lists each pod under its role and pool, and no Deployment web exists", func(t *testing.T) {
		for dep, want := range roleDeployments {
			got := strings.TrimSpace(kn(t, "get", "deployment", dep, "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"))
			if got != fmt.Sprintf("%d/%d", want, want) {
				t.Errorf("Deployment %s has %s ready/desired replicas, want %d/%d", dep, got, want, want)
			}
		}
		if out, err := kubectl("-n", cfg.namespace, "get", "deployment", "trove-web"); err == nil {
			t.Errorf("the Deployment trove-web exists outside the bridge of R-HA-064: %s", out)
		}
		rs, err := replicas()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rs {
			if r.Lost || !strings.HasPrefix(r.Name, "trove-") {
				continue
			}
			wantRole, wantPool := "", ""
			switch {
			case strings.HasPrefix(r.Name, "trove-api-"):
				wantRole = "api"
			case strings.HasPrefix(r.Name, "trove-packages-"):
				wantRole = "packages"
			case strings.HasPrefix(r.Name, "trove-worker-vuln-"):
				wantRole, wantPool = "worker", "vuln"
			case strings.HasPrefix(r.Name, "trove-worker-"):
				wantRole = "worker"
			default:
				continue
			}
			if r.role() != wantRole || r.Pool != wantPool {
				t.Errorf("%s is listed as role %q pool %q, want %q and %q", r.Name, r.role(), r.Pool, wantRole, wantPool)
			}
		}
	})

	t.Run("R-HA-002 R-HA-037 E6a.5 each role Service selects the pods of its role alone and balances without affinity", func(t *testing.T) {
		for svc, sel := range map[string]string{"trove-api": selAPI, "trove-packages": selPackages} {
			want := names(livePods(t, sel))
			if got := serviceBackends(t, svc); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("Service %s has the backends %v, want the pods %v", svc, got, want)
			}
			if aff := strings.TrimSpace(kn(t, "get", "service", svc, "-o", "jsonpath={.spec.sessionAffinity}")); aff != "None" {
				t.Errorf("Service %s has sessionAffinity %q, want None", svc, aff)
			}
		}
		if out, err := kubectl("-n", cfg.namespace, "get", "service", "trove-worker"); err == nil {
			t.Errorf("a worker has a client Service: %s", out)
		}
	})

	t.Run("R-HA-037 E6a.5 the edge splits one host by path: management API requests reach the api pods, package and file requests the packages pods, every replica of a role in turn", func(t *testing.T) {
		api, pkg := map[string]int{}, map[string]int{}
		for i := 0; i < 90; i++ {
			code, h, _, err := edge("/api/v1/system/ping")
			if err != nil || code != 200 {
				t.Fatalf("ping through the edge: %d %v", code, err)
			}
			api[h.Get("X-Trove-Replica")]++
			// A package-family request without a credential is refused or not found; either way the pod names itself.
			_, h, _, err = edge("/api/npm/e2e-roles-distribution/-/ping")
			if err != nil {
				t.Fatal(err)
			}
			pkg[h.Get("X-Trove-Replica")]++
		}
		t.Logf("api requests per replica %v, packages requests per replica %v", api, pkg)
		if got, want := sortedKeys(api), names(livePods(t, selAPI)); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("/api/v1 requests reached %v, want every api pod %v and no other", got, want)
		}
		if got, want := sortedKeys(pkg), names(livePods(t, selPackages)); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("/api/npm requests reached %v, want every packages pod %v and no other", got, want)
		}
	})

	t.Run("R-HA-037 R-ARCH-001 E6a.5 package traffic appears only in the access logs of the packages pods, and UI, sign-in and management traffic only in those of the api pods", func(t *testing.T) {
		start := time.Now().Add(-5 * time.Second)
		marker := fmt.Sprintf("e2e-roles-%d", time.Now().UnixNano())
		pkgPaths := []string{"/api/npm/" + marker, "/api/nuget/" + marker + "/v3/index.json", "/files/" + marker + "/x", "/v2/" + marker + "/x", "/.well-known/" + marker}
		apiPaths := []string{"/api/v1/repositories/" + marker, "/ui/" + marker, "/auth/oidc/" + marker + "/start", "/api/v1/system/ping"}
		for i := 0; i < 24; i++ {
			for _, p := range append(append([]string{}, pkgPaths...), apiPaths...) {
				if _, _, _, err := edge(p); err != nil {
					t.Fatal(err)
				}
			}
		}
		time.Sleep(2 * time.Second) // the logs of the pods reach the API server
		apiLog, pkgLog := requestLines(t, selAPI, start), requestLines(t, selPackages, start)
		// A sent path counts once: /ui/<marker> answers a redirect to its locale, which the client follows and the pod logs
		// as a second request; only the paths this test sent are counted.
		sent := map[string]bool{}
		for _, p := range append(append([]string{}, pkgPaths...), apiPaths...) {
			sent[p] = true
		}
		count := func(logs map[string][]string, family []string) (n int, pods []string) {
			for pod, paths := range logs {
				hit := false
				for _, p := range paths {
					if hasPrefixAny(p, family) && strings.Contains(p, marker) && sent[p] {
						n++
						hit = true
					}
				}
				if hit {
					pods = append(pods, pod)
				}
			}
			sort.Strings(pods)
			return n, pods
		}
		if n, pods := count(pkgLog, packageFamily); n != 24*len(pkgPaths) {
			t.Errorf("the packages pods logged %d of %d package requests (on %v)", n, 24*len(pkgPaths), pods)
		}
		if n, pods := count(apiLog, packageFamily); n != 0 {
			t.Errorf("api pods logged %d package-family requests: %v", n, pods)
		}
		if n, pods := count(apiLog, apiFamily); n != 24*(len(apiPaths)-1) {
			t.Errorf("the api pods logged %d of %d UI, sign-in and management requests (on %v)", n, 24*(len(apiPaths)-1), pods)
		}
		// Not only the marked requests: in the window nothing of the other family reached a pod, whoever sent it.
		for pod, paths := range apiLog {
			for _, p := range paths {
				if hasPrefixAny(p, packageFamily) {
					t.Errorf("api pod %s logged the package or raw-file request %s", pod, p)
				}
			}
		}
		for pod, paths := range pkgLog {
			for _, p := range paths {
				if hasPrefixAny(p, apiFamily) {
					t.Errorf("packages pod %s logged the UI, sign-in or management request %s", pod, p)
				}
			}
		}
	})

	t.Run("R-ARCH-001 R-EP-005 E6a.5 a route of the other role's family answers the 404 of an unknown path at the pod, with a credential and the same body as a path that does not exist", func(t *testing.T) {
		tok := adminToken(t)
		check := func(selector, role string, paths []string) {
			addrs, stop := podForwardsOf(t, selector, 8080)
			defer stop()
			for i, addr := range addrs {
				a := apiAtPod(t, addr, tok)
				unknown := a.get("/does-not-exist-e2e-roles")
				if unknown.Status != http.StatusNotFound {
					t.Fatalf("%s pod %d: an unknown path answered %s, want 404", role, i, unknown)
				}
				for _, p := range paths {
					r := a.get(p)
					if r.Status != http.StatusNotFound {
						t.Errorf("%s pod %d: GET %s = %s, want the 404 of an unknown path", role, i, p, r)
						continue
					}
					if got, want := problemOf(t, r), problemOf(t, unknown); got != want {
						t.Errorf("%s pod %d: GET %s answered %s, the unknown path %s", role, i, p, got, want)
					}
				}
			}
		}
		check(selAPI, "api", []string{"/api/npm/generic/-/ping", "/api/nuget/nuget-local/v3/index.json", "/files/generic-local/x", "/v2/", "/.well-known/x"})
		check(selPackages, "packages", []string{"/api/v1/system/ping", "/api/v1/auth/whoami", "/api/v1/repositories", "/ui/", "/auth/oidc/keycloak/start"})
	})
}
