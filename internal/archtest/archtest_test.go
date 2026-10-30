package archtest

import (
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const mod = "github.com/guided-traffic/trove"

func pkg(rel string, imports ...string) Package {
	full := mod + "/" + rel
	for i, imp := range imports {
		if !strings.Contains(imp, ".") && !strings.Contains(imp, "/") { // std package
			continue
		}
		if strings.HasPrefix(imp, "internal/") || strings.HasPrefix(imp, "cmd/") {
			imports[i] = mod + "/" + imp
		}
	}
	return Package{ImportPath: full, Rel: rel, Imports: imports}
}

// wantEdge asserts that exactly the named rule fires and names the edge.
func wantEdge(t *testing.T, vs []Violation, rule, from, to string) {
	t.Helper()
	for _, v := range vs {
		if v.Rule == rule && v.From == from && v.To == to {
			if !strings.Contains(v.String(), from) || !strings.Contains(v.String(), to) {
				t.Fatalf("violation text %q does not name the edge", v.String())
			}
			return
		}
	}
	t.Fatalf("no %s violation for %s -> %s in %v", rule, from, to, Strings(vs))
}

func wantClean(t *testing.T, vs []Violation) {
	t.Helper()
	if len(vs) != 0 {
		t.Fatalf("unexpected violations:\n%s", strings.Join(Strings(vs), "\n"))
	}
}

func TestLayers(t *testing.T) {
	t.Run("R-ARCH-002 a format importing meta/postgres names the edge", func(t *testing.T) {
		vs := CheckModuleImportBans(mod, []Package{pkg("internal/formats/npm", "internal/meta/postgres")})
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/npm", "internal/meta/postgres")
	})
	t.Run("R-ARCH-002 infrastructure importing the domain layer names the edge", func(t *testing.T) {
		vs := CheckLayers(mod, []Package{pkg("internal/cluster", "internal/proxy")})
		wantEdge(t, vs, "R-ARCH-002", "internal/cluster", "internal/proxy")
	})
	t.Run("R-ARCH-002 the domain layer importing the edge layer names the edge", func(t *testing.T) {
		vs := CheckLayers(mod, []Package{pkg("internal/proxy", "internal/httpserver")})
		wantEdge(t, vs, "R-ARCH-002", "internal/proxy", "internal/httpserver")
	})
	t.Run("R-ARCH-002 a leaf package importing a higher layer names the edge", func(t *testing.T) {
		vs := CheckLayers(mod, []Package{pkg("internal/cache", "internal/repository")})
		wantEdge(t, vs, "R-ARCH-002", "internal/cache", "internal/repository")
		vs = CheckLayers(mod, []Package{pkg("internal/config", "internal/meta/postgres")})
		wantEdge(t, vs, "R-ARCH-002", "internal/config", "internal/meta/postgres")
	})
	t.Run("R-ARCH-002 internal/virtual and internal/formatdata are domain packages", func(t *testing.T) {
		for _, p := range []string{"internal/virtual", "internal/formatdata/npm", "internal/proxy/upstream", "internal/proxy/invalidation"} {
			if l, ok := layerOf(p); !ok || l != layerDomain {
				t.Errorf("%s: layer %v (found %v), want domain", p, l, ok)
			}
		}
		wantEdge(t, CheckLayers(mod, []Package{pkg("internal/virtual", "internal/formats/npm")}), "R-ARCH-002", "internal/virtual", "internal/formats/npm")
		wantEdge(t, CheckLayers(mod, []Package{pkg("internal/formatdata/npm", "internal/httpserver")}), "R-ARCH-002", "internal/formatdata/npm", "internal/httpserver")
		wantEdge(t, CheckLayers(mod, []Package{pkg("internal/cluster", "internal/virtual")}), "R-ARCH-002", "internal/cluster", "internal/virtual")
		wantClean(t, CheckLayers(mod, []Package{
			pkg("internal/virtual", "internal/repository", "internal/authz", "internal/proxy", "internal/cache", "internal/cluster", "internal/meta"),
			pkg("internal/formatdata/npm", "internal/meta", "internal/meta/postgres/q"),
		}))
	})
	t.Run("R-ARCH-002 the domain layer importing the format contract names the edge, the catalog and the scope leaf pass", func(t *testing.T) {
		vs := CheckLayers(mod, []Package{pkg("internal/repository", "internal/formats")})
		wantEdge(t, vs, "R-ARCH-002", "internal/repository", "internal/formats")
		vs = CheckLayers(mod, []Package{pkg("internal/authz", "internal/formats")})
		wantEdge(t, vs, "R-ARCH-002", "internal/authz", "internal/formats")
		wantClean(t, CheckLayers(mod, []Package{
			pkg("internal/repository", "internal/formats/catalog", "internal/errs"),
			pkg("internal/authz", "internal/formats/catalog", "internal/auth/scope", "internal/repository"),
			pkg("internal/httpserver", "internal/auth/scope"),
			pkg("internal/formats", "internal/formats/catalog", "internal/authz", "internal/repository"),
			pkg("internal/formats/catalog", "internal/errs"),
			pkg("internal/auth/scope", "internal/errs", "internal/config"),
		}))
	})
	t.Run("R-ARCH-002 the catalog and the scope leaves import no higher layer", func(t *testing.T) {
		vs := CheckLayers(mod, []Package{
			pkg("internal/formats/catalog", "internal/repository"),
			pkg("internal/auth/scope", "internal/authz"),
		})
		wantEdge(t, vs, "R-ARCH-002", "internal/formats/catalog", "internal/repository")
		wantEdge(t, vs, "R-ARCH-002", "internal/auth/scope", "internal/authz")
		for _, r := range []string{"internal/formats/catalog", "internal/auth/scope"} {
			if l, _ := layerOf(r); l != layerLeaf {
				t.Errorf("%s is %s", r, l)
			}
		}
	})
	t.Run("R-ARCH-002 allowed edges pass", func(t *testing.T) {
		wantClean(t, CheckLayers(mod, []Package{
			pkg("cmd/trove", "internal/app", "internal/config", "internal/buildinfo"),
			pkg("internal/app", "internal/httpserver", "internal/meta/postgres", "internal/cluster"),
			pkg("internal/httpserver", "internal/api/v1", "internal/formats", "internal/auth/session", "internal/errs"),
			pkg("internal/api/v1", "internal/repository", "internal/jobs", "internal/api/v1/gen"),
			pkg("internal/formats/npm", "internal/formats", "internal/errs"),
			pkg("internal/proxy", "internal/outbound", "internal/blob", "internal/cluster", "internal/cache", "internal/meta"),
			pkg("internal/blob/s3", "internal/blob", "internal/outbound"),
			pkg("internal/backup", "internal/meta/postgres", "internal/blob/s3", "internal/backup/tables"),
			pkg("internal/meta/postgres", "internal/meta", "internal/config", "internal/backup/tables"),
			pkg("internal/cluster", "internal/cache", "internal/observability", "internal/config"),
			pkg("internal/cache", "internal/errs"),
		}))
	})
	t.Run("R-ARCH-002 a package without a layer is reported", func(t *testing.T) {
		vs := CheckLayers(mod, []Package{pkg("internal/misc")})
		if len(vs) != 1 || vs[0].From != "internal/misc" || !strings.Contains(vs[0].Detail, "no layer") {
			t.Fatalf("got %v", Strings(vs))
		}
	})
	t.Run("R-ARCH-002 the longest prefix decides: backup/tables is a leaf, backup is domain", func(t *testing.T) {
		if l, _ := layerOf("internal/backup/tables"); l != layerLeaf {
			t.Errorf("backup/tables is %s", l)
		}
		if l, _ := layerOf("internal/backup"); l != layerDomain {
			t.Errorf("backup is %s", l)
		}
		if l, _ := layerOf("internal/meta/postgres/q"); l != layerInfra {
			t.Errorf("meta/postgres/q is %s", l)
		}
	})
}

func TestFormatImportAllowlist(t *testing.T) {
	cases := []struct {
		name string
		pkg  Package
		bad  []string // targets that must be named
	}{
		{
			"a format importing the object store, the job queue, the cache registry and meta is named",
			pkg("internal/formats/npm", "internal/blob", "internal/jobs", "internal/cache", "internal/meta", "internal/backup/tables"),
			[]string{"internal/blob", "internal/jobs", "internal/cache", "internal/meta", "internal/backup/tables"},
		},
		{
			"a format importing a sibling format is named",
			pkg("internal/formats/nuget", "internal/formats/npm"),
			[]string{"internal/formats/npm"},
		},
		{
			"a format importing the HTTP server shell or the management API is named",
			pkg("internal/formats/npm", "internal/httpserver", "internal/api/v1"),
			[]string{"internal/httpserver", "internal/api/v1"},
		},
		{
			"a format importing the composition root is named",
			pkg("internal/formats/npm", "internal/app"),
			[]string{"internal/app"},
		},
		{
			"the contract package importing a concrete format is named",
			pkg("internal/formats", "internal/formats/npm"),
			[]string{"internal/formats/npm"},
		},
		{
			"a package that did not exist when the rule was written is refused by default",
			pkg("internal/formats/npm", "internal/newthing"),
			[]string{"internal/newthing"},
		},
	}
	for _, c := range cases {
		t.Run("R-ARCH-008 "+c.name, func(t *testing.T) {
			vs := CheckModuleImportBans(mod, []Package{c.pkg})
			for _, b := range c.bad {
				wantEdge(t, vs, "R-ARCH-008", c.pkg.Rel, b)
			}
			if len(vs) != len(c.bad) {
				t.Fatalf("want exactly %v, got %v", c.bad, Strings(vs))
			}
		})
	}
	t.Run("R-ARCH-008 the contract, the catalog, errs, config, observability, standard library and the format's own subpackages pass", func(t *testing.T) {
		wantClean(t, CheckModuleImportBans(mod, []Package{
			pkg("internal/formats/npm", "internal/formats", "internal/formats/catalog", "internal/errs", "internal/config", "internal/observability", "internal/formats/npm/parse", "net/http", "github.com/prometheus/client_golang/prometheus"),
			pkg("internal/formats/catalog", "internal/errs"),
			pkg("internal/formats/npm/parse", "internal/errs", "internal/formats/npm"),
			pkg("internal/formats", "internal/errs", "internal/config"),
			pkg("internal/proxy", "internal/blob", "internal/jobs", "internal/meta"),
		}))
	})
	t.Run("R-ARCH-010 the registration package imports formats and nothing else", func(t *testing.T) {
		wantClean(t, CheckModuleImportBans(mod, []Package{
			pkg("internal/formats/all", "internal/formats", "internal/formats/npm", "internal/formats/nuget", "internal/formats/generic"),
		}))
		vs := CheckModuleImportBans(mod, []Package{pkg("internal/formats/all", "internal/formats/npm", "internal/blob")})
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/all", "internal/blob")
		if len(vs) != 1 {
			t.Fatalf("want exactly the blob edge, got %v", Strings(vs))
		}
	})
	t.Run("R-ARCH-008 the fixture module: infrastructure, object store and sibling imports are named, own subpackage and contract are not", func(t *testing.T) {
		module, pkgs, err := ListPackages(fixtureDir(t, "layers"))
		if err != nil {
			t.Fatal(err)
		}
		vs := CheckModuleImportBans(module, pkgs)
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/npm", "internal/meta/postgres")
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/npm", "internal/blob")
		wantEdge(t, vs, "R-ARCH-008", "internal/formats/nuget", "internal/formats/npm")
		wantEdge(t, vs, "R-ARCH-008", "internal/formats", "internal/blob")
		if len(vs) != 4 {
			t.Fatalf("want 4 violations, got %v", Strings(vs))
		}
	})
}

func TestModuleBans(t *testing.T) {
	t.Run("R-ARCH-002 the login routes importing the management API name the edge, also from a subpackage", func(t *testing.T) {
		vs := CheckModuleBans(mod, []Package{
			pkg("internal/api/login", "internal/api/v1"),
			pkg("internal/api/login/page", "internal/api/v1/gen"),
		})
		wantEdge(t, vs, "R-ARCH-002", "internal/api/login", "internal/api/v1")
		wantEdge(t, vs, "R-ARCH-002", "internal/api/login/page", "internal/api/v1/gen")
		if len(vs) != 2 {
			t.Fatalf("want exactly the two edges, got %v", Strings(vs))
		}
	})
	t.Run("R-ARCH-002 the login routes importing the login domain, the session store, the shell and the edge conversions pass, and the management API may import them", func(t *testing.T) {
		wantClean(t, CheckModuleBans(mod, []Package{
			pkg("internal/api/login", "internal/auth/oidc", "internal/auth/session", "internal/httpserver", "internal/api/edge", "internal/errs"),
			pkg("internal/api/v1", "internal/api/login", "internal/api/edge", "internal/auth/oidc"),
			pkg("internal/app", "internal/api/login", "internal/api/v1"),
		}))
	})
	t.Run("R-ARCH-002 the item store importing the decision point names the edge", func(t *testing.T) {
		wantEdge(t, CheckModuleBans(mod, []Package{pkg("internal/items", "internal/authz")}), "R-ARCH-002", "internal/items", "internal/authz")
		wantEdge(t, CheckModuleBans(mod, []Package{pkg("internal/items/sub", "internal/authz")}), "R-ARCH-002", "internal/items/sub", "internal/authz")
	})
	t.Run("R-ARCH-002 the generic layer reaches storage only through the item store", func(t *testing.T) {
		wantEdge(t, CheckModuleBans(mod, []Package{pkg("internal/files", "internal/blob")}), "R-ARCH-002", "internal/files", "internal/blob")
		wantEdge(t, CheckModuleBans(mod, []Package{pkg("internal/files", "internal/meta/postgres/q")}), "R-ARCH-002", "internal/files", "internal/meta/postgres/q")
		wantClean(t, CheckModuleBans(mod, []Package{pkg("internal/files", "internal/items", "internal/meta", "internal/authz", "internal/repository")}))
	})
	t.Run("R-ARCH-002 the raw routes and the shared edge package do not import the management API", func(t *testing.T) {
		wantEdge(t, CheckModuleBans(mod, []Package{pkg("internal/api/rawfiles", "internal/api/v1")}), "R-ARCH-002", "internal/api/rawfiles", "internal/api/v1")
		wantEdge(t, CheckModuleBans(mod, []Package{pkg("internal/api/rawfiles", "internal/api/v1/gen")}), "R-ARCH-002", "internal/api/rawfiles", "internal/api/v1/gen")
		wantEdge(t, CheckModuleBans(mod, []Package{pkg("internal/api/edge", "internal/api/v1")}), "R-ARCH-002", "internal/api/edge", "internal/api/v1")
		wantClean(t, CheckModuleBans(mod, []Package{
			pkg("internal/api/rawfiles", "internal/api/edge", "internal/files", "internal/items"),
			pkg("internal/api/v1", "internal/api/edge", "internal/files", "internal/items"),
			pkg("internal/items", "internal/blob", "internal/meta/postgres", "internal/repository"),
		}))
	})
	t.Run("R-ARCH-002 the item store is a domain package: the edge may import it, the infrastructure may not", func(t *testing.T) {
		wantClean(t, CheckLayers(mod, []Package{pkg("internal/api/edge", "internal/items"), pkg("internal/files", "internal/items")}))
		wantEdge(t, CheckLayers(mod, []Package{pkg("internal/blob", "internal/items")}), "R-ARCH-002", "internal/blob", "internal/items")
	})
}

func TestStateExemption(t *testing.T) {
	t.Run("R-ARCH-032 the exemption is the exact package, not its subtree", func(t *testing.T) {
		for _, ok := range []string{"internal/formats", "internal/formats/catalog", "internal/meta", "internal/backup/tables", "internal/jobs", "internal/errs", "internal/secrets", "internal/cache", "internal/cluster", "internal/observability", "test/integration/coordination", "internal/archtest"} {
			if !stateExempt(ok) {
				t.Errorf("%s must be exempt", ok)
			}
		}
		for _, bad := range []string{"internal/formats/npm", "internal/meta/postgres", "internal/meta/postgres/q", "internal/meta/postgres/listq", "internal/backup", "internal/backup/tables/x", "internal/jobs/x", "internal/cluster/x", "internal/observability/health", "internal/cache/lru"} {
			if stateExempt(bad) {
				t.Errorf("%s must not be exempt", bad)
			}
		}
	})
}

func TestImportBans(t *testing.T) {
	t.Run("R-ARCH-002 pgx outside meta/postgres and cluster names the edge", func(t *testing.T) {
		vs := CheckImportBans([]Package{pkg("internal/repository", "github.com/jackc/pgx/v5/pgxpool")})
		wantEdge(t, vs, "R-ARCH-002", "internal/repository", "github.com/jackc/pgx/v5/pgxpool")
	})
	t.Run("R-ARCH-002 pgx inside meta/postgres and cluster passes", func(t *testing.T) {
		wantClean(t, CheckImportBans([]Package{
			pkg("internal/meta/postgres", "github.com/jackc/pgx/v5"),
			pkg("internal/meta/postgres/q", "github.com/jackc/pgx/v5/pgconn"),
			pkg("internal/cluster", "github.com/jackc/pgx/v5"),
		}))
	})
	t.Run("R-ARCH-002 the Valkey client in internal/proxy names the edge", func(t *testing.T) {
		vs := CheckImportBans([]Package{pkg("internal/proxy", "github.com/valkey-io/valkey-go")})
		wantEdge(t, vs, "R-ARCH-002", "internal/proxy", "github.com/valkey-io/valkey-go")
	})
	t.Run("R-ARCH-002 the Valkey client in internal/cluster passes", func(t *testing.T) {
		wantClean(t, CheckImportBans([]Package{pkg("internal/cluster", "github.com/valkey-io/valkey-go", "github.com/valkey-io/valkey-go/valkeycompat")}))
	})
	t.Run("R-ARCH-002 the AWS SDK outside blob/s3 names the edge", func(t *testing.T) {
		vs := CheckImportBans([]Package{
			pkg("internal/backup", "github.com/aws/aws-sdk-go-v2/service/s3"),
			pkg("internal/blob", "github.com/aws/smithy-go"),
		})
		wantEdge(t, vs, "R-ARCH-002", "internal/backup", "github.com/aws/aws-sdk-go-v2/service/s3")
		wantEdge(t, vs, "R-ARCH-002", "internal/blob", "github.com/aws/smithy-go")
		wantClean(t, CheckImportBans([]Package{pkg("internal/blob/s3", "github.com/aws/aws-sdk-go-v2/service/s3", "github.com/aws/smithy-go")}))
	})
	t.Run("R-ARCH-002 format packages importing infrastructure SDKs are caught", func(t *testing.T) {
		vs := CheckImportBans([]Package{pkg("internal/formats/nuget", "github.com/jackc/pgx/v5", "github.com/valkey-io/valkey-go")})
		wantEdge(t, vs, "R-ARCH-002", "internal/formats/nuget", "github.com/jackc/pgx/v5")
		wantEdge(t, vs, "R-ARCH-002", "internal/formats/nuget", "github.com/valkey-io/valkey-go")
	})
	t.Run("R-ARCH-026 database/sql and the pgx stdlib adapter are banned everywhere", func(t *testing.T) {
		vs := CheckImportBans([]Package{
			pkg("internal/meta/postgres", "github.com/jackc/pgx/v5/stdlib"),
			pkg("internal/repository", "database/sql"),
		})
		wantEdge(t, vs, "R-ARCH-026", "internal/meta/postgres", "github.com/jackc/pgx/v5/stdlib")
		wantEdge(t, vs, "R-ARCH-026", "internal/repository", "database/sql")
	})
	t.Run("R-STOR-046 a package below internal/blob importing the Valkey client names the edge", func(t *testing.T) {
		vs := CheckStorageValkey([]Package{pkg("internal/blob/s3", "github.com/valkey-io/valkey-go"), pkg("internal/proxy", "github.com/valkey-io/valkey-go")})
		wantEdge(t, vs, "R-STOR-046", "internal/blob/s3", "github.com/valkey-io/valkey-go")
		if len(vs) != 1 {
			t.Fatalf("only the blob tree is named: %v", Strings(vs))
		}
	})
	t.Run("R-ARCH-002 test and tool trees are not checked", func(t *testing.T) {
		wantClean(t, CheckImportBans([]Package{
			pkg("test/integration/smoke", "github.com/jackc/pgx/v5", "github.com/valkey-io/valkey-go", "github.com/aws/aws-sdk-go-v2/service/s3"),
		}))
	})
}

// fixture runs a check against an on-disk module that go list can read.
func fixtureDir(t *testing.T, name string) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFixtureModules(t *testing.T) {
	t.Run("R-ARCH-002 go list -deps on a fixture module finds the forbidden edges", func(t *testing.T) {
		dir := fixtureDir(t, "layers")
		module, pkgs, err := ListPackages(dir)
		if err != nil {
			t.Fatal(err)
		}
		if module != "example.test/fix" {
			t.Fatalf("module %q", module)
		}
		wantEdge(t, CheckModuleImportBans(module, pkgs), "R-ARCH-008", "internal/formats/npm", "internal/meta/postgres")
		wantEdge(t, CheckLayers(module, pkgs), "R-ARCH-002", "internal/cluster", "internal/proxy")
		wantEdge(t, CheckImportBans(pkgs), "R-ARCH-026", "internal/repository", "database/sql")
		unlayered := false
		for _, v := range CheckLayers(module, pkgs) {
			if v.From == "internal/misc" {
				unlayered = true
			}
		}
		if !unlayered {
			t.Fatal("the unlayered package is not reported")
		}
	})
	t.Run("R-ARCH-032 package-level map, slice, pointer and sync.Map variables are named", func(t *testing.T) {
		dir := fixtureDir(t, "state")
		pkgs, err := LoadTyped(dir)
		if err != nil {
			t.Fatal(err)
		}
		vs := CheckPackageState("example.test/fix", pkgs)
		got := map[string]string{}
		for _, v := range vs {
			got[v.To] = v.From
		}
		for _, name := range []string{"registry", "current", "items", "shared"} {
			from, ok := got[name]
			if !ok || !strings.Contains(from, "internal/proxy/proxy.go:") {
				t.Errorf("variable %s not reported with its position: %v", name, Strings(vs))
			}
		}
		for _, name := range []string{"ErrGone", "counter", "entries", "spec", "pattern", "_"} {
			if _, ok := got[name]; ok {
				t.Errorf("variable %s must not be reported", name)
			}
		}
		if len(vs) != 4 {
			t.Fatalf("want exactly 4 violations, got %v", Strings(vs))
		}
		for _, v := range vs {
			if v.Rule != "R-ARCH-032" {
				t.Errorf("rule %q", v.Rule)
			}
		}
	})
	t.Run("R-ARCH-002 an HTTP client or dial outside internal/outbound is named, tests and outbound are exempt", func(t *testing.T) {
		vs, err := CheckOutbound(fixtureDir(t, "outbound"))
		if err != nil {
			t.Fatal(err)
		}
		if len(vs) != 1 || vs[0].From != "internal/pxy/p.go" {
			t.Fatalf("got %v", Strings(vs))
		}
	})
	t.Run("R-STOR-046 a session-level advisory lock below internal/blob is named, the transaction-level one is not", func(t *testing.T) {
		vs, err := CheckSourceBans(fixtureDir(t, "outbound"))
		if err != nil {
			t.Fatal(err)
		}
		var from []string
		for _, v := range vs {
			from = append(from, v.Rule+" "+v.From)
		}
		joined := strings.Join(from, "\n")
		for _, want := range []string{"R-STOR-046 internal/blob/s3/bad.go", "R-STOR-046 internal/blob/s3/bad.sql", "R-ARCH-033 internal/proxy/q.go", "R-ARCH-033 internal/virtual/q.go"} {
			if !strings.Contains(joined, want) {
				t.Errorf("missing %q in\n%s", want, joined)
			}
		}
		if strings.Contains(joined, "fine.go") {
			t.Errorf("pg_advisory_xact_lock must not be flagged:\n%s", joined)
		}
	})
}

var (
	realOnce sync.Once
	realRoot string
	realMod  string
	realPkgs []Package
	realErr  error
)

func realTree(t *testing.T) (root, module string, pkgs []Package) {
	t.Helper()
	realOnce.Do(func() {
		out, err := exec.Command("go", "env", "GOMOD").Output()
		if err != nil {
			realErr = err
			return
		}
		realRoot = filepath.Dir(strings.TrimSpace(string(out)))
		realMod, realPkgs, realErr = ListPackages(realRoot)
	})
	if realErr != nil {
		t.Fatal(realErr)
	}
	return realRoot, realMod, realPkgs
}

func TestEndpointConstruction(t *testing.T) {
	t.Run("R-EP-002 construction outside internal/app is found through aliases, function values, new, literals and type aliases", func(t *testing.T) {
		dir := fixtureDir(t, "endpoint")
		pkgs, err := LoadTyped(dir)
		if err != nil {
			t.Fatal(err)
		}
		vs := CheckEndpointConstruction("example.test/fix", pkgs)
		got := map[string]bool{}
		for _, v := range vs {
			got[v.From] = true
		}
		for _, want := range []string{"alias.go", "funcvalue.go", "newt.go", "typealias.go", "literal.go"} {
			if !got["internal/webish/"+want] {
				t.Errorf("%s not named:\n%s", want, strings.Join(Strings(vs), "\n"))
			}
		}
		for from := range got {
			if !strings.HasPrefix(from, "internal/webish/") || strings.HasSuffix(from, "clean.go") {
				t.Errorf("%s must not be named (internal/httpserver and internal/app build endpoints, reading one is fine)", from)
			}
		}
	})
}

func TestSubjectConstruction(t *testing.T) {
	t.Run("R-EP-013 a subject built or edited outside internal/authz and internal/api/edge is found through literals, new, aliases, zero values and field assignments", func(t *testing.T) {
		dir := fixtureDir(t, "subject")
		pkgs, err := LoadTyped(dir)
		if err != nil {
			t.Fatal(err)
		}
		vs := CheckSubjectConstruction("example.test/fix", pkgs)
		got := map[string]bool{}
		for _, v := range vs {
			got[v.From] = true
			if v.Rule != "R-EP-013" {
				t.Errorf("rule %s, want R-EP-013", v.Rule)
			}
		}
		for _, want := range []string{"literal.go", "newt.go", "alias.go", "typealias.go", "zerovar.go", "assign.go"} {
			if !got["internal/webish/"+want] {
				t.Errorf("%s not named:\n%s", want, strings.Join(Strings(vs), "\n"))
			}
		}
		for from := range got {
			if !strings.HasPrefix(from, "internal/webish/") || strings.HasSuffix(from, "clean.go") {
				t.Errorf("%s must not be named (authz and edge build subjects; passing one on, reading one and an audit.Subject are fine)", from)
			}
		}
		// Both lines of assign.go that edit a field are named, the edit of the subject edge returned too.
		var assigns int
		for _, v := range vs {
			if v.From == "internal/webish/assign.go" {
				assigns++
			}
		}
		if assigns != 2 { // Endpoint and Name: one violation per distinct field
			t.Errorf("assign.go names %d edits, want 2:\n%s", assigns, strings.Join(Strings(vs), "\n"))
		}
	})
}

func TestRealTree(t *testing.T) {
	t.Run("R-ARCH-002 the layer and import rules hold on the current tree", func(t *testing.T) {
		_, module, pkgs := realTree(t)
		var all []Violation
		all = append(all, CheckLayers(module, pkgs)...)
		all = append(all, CheckImportBans(pkgs)...)
		all = append(all, CheckModuleImportBans(module, pkgs)...)
		all = append(all, CheckModuleBans(module, pkgs)...)
		all = append(all, CheckStorageValkey(pkgs)...)
		wantClean(t, all)
	})
	t.Run("R-ARCH-002 no HTTP client or dial outside internal/outbound", func(t *testing.T) {
		root, _, _ := realTree(t)
		vs, err := CheckOutbound(root)
		if err != nil {
			t.Fatal(err)
		}
		wantClean(t, vs)
	})
	t.Run("R-STOR-046 no session-level advisory lock below internal/blob", func(t *testing.T) {
		root, _, _ := realTree(t)
		vs, err := CheckSourceBans(root)
		if err != nil {
			t.Fatal(err)
		}
		wantClean(t, vs)
	})
	t.Run("R-EP-002 only internal/app builds an endpoint or a chain", func(t *testing.T) {
		root, module, _ := realTree(t)
		pkgs, err := LoadTyped(root)
		if err != nil {
			t.Fatal(err)
		}
		wantClean(t, CheckEndpointConstruction(module, pkgs))
	})
	t.Run("R-EP-013 only internal/authz and internal/api/edge build an authz.Subject", func(t *testing.T) {
		root, module, _ := realTree(t)
		pkgs, err := LoadTyped(root)
		if err != nil {
			t.Fatal(err)
		}
		wantClean(t, CheckSubjectConstruction(module, pkgs))
	})
	t.Run("R-ARCH-018 no LISTEN or NOTIFY, no change-log write outside internal/cluster, no session-level SQL, no assembled SQL", func(t *testing.T) {
		root, _, _ := realTree(t)
		vs, err := CheckSQLSources(root)
		if err != nil {
			t.Fatal(err)
		}
		wantClean(t, vs)
	})
	t.Run("R-ARCH-032 no package-level map, slice, pointer or sync.Map outside the allowed packages", func(t *testing.T) {
		root, module, _ := realTree(t)
		pkgs, err := LoadTyped(root)
		if err != nil {
			t.Fatal(err)
		}
		wantClean(t, CheckPackageState(module, pkgs))
	})
}

// hasViolation reports whether a violation of rule names file (prefix) and the offending text.
func hasViolation(vs []Violation, rule, file, offence string) bool {
	for _, v := range vs {
		if v.Rule == rule && strings.HasPrefix(v.From, file) && strings.Contains(v.To, offence) {
			return strings.Contains(v.String(), file) && strings.Contains(v.String(), offence)
		}
	}
	return false
}

func TestSQLSourceRules(t *testing.T) {
	vs, err := CheckSQLSources(fixtureDir(t, "sqlsrc"))
	if err != nil {
		t.Fatal(err)
	}
	must := func(t *testing.T, rule, file, offence string) {
		t.Helper()
		if !hasViolation(vs, rule, file, offence) {
			t.Errorf("no %s violation naming %s and %q in:\n%s", rule, file, offence, strings.Join(Strings(vs), "\n"))
		}
	}
	mustNot := func(t *testing.T, file string) {
		t.Helper()
		for _, v := range vs {
			if strings.HasPrefix(v.From, file) {
				t.Errorf("unexpected violation %s", v)
			}
		}
	}

	t.Run("R-ARCH-018 LISTEN, NOTIFY and pg_notify are named in Go and in sqlc files, and so is WaitForNotification", func(t *testing.T) {
		must(t, "R-ARCH-018", "internal/proxy/listen.go", "LISTEN trove_changes")
		must(t, "R-ARCH-018", "internal/proxy/listen.go", "pg_notify")
		must(t, "R-ARCH-018", "internal/proxy/listen.go", "WaitForNotification")
		must(t, "R-ARCH-018", "internal/proxy/notify.sql", "NOTIFY trove_changes")
	})
	t.Run("R-ARCH-018 a write to cluster_epoch or cluster_change outside internal/cluster is named", func(t *testing.T) {
		must(t, "R-ARCH-018", "internal/repository/epoch.go", "UPDATE cluster_epoch")
		must(t, "R-ARCH-018", "internal/repository/epoch.go", "DELETE FROM cluster_change")
	})
	t.Run("R-ARCH-018 calling a generated statement that writes the change log from outside internal/cluster is named", func(t *testing.T) {
		must(t, "R-ARCH-018", "internal/repository/bump.go", "BumpEpoch")
	})
	t.Run("R-ARCH-018 internal/cluster, its sqlc files and migrations may write the change log", func(t *testing.T) {
		mustNot(t, "internal/cluster/")
		mustNot(t, "internal/meta/postgres/queries/cluster.sql")
		mustNot(t, "internal/meta/postgres/q/cluster.sql.go")
		mustNot(t, "internal/meta/postgres/migrations/")
	})
	t.Run("R-ARCH-019 session-level SET, RESET and set_config(false) are named, SET LOCAL and UPDATE ... SET are not", func(t *testing.T) {
		must(t, "R-ARCH-019", "internal/proxy/session.go", "SET statement_timeout =")
		must(t, "R-ARCH-019", "internal/proxy/session.go", "SET search_path TO")
		must(t, "R-ARCH-019", "internal/proxy/session.go", "RESET ALL")
		must(t, "R-ARCH-019", "internal/proxy/session.go", "set_config('app.x', '1', false)")
		for _, v := range vs {
			if strings.HasPrefix(v.From, "internal/proxy/session.go") && (strings.Contains(v.To, "SET LOCAL") || strings.Contains(v.To, "UPDATE") || strings.Contains(v.To, "set up") || strings.Contains(v.To, "set valkey")) {
				t.Errorf("fine statement flagged: %s", v)
			}
		}
	})
	t.Run("R-ARCH-019 CREATE TEMP, WITH HOLD and a non-default isolation level are named", func(t *testing.T) {
		must(t, "R-ARCH-019", "internal/proxy/session.go", "CREATE TEMP")
		must(t, "R-ARCH-019", "internal/proxy/session.go", "WITH HOLD")
		must(t, "R-ARCH-019", "internal/proxy/session.go", "ISOLATION LEVEL SERIALIZABLE")
		must(t, "R-ARCH-019", "internal/proxy/iso.go", "pgx.RepeatableRead")
		for _, v := range vs {
			if strings.HasPrefix(v.From, "internal/proxy/session.go") && strings.Contains(v.To, "READ COMMITTED") {
				t.Errorf("READ COMMITTED flagged: %s", v)
			}
		}
	})
	t.Run("R-ARCH-019 the backup snapshot may run at REPEATABLE READ and migrations may use session statements", func(t *testing.T) {
		mustNot(t, "internal/backup/")
		mustNot(t, "internal/meta/postgres/migrations/")
	})
	t.Run("R-ARCH-019 SQL built by Sprintf, Fprintf or concatenation is named outside listq and the catalog exception", func(t *testing.T) {
		must(t, "R-ARCH-019", "internal/proxy/build.go", "fmt.Fprintf")
		must(t, "R-ARCH-019", "internal/proxy/build.go", "fmt.Sprintf")
		must(t, "R-ARCH-019", "internal/proxy/build.go", "+")
		must(t, "R-ARCH-019", "internal/meta/postgres/other.go", "fmt.Sprintf")
		mustNot(t, "internal/meta/postgres/listq/")
		mustNot(t, "internal/meta/postgres/blobrefs.go")
	})
	t.Run("R-ARCH-019 constant SQL and ordinary messages are not assembly", func(t *testing.T) {
		n := 0
		for _, v := range vs {
			if strings.HasPrefix(v.From, "internal/proxy/build.go") {
				n++
			}
		}
		// Fprintf, Sprintf and the concatenation: three, not the constant or the message.
		if n != 3 {
			t.Errorf("internal/proxy/build.go has %d violations, want 3", n)
		}
	})
}
