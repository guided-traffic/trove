//go:build integration

package deblocal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/debstub/debgen"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// aptScript is what runs in the client container: the source with the repository's key, the credential in auth.conf.d (never in the
// URL), then the two commands of a user. With TROVE_DEBUG_URIS the update prints the URIs the client requests.
const aptScript = `set -eu
mkdir -p /etc/apt/auth.conf.d
umask 077
printf 'machine http://%s login x password %s\n' "$TROVE_HOSTPORT" "$TROVE_TOKEN" > /etc/apt/auth.conf.d/trove.conf
umask 022
echo "deb [signed-by=/cfg/key.asc] http://$TROVE_HOSTPORT/api/deb/it/deb-local stable main" > /etc/apt/sources.list.d/trove.list
rm -f /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list.d/ubuntu.sources
apt-get update --error-on=any -o Debug::pkgAcquire::Worker=1
apt-get install -y --no-install-recommends hello-trove=2.10-3
/usr/bin/hello-trove
`

// runApt runs apt-get update and apt-get install in a client container of each image against the replica and returns the output of
// each. It needs docker and the images; without docker the test is skipped.
func runApt(t *testing.T, e *env, images ...string) map[string]string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil || os.Getenv("TROVE_IT_NO_DOCKER") != "" {
		t.Skip("no docker")
	}
	spec := debgen.Hello("hello-trove", "2.10-3", "amd64")
	d, err := debgen.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	a := e.mustUpload(e.tokA, "hello-trove_2.10-3_amd64.deb", d.Bytes, 201, []string{"stable"})
	e.waitIndex("stable", seqOf(e, a, "stable"))

	dir := t.TempDir()
	key := e.get("signing-key.asc", e.tokA)
	if key.Status != 200 {
		t.Fatalf("signing-key.asc = %d", key.Status)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.asc"), key.Body, 0o644); err != nil {
		t.Fatal(err)
	}
	hostport := "trove.test:" + e.r.Addr("default")[strings.LastIndex(e.r.Addr("default"), ":")+1:]
	out := map[string]string{}
	for _, image := range images {
		t.Run(image, func(t *testing.T) {
			args := append([]string{"run", "--rm"}, testenv.DockerReachHost("trove.test")...)
			args = append(args, "-v", dir+":/cfg:ro", "-e", "TROVE_HOSTPORT="+hostport, "-e", "TROVE_TOKEN="+e.tokA, image, "sh", "-c", aptScript)
			cmd := exec.Command("docker", args...)
			b, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, b)
			}
			if !strings.Contains(string(b), spec.Output()) {
				t.Errorf("the installed script did not print %q:\n%s", spec.Output(), b)
			}
			out[image] = string(b)
		})
	}
	return out
}

// R-DEB-010, R-DEB-016, R-DEB-029: a real APT client reads the published indexes of a local repository, verifies them against the
// repository's key without trusted=yes, and installs a package from it. It runs in debian:13 (APT verifies with sqv) and ubuntu:22.04
// (gpgv). The replica runs without Valkey (the stack's default), so this is also the closing run of R-DEB-031 for the local class.
func TestR_DEB_016_AptGetUpdateAndInstallAgainstALocalRepository(t *testing.T) {
	runApt(t, newEnv(t), "debian:13", "ubuntu:22.04")
}

// R-DEB-011: with deb.compression listing xz, apt-get update in Debian 13 fetches Packages.xz by hash.
func TestR_DEB_011_AptFetchesPackagesXZByHash(t *testing.T) {
	e := newEnvOpts(t, envOpts{cfg: `{"deb":{"compression":["xz"]}}`})
	outs := runApt(t, e, "debian:13")
	if len(outs) == 0 {
		return
	}
	var xz, gz string
	rel := paragraphs(t, e.get("dists/stable/Release", e.tokA).Body)[0]
	for _, l := range strings.Split(rel.Value("SHA256"), "\n")[1:] {
		f := strings.Fields(l)
		switch f[2] {
		case "main/binary-amd64/Packages.xz":
			xz = f[0]
		case "main/binary-amd64/Packages.gz":
			gz = f[0]
		}
	}
	if xz == "" || gz == "" {
		t.Fatalf("the Release lists xz %q and gz %q", xz, gz)
	}
	if !strings.Contains(outs["debian:13"], "by-hash/SHA256/"+xz) {
		t.Errorf("the client did not request Packages.xz by its hash %s", xz)
	}
	if strings.Contains(outs["debian:13"], "by-hash/SHA256/"+gz) {
		t.Errorf("the client requested Packages.gz although xz is listed")
	}
}
