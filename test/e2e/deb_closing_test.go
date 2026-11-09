//go:build e2e

package e2e

// The additions of the full run of Test and Release on the commit that closes the phase (R-DEB-031, R-DEB-035): the Debian clients with Valkey
// stopped, and a read-only check against the Debian archive. The workflow starts them only when it is dispatched with closing=true; a run of
// them without TROVE_E2E_DEB_CLOSING=1 is a failure, never a skip, so they cannot go missing from the closing run (deploy/dev/e2e.sh deb-closing).

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/test/debstub"
)

func requireDebClosingRun(t testing.TB) {
	t.Helper()
	if os.Getenv("TROVE_E2E_DEB_CLOSING") != "1" {
		t.Fatal("this test belongs to the closing run: start it with deploy/dev/e2e.sh deb-closing (the closing=true dispatch of the workflow)")
	}
}

// TestDebClosing: with Valkey stopped, uploads, apt-get update and apt-get install through local, remote and virtual repositories succeed on
// all four images (R-DEB-031). Nothing of the format is read from Valkey: the rows are PostgreSQL and S3, the index builds are jobs that are
// picked up within jobs.poll_interval, and the flight of a remote file falls back to one upstream request per replica (R-ARCH-033).
func TestDebClosing(t *testing.T) {
	requireDebClosingRun(t)
	fx := debFx(t)
	s := debStub(t)
	s.resetAll(t)
	t.Cleanup(func() { s.resetAll(t) })

	local := fx.localRepo(t, "cllocal", nil)
	member := fx.localRepo(t, "clmember", nil)
	remote := fx.remoteRepo(t, "clremote", debstub.ArchiveBase+"/", nil)
	ensurePolicy(t, fx.adm, "e2e-deb-manage-cl-"+fx.run, []string{member}, [2]any{"sa:" + debPublisher, []string{"manage"}})
	virtual := "deb-clapt-" + fx.run
	fx.createRepo(t, virtual, map[string]any{
		"key": virtual, "class": "virtual", "format": "deb", "requireAuthentication": true,
		"virtual": map[string]any{"members": []string{member, remote}, "deployTarget": member},
		"deb":     map[string]any{"suites": []string{debSuite}, "components": []string{"main"}},
	})
	// the virtual repository is built before Valkey stops: its first build waits for the interval of the installation
	only := newDebPackage(t, fx.name("clvirt"), "1.0")
	fx.awaitPublished(t, member, debSuite, fx.upload(t, member, only, debSuite))
	arch := aptArch(t, aptDebian13)
	waitFor(t, "the merged index of "+virtual+" to list the packages of both members", 5*time.Minute, 2*time.Second, func() bool {
		idx, ok := fx.mergedPackages(t, virtual, arch)
		return ok && strings.Contains(idx, "Package: "+only.name+"\n") && strings.Contains(idx, "Package: trove-hello\n")
	})

	withValkeyStopped(t, func() {
		servingReadyDegraded(t, true)
		for _, img := range aptImages {
			t.Run("R-DEB-031 "+img.name+" uploads, updates and installs through a local repository with Valkey stopped", func(t *testing.T) {
				p := newDebPackage(t, fx.name("cl-"+img.name), "1.0")
				seq := fx.upload(t, local, p, debSuite)
				// with Valkey stopped the build is picked up within jobs.poll_interval (R-DEB-010)
				fx.awaitPublished(t, local, debSuite, seq)
				c := fx.client(t, img)
				c.configure(local)
				c.mustUpdate()
				c.mustInstallAndRun(p.name+"="+p.version, p.name, p.line())
			})
			t.Run("R-DEB-031 "+img.name+" updates and installs through a pull-through remote with Valkey stopped", func(t *testing.T) {
				c := fx.client(t, img)
				c.configure(remote)
				c.mustUpdate()
				if out, err := c.install("trove-hello"); err != nil {
					t.Fatalf("apt-get install trove-hello on %s: %v\n%s", img.name, err, tailLines(out, 30))
				}
			})
			t.Run("R-DEB-031 "+img.name+" installs a local and a remote package through a virtual repository with Valkey stopped", func(t *testing.T) {
				c := fx.client(t, img)
				c.configure(virtual)
				c.mustUpdate()
				c.mustInstallAndRun(only.name+"="+only.version, only.name, only.line())
				if out, err := c.install("trove-hello"); err != nil {
					t.Fatalf("apt-get install trove-hello on %s: %v\n%s", img.name, err, tailLines(out, 30))
				}
			})
		}
	})
	servingReadyDegraded(t, false)
}

// debArchiveKeyFingerprint is the fingerprint of the Debian archive key of Debian 13 (trixie), "Debian Archive Automatic Signing Key
// (13/trixie)", as ftp-master.debian.org publishes it. The test fetches the key and refuses it unless the fingerprint is this one.
const debArchiveKeyFingerprint = "04B54C3CDCA79751B16BC6B5225629DF75B188BD"

// debPublicRequestBound is the most requests the read-only check may send to the Debian archive (R-DEB-035).
const debPublicRequestBound = 30

// TestDebClosingPublic is the read-only check against the Debian archive (R-DEB-035): a pull-through remote of https://deb.debian.org/debian/
// with Debian's archive key, fetched here and checked against a pinned fingerprint, and `apt-get install hello` in debian:13, anonymously. Nothing
// is changed upstream. The archive's request count cannot be read from outside; the remote counts the requests it sent (trove_remote_upstream_requests_total).
func TestDebClosingPublic(t *testing.T) {
	requireDebClosingRun(t)
	fx := debFx(t)

	resp, err := (&http.Client{Timeout: time.Minute}).Get("https://ftp-master.debian.org/keys/archive-key-13.asc")
	if err != nil {
		t.Fatalf("fetch the archive key: %v", err)
	}
	armored, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("fetch the archive key: %d %v", resp.StatusCode, err)
	}
	certs, err := openpgp.ParseCertificates(armored, time.Now(), openpgp.Limits{})
	if err != nil {
		t.Fatalf("the archive key does not parse: %v", err)
	}
	if len(certs) != 1 || !strings.EqualFold(certs[0].Fingerprint, debArchiveKeyFingerprint) {
		got := make([]string, len(certs))
		for i, c := range certs {
			got[i] = c.Fingerprint
		}
		t.Fatalf("the key ftp-master.debian.org serves has the fingerprints %v, the test pins %s", got, debArchiveKeyFingerprint)
	}

	key := "deb-public-" + fx.run
	fx.createRepo(t, key, map[string]any{
		"key": key, "class": "remote", "format": "deb", "requireAuthentication": true,
		"remote": map[string]any{"url": "https://deb.debian.org/debian/", "mode": "pullThrough"},
		"deb":    map[string]any{"upstreamKeys": string(armored)},
	})

	t.Run("R-DEB-019 R-DEB-020 R-DEB-035 debian:13 installs hello from the Debian archive through the remote, anonymously, within 30 upstream requests", func(t *testing.T) {
		img := aptDebian13
		c := fx.client(t, img)
		c.configure(key)
		// the configure snippet names the suite stable; the check names the release it was written for
		aptWrite(t, img, debSourceFile(key), []byte("Types: deb\nURIs: "+debRepoURL(t, key)+"\nSuites: trixie\nComponents: main\nSigned-By: "+debKeyFile(key)+"\n"))
		c.mustUpdate()
		if out, err := c.install("hello"); err != nil {
			t.Fatalf("apt-get install hello: %v\n%s", err, tailLines(out, 30))
		}
		if out := c.must(`/usr/bin/hello`); !strings.Contains(out, "Hello, world!") {
			t.Fatalf("hello printed %q", trimLine(out))
		}
		n := sumSeries(t, "trove_remote_upstream_requests_total", key+`"`)
		t.Logf("the remote sent %d requests to deb.debian.org", int(n))
		if n < 1 || n > debPublicRequestBound {
			t.Fatalf("the remote sent %d requests to the Debian archive, want 1 to %d", int(n), debPublicRequestBound)
		}
	})
}
