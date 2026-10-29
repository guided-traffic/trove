package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// stateAllowed lists the trees where a package-level variable of map, slice,
// pointer or sync.Map type is permitted (R-ARCH-032): the caches and the
// cluster state, the observability primitives, and the registries that are
// filled once at start-up and frozen: formats, jobs, errs, secrets, the
// revision registry of meta and the table registry of backup.
func stateAllowed() []string {
	// Tooling and tests are not part of a replica's request path.
	return append(stateTrees(), "internal/archtest", "internal/tools", "test", "tools", "perf", "web", "deploy")
}

func stateTrees() []string {
	return []string{
		"internal/cache", "internal/cluster", "internal/observability",
		"internal/formats", "internal/jobs", "internal/errs", "internal/secrets",
		"internal/meta", "internal/backup/tables",
	}
}

// LoadTyped loads every package of the module in dir with syntax and type
// information, for the checks that need real types.
func LoadTyped(dir string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Dir:  dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedModule,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, p := range pkgs {
		for _, e := range p.Errors {
			bad = append(bad, e.Error())
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("loading packages: %s", strings.Join(bad, "; "))
	}
	return pkgs, nil
}

// CheckPackageState implements R-ARCH-032: no package-level variable of map,
// slice, pointer or sync.Map type outside the allowed trees. A variable whose
// declared or inferred type is an interface (an error sentinel, an interface
// assertion) is not state of these kinds. Generated files and variables with an
// embed directive are skipped: they are not hand-written state.
func CheckPackageState(module string, pkgs []*packages.Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		r := rel(module, p.PkgPath)
		if p.Types == nil || underAny(r, stateAllowed()) {
			continue
		}
		for _, f := range p.Syntax {
			if isGenerated(f) {
				continue
			}
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok.String() != "var" || hasEmbed(gd) {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					for _, name := range vs.Names {
						if name.Name == "_" {
							continue
						}
						obj := p.TypesInfo.Defs[name]
						if obj == nil {
							continue
						}
						if kind := stateKind(obj.Type()); kind != "" {
							pos := p.Fset.Position(name.Pos())
							file := pos.Filename
							if i := strings.Index(file, r+"/"); i >= 0 {
								file = file[i:]
							}
							out = append(out, Violation{
								Rule:   "R-ARCH-032",
								From:   fmt.Sprintf("%s:%d", file, pos.Line),
								To:     name.Name,
								Detail: fmt.Sprintf("package-level variable of %s type %s; allowed only in %s", kind, obj.Type(), strings.Join(stateTrees(), ", ")),
							})
						}
					}
				}
			}
		}
	}
	sortViolations(out)
	return out
}

// immutablePointees are pointer targets that are values in everything but
// syntax: a compiled regular expression is an immutable program shared by every
// goroutine, not state a request can leave behind. The exemption is exact, by
// package path and type name; every other pointer variable is still reported.
func immutablePointees() []string {
	return []string{"regexp.Regexp"}
}

// stateKind names the kind of state a type is, or returns "".
func stateKind(t types.Type) string {
	if n, ok := t.(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "sync" && n.Obj().Name() == "Map" {
		return "sync.Map"
	}
	switch t.Underlying().(type) {
	case *types.Map:
		return "map"
	case *types.Slice:
		return "slice"
	case *types.Pointer:
		if n, ok := t.Underlying().(*types.Pointer).Elem().(*types.Named); ok && n.Obj().Pkg() != nil {
			full := n.Obj().Pkg().Path() + "." + n.Obj().Name()
			for _, ok := range immutablePointees() {
				if full == ok {
					return ""
				}
			}
		}
		return "pointer"
	}
	return ""
}

func isGenerated(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "// Code generated") && strings.HasSuffix(c.Text, "DO NOT EDIT.") {
				return true
			}
		}
	}
	return false
}

func hasEmbed(gd *ast.GenDecl) bool {
	if gd.Doc == nil {
		return false
	}
	for _, c := range gd.Doc.List {
		if strings.HasPrefix(c.Text, "//go:embed") {
			return true
		}
	}
	return false
}
