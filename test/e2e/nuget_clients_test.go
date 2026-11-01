//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The clients pods of the NuGet acceptance tests (R-NUGET-027): test/e2e/nugetclients/Dockerfile holds the .NET SDK
// (`dotnet`) and nuget.exe on Mono, and deploy/dev/e2e.sh (marker M3 nuget-clients) starts the pods trove-e2e-nuget-<i>,
// each with a forwarder on 127.0.0.1:<base-URL port>, the dev CA mounted and the trust of both runtimes built at start.
// The tests run the clients through `kubectl exec`; the scenarios of the NuGet suite build on nugetClientExec.

// nugetClientPod returns the name of the i-th clients pod; a missing setting is a failure, never a skip.
func nugetClientPod(t testing.TB, i int) string {
	t.Helper()
	prefix, count := os.Getenv("TROVE_E2E_NUGET_CLIENTS_POD_PREFIX"), os.Getenv("TROVE_E2E_NUGET_CLIENTS_PODS")
	if prefix == "" || count == "" {
		t.Fatal("TROVE_E2E_NUGET_CLIENTS_POD_PREFIX and TROVE_E2E_NUGET_CLIENTS_PODS are not set; run `deploy/dev/e2e.sh nuget-clients`")
	}
	n, err := strconv.Atoi(count)
	if err != nil || i < 0 || i >= n {
		t.Fatalf("clients pod %d is outside TROVE_E2E_NUGET_CLIENTS_PODS=%q", i, count)
	}
	return prefix + strconv.Itoa(i)
}

// nugetClientExec runs a command in clients pod i and returns its standard output; a failing command fails the test.
// A test that expects a failure uses nugetClientTry.
func nugetClientExec(t testing.TB, i int, argv ...string) string {
	t.Helper()
	return kn(t, append([]string{"exec", nugetClientPod(t, i), "--"}, argv...)...)
}

// nugetClientTry runs a command in clients pod i and returns its standard output and standard error together and the
// error of a non-zero exit; a failed NuGet command says why on either stream, and dotnet exits 0 on some failures.
func nugetClientTry(t testing.TB, i int, argv ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	args := append([]string{"--context", cfg.kubeContext, "-n", cfg.namespace, "exec", nugetClientPod(t, i), "--"}, argv...)
	out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
	return string(out), err
}

// nugetPinned reads one pin of tools/versions.env, so the expectation has no second copy.
func nugetPinned(t testing.TB, key string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cfg.repo, "tools", "versions.env"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `=(\S+)$`).FindSubmatch(raw)
	if m == nil {
		t.Fatalf("tools/versions.env has no %s", key)
	}
	return string(m[1])
}

func TestNuGetClients(t *testing.T) {
	// R-NUGET-027: the Linux rows run the .NET 10 SDK, and nuget.exe runs on Mono. The image takes both from
	// tools/versions.env, so the pins and the clients cannot drift apart.
	t.Run("R-NUGET-027 dotnet runs the pinned SDK", func(t *testing.T) {
		image := nugetPinned(t, "E2E_NUGETCLIENTS_SDK_IMAGE")
		m := regexp.MustCompile(`:([0-9][^@]*)@sha256:`).FindStringSubmatch(image)
		if m == nil {
			t.Fatalf("E2E_NUGETCLIENTS_SDK_IMAGE %q has no tag", image)
		}
		if got := strings.TrimSpace(nugetClientExec(t, 0, "dotnet", "--version")); got != m[1] {
			t.Fatalf("dotnet --version printed %q, tools/versions.env pins the SDK %q", got, m[1])
		}
	})

	t.Run("R-NUGET-027 nuget.exe runs the pinned version on Mono", func(t *testing.T) {
		want := nugetPinned(t, "E2E_NUGETCLIENTS_NUGET_EXE_VERSION")
		first := strings.SplitN(nugetClientExec(t, 0, "nuget", "help"), "\n", 2)[0]
		// nuget.exe prints its four-part file version: 7.9.0.83 for the release 7.9.0.
		if !strings.HasPrefix(strings.TrimSpace(first), "NuGet Version: "+want+".") {
			t.Fatalf("nuget help printed %q, tools/versions.env pins nuget.exe %s", first, want)
		}
	})

	// The base URL of the setup snippets (R-NUGET-026) is https://localhost:<port>. The forwarder in the pod carries
	// it to the cluster. Each runtime verifies the edge's certificate against `localhost` with the CA the entrypoint
	// put into its trust (SSL_CERT_FILE for .NET, the Mono store for nuget.exe). A source that trove answers with an
	// HTTP status (401 for the anonymous request, 404 for a repository that does not exist) proves the handshake
	// passed; a failed verification ends in a certificate error instead and never reaches a status.
	t.Run("R-NUGET-027 both clients verify the edge's certificate", func(t *testing.T) {
		port := os.Getenv("TROVE_DEV_HTTP_PORT")
		if port == "" {
			t.Fatal("TROVE_DEV_HTTP_PORT is not set")
		}
		source := fmt.Sprintf("https://localhost:%s/api/nuget/no-such-repository/v3/index.json", port)
		for name, argv := range map[string][]string{
			// a restore reports the HTTP status of the source (dotnet package search hides it)
			"dotnet": {
				"sh", "-c", `d=$(mktemp -d /work/verify.XXXXXX) && printf '%s' "$1" >"$d/p.csproj" && dotnet restore "$d/p.csproj" --source "$2"`,
				"sh", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup><ItemGroup><PackageReference Include="Acme.Widgets" Version="1.0.0" /></ItemGroup></Project>`, source,
			},
			"nuget.exe": {"nuget", "search", "acme", "-Source", source, "-NonInteractive"},
		} {
			t.Run(name, func(t *testing.T) {
				out, _ := nugetClientTry(t, 0, argv...)
				if regexp.MustCompile(`(?i)ssl|certificate|trust`).MatchString(out) {
					t.Fatalf("%v: the output names the certificate, the TLS verification failed:\n%s", argv, out)
				}
				if !regexp.MustCompile(`status code does not indicate success: (401|404)`).MatchString(out) {
					t.Fatalf("%v: want an HTTP status of trove (401 or 404) in the output, got:\n%s", argv, out)
				}
			})
		}
	})
}
