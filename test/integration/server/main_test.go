//go:build integration

// Package server runs the real trove binary against the integration stack:
// PostgreSQL, Valkey with Sentinel over TLS and an ACL user, SeaweedFS. It
// proves the replica lifecycle end to end: migrate, start the roles, probes,
// identification headers, the request log, the connection names and the
// drain-safe shutdown under load.
package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// binary is the trove executable built once for the package.
var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "trove-it-server-")
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
