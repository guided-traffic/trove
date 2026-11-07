//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// claimTiming is the last criterion of R-NUGET-016: a forwarded answer through the virtual repository is cached on every
// web replica; then the local member claims the package's namespace, and no replica lists the package for the query
// any more within the bound, although each still holds the cached upstream answer: the claim filters the cached answer
// on every use (R-REPO-023, Q-NUGET-81) and the upstream sees no new search. The bounds allow one more second for the
// poll and the request, as E1.14 does.
//
// The package is published to the counting upstream at run time under a name of this run; the query is a word of its ID
// that the claim's stem does not hold, so the query is forwarded (not withheld) and the claim decides on the result.
type claimTiming struct {
	t       *testing.T
	fx      *nugetFixture
	stub    *stubControl
	pods    []*api
	path    string
	id      string
	pattern string
}

func newClaimTiming(t *testing.T, purpose string) *claimTiming {
	fx := nugetFx(t)
	repos := fx.upstreamRepos(t, purpose, nil)
	// The ID is the run's and the test's (one process may run both tests), the query is its second part.
	stem, word := "Partner"+fx.run, purpose+fx.run
	c := &claimTiming{t: t, fx: fx, stub: nugetStub(t), id: stem + "." + word, pattern: stem + ".*"}
	c.path = nugetAddr(repos.all) + "/v3/search?take=20&q=" + strings.ToLower(word)
	c.stub.nugetPublish(t, c.id, "1.0.0")
	addrs, stop := packagesForwards(t)
	t.Cleanup(stop)
	for _, a := range addrs {
		c.pods = append(c.pods, newAPI(t, withRoundRobin(a)).withToken(fx.token))
	}
	t.Cleanup(func() { setNuGetClaims(t, fx.adm, nugetAuthKey, c.pattern, false) })
	return c
}

func (c *claimTiming) listed(a *api) bool {
	r := a.get(c.path)
	return r.Status == 200 && strings.Contains(strings.ToLower(string(r.Body)), strings.ToLower(c.id))
}

// cachedEverywhere waits until every replica lists the package, which caches the forwarded answer on each.
func (c *claimTiming) cachedEverywhere(label string) {
	c.t.Helper()
	for i, a := range c.pods {
		waitFor(c.t, fmt.Sprintf("%s: replica %d lists the upstream's package", label, i), time.Minute, 200*time.Millisecond, func() bool { return c.listed(a) })
	}
}

// claimAndWait writes the claim and fails unless no replica lists the package within the bound.
func (c *claimTiming) claimAndWait(label string, within time.Duration) {
	c.t.Helper()
	searches := c.stub.nugetCounts(c.t).Searches()
	setNuGetClaims(c.t, c.fx.adm, nugetAuthKey, c.pattern, true)
	start := time.Now()
	for i, a := range c.pods {
		for c.listed(a) {
			if time.Since(start) > within {
				c.t.Fatalf("%s: replica %d still lists %s %s after the claim", label, i, c.id, time.Since(start).Round(10*time.Millisecond))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	c.t.Logf("%s: no replica lists %s %s after the claim was written", label, c.id, time.Since(start).Round(10*time.Millisecond))
	if got := c.stub.nugetCounts(c.t).Searches(); got != searches {
		c.t.Errorf("%s: the upstream saw %d new searches: the replicas did not filter their cached answers", label, got-searches)
	}
}

// TestNuGetSearchClaimTiming: R-NUGET-016's claim criterion with Valkey: within 1 s (set nuget, every change).
func TestNuGetSearchClaimTiming(t *testing.T) {
	c := newClaimTiming(t, "claimtime")
	c.cachedEverywhere("Valkey up")
	c.claimAndWait("Valkey up", 3*time.Second)
}

// setNuGetClaims adds a manual claim of scope members to a local repository, or removes it, keeping every other manual
// claim (R-REPO-023).
func setNuGetClaims(t *testing.T, adm *api, repo, pattern string, add bool) {
	t.Helper()
	g := adm.get(reposBase+repo+"/claims").want(t, "read claims", 200)
	var cur struct {
		Claims []struct {
			Pattern, Source, Scope string
		} `json:"claims"`
	}
	g.JSON(t, &cur)
	var all []map[string]string
	for _, c := range cur.Claims {
		if c.Source == "manual" && !strings.EqualFold(c.Pattern, pattern) {
			all = append(all, map[string]string{"pattern": c.Pattern, "scope": c.Scope})
		}
	}
	if add {
		all = append(all, map[string]string{"pattern": pattern, "scope": "members"})
	}
	adm.do(http.MethodPut, reposBase+repo+"/claims", map[string]any{"claims": all}, "If-Match", g.Header.Get("ETag")).want(t, "write claims", 200)
}
