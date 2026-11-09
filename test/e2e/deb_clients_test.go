//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The client pods of the Debian acceptance tests (R-DEB-035, Q-DEB-27): deploy/dev/apt-clients.yaml holds the pod (a real APT of the pinned image
// with the verifier of its release, curl, a forwarder on 127.0.0.1:<edge port>, the dev CA trusted through Acquire::https::CAInfo),
// test/e2e/aptclients builds the four images, and deploy/dev/e2e.sh (marker T17 apt-clients) starts the pods trove-e2e-apt-<image> in the
// namespace trove-e2e-apt. The tests run the clients through `kubectl exec`; the scenarios build on aptImage.run (deb_env_test.go).

// aptImage is one of the four clients: the pin in tools/versions.env that names it and what the image must be.
type aptImage struct {
	name     string // the suffix of the pod and the image: debian13, debian12, ubuntu2404, ubuntu2204
	pin      string // the key in tools/versions.env
	osID     string // ID of /etc/os-release
	version  string // VERSION_ID of /etc/os-release
	apt      string // the release series of apt (major.minor)
	verifier string // the program that checks the signatures: sqv (APT 3.0) or gpgv
}

// The four images of R-DEB-035. Ubuntu 24.04 ships APT 2.8.
var (
	aptDebian13   = aptImage{"debian13", "E2E_APTCLIENTS_DEBIAN13_IMAGE", "debian", "13", "3.0", "sqv"}
	aptDebian12   = aptImage{"debian12", "E2E_APTCLIENTS_DEBIAN12_IMAGE", "debian", "12", "2.6", "gpgv"}
	aptUbuntu2404 = aptImage{"ubuntu2404", "E2E_APTCLIENTS_UBUNTU2404_IMAGE", "ubuntu", "24.04", "2.8", "gpgv"}
	aptUbuntu2204 = aptImage{"ubuntu2204", "E2E_APTCLIENTS_UBUNTU2204_IMAGE", "ubuntu", "22.04", "2.4", "gpgv"}

	aptImages = []aptImage{aptDebian13, aptDebian12, aptUbuntu2404, aptUbuntu2204}
	// aptRotationImages are the clients of the rotation row: the two kinds of verifier and both gpgv generations (R-DEB-017).
	aptRotationImages = []aptImage{aptDebian13, aptUbuntu2404, aptUbuntu2204}
)

// aptPod returns the name of the pod of an image; a missing setting is a failure, never a skip.
func aptPod(t testing.TB, img aptImage) string {
	t.Helper()
	prefix := os.Getenv("TROVE_E2E_APT_CLIENTS_POD_PREFIX")
	if prefix == "" {
		t.Fatal("TROVE_E2E_APT_CLIENTS_POD_PREFIX is not set; run `deploy/dev/e2e.sh apt-clients`")
	}
	return prefix + img.name
}

// aptClientsNamespace is the namespace of the client pods (Pod Security level baseline).
func aptClientsNamespace(t testing.TB) string {
	t.Helper()
	ns := os.Getenv("TROVE_E2E_APT_CLIENTS_NAMESPACE")
	if ns == "" {
		t.Fatal("TROVE_E2E_APT_CLIENTS_NAMESPACE is not set; run `deploy/dev/e2e.sh apt-clients`")
	}
	return ns
}

// aptKubectl runs kubectl in the namespace of the client pods.
func aptKubectl(t testing.TB, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--context", cfg.kubeContext, "-n", aptClientsNamespace(t)}, args...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// aptTry runs a shell script in the pod of an image, with the environment given (NAME=value), and returns its output (both streams) and
// the error of a non-zero exit.
func aptTry(t testing.TB, img aptImage, env []string, script string) (string, error) {
	t.Helper()
	argv := []string{"exec", "-c", "apt", aptPod(t, img), "--", "env", "DEBIAN_FRONTEND=noninteractive"}
	argv = append(argv, env...)
	argv = append(argv, "sh", "-c", script)
	return aptKubectl(t, 15*time.Minute, argv...)
}

// aptWrite puts a file into the pod of an image.
func aptWrite(t testing.TB, img aptImage, path string, content []byte) {
	t.Helper()
	out, err := aptTry(t, img, nil, fmt.Sprintf(`mkdir -p "$(dirname %[1]s)" && printf %%s %[2]s | base64 -d >%[1]s`, shellQuoteWord(path), shellQuoteWord(base64.StdEncoding.EncodeToString(content))))
	if err != nil {
		t.Fatalf("write %s in %s: %v\n%s", path, aptPod(t, img), err, tailLines(out, 20))
	}
}

// shellQuoteWord quotes a word for sh with single quotes.
func shellQuoteWord(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// aptEdge is the host and port of the repository URLs of the setup snippets: the base URL of the edge, localhost and its port.
func aptEdge(t testing.TB) string { return "localhost:" + edgePort(t) }

func TestAPTClients(t *testing.T) {
	for _, img := range aptImages {
		t.Run("R-DEB-035 "+img.name+" is the pinned release with the verifier of its APT", func(t *testing.T) {
			// the pin names the image and the release; /etc/os-release and apt --version say what the pod runs
			pin := nugetPinned(t, img.pin)
			m := regexp.MustCompile(`^docker\.io/library/(debian|ubuntu):([0-9.]+)@sha256:[0-9a-f]{64}$`).FindStringSubmatch(pin)
			if m == nil {
				t.Fatalf("%s %q is not <debian|ubuntu>:<release>@sha256:<digest>", img.pin, pin)
			}
			if m[1] != img.osID || m[2] != img.version {
				t.Fatalf("%s names %s:%s, the test expects %s:%s", img.pin, m[1], m[2], img.osID, img.version)
			}
			out, err := aptTry(t, img, nil, `. /etc/os-release && echo "$ID $VERSION_ID" && apt-get --version | head -1 && command -v `+img.verifier)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !strings.HasPrefix(out, img.osID+" "+img.version+"\n") {
				t.Errorf("/etc/os-release of %s says %q, want %s %s", img.name, firstLine(out), img.osID, img.version)
			}
			if !regexp.MustCompile(`(?m)^apt ` + regexp.QuoteMeta(img.apt) + `\.\d+`).MatchString(out) {
				t.Errorf("apt of %s: %s, want the series %s", img.name, strings.Split(out, "\n")[1], img.apt)
			}
			if !strings.Contains(out, "/"+img.verifier+"\n") {
				t.Errorf("%s has no %s on its path:\n%s", img.name, img.verifier, out)
			}
		})

		// The base URL of the setup snippets is https://localhost:<port>/api/deb/... The forwarder in the pod carries it to the cluster; APT and
		// curl verify the edge's certificate for `localhost` with the dev CA, which the entrypoint put into Acquire::https::CAInfo and .curlrc.
		// A status line proves the handshake passed; a failed verification ends in curl's exit code 60 and never reaches trove.
		t.Run("R-DEB-035 "+img.name+" verifies the edge's certificate with the dev CA", func(t *testing.T) {
			out, err := aptTry(t, img, nil, fmt.Sprintf(`curl -sS -o /dev/null -w '%%{http_code}' https://%s/api/v1/formats`, aptEdge(t)))
			if err != nil {
				t.Fatalf("curl the edge: %v\n%s", err, out)
			}
			if code := strings.TrimSpace(out); code == "000" || len(code) != 3 {
				t.Fatalf("the edge answered %q", out)
			}
			ca, err := aptTry(t, img, nil, `grep -h CAInfo /etc/apt/apt.conf.d/*`)
			if err != nil || !strings.Contains(ca, "Acquire::https::CAInfo") {
				t.Fatalf("APT is not told to trust the dev CA: %v\n%s", err, ca)
			}
		})
	}
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }
