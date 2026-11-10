//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The client pods of the Ansible acceptance tests (R-ANS-032, Q-ANS-23, Q-ANS-49): deploy/dev/ansible-clients.yaml holds the pod (a real
// ansible-galaxy of the pinned release on the pinned Python, curl, a forwarder on 127.0.0.1:<edge port>, the dev CA trusted through
// SSL_CERT_FILE), test/e2e/ansibleclients builds the two images, and deploy/dev/e2e.sh (marker F0 ansible) starts the pods
// trove-e2e-ansible-<image> in the namespace trove-e2e-ansible. The tests run the clients through `kubectl exec`, every command in a clean
// home (trove-clean), so that no ansible.cfg, ~/.netrc or collection of an earlier scenario reaches it.

// ansibleImage is one of the two clients: the pins in tools/versions.env that name it and what the image must run.
type ansibleImage struct {
	name       string // the suffix of the pod and the image: core217, core221
	versionPin string // the key of the ansible-core version in tools/versions.env
	imagePin   string // the key of the Python image in tools/versions.env
	python     string // the Python series the image runs (the release supports it, see tools/versions.env)
}

var (
	ansibleCore217 = ansibleImage{"core217", "E2E_ANSIBLECLIENTS_CORE217_VERSION", "E2E_ANSIBLECLIENTS_CORE217_IMAGE", "3.11"}
	ansibleCore221 = ansibleImage{"core221", "E2E_ANSIBLECLIENTS_CORE221_VERSION", "E2E_ANSIBLECLIENTS_CORE221_IMAGE", "3.13"}

	ansibleImages = []ansibleImage{ansibleCore217, ansibleCore221}
)

// ansiblePod returns the name of the pod of an image; a missing setting is a failure, never a skip.
func ansiblePod(t testing.TB, img ansibleImage) string {
	t.Helper()
	prefix := os.Getenv("TROVE_E2E_ANSIBLE_CLIENTS_POD_PREFIX")
	if prefix == "" {
		t.Fatal("TROVE_E2E_ANSIBLE_CLIENTS_POD_PREFIX is not set; run `deploy/dev/e2e.sh ansible-clients`")
	}
	return prefix + img.name
}

// ansibleClientsNamespace is the namespace of the client pods (Pod Security level baseline).
func ansibleClientsNamespace(t testing.TB) string {
	t.Helper()
	ns := os.Getenv("TROVE_E2E_ANSIBLE_CLIENTS_NAMESPACE")
	if ns == "" {
		t.Fatal("TROVE_E2E_ANSIBLE_CLIENTS_NAMESPACE is not set; run `deploy/dev/e2e.sh ansible-clients`")
	}
	return ns
}

// ansibleKubectl runs kubectl in the namespace of the client pods.
func ansibleKubectl(t testing.TB, timeout time.Duration, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--context", cfg.kubeContext, "-n", ansibleClientsNamespace(t)}, args...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// ansibleTry runs a shell script in a clean home in the pod of an image, with the environment given (NAME=value), and returns its output
// (both streams) and the error of a non-zero exit.
func ansibleTry(t testing.TB, img ansibleImage, env []string, script string) (string, error) {
	t.Helper()
	argv := []string{"exec", "-c", "ansible", ansiblePod(t, img), "--", "trove-clean", "env"}
	argv = append(argv, env...)
	argv = append(argv, "sh", "-c", script)
	return ansibleKubectl(t, 15*time.Minute, argv...)
}

// ansibleEdge is the host and port of the repository URLs of the setup snippets: the base URL of the edge, localhost and its port.
func ansibleEdge(t testing.TB) string { return "localhost:" + edgePort(t) }

func TestAnsibleClients(t *testing.T) {
	for _, img := range ansibleImages {
		t.Run("R-ANS-032 "+img.name+" is the pinned ansible-core on the Python of its pin", func(t *testing.T) {
			// the pins name the release and the Python image; ansible-galaxy --version says what the pod runs
			want := nugetPinned(t, img.versionPin)
			pin := nugetPinned(t, img.imagePin)
			m := regexp.MustCompile(`^docker\.io/library/python:([0-9]+\.[0-9]+)-[a-z0-9-]+@sha256:[0-9a-f]{64}$`).FindStringSubmatch(pin)
			if m == nil {
				t.Fatalf("%s %q is not python:<series>-<variant>@sha256:<digest>", img.imagePin, pin)
			}
			if m[1] != img.python {
				t.Fatalf("%s names Python %s, the test expects %s", img.imagePin, m[1], img.python)
			}
			out, err := ansibleTry(t, img, nil, `ansible-galaxy --version`)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if first := firstLine(out); first != "ansible-galaxy [core "+want+"]" {
				t.Errorf("ansible-galaxy of %s says %q, tools/versions.env pins ansible-core %s", img.name, first, want)
			}
			if !regexp.MustCompile(`(?m)^\s*python version = ` + regexp.QuoteMeta(img.python) + `\.\d+ `).MatchString(out) {
				t.Errorf("ansible-galaxy of %s runs another Python than %s:\n%s", img.name, img.python, out)
			}
		})

		// The base URL of the setup snippets is https://localhost:<port>/api/ansible/... The forwarder in the pod carries it to the cluster; the
		// client verifies the edge's certificate for `localhost` with the dev CA of SSL_CERT_FILE. Any HTTP status proves that the handshake
		// passed; a failed verification is a certificate error and never reaches trove.
		t.Run("R-ANS-032 "+img.name+" verifies the edge's certificate with the dev CA", func(t *testing.T) {
			script := fmt.Sprintf(`python3 -c 'import sys, urllib.error, urllib.request
try:
    urllib.request.urlopen(sys.argv[1])
except urllib.error.HTTPError as e:
    print(e.code)
else:
    print(200)' https://%s/api/v1/formats`, ansibleEdge(t))
			out, err := ansibleTry(t, img, nil, script)
			if err != nil {
				t.Fatalf("request the edge: %v\n%s", err, out)
			}
			if code := strings.TrimSpace(out); len(code) != 3 {
				t.Fatalf("the edge answered %q", out)
			}
		})

		t.Run("R-ANS-032 "+img.name+" starts every command in a clean home", func(t *testing.T) {
			out, err := ansibleTry(t, img, nil, `echo x >"$HOME/marker"; pwd; ls -A "$HOME"`)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			again, err := ansibleTry(t, img, nil, `ls -A "$HOME"`)
			if err != nil {
				t.Fatalf("%v\n%s", err, again)
			}
			if strings.Contains(again, "marker") || strings.TrimSpace(again) != "" {
				t.Errorf("a second command meets the state of the first: %q", again)
			}
			if !strings.Contains(out, "marker") {
				t.Errorf("the first command does not run in its home: %q", out)
			}
		})
	}
}
