package uploads

import (
	"os/exec"
	"strings"
	"testing"
)

// R-OCI-032: no manifest, tag, blob, session or permission is read from Valkey, and Valkey stopped must not break an upload. The
// session service reaches Valkey through no package it imports: neither the coordination package that holds the client nor a client
// library is among its dependencies.
func TestR_OCI_032_TheSessionServiceReachesNoValkeyClient(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "github.com/guided-traffic/trove/internal/uploads").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		switch {
		case strings.HasSuffix(pkg, "/internal/cluster"), strings.Contains(pkg, "valkey"), strings.Contains(pkg, "redis"):
			t.Errorf("internal/uploads depends on %s", pkg)
		}
	}
}
