package authz

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestR_VULN_028_CoordinateVersionIsReadByTheExposureOnly: ReadScope.VersionIndependent (Q-VULN-81) claims that no input of the
// decision looks at the version of a coordinate except the exposure of an endpoint with rules. The claim holds while
// Coordinate.Version is read in exposure.go alone (paths.go sets it from a path); a new reader anywhere else in the package makes
// a listing of findings decide a package once for versions the decision point may refuse. A new reader has to make
// VersionIndependent false for the subjects it applies to, and then be added here.
func TestR_VULN_028_CoordinateVersionIsReadByTheExposureOnly(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil || len(pkgs) != 1 || len(pkgs[0].Errors) > 0 {
		t.Fatalf("load the package: %v %v", err, pkgs)
	}
	pkg := pkgs[0]
	obj := pkg.Types.Scope().Lookup("Coordinate")
	if obj == nil {
		t.Fatal("authz.Coordinate not found")
	}
	allowed := map[string]bool{"exposure.go": true, "paths.go": true}
	found := map[string]int{}
	isCoordinate := func(tv types.Type) bool {
		for {
			p, ok := tv.(*types.Pointer)
			if !ok {
				break
			}
			tv = p.Elem()
		}
		n, ok := tv.(*types.Named)
		return ok && n.Obj() == obj
	}
	for _, f := range pkg.Syntax {
		file := filepath.Base(pkg.Fset.Position(f.Pos()).Filename)
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if x.Sel.Name == "Version" && isCoordinate(pkg.TypesInfo.TypeOf(x.X)) {
					found[file]++
					if !allowed[file] {
						t.Errorf("%s: Coordinate.Version is used at %s; only exposure.go reads it (Q-VULN-81)", file, pkg.Fset.Position(x.Pos()))
					}
				}
			}
			return true
		})
	}
	if found["exposure.go"] == 0 {
		t.Error("exposure.go no longer reads Coordinate.Version: this test is blind")
	}
}
