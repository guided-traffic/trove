//go:build integration

package identity

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_AUTH_015_TheTokenCommandNamesAProjectAccount: `trove admin token create --service-account` takes the
// name of an instance-wide account, and <project>/<name> or sa:<project>/<name> for the account of a project
// (R-AUTHZ-026); the bare name never reaches a project's account.
func TestR_AUTH_015_TheTokenCommandNamesAProjectAccount(t *testing.T) {
	e := newEnv(t)
	e.project("web")
	a := e.newReplica("a", replicaOpts{})
	ctx := context.Background()
	if _, err := a.manager.CreateProjectServiceAccount(ctx, admin, "web", principal.NewServiceAccount{Name: "partner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.manager.CreateServiceAccount(ctx, admin, principal.NewServiceAccount{Name: "ops"}); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(t.TempDir(), "trove")
	build := exec.Command("go", "build", "-o", bin, "github.com/guided-traffic/trove/cmd/trove")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cfgEnv := []string{
		"TROVE_DATABASE_HOST=" + e.cfg.Host, "TROVE_DATABASE_PORT=" + strconv.Itoa(e.cfg.Port), "TROVE_DATABASE_NAME=" + e.cfg.Name,
		"TROVE_DATABASE_USERNAME_FILE=" + e.cfg.UsernameFile, "TROVE_DATABASE_PASSWORD_FILE=" + e.cfg.PasswordFile,
		"TROVE_DATABASE_SSL_MODE=verify-full", "TROVE_DATABASE_SSL_ROOT_CERT_FILE=" + testenv.CAFile(t),
		"TROVE_DATABASE_PREFLIGHT_ALLOW_SUPERUSER=true", "TROVE_LOG_LEVEL=warn",
	}
	run := func(account string) (int, string, string) {
		runCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(runCtx, bin, "admin", "token", "create", "--service-account", account)
		cmd.Env = append(os.Environ(), cfgEnv...)
		var o, er bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &er
		code := 0
		if err := cmd.Run(); err != nil {
			ee, ok := err.(*exec.ExitError) //nolint:errorlint // exec returns it unwrapped
			if !ok {
				t.Fatal(err)
			}
			code = ee.ExitCode()
		}
		return code, strings.TrimSpace(o.String()), er.String()
	}

	for _, ref := range []string{"web/partner", "sa:web/partner", "ops", "sa:ops"} {
		code, out, errOut := run(ref)
		if code != 0 || !strings.HasPrefix(out, "trv_sat_") {
			t.Errorf("%s: exit %d, stdout %q, stderr %s", ref, code, out, errOut)
		}
	}
	for _, ref := range []string{"partner", "web/ops", "mobile/partner", "web/ghost", "web/"} {
		if code, out, _ := run(ref); code == 0 || out != "" {
			t.Errorf("%s: exit %d, stdout %q; want a failure and no token", ref, code, out)
		}
	}
}
