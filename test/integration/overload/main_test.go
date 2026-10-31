//go:build integration

// Package overload runs the trove binary against the integration stack with a
// small memory budget and drives more concurrent downloads than fit: the replica
// sheds the excess with 503 and stays alive (R-ARCH-005, R-HA-036). It runs the
// binary rather than the in-process replica because the memory limit and the
// heap it bounds belong to one process.
package overload

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// binary is the trove executable built once for the package.
var binary string

func TestMain(m *testing.M) {
	if err := testenv.CheckTLS(); err != nil {
		fmt.Fprintf(os.Stderr, "integration environment: %v\n", err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "trove-it-overload-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "trove")
	cmd := exec.Command("go", "build", "-o", binary, "github.com/guided-traffic/trove/cmd/trove")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
