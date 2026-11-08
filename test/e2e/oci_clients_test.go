//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The client pods of the OCI acceptance tests (R-OCI-035, Q-OCI-21): deploy/dev/oci-clients.yaml holds the pod (the Docker Engine with
// Buildx in the container `docker`, Podman in the container `podman`, a forwarder on 127.0.0.1:<edge port>, the dev CA trusted by both),
// test/e2e/ociclients builds its two images, and deploy/dev/e2e.sh (marker T16 oci-clients) starts the pods trove-e2e-oci-<i> in the
// namespace trove-e2e-clients, which admits privileged pods. The tests run the clients through `kubectl exec`; the scenarios build on
// ociClientExec and ociClientTry.

// The containers of a client pod.
const (
	ociDocker = "docker"
	ociPodman = "podman"
)

// ociClientPod returns the name of the i-th clients pod; a missing setting is a failure, never a skip.
func ociClientPod(t testing.TB, i int) string {
	t.Helper()
	prefix, count := os.Getenv("TROVE_E2E_OCI_CLIENTS_POD_PREFIX"), os.Getenv("TROVE_E2E_OCI_CLIENTS_PODS")
	if prefix == "" || count == "" {
		t.Fatal("TROVE_E2E_OCI_CLIENTS_POD_PREFIX and TROVE_E2E_OCI_CLIENTS_PODS are not set; run `deploy/dev/e2e.sh oci-clients`")
	}
	n, err := strconv.Atoi(count)
	if err != nil || i < 0 || i >= n {
		t.Fatalf("OCI clients pod %d is outside TROVE_E2E_OCI_CLIENTS_PODS=%q", i, count)
	}
	return prefix + strconv.Itoa(i)
}

// ociClientsNamespace is the namespace of the client pods (Pod Security level privileged).
func ociClientsNamespace(t testing.TB) string {
	t.Helper()
	ns := os.Getenv("TROVE_E2E_OCI_CLIENTS_NAMESPACE")
	if ns == "" {
		t.Fatal("TROVE_E2E_OCI_CLIENTS_NAMESPACE is not set; run `deploy/dev/e2e.sh oci-clients`")
	}
	return ns
}

// ociKubectl runs kubectl in the namespace of the client pods.
func ociKubectl(t testing.TB, timeout time.Duration, stdin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--context", cfg.kubeContext, "-n", ociClientsNamespace(t)}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// ociClientTry runs a command in a container of client pod i and returns its output (both streams) and the error of a non-zero exit.
func ociClientTry(t testing.TB, i int, container string, argv ...string) (string, error) {
	t.Helper()
	return ociKubectl(t, 20*time.Minute, "", append([]string{"exec", "-c", container, ociClientPod(t, i), "--"}, argv...)...)
}

// ociClientExec is ociClientTry for a command that must succeed; it returns the output.
func ociClientExec(t testing.TB, i int, container string, argv ...string) string {
	t.Helper()
	out, err := ociClientTry(t, i, container, argv...)
	if err != nil {
		t.Fatalf("%s in pod %d (%s): %v\n%s", strings.Join(argv, " "), i, container, err, tailLines(out, 30))
	}
	return out
}

// ociPin makes the forwarder of client pod i relay to one packages pod's listener (pod IP, port 8080) instead of the edge, through the
// target file of the forwarder (test/e2e/clients/fwd); the registry host and the certificate the clients verify stay those of the edge.
func ociPin(t testing.TB, i int, webPodIP string) {
	t.Helper()
	ociForwarderTarget(t, i, webPodIP+":8080")
}
func ociUnpin(t testing.TB, i int) { t.Helper(); ociForwarderTarget(t, i, "") }

func ociForwarderTarget(t testing.TB, i int, target string) {
	t.Helper()
	ociClientExec(t, i, ociDocker, "sh", "-c", fmt.Sprintf("printf '%%s' '%s' > /tmp/target", target))
}

// ociArch is the architecture of the client pods (they run on the control-plane node): amd64 or arm64.
func ociArch(t testing.TB) string {
	t.Helper()
	switch m := strings.TrimSpace(ociClientExec(t, 0, ociDocker, "uname", "-m")); m {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		t.Fatalf("the client pods run on the architecture %q", m)
		return ""
	}
}

// ociRegistryHost is the host of every image reference of the setup snippets: the base URL of the edge, localhost and its port.
func ociRegistryHost(t testing.TB) string { return "localhost:" + edgePort(t) }

func TestOCIClients(t *testing.T) {
	host := ociRegistryHost(t)

	// R-OCI-035: the clients are the pinned ones. The tags of tools/versions.env are the versions the tests expect, so the pins and
	// the clients cannot drift apart.
	t.Run("R-OCI-035 the Docker Engine, its CLI and Buildx run at the pinned version", func(t *testing.T) {
		image := nugetPinned(t, "E2E_OCICLIENTS_DOCKER_IMAGE")
		m := regexp.MustCompile(`:([0-9][^@-]*)-dind@sha256:`).FindStringSubmatch(image)
		if m == nil {
			t.Fatalf("E2E_OCICLIENTS_DOCKER_IMAGE %q has no version tag", image)
		}
		for _, side := range []string{"Client", "Server"} {
			got := strings.TrimSpace(ociClientExec(t, 0, ociDocker, "docker", "version", "--format", "{{."+side+".Version}}"))
			if got != m[1] {
				t.Fatalf("docker version %s: %q, tools/versions.env pins %s", side, got, m[1])
			}
		}
		if out := ociClientExec(t, 0, ociDocker, "docker", "buildx", "version"); !strings.Contains(out, "buildx") {
			t.Fatalf("docker buildx version printed %q", out)
		}
	})

	t.Run("R-OCI-035 Podman runs at the pinned version", func(t *testing.T) {
		image := nugetPinned(t, "E2E_OCICLIENTS_PODMAN_IMAGE")
		m := regexp.MustCompile(`:v([0-9][^@]*)@sha256:`).FindStringSubmatch(image)
		if m == nil {
			t.Fatalf("E2E_OCICLIENTS_PODMAN_IMAGE %q has no version tag", image)
		}
		got := strings.TrimSpace(ociClientExec(t, 0, ociPodman, "podman", "--version"))
		if got != "podman version "+m[1] {
			t.Fatalf("podman --version printed %q, tools/versions.env pins %s", got, m[1])
		}
	})

	// The base URL of the setup snippets is localhost:<port>. The forwarder in the pod carries it to the cluster; both clients verify the
	// edge's certificate for `localhost` with the dev CA put into their trust at pod start. A refused login that names the credential
	// proves the handshake passed: the registry answered, with a token request the client could make; a failed verification ends in a
	// certificate error and never reaches the registry.
	for name, argv := range map[string][]string{
		ociDocker: {"docker", "login", host, "-u", "nobody", "--password-stdin"},
		ociPodman: {"podman", "login", host, "-u", "nobody", "--password-stdin"},
	} {
		t.Run("R-OCI-035 R-OCI-004 "+name+" verifies the edge's certificate and the registry refuses a wrong credential", func(t *testing.T) {
			out, err := ociKubectl(t, 2*time.Minute, "not-a-token", append([]string{"exec", "-i", "-c", name, ociClientPod(t, 0), "--"}, argv...)...)
			if err == nil {
				t.Fatalf("%v succeeded with a wrong credential:\n%s", argv, out)
			}
			if regexp.MustCompile(`(?i)x509|certificate|tls:`).MatchString(out) {
				t.Fatalf("%v: the output names the certificate, the TLS verification failed:\n%s", argv, out)
			}
			// Docker treats localhost as a registry that may be plain HTTP: after the refusal over TLS it tries HTTP and reports that last
			// failure (an EOF), so the refusal over TLS is read from the daemon's log.
			if name == ociDocker {
				logs, _ := ociKubectl(t, time.Minute, "", "logs", "-c", ociDocker, ociClientPod(t, 0), "--tail=300")
				out += "\n" + logs
			}
			if !regexp.MustCompile(`(?i)unauthorized|denied|401|invalid username/password`).MatchString(out) {
				t.Fatalf("%v: want a refusal by the registry (unauthorized), got:\n%s", argv, out)
			}
		})
	}
}
