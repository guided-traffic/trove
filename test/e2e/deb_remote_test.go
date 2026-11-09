//go:build e2e

package e2e

// The remote class of the Debian format with the real clients (R-DEB-035, R-DEB-019 to R-DEB-023): a pull-through remote over the counting
// Debian archive test upstream (deploy/dev/debstub.yaml, test/debstub), whose upstream keys are the stub's key a. The release is the
// upstream's own InRelease, passed through byte for byte, so the clients verify the upstream's signature with the key the repository serves as
// signing-key.asc. What the upstream saw is the stub's count.

import (
	"regexp"
	"testing"

	"github.com/guided-traffic/trove/test/debstub"
)

func TestDebRemote(t *testing.T) {
	fx := debFx(t)
	s := debStub(t)
	s.resetAll(t)
	s.debMode(t, debstub.Mode{})
	t.Cleanup(func() { s.resetAll(t) })

	repo := fx.remoteRepo(t, "debian", debstub.ArchiveBase+"/", nil)
	hello := regexp.MustCompile(`^hello from trove-hello \S+ \((amd64|arm64)\)$`)
	var afterFirst int

	for i, img := range aptImages {
		t.Run("R-DEB-035 R-DEB-019 R-DEB-022 "+img.name+" updates and installs trove-hello through the pull-through remote", func(t *testing.T) {
			c := fx.client(t, img)
			c.configure(repo)
			c.mustUpdate()
			if out, err := c.install("trove-hello"); err != nil {
				t.Fatalf("apt-get install trove-hello on %s: %v\n%s", img.name, err, tailLines(out, 30))
			}
			// trove-hello depends on libtrove1 (>= 1.2): both come from the remote, and the script is the package's
			if out := c.must(`/usr/bin/trove-hello`); !hello.MatchString(trimLine(out)) {
				t.Fatalf("trove-hello printed %q", trimLine(out))
			}
			if out := c.must(`dpkg-query -W -f '${Version}\n' libtrove1`); trimLine(out) == "" {
				t.Fatalf("libtrove1 is not installed after trove-hello: %q", out)
			}
			if i == 0 {
				afterFirst = s.debCounts(t).Total()
				if afterFirst == 0 {
					t.Fatalf("the test upstream saw no request after the first client installed through the remote")
				}
			}
		})
	}

	// R-DEB-021, R-DEB-022: the index files and the package files are in the remote's cache after the first client, so the three clients
	// after it (another image, another replica for nearly every request) cost the upstream nothing.
	t.Run("R-DEB-019 R-DEB-021 R-DEB-022 the clients after the first cost the upstream no request", func(t *testing.T) {
		if got := s.debCounts(t).Total(); got != afterFirst {
			t.Fatalf("the test upstream saw %d requests after the four clients, %d after the first: %+v", got, afterFirst, s.debCounts(t).Entries)
		}
	})
}

// TestDebFlatRoot is the flat root `deb <repo URL> ./` (R-DEB-003, Q-DEB-39): APT asks for <repo URL>/./InRelease, the remote's upstream URL
// is a flat directory itself, and Ubuntu 24.04 updates and installs from it with the verification on.
func TestDebFlatRoot(t *testing.T) {
	fx := debFx(t)
	s := debStub(t)
	s.resetAll(t)
	t.Cleanup(func() { s.resetAll(t) })

	repo := fx.remoteRepo(t, "flatroot", debstub.FlatRootBase+"/", nil)
	img := aptUbuntu2404
	t.Run("R-DEB-003 R-DEB-035 "+img.name+" updates and installs from the flat root of a remote", func(t *testing.T) {
		c := fx.client(t, img)
		// the configure snippet installs the key and the credential; its source names the suite stable, which a flat repository has not: the
		// source of a flat root is `deb <repo URL> ./` (deb822: Suites: ./ and no Components)
		c.configure(repo)
		aptWrite(t, img, debSourceFile(repo), []byte("Types: deb\nURIs: "+"https://"+aptEdge(t)+debRepoPath(repo)+"\nSuites: ./\nSigned-By: "+debKeyFile(repo)+"\n"))
		c.mustUpdate()
		if out, err := c.install("trove-flat"); err != nil {
			t.Fatalf("apt-get install trove-flat: %v\n%s", err, tailLines(out, 30))
		}
		if out := c.must(`/usr/bin/trove-flat`); !regexp.MustCompile(`^hello from trove-flat \S+ \(\w+\)$`).MatchString(trimLine(out)) {
			t.Fatalf("trove-flat printed %q", trimLine(out))
		}
		// APT asks the remote for <repo URL>/./InRelease (Q-DEB-39); the remote asks its upstream for the canonical path of the directory
		if n := s.debCounts(t).Requests("GET", debstub.FlatRootBase+"/InRelease"); n == 0 {
			t.Fatalf("the upstream saw no GET %s/InRelease: %+v", debstub.FlatRootBase, s.debCounts(t).Entries)
		}
	})
}
