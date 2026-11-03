package archtest

import "testing"

func TestBastionImportAllowlist(t *testing.T) {
	t.Run("R-BAS-001 the Bastion importing PostgreSQL, the blob store, the decision point, the formats, sessions, login or jobs names the edge", func(t *testing.T) {
		bad := []string{
			"internal/meta", "internal/meta/postgres", "internal/meta/postgres/q", "internal/blob", "internal/authz",
			"internal/formats", "internal/formats/npm", "internal/auth/session", "internal/auth/oidc", "internal/auth/token",
			"internal/jobs", "internal/proxy", "internal/virtual", "internal/repository", "internal/items", "internal/api/v1",
			"internal/api/login", "internal/audit", "internal/app", "internal/bastionmain", "internal/webui",
		}
		vs := CheckBastionImports(mod, []Package{pkg("internal/bastion/engine", append([]string(nil), bad...)...)})
		for _, b := range bad {
			wantEdge(t, vs, "R-BAS-001", "internal/bastion/engine", b)
		}
		if len(vs) != len(bad) {
			t.Fatalf("want exactly the %d edges, got %v", len(bad), Strings(vs))
		}
	})
	t.Run("R-BAS-001 the platform packages, the Bastion's own tree and the standard library pass", func(t *testing.T) {
		wantClean(t, CheckBastionImports(mod, []Package{
			pkg("internal/bastion/server", "internal/errs", "internal/config", "internal/buildinfo", "internal/observability",
				"internal/observability/health", "internal/tlsfiles", "internal/outbound", "internal/httpserver", "internal/httpserver/content",
				"internal/bastion/engine", "internal/bastion/iface", "net/http", "github.com/prometheus/client_golang/prometheus"),
			pkg("internal/bastion/engine/sub", "internal/bastion/cache"),
		}))
	})
	t.Run("R-BAS-001 the Valkey client and the S3 client are allowed in the packages that need them only", func(t *testing.T) {
		wantClean(t, CheckBastionImports(mod, []Package{
			pkg("internal/bastion/shared", "internal/cluster"),
			pkg("internal/bastion/shared/valkey", "internal/cluster"),
			pkg("internal/bastion/cache", "internal/blob/s3"),
			pkg("internal/bastion/cache/bucket", "internal/blob/s3"),
		}))
		vs := CheckBastionImports(mod, []Package{
			pkg("internal/bastion/engine", "internal/cluster", "internal/blob/s3"),
			pkg("internal/bastion/guard", "internal/blob/s3"),
		})
		wantEdge(t, vs, "R-BAS-001", "internal/bastion/engine", "internal/cluster")
		wantEdge(t, vs, "R-BAS-001", "internal/bastion/engine", "internal/blob/s3")
		wantEdge(t, vs, "R-BAS-001", "internal/bastion/guard", "internal/blob/s3")
	})
	t.Run("R-BAS-001 packages outside the Bastion's tree are not held to the rule", func(t *testing.T) {
		wantClean(t, CheckBastionImports(mod, []Package{
			pkg("internal/bastionmain", "internal/authz", "internal/bastion/iface"),
			pkg("internal/app", "internal/bastion/server"),
		}))
	})
	t.Run("R-ARCH-002 the Bastion's packages are layered: the leaf packages import no infrastructure", func(t *testing.T) {
		vs := CheckLayers(mod, []Package{
			pkg("internal/bastion/iface", "internal/bastion/cache"),
			pkg("internal/bastion/answers", "internal/bastion/shared"),
		})
		wantEdge(t, vs, "R-ARCH-002", "internal/bastion/iface", "internal/bastion/cache")
		wantEdge(t, vs, "R-ARCH-002", "internal/bastion/answers", "internal/bastion/shared")
		wantClean(t, CheckLayers(mod, []Package{
			pkg("internal/bastion/engine", "internal/bastion/cache", "internal/bastion/mainclient", "internal/bastion/guard", "internal/bastion/iface"),
			pkg("internal/bastion/server", "internal/bastion/engine", "internal/httpserver"),
			pkg("internal/app", "internal/bastion/server"),
		}))
	})
}
