//go:build integration

package login

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
)

// TestR_AUTH_015_IdentityCommands runs the bootstrap commands of the identity
// area against the test database: they commit through the change log, which the
// running replicas apply (R-AUTH-023).
func TestR_AUTH_015_IdentityCommands(t *testing.T) {
	e := newEnv(t)
	e.start(2, e.defaultAuth())
	bin := buildTrove(t)
	cfgEnv := adminEnvFor(t, e)
	conn := connect(t, e.cfg.Name)
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The replicas of a running installation keep the partitions; the test
	// database has none until one calls this.
	if _, err := conn.Exec(context.Background(), `SELECT `+schemaName+`.audit_maintain_partitions()`); err != nil {
		t.Fatal(err)
	}

	t.Run("R-AUTH-015 service-account create --admin binds the system role admin in the creating transaction", func(t *testing.T) {
		before := count(`SELECT epoch FROM ` + schemaName + `.cluster_epoch WHERE area = 'authz'`)
		code, out, errOut := runTrove(t, bin, cfgEnv, "admin", "service-account", "create", "bootstrap", "--admin", "--description", "bootstrap")
		if code != 0 || !strings.Contains(out, "bootstrap") {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		if n := count(`SELECT count(*) FROM ` + schemaName + `.authz_role_binding b JOIN ` + schemaName + `.principal p ON p.id = b.principal_id WHERE b.role = 'admin' AND p.name = 'bootstrap'`); n != 1 {
			t.Fatalf("admin bindings = %d", n)
		}
		if after := count(`SELECT epoch FROM ` + schemaName + `.cluster_epoch WHERE area = 'authz'`); after <= before {
			t.Fatalf("authz epoch %d -> %d", before, after)
		}
		if n := count(`SELECT count(*) FROM `+schemaName+`.audit_event WHERE action = ANY($1) AND actor_type = 'admin-cli'`, []string{"serviceaccount.created", "role.bound"}); n < 1 {
			t.Fatalf("audit events of the admin-cli = %d", n)
		}
	})

	var secret string
	t.Run("R-AUTH-015 token create prints a service-account token once, on stdout only", func(t *testing.T) {
		code, out, errOut := runTrove(t, bin, cfgEnv, "admin", "token", "create", "--service-account", "bootstrap")
		secret = strings.TrimSpace(out)
		if _, err := token.Check(secret); code != 0 || err != nil || strings.Contains(errOut, secret) {
			t.Fatalf("exit %d\n%q\n%s", code, out, errOut)
		}
		if n := count(`SELECT count(*) FROM ` + schemaName + `.api_token WHERE type = 'sat' AND revoked_at IS NULL AND expires_at <= now() + interval '15 minutes 5 seconds'`); n != 1 {
			t.Fatalf("active tokens with the 15 minute default = %d", n)
		}
	})

	t.Run("R-AUTH-015 token create for an unknown service account fails", func(t *testing.T) {
		if code, _, _ := runTrove(t, bin, cfgEnv, "admin", "token", "create", "--service-account", "nobody"); code == 0 {
			t.Fatal("exit 0")
		}
	})

	t.Run("R-AUTH-015 token revoke --value-stdin revokes the token", func(t *testing.T) {
		code, out, errOut := runTroveStdin(t, bin, cfgEnv, secret, "admin", "token", "revoke", "--value-stdin")
		if code != 0 || !strings.Contains(out, "1 tokens revoked") {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		if n := count(`SELECT count(*) FROM ` + schemaName + `.api_token WHERE revoked_at IS NOT NULL`); n != 1 {
			t.Fatalf("revoked tokens = %d", n)
		}
	})

	t.Run("R-AUTH-015 user disable ends the user's sessions on every replica", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		if code, _ := b.whoami(); code != http.StatusOK {
			t.Fatalf("whoami %d", code)
		}
		if code, out, errOut := runTrove(t, bin, cfgEnv, "admin", "user", "disable", "alice"); code != 0 || !strings.Contains(out, "alice disabled") {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		if !eventually(t, 3*time.Second, func() bool {
			for range 3 {
				if code, _ := b.whoami(); code != http.StatusUnauthorized {
					return false
				}
			}
			return true
		}) {
			t.Fatal("the session of a disabled user survived")
		}
	})

	t.Run("R-AUTH-015 token revoke --id of an unknown token fails", func(t *testing.T) {
		if code, _, _ := runTrove(t, bin, cfgEnv, "admin", "token", "revoke", "--id", "00000000-0000-0000-0000-000000000001"); code == 0 {
			t.Fatal("exit 0")
		}
	})
}
