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
		for _, want := range []string{"R-STOR-046 internal/blob/s3/bad.go", "R-STOR-046 internal/blob/s3/bad.sql", "R-ARCH-033 internal/proxy/q.go"} {
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

func TestRealTree(t *testing.T) {
	t.Run("R-ARCH-002 the layer and import rules hold on the current tree", func(t *testing.T) {
		_, module, pkgs := realTree(t)
		var all []Violation
		all = append(all, CheckLayers(module, pkgs)...)
		all = append(all, CheckImportBans(pkgs)...)
		all = append(all, CheckModuleImportBans(module, pkgs)...)
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
	t.Run("R-ARCH-032 no package-level map, slice, pointer or sync.Map outside the allowed packages", func(t *testing.T) {
		root, module, _ := realTree(t)
		pkgs, err := LoadTyped(root)
		if err != nil {
			t.Fatal(err)
		}
		wantClean(t, CheckPackageState(module, pkgs))
	})
}
