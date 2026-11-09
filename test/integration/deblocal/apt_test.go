//go:build integration

package deblocal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/debstub/debgen"
)

// aptScript is what runs in the client container: the source with the repository's key, the credential in auth.conf.d (never in the
// URL), then the two commands of a user.
const aptScript = `set -eu
mkdir -p /etc/apt/auth.conf.d
umask 077
printf 'machine http://%s login x password %s\n' "$TROVE_HOSTPORT" "$TROVE_TOKEN" > /etc/apt/auth.conf.d/trove.conf
umask 022
echo "deb [signed-by=/cfg/key.asc] http://$TROVE_HOSTPORT/api/deb/it/deb-local stable main" > /etc/apt/sources.list.d/trove.list
rm -f /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list.d/ubuntu.sources
apt-get update --error-on=any
apt-get install -y --no-install-recommends hello-trove=2.10-3
/usr/bin/hello-trove
`

// R-DEB-010, R-DEB-016, R-DEB-029: a real APT client reads the published indexes of a local repository, verifies them against the
// repository's key without trusted=yes, and installs a package from it. It runs in debian:13 (APT verifies with sqv) and ubuntu:22.04
// (gpgv). It needs docker and the two images; without them it is skipped.
func TestR_DEB_016_AptGetUpdateAndInstallAgainstALocalRepository(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil || os.Getenv("TROVE_IT_NO_DOCKER") != "" {
		t.Skip("no docker")
	}
	e := newEnv(t)
	spec := debgen.Hello("hello-trove", "2.10-3", "amd64")
	d, err := debgen.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	a := e.mustUpload(e.tokA, "hello-trove_2.10-3_amd64.deb", d.Bytes, 201, []string{"stable"})
	e.waitIndex("stable", seqOf(e, a, "stable"))
	// The Release is dated a minute behind the database: the client's clock, the host's, must not trail it by more.
	time.Sleep(time.Second)

	dir := t.TempDir()
	key := e.get("signing-key.asc", e.tokA)
	if key.Status != 200 {
		t.Fatalf("signing-key.asc = %d", key.Status)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.asc"), key.Body, 0o644); err != nil {
		t.Fatal(err)
	}
	hostport := "trove.test:" + e.r.Addr("default")[strings.LastIndex(e.r.Addr("default"), ":")+1:]
	for _, image := range []string{"debian:13", "ubuntu:22.04"} {
		t.Run(image, func(t *testing.T) {
			cmd := exec.Command("docker", "run", "--rm", "--add-host", "trove.test:host-gateway", "-v", dir+":/cfg:ro",
				"-e", "TROVE_HOSTPORT="+hostport, "-e", "TROVE_TOKEN="+e.tokA, image, "sh", "-c", aptScript)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !strings.Contains(string(out), spec.Output()) {
				t.Errorf("the installed script did not print %q:\n%s", spec.Output(), out)
			}
		})
	}
}
