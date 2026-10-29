//go:build integration

package valkey

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// pinned reads an image reference from tools/versions.env, the one pin file.
func pinned(t *testing.T, key string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	f, err := os.Open(filepath.Join(filepath.Dir(file), "..", "..", "..", "tools", "versions.env"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok && k == key {
			return v
		}
	}
	t.Fatalf("%s is not pinned in tools/versions.env", key)
	return ""
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// throwaway starts a standalone, plaintext Valkey of the pinned image on a
// random local port, with the stack's least-privilege ACL user, and removes it
// when the test ends. It returns the host:port.
func throwaway(t *testing.T, image string, delifeq bool) string {
	t.Helper()
	v := testenv.ValkeyConfig(t)
	name := "trove-it-throwaway-" + randHex(3)
	docker(t, "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::6379", image, "valkey-server", "--save", "", "--appendonly", "no")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	deadline := time.Now().Add(20 * time.Second)
	for {
		if out, err := exec.Command("docker", "exec", name, "valkey-cli", "ping").Output(); err == nil && strings.Contains(string(out), "PONG") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the throwaway Valkey did not start")
		}
		time.Sleep(200 * time.Millisecond)
	}
	delifeqRule := "+delifeq "
	if !delifeq {
		delifeqRule = "" // the command does not exist before 9.0, and ACL SETUSER refuses an unknown one
	}
	rule := fmt.Sprintf("user %s on >%s resetkeys ~trove:* resetchannels &trove:* -@all +get +set %s+incr +expire +multi +exec +publish +subscribe +unsubscribe +ping +hello +role +client|setname +client|setinfo", v.Username, v.Password, delifeqRule)
	args := append([]string{"exec", name, "valkey-cli", "ACL", "SETUSER"}, strings.Fields(strings.TrimPrefix(rule, "user "))...)
	if out := docker(t, args...); out != "OK" {
		t.Fatalf("ACL SETUSER: %s", out)
	}
	port := docker(t, "port", name, "6379/tcp")
	return strings.Split(strings.Split(port, "\n")[0], "->")[0]
}

func standalone(t *testing.T, addr string) config.Valkey {
	cfg := stackConfig(t, 2*time.Second)
	cfg.Mode, cfg.Address = "standalone", addr
	cfg.Sentinel = config.ValkeySentinel{TLS: true, RefreshInterval: 2 * time.Second}
	cfg.TLS, cfg.AllowInsecure = cfgTLSDisabled(), true
	return cfg
}

func TestUnsupportedVersion(t *testing.T) {
	t.Run("R-ARCH-031 a Valkey 8.1 server is refused and the client runs degraded", func(t *testing.T) {
		addr := throwaway(t, pinned(t, "INTEGRATION_VALKEY_81_IMAGE"), false)
		c := newClient(t, standalone(t, addr))
		eventually(t, "the version check", 15*time.Second, func() bool { return c.Status().Version != "" })
		st := c.Status()
		if !strings.HasPrefix(st.Version, "8.1.") || st.Available {
			t.Fatalf("status = %+v", st)
		}
		if want := "valkey " + st.Version + " is not supported; trove requires 9.0 or later"; st.DegradedReason != want {
			t.Fatalf("reason = %q, want %q", st.DegradedReason, want)
		}
		if !strings.Contains(c.Log.String(), "is not supported; trove requires 9.0 or later") {
			t.Fatalf("the refusal is not logged:\n%s", c.Log.String())
		}
		if err := c.StartupError(); err != nil {
			t.Fatalf("StartupError = %v: an unsupported version is degraded, not a configuration error", err)
		}
		k, _ := c.Key(kvFamily, "v81")
		if err := c.Set(context.Background(), k, "x"); err == nil {
			t.Fatal("Set reached an unsupported server")
		}
	})
}

// TestValkey90 runs the API against a Valkey 9.0 server, the second supported
// line, as a throwaway standalone container: switching the shared stack's line
// would recreate it under the other tests.
func TestValkey90(t *testing.T) {
	t.Run("R-ARCH-031 the API works against Valkey 9.0 with the example ACL rule", func(t *testing.T) {
		addr := throwaway(t, pinned(t, "VALKEY_90_IMAGE"), true)
		c := newClient(t, standalone(t, addr))
		waitAvailable(t, c)
		if v := c.Status().Version; !strings.HasPrefix(v, "9.0.") {
			t.Fatalf("version %q", v)
		}
		exerciseAPI(context.Background(), t, c)
	})
}
