//go:build integration

package bastionreg

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

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func buildTrove(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "trove")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/guided-traffic/trove/cmd/trove")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// adminEnv is the environment of a trove admin command against the test's database: the pod's configuration,
// here as variables.
func (e *env) adminEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"TROVE_DATABASE_HOST=" + e.cfg.Host, "TROVE_DATABASE_PORT=" + strconv.Itoa(e.cfg.Port), "TROVE_DATABASE_NAME=" + e.cfg.Name,
		"TROVE_DATABASE_USERNAME_FILE=" + e.cfg.UsernameFile, "TROVE_DATABASE_PASSWORD_FILE=" + e.cfg.PasswordFile,
		"TROVE_DATABASE_SSL_MODE=verify-full", "TROVE_DATABASE_SSL_ROOT_CERT_FILE=" + testenv.CAFile(t),
		"TROVE_DATABASE_PREFLIGHT_ALLOW_SUPERUSER=true", "TROVE_LOG_LEVEL=warn",
	}
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

// TestR_BAS_004_AdminCommands runs the three commands of R-BAS-004 from a pod: they write through the same
// service as the API, with the actor admin-cli, and the token is printed once on stdout only.
func TestR_BAS_004_AdminCommands(t *testing.T) {
	e := newEnv(t)
	r, c := e.newCast("web-1", replicaOpts{})
	bin := buildTrove(t)
	cfgEnv := e.adminEnv(t)
	// The replica keeps the audit partitions of a running installation; the command's own writer finds them.
	_ = r

	code, out, errOut := runTrove(t, bin, cfgEnv, "admin", "bastion", "create", "partners", "--description", "The partner network")
	if code != 0 || !strings.Contains(out, "bastion partners registered") {
		t.Fatalf("create: exit %d\n%s\n%s", code, out, errOut)
	}
	if code, _, _ := runTrove(t, bin, cfgEnv, "admin", "bastion", "create", "partners"); code == 0 {
		t.Fatal("a second create of the same name exited 0")
	}
	if code, _, _ := runTrove(t, bin, cfgEnv, "admin", "bastion", "create", "Not-Valid"); code == 0 {
		t.Fatal("a name outside the rule exited 0")
	}

	code, out, errOut = runTrove(t, bin, cfgEnv, "admin", "bastion", "token", "create", "partners", "--ttl", "720h")
	secret := strings.TrimSpace(out)
	if typ, err := token.Check(secret); code != 0 || err != nil || typ != token.TypeBST || strings.Contains(errOut, secret) {
		t.Fatalf("token create: exit %d\n%q\n%s", code, out, errOut)
	}
	if n := e.number(`SELECT count(*) FROM api_token WHERE type = 'bst' AND created_via = 'admin-cli' AND expires_at BETWEEN now() + interval '719 hours' AND now() + interval '721 hours'`); n != 1 {
		t.Fatalf("tokens of 720 hours = %d", n)
	}
	if code, _, _ := runTrove(t, bin, cfgEnv, "admin", "bastion", "token", "create", "partners", "--ttl", "20000h"); code == 0 {
		t.Fatal("a lifetime above max_ttl exited 0")
	}
	if code, _, _ := runTrove(t, bin, cfgEnv, "admin", "bastion", "token", "create", "ghost"); code == 0 {
		t.Fatal("a token of an unknown registration exited 0")
	}

	// The token the command printed is valid on the running replica (within the revocation bound: it is new).
	if _, err := r.auth.Authenticate(context.Background(), secret); err != nil {
		t.Fatalf("the printed token does not authenticate: %v", err)
	}
	tid := e.text(`SELECT id::text FROM api_token WHERE type = 'bst'`)
	code, out, errOut = runTrove(t, bin, cfgEnv, "admin", "bastion", "token", "revoke", "partners", tid)
	if code != 0 || !strings.Contains(out, "revoked") {
		t.Fatalf("token revoke: exit %d\n%s\n%s", code, out, errOut)
	}
	waitFor(t, "the revoked token is refused on the running replica", revocationBound, func() bool {
		_, err := r.auth.Authenticate(context.Background(), secret)
		return err != nil
	})
	if code, _, _ := runTrove(t, bin, cfgEnv, "admin", "bastion", "token", "revoke", "other", tid); code == 0 {
		t.Fatal("a revocation through another registration exited 0")
	}
	if code, _, _ := runTrove(t, bin, cfgEnv, "admin", "bastion", "token"); code == 0 {
		t.Fatal("an incomplete command exited 0")
	}

	for _, ev := range []string{audit.EventBastionCreated, audit.EventTokenRevoked} {
		if n := e.number(`SELECT count(*) FROM audit_event WHERE action = $1 AND actor_type = 'admin-cli' AND resource_type = 'bastion'`, ev); n != 1 {
			t.Errorf("%s by the admin-cli = %d, want 1", ev, n)
		}
	}
	if n := e.number(`SELECT count(*) FROM audit_event WHERE action = 'token.created' AND actor_type = 'admin-cli' AND resource_type = 'bastion'`); n != 1 {
		t.Errorf("token.created by the admin-cli = %d, want 1", n)
	}
	_ = c
}
