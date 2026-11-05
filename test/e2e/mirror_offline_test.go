//go:build e2e

package e2e

// E6b.3 and R-REPO-059 on kind (set `mirror`): a client restores a version the mirror stored while the test upstream is stopped.
// The test is the last of the set: a stub that is scaled to zero loses its dataset, and the test restores it (seedNPM) when it ends.

import (
	"strings"
	"testing"
	"time"
)

// TestMirror_R_REPO_059_RestoreWithTheUpstreamStopped: after a complete pass of each mirror, both test upstreams are scaled to
// zero, and `npm install` of a mirrored npm version and `dotnet restore` of a mirrored NuGet version through the same remotes
// succeed with an empty client cache.
func TestMirror_R_REPO_059_RestoreWithTheUpstreamStopped(t *testing.T) {
	e := mirrorFx(t)
	e.needMirrors(t)
	e.mu.Lock()
	npmKey, nugetKey := e.npmMirror, e.nugetMirror
	e.mu.Unlock()

	for _, name := range []string{"npmstub", "nugetstub"} {
		markStubStopped(name, true)
		stubScale(t, name, 0)
	}
	t.Cleanup(func() {
		for _, name := range []string{"npmstub", "nugetstub"} {
			stubScale(t, name, 1)
			markStubStopped(name, false)
		}
		e.seedNPM(t)
	})

	t.Run("npm", func(t *testing.T) {
		for i, name := range e.extras[:3] {
			r, has := e.npmInstall(t, i, npmKey, "stopped", name, "1.0.0")
			if r.Code != 0 || !has {
				t.Errorf("npm install %s@1.0.0 through %s with the upstream stopped: exit %d, installed %v:\n%s", name, npmKey, r.Code, has, tailLines(r.Out, 20))
				continue
			}
			t.Logf("npm install %s@1.0.0 with the upstream stopped: %s", name, r.Dur.Round(time.Millisecond))
		}
	})
	t.Run("nuget", func(t *testing.T) {
		c := e.nuget.client(t, 0, "mirror-stopped")
		const id, version = "Newtonsoft.Json", "13.0.3" // the newest version of the ID in the test upstream's corpus
		c.config(nugetFeed(t, "localhost", nugetKey), true)
		file := c.consumer("consumer", id, version)
		start := time.Now()
		out, err := c.try("dotnet", "restore", file, "--no-cache")
		if err != nil || !c.hasPackage(id, version) {
			t.Fatalf("dotnet restore of %s %s through %s with the upstream stopped: %v\n%s", id, version, nugetKey, err, tailLines(out, 25))
		}
		t.Logf("dotnet restore of %s %s with the upstream stopped: %s", id, version, time.Since(start).Round(time.Millisecond))
		if strings.Contains(out, "warn") {
			t.Logf("restore output: %s", tailLines(out, 8))
		}
	})
	e.jobs.check(t)
}
