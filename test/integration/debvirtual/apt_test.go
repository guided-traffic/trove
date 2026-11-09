//go:build integration

package debvirtual

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/debstub/debgen"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// aptScript is what runs in the client container: the source with the virtual repository's key, the credential in auth.conf.d (never in
// the URL), then the commands of a user. The packages come one from each member: hello-trove from the local member, trove-hello (and its
// dependency libtrove1) from the remote member, through the member paths of the merged index.
const aptScript = `set -eu
mkdir -p /etc/apt/auth.conf.d
umask 077
printf 'machine http://%s login x password %s\n' "$TROVE_HOSTPORT" "$TROVE_TOKEN" > /etc/apt/auth.conf.d/trove.conf
umask 022
echo "deb [signed-by=/cfg/key.asc arch=amd64] http://$TROVE_HOSTPORT/api/deb/it/apt stable main" > /etc/apt/sources.list.d/trove.list
rm -f /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list.d/ubuntu.sources
apt-get update --error-on=any -o Debug::pkgAcquire::Worker=1
apt-get install -y --no-install-recommends hello-trove=2.10-3 trove-hello
/usr/bin/hello-trove
dpkg-query -W -f='${Package} ${Version} ${db:Status-Status}\n' hello-trove trove-hello libtrove1
`

// R-DEB-025, R-DEB-027: a real APT client reads the merged and re-signed indexes of a virtual repository, verifies them against the
// virtual repository's own key without trusted=yes, and installs a package of the local member and one of the remote member through the
// member paths. It runs in debian:13 (APT verifies with sqv) and ubuntu:22.04 (gpgv). The replica runs without Valkey (the stack's
// default), so this is also the closing run of R-DEB-031 for the virtual class.
func TestR_DEB_025_AptGetUpdateAndInstallThroughAVirtualRepository(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil || os.Getenv("TROVE_IT_NO_DOCKER") != "" {
		t.Skip("no docker")
	}
	e := newEnv(t, options{})
	spec := debgen.Hello("hello-trove", "2.10-3", "amd64")
	d, err := debgen.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	if a := e.uploadBytes(rLocal, e.admin, "hello-trove_2.10-3_amd64.deb", d.Bytes, "stable"); a.Status != 201 {
		t.Fatalf("upload = %d %s", a.Status, a.Body)
	}
	testenv.WaitFor(t, "the virtual index to list both members", 90*time.Second, func() bool {
		if s, ok := e.suite(rApt, "stable"); !ok || s.Generation == 0 {
			return false
		}
		list := e.packages(rApt, "stable", e.admin)
		_, local := find(list, "hello-trove", "2.10-3")
		_, remote := find(list, "trove-hello", "")
		return local && remote
	})

	dir := t.TempDir()
	key := e.get(rApt, "signing-key.asc", e.admin)
	if key.Status != 200 {
		t.Fatalf("signing-key.asc = %d", key.Status)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.asc"), key.Body, 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		// The server saw the member paths of the merged index, one request for each member's package.
		log := e.r.Logs.String()
		for _, want := range []string{"/apt/members/" + rLocal + "/pool/h/hello-trove/", "/apt/members/" + rRemote + "/pool/main/t/trove-hello/"} {
			if !strings.Contains(log, want) {
				t.Errorf("no request for %s in the log of the replica", want)
			}
		}
	}()
	addr := e.r.Addr("default")
	hostport := "trove.test:" + addr[strings.LastIndex(addr, ":")+1:]
	for _, image := range []string{"debian:13", "ubuntu:22.04"} {
		t.Run(image, func(t *testing.T) {
			cmd := exec.Command("docker", "run", "--rm", "--platform", "linux/amd64", "--add-host", "trove.test:host-gateway", "-v", dir+":/cfg:ro",
				"-e", "TROVE_HOSTPORT="+hostport, "-e", "TROVE_TOKEN="+e.alice, image, "sh", "-c", aptScript)
			b, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, b)
			}
			out := string(b)
			if !strings.Contains(out, spec.Output()) {
				t.Errorf("the installed script did not print %q:\n%s", spec.Output(), out)
			}
			for _, want := range []string{"hello-trove 2.10-3 installed", "trove-hello ", "libtrove1 "} {
				if !strings.Contains(out, want) {
					t.Errorf("the package state lacks %q:\n%s", want, out)
				}
			}
		})
	}
}
