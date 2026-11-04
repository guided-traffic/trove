//go:build integration

package querybudget

import (
	"bytes"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/nugetstub"
)

// R-REPO-026, R-ARCH-027: concurrent cold requests of one artifact through a virtual repository that has a remote member, on
// several replicas, are all answered with the artifact and cost the upstream one request: a request that finds the cache entry
// or the item half way through a fill joins the flight and is never answered with a refusal.
func TestR_ARCH_027_ConcurrentColdRequestsThroughAVirtualRepositoryAreAllServed(t *testing.T) {
	e := newEnvWith(t, true)
	reps := []*testenv.Replica{
		e.st.Start(t, testenv.ReplicaOptions{Name: "web-1"}),
		e.st.Start(t, testenv.ReplicaOptions{Name: "web-2"}),
		e.st.Start(t, testenv.ReplicaOptions{Name: "web-3"}),
	}
	pkg, _ := nugetstub.Default().Get(nugetstub.NewtonsoftID)
	ver := pkg.Versions[1].Version
	cases := []struct {
		name, path string
		upstream   func() int
	}{
		{"npm virtual", npmTarballPath(rNPMVirt, e.pkg, e.ver), func() int { return e.npmStub.Counts().Tarballs(e.pkg) }},
		{"nuget virtual", nupkgPath(rNuGetVrt, "Newtonsoft.Json", ver), func() int { return e.nugetStub.Counts().Nupkgs("newtonsoft.json") }},
		{"nuget remote", nupkgPath(rNuGetRem, "Newtonsoft.Json", pkg.Versions[0].Version), func() int { return e.nugetStub.Counts().Nupkgs("newtonsoft.json") }},
	}
	// A slow upstream keeps the fill open while the others arrive: they find the flight, not a finished entry.
	if err := e.nugetStub.SetMode(nugetstub.Mode{DelayMS: map[string]int{nugetstub.ClassNupkg: 1500}}); err != nil {
		t.Fatal(err)
	}
	if err := e.npmStub.SetMode(npmstub.Mode{DelayMS: map[string]int{npmstub.KindTarball: 1500}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := c.upstream()
			var wg sync.WaitGroup
			type res struct {
				status int
				body   []byte
			}
			out := make([]res, 18)
			for i := range out {
				wg.Add(1)
				go func() {
					defer wg.Done()
					a := e.st.Do(reps[i%len(reps)], "GET", c.path, e.tokReader, nil)
					out[i] = res{a.Status, a.Body}
				}()
			}
			wg.Wait()
			for i, r := range out {
				if r.status != 200 || !bytes.Equal(r.body, out[0].body) {
					t.Errorf("request %d: %d (%.120s)", i, r.status, r.body)
				}
			}
			if got := c.upstream() - before; got != 1 {
				t.Errorf("the upstream saw %d requests for the artifact, want 1", got)
			}
		})
	}
}
