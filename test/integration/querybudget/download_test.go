//go:build integration

package querybudget

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/nugetstub"
)

// background are the statements of the replica itself (its health probes and its registration), which run whatever is served.
var background = map[string]bool{
	"GetSchemaState": true, "TableExists": true, "SELECT NOT pg_is_in_recovery()": true, "-- ping": true, "ping": true, "UpsertReplica": true,
	"ListClusterEpochs": true, "EndpointsCountServingReplicas": true,
}

// requests is how many downloads of one path a window holds: a statement of the request runs once per download, the batched and
// the cached ones (download statistics, token and principal reads) run less often.
const requests = 30

// perDownload runs a path against a replica and returns the statements of the window by name, per download: the statements that
// ran for nearly every request, divided by the number of requests.
func perDownload(t *testing.T, e *env, r *testenv.Replica, path string) map[string]int {
	t.Helper()
	if a := e.do(r, path); a.Status != 200 {
		t.Fatalf("warm-up %s: %d %.200s", path, a.Status, a.Body)
	}
	time.Sleep(time.Second)
	start := time.Now()
	for range requests {
		if a := e.do(r, path); a.Status != 200 {
			t.Fatalf("%s: %d", path, a.Status)
		}
	}
	time.Sleep(300 * time.Millisecond)
	counts := map[string]int{}
	for _, s := range testenv.StatementsSince(t, start.Add(-100*time.Millisecond)) {
		counts[testenv.StatementName(s)]++
	}
	// A statement of the request runs once per download. The log is read from a container that other writers share, so a line
	// can be lost where two writers meet: a statement counts as the request's when it ran for nine downloads in ten, and its
	// number per download is the nearest whole one.
	out := map[string]int{}
	for k, v := range counts {
		if !background[k] && v*10 >= requests*9 {
			out[k] = (v + requests/2) / requests
		}
	}
	if len(out) == 0 {
		t.Fatalf("no statement ran per request; the log is not read: %v", counts)
	}
	return out
}

func describe(c map[string]int) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s x%d", k, c[k])
	}
	return strings.Join(parts, ", ")
}

// R-ARCH-027: a download of a stored file costs one PostgreSQL statement on every route and every format that serves one: a local
// file, a remote hit, and the tiers of a virtual repository over both. The item lookup carries what the serve check decides on
// (the tombstone of npm and NuGet, the facts of an npm remote version) and, for a remote hit, the cache entry, so that no
// second statement of the request is left.
func TestR_ARCH_027_ADownloadCostsOneStatement(t *testing.T) {
	e := newEnv(t)
	e.st.LogStatements(t)
	r := e.st.Start(t, testenv.ReplicaOptions{Name: "web-1"})

	const local = "budget-local"
	e.publish(r, rNPM, local, "1.0.0")
	e.push(r, rNuGet, "Budget.Local", "1.0.0")

	// The remote hits are filled first, through the remote repository itself.
	if a := e.do(r, "/api/npm/"+rNPMRem+"/"+e.pkg); a.Status != 200 {
		t.Fatalf("npm document: %d %s", a.Status, a.Body)
	}
	npmRemote := npmTarballPath(rNPMRem, e.pkg, e.ver)
	if a := e.do(r, npmRemote); a.Status != 200 {
		t.Fatalf("npm fill: %d %.200s", a.Status, a.Body)
	}
	pkg, _ := nugetstub.Default().Get(nugetstub.NewtonsoftID)
	nugetVer := pkg.Versions[1].Version
	nugetRemote := nupkgPath(rNuGetRem, "Newtonsoft.Json", nugetVer)
	if a := e.do(r, nugetRemote); a.Status != 200 {
		t.Fatalf("nuget fill: %d %.200s", a.Status, a.Body)
	}

	// budget is one statement per member the resolution of a virtual repository visits (Q-ARCH-290): the local member is
	// asked first, so a file of the remote member costs the miss at the local one and the hit.
	routes := []struct {
		name, path string
		budget     int
	}{
		{"an npm tarball of a local repository", npmTarballPath(rNPM, local, "1.0.0"), 1},
		{"the raw view of an npm tarball", "/files/" + rNPM + "/" + e.itemPath(rNPM, "-1.0.0.tgz"), 1},
		{"an npm tarball of a remote repository (hit)", npmRemote, 1},
		{"an npm tarball of the local member of a virtual repository", npmTarballPath(rNPMVirt, local, "1.0.0"), 1},
		{"an npm tarball of the remote member of a virtual repository", npmTarballPath(rNPMVirt, e.pkg, e.ver), 2},
		{"a .nupkg of a local NuGet repository", nupkgPath(rNuGet, "Budget.Local", "1.0.0"), 1},
		{"a .nupkg of a remote NuGet repository (hit)", nugetRemote, 1},
		{"a .nupkg of the local member of a virtual NuGet repository", nupkgPath(rNuGetVrt, "Budget.Local", "1.0.0"), 1},
		{"a .nupkg of the remote member of a virtual NuGet repository", nupkgPath(rNuGetVrt, "Newtonsoft.Json", nugetVer), 2},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			got := perDownload(t, e, r, rt.path)
			total := 0
			for _, v := range got {
				total += v
			}
			t.Logf("statements per download: %d (%s)", total, describe(got))
			if total > rt.budget {
				t.Errorf("a download runs %d statements (%s), the budget of R-ARCH-027 is %d", total, describe(got), rt.budget)
			}
		})
	}
}
