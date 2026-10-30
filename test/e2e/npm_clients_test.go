//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The clients pod of the npm acceptance matrix (R-NPM-045): test/e2e/clients/Dockerfile holds the eight
// gate rows, and deploy/dev/e2e.sh (marker M2 npm-clients) starts the pods trove-e2e-npm-<i>, each with a
// forwarder on 127.0.0.1:<base-URL port> and the dev CA mounted. These tests run the clients through
// `kubectl exec`; the scenarios of the matrix build on npmClientExec.

// npmGateRows are the gate rows of R-NPM-045. A row is the alias of test/e2e/clients/package.json that pins
// it and the name of its wrapper in the image.
var npmGateRows = []string{"npm10", "npm11", "npm12", "pnpm10", "pnpm11", "pnpm12", "yarn4", "yarn1"}

// npmClientPod returns the name of the i-th clients pod; a missing setting is a failure, never a skip.
func npmClientPod(t testing.TB, i int) string {
	t.Helper()
	prefix, count := os.Getenv("TROVE_E2E_NPM_CLIENTS_POD_PREFIX"), os.Getenv("TROVE_E2E_NPM_CLIENTS_PODS")
	if prefix == "" || count == "" {
		t.Fatal("TROVE_E2E_NPM_CLIENTS_POD_PREFIX and TROVE_E2E_NPM_CLIENTS_PODS are not set; run `deploy/dev/e2e.sh clients`")
	}
	n, err := strconv.Atoi(count)
	if err != nil || i < 0 || i >= n {
		t.Fatalf("clients pod %d is outside TROVE_E2E_NPM_CLIENTS_PODS=%q", i, count)
	}
	return prefix + strconv.Itoa(i)
}

// npmClientExec runs a command in clients pod i and returns its standard output.
func npmClientExec(t testing.TB, i int, argv ...string) string {
	t.Helper()
	return kn(t, append([]string{"exec", npmClientPod(t, i), "--"}, argv...)...)
}

// pinnedClientVersions reads the exact version each alias pins in test/e2e/clients/package.json, so the
// expectation has no second copy.
func pinnedClientVersions(t testing.TB) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cfg.repo, "test", "e2e", "clients", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for alias, spec := range pkg.Dependencies {
		// "npm:<package>@<version>": the version follows the last "@".
		at := strings.LastIndex(spec, "@")
		if !strings.HasPrefix(spec, "npm:") || at < len("npm:") {
			t.Fatalf("package.json pins %s as %q, want npm:<package>@<exact version>", alias, spec)
		}
		out[alias] = spec[at+1:]
	}
	return out
}

func TestNPMClients(t *testing.T) {
	// R-NPM-045: the gate has eight rows and each pins one exact version once. Every wrapper answers with
	// the pinned version, so the image and package.json cannot drift apart.
	t.Run("R-NPM-045 every gate row runs --version", func(t *testing.T) {
		pins := pinnedClientVersions(t)
		if len(pins) != len(npmGateRows) {
			t.Fatalf("package.json pins %d clients, the gate has %d rows", len(pins), len(npmGateRows))
		}
		for _, row := range npmGateRows {
			want, ok := pins[row]
			if !ok {
				t.Fatalf("package.json has no alias %s", row)
			}
			t.Run(row, func(t *testing.T) {
				got := strings.TrimSpace(npmClientExec(t, 0, row, "--version"))
				if got != want {
					t.Fatalf("%s --version printed %q, package.json pins %q", row, got, want)
				}
			})
		}
	})

	// The base URL of the setup snippets (R-NPM-041) is https://localhost:<port>. The forwarder in the pod
	// carries it to the cluster, and the client verifies the certificate against `localhost` with the
	// mounted dev CA: a plain Node request without any option of its own is the strictest client.
	t.Run("R-NPM-045 the base URL reaches the cluster with a verified certificate", func(t *testing.T) {
		port := os.Getenv("TROVE_DEV_HTTP_PORT")
		if port == "" {
			t.Fatal("TROVE_DEV_HTTP_PORT is not set")
		}
		js := fmt.Sprintf(`require('https').get('https://localhost:%s/api/v1/system/ping',r=>{console.log(r.statusCode);r.resume()}).on('error',e=>{console.error(e.message);process.exit(1)})`, port)
		if got := strings.TrimSpace(npmClientExec(t, 0, "node", "-e", js)); got != "200" {
			t.Fatalf("GET https://localhost:%s/api/v1/system/ping from the clients pod answered %q, want 200", port, got)
		}
	})
}
