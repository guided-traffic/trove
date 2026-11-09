//go:build e2e

package e2e

// The virtual class of the Debian format with the real clients (R-DEB-035, R-DEB-025 to R-DEB-027): `apt` merges a local member (the
// deployment target) and a verified remote member over the counting Debian archive test upstream, signs the merged indexes with its own key,
// and the four images update and install a package of each member from it. The claim row: a name the local member published first is
// claimed by it (R-REPO-023), so the remote's paragraphs of that name are not merged and the client installs the local version. The row is
// written against the spec and switched on by debVirtualBuilt when the virtual class of the format (T-17 WP8) is on main.

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/debstub"
)

// debVirtualBuilt says whether the virtual class of the Debian format is on main. While it is not, the row is skipped, with this reason.
var debVirtualBuilt = true

func TestDebVirtual(t *testing.T) {
	if !debVirtualBuilt {
		t.Skip("the virtual class of the Debian format (T-17 WP8) is not on main: a virtual Debian repository answers 501 until its index build is built")
	}
	fx := debFx(t)
	s := debStub(t)
	s.resetAll(t)
	t.Cleanup(func() { s.resetAll(t) })

	local := fx.localRepo(t, "vlocal", nil)
	remote := fx.remoteRepo(t, "vremote", debstub.ArchiveBase+"/", nil)
	// a name a remote member of the virtual repository serves may be published into the local member only by a holder of manage (R-REPO-048)
	ensurePolicy(t, fx.adm, "e2e-deb-manage-"+fx.run, []string{local}, [2]any{"sa:" + debPublisher, []string{"manage"}})

	virtual := "deb-apt-" + fx.run
	fx.createRepo(t, virtual, map[string]any{
		"key": virtual, "class": "virtual", "format": "deb", "requireAuthentication": true,
		"virtual": map[string]any{"members": []string{local, remote}, "deployTarget": local},
		"deb":     map[string]any{"suites": []string{debSuite}, "components": []string{"main"}},
	})

	// a package only the local member has, and a name the remote serves in a higher version: the first upload claims it
	only := newDebPackage(t, fx.name("virt"), "1.0")
	claimed := newDebPackage(t, "trove-tools", "0.1")
	seq := fx.upload(t, local, only, debSuite)
	fx.awaitPublished(t, local, debSuite, seq)
	fx.awaitPublished(t, local, debSuite, fx.upload(t, local, claimed, debSuite))

	// the virtual repository builds its indexes by interval; its Release names the merged files
	// (the delay bound is deb.virtual_index_interval + 60 s, R-DEB-026): wait until the merged index lists both members' packages
	arch := aptArch(t, aptDebian13)
	waitFor(t, "the merged index of "+virtual+" to list the packages of both members", 5*time.Minute, 2*time.Second, func() bool {
		idx, ok := fx.mergedPackages(t, virtual, arch)
		return ok && strings.Contains(idx, "Package: "+only.name+"\n") && strings.Contains(idx, "Package: trove-hello\n")
	})

	for _, img := range aptImages {
		t.Run("R-DEB-025 R-DEB-027 R-DEB-035 "+img.name+" installs a local and a remote package through the virtual repository", func(t *testing.T) {
			c := fx.client(t, img)
			c.configure(virtual)
			c.mustUpdate()
			c.mustInstallAndRun(only.name+"="+only.version, only.name, only.line())
			// a package of the remote member (with its dependency)
			if out, err := c.install("trove-hello"); err != nil {
				t.Fatalf("apt-get install trove-hello on %s: %v\n%s", img.name, err, tailLines(out, 30))
			}
			if out := c.must(`/usr/bin/trove-hello`); !strings.HasPrefix(trimLine(out), "hello from trove-hello ") {
				t.Fatalf("trove-hello printed %q", trimLine(out))
			}
		})

		t.Run("R-DEB-025 R-REPO-023 "+img.name+" installs the claimed name from the local member although the remote has a higher version", func(t *testing.T) {
			c := fx.client(t, img)
			c.configure(virtual)
			c.mustUpdate()
			c.mustInstallAndRun("trove-tools", claimed.name, claimed.line())
		})
	}

	t.Run("R-DEB-025 the merged index of an architecture lists the architecture all packages the stub keeps in binary-all only", func(t *testing.T) {
		idx, ok := fx.mergedPackages(t, virtual, arch)
		if !ok || countParagraphs(idx, "trove-config") != 1 {
			t.Fatalf("the merged Packages of %s lists trove-config %d times, want once (ok=%v)", arch, countParagraphs(idx, "trove-config"), ok)
		}
	})

	t.Run("R-DEB-025 the merged index lists the claimed name once, from the local member", func(t *testing.T) {
		idx, ok := fx.mergedPackages(t, virtual, arch)
		if !ok {
			t.Fatal("the merged Packages.gz is not served")
		}
		if n := countParagraphs(idx, "trove-tools"); n != 1 {
			t.Fatalf("the merged Packages lists trove-tools %d times, want once:\n%.2000s", n, idx)
		}
		if !strings.Contains(idx, "members/"+local+"/") {
			t.Errorf("the merged Packages names no file of the member %s", local)
		}
	})
}

// mergedPackages reads the merged Packages.gz of the main component for an architecture; ok is false while it is not served.
func (fx *debFixture) mergedPackages(t testing.TB, key, arch string) (string, bool) {
	t.Helper()
	r, err := fx.registry(t).send(http.MethodGet, debRepoPath(key)+"/dists/"+debSuite+"/main/binary-"+arch+"/Packages.gz", nil)
	if err != nil || r.Status != 200 {
		return "", false
	}
	zr, err := gzip.NewReader(bytes.NewReader(r.Body))
	if err != nil {
		t.Fatalf("Packages.gz of %s is not gzip: %v", key, err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("Packages.gz of %s: %v", key, err)
	}
	return string(b), true
}

// countParagraphs counts the paragraphs of a Packages index for a package name.
func countParagraphs(index, name string) int {
	n := 0
	for _, para := range strings.Split(index, "\n\n") {
		for _, line := range strings.Split(para, "\n") {
			if line == "Package: "+name {
				n++
			}
		}
	}
	return n
}
