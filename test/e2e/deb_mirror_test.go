//go:build e2e

package e2e

// The full mirror of a Debian remote with the real clients (R-DEB-035, R-DEB-024, R-REPO-050, R-REPO-051): a pass stores the suite stable of
// the counting Debian archive test upstream, the test upstream's connections are cut, and the four images update and install from the
// mirror alone. The row is written against the spec and switched on by debMirrorBuilt when the mirror of the format (T-17 WP7) is on main.

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/debstub"
)

// debMirrorBuilt says whether the mirror lister of the Debian format is on main. While it is not, the row is skipped, with this reason.
var debMirrorBuilt = true

func TestDebMirror(t *testing.T) {
	if !debMirrorBuilt {
		t.Skip("the mirror of the Debian format (T-17 WP7) is not on main: the repository document of a Debian remote in mode mirror answers 501 until its lister is built")
	}
	fx := debFx(t)
	s := debStub(t)
	s.resetAll(t)
	t.Cleanup(func() { s.resetAll(t) })

	arch := aptArch(t, aptDebian13)
	key := "deb-mirror-" + fx.run
	body := map[string]any{
		"key": key, "class": "remote", "format": "deb", "requireAuthentication": true,
		"remote": map[string]any{
			"url":  "https://" + debStubHost(t) + debstub.ArchiveBase + "/",
			"mode": "mirror",
			// the architecture is the one of the client pods (all is always kept)
			"mirror": map[string]any{"concurrency": 2, "maxVersionsPerPackage": 2},
		},
		// the names of the stub: trove-* and libtrove1, which trove-hello depends on (a pattern that leaves a dependency out leaves it out of the mirror)
		"includePatterns": []string{"trove-*", "libtrove1"},
		"deb": map[string]any{
			"upstreamKeys": string(s.keyring(t, "a")),
			"mirror":       map[string]any{"suites": []string{"stable"}, "components": []string{"main"}, "architectures": []string{arch}},
		},
	}
	fx.adm.do("POST", reposRoute, body).want(t, "create "+key, 201)
	t.Cleanup(func() { deleteRepository(t, fx.adm, key) })
	debReadableOnEveryReplica(t, fx.token, key)

	e := &mirrorEnv{adm: fx.adm}
	e.run_(t, key, false)
	done := e.awaitComplete(t, key, 10*time.Minute)
	sum := done.LastCompletePass
	if sum.Outcome != "complete" || sum.PackagesFailed != 0 || sum.Packages == 0 {
		t.Fatalf("the pass ended %+v, want complete with the trove-* packages and no failed package", *sum)
	}
	t.Logf("the pass ended %s: %d packages, %d items, %d bytes, method %s", sum.Outcome, sum.Packages, sum.ItemsStored, sum.BytesStored, sum.Method)

	// The upstream goes away: its connections are cut at once (the switch `down: reset` of the stub). Scaling its Deployment to zero would leave
	// the Service without an endpoint, and a kind node drops those connections without an answer, so that every request waits for the upstream
	// client's connect timeout.
	s.debMode(t, debstub.Mode{Down: debstub.DownReset})
	delivered := func() int {
		c := s.debCounts(t)
		n := 0
		for _, kind := range []string{debstub.ClassRelease, debstub.ClassIndex, debstub.ClassByHash, debstub.ClassPool, debstub.ClassOther} {
			n += c.Delivered(kind)
		}
		return n
	}
	before := delivered()

	for _, img := range aptImages {
		t.Run("R-DEB-024 R-REPO-050 R-DEB-035 "+img.name+" updates and installs trove-hello from the mirror with the upstream stopped", func(t *testing.T) {
			c := fx.client(t, img)
			c.configure(key)
			c.mustUpdate()
			if out, err := c.install("trove-hello"); err != nil {
				t.Fatalf("apt-get install trove-hello on %s: %v\n%s", img.name, err, tailLines(out, 30))
			}
			if out := c.must(`/usr/bin/trove-hello`); trimLine(out) == "" {
				t.Fatalf("trove-hello printed nothing")
			}
		})
	}
	if got := delivered(); got != before {
		t.Fatalf("the test upstream delivered %d answers after its connections were cut, %d before: a client was served by pull-through, not by the mirror", got, before)
	}
}

// aptArch is the architecture of the client pods (they run on the control-plane node): amd64 or arm64.
func aptArch(t testing.TB, img aptImage) string {
	t.Helper()
	out, err := aptTry(t, img, nil, `dpkg --print-architecture`)
	if err != nil {
		t.Fatalf("dpkg --print-architecture on %s: %v\n%s", img.name, err, out)
	}
	return trimLine(out)
}
