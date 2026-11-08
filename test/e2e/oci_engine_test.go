//go:build e2e

package e2e

// Two rows that reach the registry the way no CLI does: a Docker Engine that names a bound host in registry-mirrors (R-OCI-029, R-EP-007) and
// the kubelet of a Kubernetes pod that pulls its image from a virtual repository with an image pull secret (R-OCI-004, R-OCI-030).

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ociEngineMirrorKey is the repository the endpoint ocimirror binds to its host (deploy/dev/values-dev.yaml endpoints.ocimirror.hostRoots).
const ociEngineMirrorKey = "oci-engine-mirror"

// TestOCIEngineMirror: the daemons of the client pods name https://oci-mirror.trove.localhost:<port> in registry-mirrors (daemon.json,
// written by test/e2e/ociclients/entrypoint-docker.sh). `docker pull app:1` asks Docker Hub's name, docker.io/library/app, and the daemon
// asks the mirror first: the bound host answers /v2/library/app/... as the root of the remote repository over the counting test upstream,
// which records the manifest request.
func TestOCIEngineMirror(t *testing.T) {
	fx := ociFx(t)
	s := ociStub(t)
	s.resetAll(t)
	defer s.resetAll(t)

	body := ociRemoteBody(t, ociEngineMirrorKey, nil)
	body["requireAuthentication"] = false
	fx.adm.do(http.MethodPost, reposRoute, body).want(t, "create "+ociEngineMirrorKey, 201, 409)
	t.Cleanup(func() { deleteRepository(t, fx.adm, ociEngineMirrorKey) })
	ensurePolicy(t, fx.adm, "e2e-oci-engine-mirror", []string{ociEngineMirrorKey}, [2]any{"anonymous", []string{"read"}})
	fx.withAnonymousRead(t, ociEngineMirrorKey)

	t.Run("R-OCI-029 R-EP-007 a Docker Engine with registry-mirrors pulls app:1 through the bound host and the upstream records one manifest request", func(t *testing.T) {
		s.reset(t)
		want := s.ociImage(t, "library/app", "1")
		c := fx.client(t, 0, ociDocker, "engine-mirror")
		c.token = "" // the daemon pulls anonymously: it holds no login for Docker Hub
		c.pull("app:1")
		if !c.holds("app:1", want.Digest) {
			t.Fatalf("docker holds %q after `docker pull app:1`, the upstream's library/app:1 is %s", c.digests("app:1"), want.Digest)
		}
		counts := s.ociCounts(t)
		if n := counts.ManifestGets("library/app", "1"); n != 1 {
			t.Fatalf("the upstream saw %d manifest GET requests for library/app:1 through the mirror, want 1: %s", n, counts.Summary())
		}
	})

	t.Run("R-OCI-029 on the bound host a path is read as sent: /v2/e2e/oci-engine-mirror/library/app is an image name of the bound repository, not a repository", func(t *testing.T) {
		s.reset(t)
		host := fmt.Sprintf("oci-mirror.trove.localhost:%s", edgePort(t))
		name := repoRef(ociEngineMirrorKey) + "/library/app"
		out := ociClientExec(t, 0, ociPodman, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--cacert", "/etc/trove-ca/ca.crt",
			"-H", "Accept: "+ociManifestAccept, "https://"+host+"/v2/"+name+"/manifests/1")
		if strings.TrimSpace(out) != "404" {
			t.Fatalf("GET /v2/%s/manifests/1 on the bound host answered %q, want 404", name, out)
		}
		// the remote asked its upstream for the name as sent: on the bound host the path has no repository segment
		if n := s.ociCounts(t).RepositoryRequests(name); n < 1 {
			t.Fatalf("the upstream saw no request for the image name %q: %s", name, s.ociCounts(t).Summary())
		}
	})
}

// ociPodManifest is a Pod that runs the forwarder image the test pushed (a static binary, so the image needs no base): it stays up.
func ociPodManifest(name, image, pullSecret string) string {
	secret := ""
	if pullSecret != "" {
		secret = "  imagePullSecrets:\n    - name: " + pullSecret + "\n"
	}
	return `apiVersion: v1
kind: Pod
metadata:
  name: ` + name + `
spec:
  terminationGracePeriodSeconds: 1
  nodeSelector: { node-role.kubernetes.io/control-plane: "" }
  tolerations: [{ key: node-role.kubernetes.io/control-plane, operator: Exists, effect: NoSchedule }]
  automountServiceAccountToken: false
` + secret + `  containers:
    - name: app
      image: ` + image + `
      imagePullPolicy: Always
`
}

// ociKubeconfig writes a kubeconfig whose current context is the suite's, in the namespace of the client pods, so that a setup snippet that
// calls kubectl without flags creates its Secret there.
func ociKubeconfig(t testing.TB) string {
	t.Helper()
	out, err := kubectl("config", "view", "--minify", "--flatten", "--context", cfg.kubeContext)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(file, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	if o, err := exec.Command("kubectl", "--kubeconfig", file, "config", "set-context", "--current", "--namespace="+ociClientsNamespace(t)).CombinedOutput(); err != nil {
		t.Fatalf("kubectl config set-context: %v\n%s", err, o)
	}
	return file
}

// TestOCIKubernetesPull: a pod whose image is in a virtual repository starts with the image pull secret of the setup snippet and does not
// start without it (R-OCI-004, R-OCI-030). The kubelet's containerd reaches the registry as localhost:<port> through the forwarder and the CA
// that deploy/dev/e2e.sh put on the node (marker T16 oci-clients); the image is a static binary, so nothing else is needed to run it.
func TestOCIKubernetes(t *testing.T) {
	fx := ociFx(t)
	host := ociRegistryHost(t)
	repos := fx.upstreamRepos(t, "k8s", nil)
	name := fx.name("k8s")
	ref := fx.image(t, repos.all, name, "1")

	c := fx.client(t, 0, ociDocker, "k8s")
	c.login(host)
	ctx := c.buildContext("", "FROM scratch\nCOPY fwd /fwd\nENTRYPOINT [\"/fwd\", \"-listen\", \"127.0.0.1:9\", \"-to\", \"127.0.0.1:1\"]\n")
	ociClientExec(t, 0, ociDocker, "cp", "/usr/local/bin/fwd", c.dir()+"/"+ctx+"/fwd")
	c.exec("build", "--tag", ref, ctx)
	c.push(ref)
	digest := fx.digestOf(t, repos.local, name, "1")

	t.Run("R-OCI-004 R-OCI-030 a pod without an image pull secret does not pull from a repository that needs a login", func(t *testing.T) {
		pod := "oci-nosecret-" + fx.run
		if out, err := ociKubectl(t, time.Minute, ociPodManifest(pod, ref, ""), "apply", "-f", "-"); err != nil {
			t.Fatalf("create the pod: %v\n%s", err, out)
		}
		defer ociKubectl(t, time.Minute, "", "delete", "pod", pod, "--ignore-not-found", "--wait=false")
		waitFor(t, "the pod without a pull secret to report a failed pull", 3*time.Minute, 2*time.Second, func() bool {
			out, _ := ociKubectl(t, time.Minute, "", "get", "pod", pod, "-o", "jsonpath={.status.containerStatuses[0].state.waiting.reason}")
			return out == "ErrImagePull" || out == "ImagePullBackOff"
		})
	})

	t.Run("R-OCI-004 R-OCI-030 R-OCI-032 a pod with the image pull secret of the setup snippet pulls its image from the virtual repository and runs it", func(t *testing.T) {
		// the secret is made by the snippet the setup API renders for the repository, unchanged
		r := fx.adm.get(repoAPI(repos.all)+"/client-setup?client=docker&credentials=env").want(t, "client setup of "+repos.all, 200)
		var setup struct {
			Snippets []struct {
				ID      string `json:"id"`
				Content string `json:"content"`
			} `json:"snippets"`
		}
		r.JSON(t, &setup)
		var snippet string
		for _, s := range setup.Snippets {
			if s.ID == "docker.configure.kubernetes" {
				snippet = s.Content
			}
		}
		if snippet == "" || !strings.Contains(snippet, "kubectl create secret") {
			t.Fatalf("the client setup of %s has no Kubernetes snippet: %+v", repos.all, setup.Snippets)
		}
		_, _ = ociKubectl(t, time.Minute, "", "delete", "secret", "trove-registry", "--ignore-not-found")
		cmd := exec.Command("sh", "-c", snippet)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+ociKubeconfig(t), "TROVE_TOKEN="+fx.token)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("the Kubernetes snippet failed: %v\n%s", err, out)
		}
		defer ociKubectl(t, time.Minute, "", "delete", "secret", "trove-registry", "--ignore-not-found")

		pod := "oci-pull-" + fx.run
		if out, err := ociKubectl(t, time.Minute, ociPodManifest(pod, ref, "trove-registry"), "apply", "-f", "-"); err != nil {
			t.Fatalf("create the pod: %v\n%s", err, out)
		}
		defer ociKubectl(t, time.Minute, "", "delete", "pod", pod, "--ignore-not-found", "--wait=false")
		if out, err := ociKubectl(t, 4*time.Minute, "", "wait", "--for=condition=Ready", "pod/"+pod, "--timeout=3m"); err != nil {
			desc, _ := ociKubectl(t, time.Minute, "", "describe", "pod", pod)
			t.Fatalf("the pod did not become ready: %v\n%s\n%s", err, out, tailLines(desc, 25))
		}
		id, err := ociKubectl(t, time.Minute, "", "get", "pod", pod, "-o", "jsonpath={.status.containerStatuses[0].imageID}")
		if err != nil || !strings.Contains(id, strings.TrimPrefix(digest, "sha256:")) {
			t.Fatalf("the pod runs the image %q (%v), want the digest %s of the pushed manifest", id, err, digest)
		}
	})
}
