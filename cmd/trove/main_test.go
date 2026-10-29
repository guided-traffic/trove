package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCmd(t *testing.T, env []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, streams{stdout: &o, stderr: &e, environ: env})
	return code, o.String(), e.String()
}

func baseEnv(extra ...string) []string {
	return append([]string{
		"TROVE_DATABASE_HOST=db.example", "TROVE_DATABASE_NAME=trove",
		"TROVE_VALKEY_SENTINEL_ADDRESSES=[\"s1:26379\"]", "TROVE_VALKEY_SENTINEL_PRIMARY_NAME=trove",
		"TROVE_STORAGE_S3_BUCKET=blobs",
		"TROVE_SECRETS_ENCRYPTION_KEY_FILES=[\"/keys/2026-10.key\"]", "TROVE_SECRETS_ACTIVE_KEY_ID=2026-10",
	}, extra...)
}

// ephemeral keeps the serve tests off the default ports.
var ephemeral = []string{"TROVE_SERVER_LISTEN=127.0.0.1:0", "TROVE_SERVER_OPS_LISTEN=localhost:0"}

func TestServe(t *testing.T) {
	t.Run("R-ARCH-001 serve without --role and without process.role prints the role message", func(t *testing.T) {
		cfg := writeConfig(t, "log:\n  level: info\n")
		code, _, stderr := runCmd(t, baseEnv(ephemeral...), "serve", "--config", cfg)
		if code != 1 || !strings.Contains(stderr, "process.role must be web, worker or all") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})
	t.Run("R-ARCH-001 --role is accepted and serve starts: a missing key ring file stops it", func(t *testing.T) {
		cfg := writeConfig(t, "log:\n  level: info\n")
		code, _, stderr := runCmd(t, baseEnv(ephemeral...), "serve", "--role", "worker", "--config", cfg)
		if code != 1 || !strings.Contains(stderr, "key file /keys/2026-10.key does not exist") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})
	t.Run("R-ARCH-001 process.role from the file satisfies serve and serve starts", func(t *testing.T) {
		cfg := writeConfig(t, "process:\n  role: web\n")
		_, _, stderr := runCmd(t, baseEnv(ephemeral...), "serve", "--config", cfg)
		if !strings.Contains(stderr, "key file /keys/2026-10.key does not exist") {
			t.Fatalf("stderr %q", stderr)
		}
	})
	t.Run("R-ARCH-001 an invalid --role is rejected", func(t *testing.T) {
		cfg := writeConfig(t, "")
		code, _, stderr := runCmd(t, baseEnv(ephemeral...), "serve", "--role", "primary", "--config", cfg)
		if code != 1 || !strings.Contains(stderr, "process.role") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})
	t.Run("R-OPS-028 a mistyped variable stops serve naming it", func(t *testing.T) {
		cfg := writeConfig(t, "")
		code, _, stderr := runCmd(t, baseEnv("TROVE_LOG_LEVL=debug"), "serve", "--role", "web", "--config", cfg)
		if code != 1 || !strings.Contains(stderr, "TROVE_LOG_LEVL") || !strings.Contains(stderr, "TROVE_LOG_LEVEL") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})
}

func TestMigrate(t *testing.T) {
	t.Run("R-ARCH-001 migrate needs no role and reaches the runner", func(t *testing.T) {
		cfg := writeConfig(t, "")
		user := filepath.Join(t.TempDir(), "username")
		if err := os.WriteFile(user, []byte("trove\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// A refused connection: the runner reports it after the retry bound.
		env := baseEnv("TROVE_DATABASE_USERNAME_FILE="+user, "TROVE_DATABASE_PASSWORD_FILE=", "TROVE_DATABASE_HOST=127.0.0.1", "TROVE_DATABASE_PORT=1", "TROVE_DATABASE_RETRY_MAX_ELAPSED=1s",
			"TROVE_DATABASE_SSL_MODE=disable", "TROVE_DATABASE_ALLOW_INSECURE=true")
		for _, args := range [][]string{{"migrate"}, {"migrate", "up"}, {"migrate", "status"}} {
			code, _, stderr := runCmd(t, env, append(args, "--config", cfg)...)
			if code != 1 || !strings.Contains(stderr, "cannot connect to 127.0.0.1:1/trove") {
				t.Errorf("%v: code %d stderr %q", args, code, stderr)
			}
		}
	})
	t.Run("R-ARCH-001 migrate rejects an unknown action", func(t *testing.T) {
		code, _, _ := runCmd(t, baseEnv(), "migrate", "down")
		if code != 2 {
			t.Fatalf("code %d, want 2", code)
		}
	})
}

func TestAdminConfigValidate(t *testing.T) {
	t.Run("R-ARCH-020 admin config validate lists every key with its source", func(t *testing.T) {
		cfg := writeConfig(t, "log:\n  level: warn\n")
		code, stdout, stderr := runCmd(t, baseEnv("TROVE_JOBS_POLL_INTERVAL=3s"), "admin", "config", "validate", "--config", cfg)
		if code != 0 {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
		for _, want := range []string{"KEY", "log.level", "warn", "file", "jobs.poll_interval", "3s", "env", "server.listen", ":8080", "default", "process.role", "TROVE_PROCESS_ROLE"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("output lacks %q", want)
			}
		}
		if !strings.Contains(stderr, "configuration is valid") {
			t.Errorf("stderr %q", stderr)
		}
	})
	t.Run("R-OPS-028 admin config validate reports an invalid configuration", func(t *testing.T) {
		cfg := writeConfig(t, "log:\n  levl: warn\n")
		code, stdout, stderr := runCmd(t, baseEnv(), "admin", "config", "validate", "--config", cfg)
		if code != 1 || stdout != "" || !strings.Contains(stderr, `"log.levl"`) {
			t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
		}
	})
	t.Run("R-OPS-028 admin config validate --format json redacts and keeps the spec shape", func(t *testing.T) {
		cfg := writeConfig(t, "")
		code, stdout, _ := runCmd(t, baseEnv(), "admin", "config", "validate", "--format", "json", "--config", cfg)
		if code != 0 || !strings.Contains(stdout, `"values"`) || !strings.Contains(stdout, `"source": "default"`) {
			t.Fatalf("code %d stdout %q", code, stdout)
		}
	})
	t.Run("R-ARCH-020 admin without a known subcommand is a usage error", func(t *testing.T) {
		if code, _, _ := runCmd(t, nil, "admin", "nope"); code != 2 {
			t.Fatalf("code %d", code)
		}
	})
}

func TestDispatch(t *testing.T) {
	t.Run("R-ARCH-001 version and --version print the build information", func(t *testing.T) {
		for _, a := range []string{"version", "--version"} {
			code, stdout, _ := runCmd(t, nil, a)
			if code != 0 || !strings.HasPrefix(stdout, "trove ") || !strings.Contains(stdout, "commit") {
				t.Errorf("%s: code %d out %q", a, code, stdout)
			}
		}
	})
	t.Run("R-ARCH-001 an unknown command and no command are usage errors", func(t *testing.T) {
		if code, _, _ := runCmd(t, nil, "frobnicate"); code != 2 {
			t.Errorf("unknown: code %d", code)
		}
		if code, _, _ := runCmd(t, nil); code != 2 {
			t.Errorf("none: code %d", code)
		}
	})
	t.Run("R-ARCH-001 backup is reserved and says so", func(t *testing.T) {
		code, _, stderr := runCmd(t, nil, "backup", "list")
		if code != 1 || !strings.Contains(stderr, "not implemented yet") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "trove.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
