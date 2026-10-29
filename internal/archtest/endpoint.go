package archtest

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// endpointRule is the requirement the endpoint-construction check enforces.
const endpointRule = "R-EP-002"

// CheckEndpointConstruction implements R-EP-002 on typed syntax: the endpoint of
// a request is the one whose listener accepted it, so only the composition root
// (internal/app) builds an endpoint snapshot (NewEndpoint) or an endpoint-bound
// middleware chain (Chain), and nothing outside internal/httpserver makes an
// Endpoint value up. The check resolves what an expression is, not how it is
// spelled, so an import alias, a function value (f := httpserver.NewEndpoint), a
// new(httpserver.Endpoint), a composite literal or a local type alias of Chain
// do not get around it. Test files are not loaded.
func CheckEndpointConstruction(module string, pkgs []*packages.Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		r := rel(module, p.PkgPath)
		if p.TypesInfo == nil || under(r, "internal/httpserver") || under(r, "internal/app") {
			continue
		}
		seen := map[string]bool{}
		add := func(file, what string) {
			key := file + "|" + what
			if seen[key] {
				return
			}
			seen[key] = true
			out = append(out, Violation{
				Rule: endpointRule, From: file, To: what,
				Detail: "only internal/app builds an endpoint or a chain; read the endpoint of a request with httpserver.EndpointFrom",
			})
		}
		for _, f := range p.Syntax {
			file := rel(module, p.PkgPath) + "/" + fileBase(p, f)
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					if fn, ok := p.TypesInfo.Uses[x].(*types.Func); ok && fromHTTPServer(fn.Pkg()) && fn.Name() == "NewEndpoint" {
						add(file, "httpserver.NewEndpoint")
					}
				case *ast.CompositeLit:
					if name := bannedValueType(p.TypesInfo.TypeOf(x)); name != "" {
						add(file, "httpserver."+name+"{}")
					}
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok {
						if _, builtin := p.TypesInfo.Uses[id].(*types.Builtin); builtin && id.Name == "new" {
							if ptr, ok := p.TypesInfo.TypeOf(x).(*types.Pointer); ok {
								if name := bannedValueType(ptr.Elem()); name != "" {
									add(file, "new(httpserver."+name+")")
								}
							}
						}
					}
				}
				return true
			})
		}
		// A variable, field or parameter of the value type is a zero value made up.
		for id, obj := range p.TypesInfo.Defs {
			v, ok := obj.(*types.Var)
			if !ok {
				continue
			}
			if name := bannedValueType(v.Type()); name != "" {
				pos := p.Fset.Position(id.Pos())
				add(rel(module, p.PkgPath)+"/"+baseName(pos.Filename), "a value of httpserver."+name)
			}
		}
	}
	sortViolations(out)
	return out
}

func fileBase(p *packages.Package, f *ast.File) string {
	return baseName(p.Fset.Position(f.Pos()).Filename)
}

func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

func fromHTTPServer(pkg *types.Package) bool {
	return pkg != nil && strings.HasSuffix(pkg.Path(), "/internal/httpserver")
}

// bannedValueType returns "Chain" or "Endpoint" when t is that httpserver type
// itself (not a pointer to it, not an interface).
func bannedValueType(t types.Type) string {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || !fromHTTPServer(n.Obj().Pkg()) {
		return ""
	}
	switch n.Obj().Name() {
	case "Chain", "Endpoint":
		return n.Obj().Name()
	}
	return ""
}
