//go:build integration

package login

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const (
	bgPassword = "break-glass-dev-password-1"
	bgRotated  = "break-glass-rotated-password-2"
)

// breakGlassEnv is an environment whose replicas run the break-glass account
// with a plaintext password file.
func breakGlassEnv(t *testing.T, replicas int, mut func(*config.AuthBreakGlass)) (*env, config.Auth, string) {
	t.Helper()
	e := newEnv(t)
	auth := e.defaultAuth()
	pwFile := filepath.Join(t.TempDir(), "password")
	writeFile(t, pwFile, bgPassword+"\n")
	auth.BreakGlass.Enabled, auth.BreakGlass.PasswordFile = true, pwFile
	if mut != nil {
		mut(&auth.BreakGlass)
	}
	e.start(replicas, auth)
	return e, auth, pwFile
}

func (b *browser) localLogin(user, password string) (answer, string) {
	b.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": password})
	return b.post("/auth/local/login", string(body), nil)
}

func TestR_AUTH_014_BreakGlass(t *testing.T) {
	e, _, _ := breakGlassEnv(t, 3, nil)

	t.Run("R-AUTH-014 the local form signs in with 204 and the session cookies; the session is break-glass on every replica", func(t *testing.T) {
		b := e.browser()
		resp, body := b.localLogin("admin", bgPassword)
		if resp.StatusCode != http.StatusNoContent || b.cookie(session.CookieSession) == "" || b.cookie(session.CookieCSRF) == "" {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		for range 3 {
			if code, m := b.whoami(); code != 200 || m["kind"] != "break_glass" || m["name"] != "admin" {
				t.Fatalf("whoami %d %v", code, m)
			}
		}
		evs := e.audit.events(audit.EventAuthBreakGlassLogin)
		if len(evs) != 1 || evs[0].Outcome != audit.Success || !strings.Contains(string(evs[0].Details), `"severity":"high"`) {
			t.Fatalf("events %+v", evs)
		}
	})

	t.Run("R-AUTH-014 R-AUTH-012 a local login from another origin is refused", func(t *testing.T) {
		b := e.browser()
		body, _ := json.Marshal(map[string]string{"username": "admin", "password": bgPassword})
		resp, _ := b.post("/auth/local/login", string(body), map[string]string{"Origin": "https://evil.test"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%d", resp.StatusCode)
		}
	})

	t.Run("R-AUTH-020 a service account name with its token as password gets 401 invalid_credentials", func(t *testing.T) {
		resp, body := e.browser().localLogin("sa:ci-bot", "trv_sat_0000000000000000000000000000000000000000000000000")
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "invalid_credentials") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	})

	t.Run("R-AUTH-038 five wrong passwords across replicas lock the account on all; the right one then gets 429", func(t *testing.T) {
		// The failure above counts too.
		for i := range 4 {
			resp, body := e.browser().localLogin("admin", "wrong-password-"+strconv.Itoa(i))
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("attempt %d: %d %s", i, resp.StatusCode, body)
			}
		}
		for range 3 {
			resp, body := e.browser().localLogin("admin", bgPassword)
			if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(body, "break_glass_locked") || resp.Header.Get("Retry-After") == "" {
				t.Fatalf("locked: %d %s", resp.StatusCode, body)
			}
		}
		if n := len(e.audit.events(audit.EventAuthBreakGlassLocked)); n != 1 {
			t.Fatalf("auth.breakglass.locked events = %d", n)
		}
	})

	bin := buildTrove(t)
	cfgEnv := adminEnvFor(t, e)

	t.Run("R-AUTH-015 trove admin break-glass status shows the lock and the live sessions", func(t *testing.T) {
		code, out, errOut := runTrove(t, bin, cfgEnv, "admin", "break-glass", "status")
		var st struct {
			Enabled      bool    `json:"enabled"`
			LockedUntil  *string `json:"lockedUntil"`
			LiveSessions int64   `json:"liveSessions"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &st) != nil || st.LockedUntil == nil || st.LiveSessions != 1 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
	})

	t.Run("R-AUTH-015 trove admin break-glass unlock clears the lock at once on every replica", func(t *testing.T) {
		if code, out, errOut := runTrove(t, bin, cfgEnv, "admin", "break-glass", "unlock"); code != 0 || !strings.Contains(out, "unlocked") {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		for range 3 {
			if resp, body := e.browser().localLogin("admin", bgPassword); resp.StatusCode != http.StatusNoContent {
				t.Fatalf("after unlock: %d %s", resp.StatusCode, body)
			}
		}
	})

	t.Run("R-AUTH-015 break-glass end-sessions ends every break-glass session on every replica", func(t *testing.T) {
		b := e.browser()
		if resp, _ := b.localLogin("admin", bgPassword); resp.StatusCode != http.StatusNoContent {
			t.Fatal("login")
		}
		if code, out, errOut := runTrove(t, bin, cfgEnv, "admin", "break-glass", "end-sessions"); code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		if !eventually(t, 2*time.Second, func() bool {
			for range 3 {
				if code, _ := b.whoami(); code != http.StatusUnauthorized {
					return false
				}
			}
			return true
		}) {
			t.Fatal("a break-glass session survived end-sessions")
		}
		var n int
		if err := connect(t, e.cfg.Name).QueryRow(context.Background(),
			`SELECT count(*) FROM `+schemaName+`.audit_event WHERE action = $1 AND details->>'reason' = 'revoked'`, audit.EventAuthSessionEnded).Scan(&n); err != nil || n == 0 {
			t.Fatalf("auth.session.ended events with reason revoked: %d, %v", n, err)
		}
	})

	t.Run("R-AUTH-015 sessions end --all ends every session in batches", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		if code, out, errOut := runTrove(t, bin, cfgEnv, "admin", "sessions", "end", "--all"); code != 0 || !strings.Contains(out, "sessions ended") {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		if !eventually(t, 2*time.Second, func() bool { code, _ := b.whoami(); return code == http.StatusUnauthorized }) {
			t.Fatal("the session survived")
		}
	})
}

func TestR_AUTH_014_RotationHold(t *testing.T) {
	e, _, pwFile := breakGlassEnv(t, 2, func(b *config.AuthBreakGlass) { b.RotationHold = 2 * time.Second })
	a, other := e.replicas[0], e.replicas[1]
	open := e.browser()
	if resp, body := open.localLogin("admin", bgPassword); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}

	t.Run("R-AUTH-014 Q-AUTH-14 the first replica that notices a rotation opens the hold: every replica answers 503 break_glass_rotating and earlier sessions end", func(t *testing.T) {
		time.Sleep(10 * time.Millisecond)
		writeFile(t, pwFile, bgRotated+"\n")
		if err := a.login.BreakGlass().CheckRotation(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, pw := range []string{bgPassword, bgRotated} {
			resp, body := e.browser().localLogin("admin", pw)
			if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "break_glass_rotating") || resp.Header.Get("Retry-After") == "" {
				t.Fatalf("during the hold: %d %s", resp.StatusCode, body)
			}
		}
		if !eventually(t, 2*time.Second, func() bool {
			for range 2 {
				if code, _ := open.whoami(); code != http.StatusUnauthorized {
					return false
				}
			}
			return true
		}) {
			t.Fatal("the session opened with the old password survived the rotation")
		}
	})

	t.Run("R-AUTH-014 a second replica noticing within the hold opens no second hold; afterwards only the new password works", func(t *testing.T) {
		if err := other.login.BreakGlass().CheckRotation(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2200 * time.Millisecond)
		for range 2 {
			if resp, body := e.browser().localLogin("admin", bgRotated); resp.StatusCode != http.StatusNoContent {
				t.Fatalf("new password: %d %s", resp.StatusCode, body)
			}
		}
		if resp, _ := e.browser().localLogin("admin", bgPassword); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("old password: %d", resp.StatusCode)
		}
	})
}

func TestR_AUTH_014_DisabledAccount(t *testing.T) {
	e, auth, _ := breakGlassEnv(t, 1, nil)
	b := e.browser()
	if resp, body := b.localLogin("admin", bgPassword); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	t.Run("R-AUTH-014 a replica with the account disabled rejects a break-glass session another replica opened and ends it", func(t *testing.T) {
		disabled := auth
		disabled.BreakGlass.Enabled = false
		// A replica of the rollout of enabled: false; its start ends the remaining sessions.
		e.addReplica("web-disabled", disabled)
		for range 2 {
			if code, _ := b.whoami(); code != http.StatusUnauthorized {
				t.Fatalf("break-glass session accepted: %d", code)
			}
		}
		var live int
		if err := e.admin.QueryRow(context.Background(), `SELECT count(*) FROM ui_session WHERE auth_method = 'break_glass' AND ended_at IS NULL`).Scan(&live); err != nil || live != 0 {
			t.Fatalf("live break-glass sessions %d, %v", live, err)
		}
	})
	t.Run("R-AUTH-014 a session a still-enabled replica opens during the rollout is ended on its next request to an updated one", func(t *testing.T) {
		late := e.browser()
		e.next.Store(0) // the enabled replica answers the login
		if resp, body := late.localLogin("admin", bgPassword); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("login on the enabled replica: %d %s", resp.StatusCode, body)
		}
		e.next.Store(1) // the disabled replica answers whoami
		if code, _ := late.whoami(); code != http.StatusUnauthorized {
			t.Fatalf("the disabled replica accepted the session: %d", code)
		}
		e.next.Store(0)
		if code, _ := late.whoami(); code != http.StatusUnauthorized {
			t.Fatalf("the session was not ended: %d", code)
		}
	})

	t.Run("R-AUTH-014 the local login of a disabled replica answers 403 local_login_disabled", func(t *testing.T) {
		e.next.Store(1) // the disabled replica is the second
		resp, body := e.browser().localLogin("admin", bgPassword)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "local_login_disabled") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	})
}

// buildTrove builds the binary once per test that needs it.
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

// adminEnvFor is the environment of a trove admin command against the test's
// database: the pod's configuration, here as variables.
func adminEnvFor(t *testing.T, e *env) []string {
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
	return runTroveStdin(t, bin, env, "", args...)
}

// runTroveStdin is runTrove with standard input.
func runTroveStdin(t *testing.T, bin string, env []string, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
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
