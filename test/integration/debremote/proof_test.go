//go:build integration

package debremote

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestProofServer is the harness of the by-hand proof of the remote class with real APT clients (docs/developer/deb.md, "Proven by
// hand"). It does nothing unless TROVE_DEB_PROOF_DIR names a directory: then it starts the installation, a replica and the stub, writes
// what a client needs into the directory (the repository URL, the token, the stub's key a and the host name to put in /etc/hosts) and
// serves until the file "done" appears there. The clients are `docker run --rm` containers of debian:13 and ubuntu:22.04.
func TestProofServer(t *testing.T) {
	dir := os.Getenv("TROVE_DEB_PROOF_DIR")
	if dir == "" {
		t.Skip("TROVE_DEB_PROOF_DIR is not set")
	}
	e := newEnv(t)
	port := e.r.Addr("default")
	port = port[strings.LastIndex(port, ":")+1:]
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("port", port)
	write("host", "trove.test")
	write("token", e.admin)
	write("key.asc", e.keyA)
	write("repo-keyed", "/api/deb/"+project+"/"+rKeyed+"/")
	write("repo-flat", "/api/deb/"+project+"/"+rFlat+"/")
	write("repo-flatroot", "/api/deb/"+project+"/"+rFlatRoot+"/")
	write("ready", strconv.FormatInt(time.Now().Unix(), 10))
	deadline := time.Now().Add(25 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "done")); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
}
