//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildTrove compiles cmd/trove once per test into a temporary directory.
func buildTrove(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "trove")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/guided-traffic/trove/cmd/trove")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func runTrove(t *testing.T, bin string, env []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok { //nolint:errorlint // exec returns it unwrapped
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, o.String(), e.String()
}

func TestMigrateCommand(t *testing.T) {
	st := getStack(t)
	bin := buildTrove(t)
	name, admin := st.newDatabase(t)
	cfg := st.config(t, name)
	cfgFile := filepath.Join(t.TempDir(), "trove.yaml")
	if err := os.WriteFile(cfgFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"TROVE_DATABASE_HOST=" + st.host, "TROVE_DATABASE_PORT=" + itoa(st.port), "TROVE_DATABASE_NAME=" + name,
		"TROVE_DATABASE_USERNAME_FILE=" + cfg.UsernameFile, "TROVE_DATABASE_PASSWORD_FILE=" + cfg.PasswordFile,
		"TROVE_DATABASE_SSL_MODE=disable", "TROVE_DATABASE_ALLOW_INSECURE=true",
		"TROVE_DATABASE_PREFLIGHT_ALLOW_SUPERUSER=true", "TROVE_POD_NAME=trove-migrate-abc",
		"TROVE_VALKEY_SENTINEL_ADDRESSES=[\"s1:26379\"]", "TROVE_VALKEY_SENTINEL_PRIMARY_NAME=trove",
		"TROVE_STORAGE_S3_BUCKET=blobs",
		"TROVE_SECRETS_ENCRYPTION_KEY_FILES=[\"/keys/2026-10.key\"]", "TROVE_SECRETS_ACTIVE_KEY_ID=2026-10",
	}

	t.Run("R-HA-019 trove migrate up creates the schema, prints a JSON summary and exits 0", func(t *testing.T) {
		code, stdout, stderr := runTrove(t, bin, env, "migrate", "up", "--config", cfgFile)
		if code != 0 {
			t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
		}
		var res struct {
			Applied       []map[string]any `json:"applied"`
			SchemaApplied int              `json:"schemaApplied"`
			NothingToDo   bool             `json:"nothingToDo"`
		}
		if err := json.Unmarshal([]byte(stdout), &res); err != nil || len(res.Applied) == 0 || res.NothingToDo {
			t.Fatalf("summary %q: %v", stdout, err)
		}
		if !strings.Contains(stderr, `"msg":"applied step"`) {
			t.Fatalf("log lines missing: %s", stderr)
		}
	})

	t.Run("R-HA-019 a second run exits 0 with nothing to do", func(t *testing.T) {
		code, stdout, _ := runTrove(t, bin, env, "migrate", "up", "--config", cfgFile)
		if code != 0 || !strings.Contains(stdout, `"nothingToDo": true`) {
			t.Fatalf("exit %d stdout %s", code, stdout)
		}
	})

	t.Run("R-ARCH-026 trove migrate status lists applied steps", func(t *testing.T) {
		code, stdout, _ := runTrove(t, bin, env, "migrate", "status", "--config", cfgFile)
		if code != 0 || !strings.Contains(stdout, `"00001_foundation"`) || !strings.Contains(stdout, `"servable": true`) {
			t.Fatalf("exit %d stdout %s", code, stdout)
		}
	})

	t.Run("R-HA-019 a half-restored database makes trove migrate up exit 3", func(t *testing.T) {
		if _, err := admin.Exec(context.Background(), `INSERT INTO trove.system_state (key, value) VALUES ('restore_in_progress', '"20261028T020000Z-7f3a"')`); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := runTrove(t, bin, env, "migrate", "up", "--config", cfgFile)
		if code != 3 || !strings.Contains(stderr, "20261028T020000Z-7f3a") {
			t.Fatalf("exit %d stderr %s", code, stderr)
		}
	})
}
