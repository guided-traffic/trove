package archtest

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// contractRule is the requirement the contract-assertion check enforces.
const contractRule = "R-ARCH-008"

// contractAssertionAllowed lists the functions that may assert a value to an interface of
// internal/formats, as "<package path relative to the module>.<function>" with the reason. A
// capability every format has is a method of the contract with a default (formats.Base,
// formats.RemoteBase); one that only some formats have is an optional interface that
// CapabilitiesOf reads off the format when it registers (Q-ARCH-254, Q-ARCH-256). Nothing else asks
// a value what it can do at the place that uses it.
func contractAssertionAllowed() map[string]string {
	return map[string]string{
		"internal/formats.CapabilitiesOf": "the one place that reads an optional capability off a format; Register records the result and the kit calls it",
	}
}

// CheckContractAssertions implements R-ARCH-008 on typed syntax (Q-ARCH-254): no type assertion and no
// type-switch case names an interface declared in internal/formats, outside the functions of
// contractAssertionAllowed. Such an assertion is an optional method the format can forget and the kit
// cannot see; the method belongs to the contract. Assertions to interfaces of the standard library,
// of other packages and to anonymous interfaces are not this rule's business. Test files are not
// loaded.
func CheckContractAssertions(module string, pkgs []*packages.Package) []Violation {
	allowed := contractAssertionAllowed()
	var out []Violation
	for _, p := range pkgs {
		r := rel(module, p.PkgPath)
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.Syntax {
			file := r + "/" + fileBase(p, f)
			for _, decl := range f.Decls {
				owner := r + "."
				switch d := decl.(type) {
				case *ast.FuncDecl:
					owner += d.Name.Name
				default:
					owner += "(declaration)"
				}
				if _, ok := allowed[owner]; ok {
					continue
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.TypeAssertExpr:
						if x.Type != nil {
							out = append(out, contractViolation(p, x.Type, file, owner)...)
						}
					case *ast.TypeSwitchStmt:
						for _, c := range x.Body.List {
							for _, e := range c.(*ast.CaseClause).List {
								out = append(out, contractViolation(p, e, file, owner)...)
							}
						}
					}
					return true
				})
			}
		}
	}
	sortViolations(out)
	return out
}

func contractViolation(p *packages.Package, e ast.Expr, file, owner string) []Violation {
	t := p.TypesInfo.TypeOf(e)
	if t == nil {
		return nil
	}
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || !strings.HasSuffix(n.Obj().Pkg().Path(), "/internal/formats") {
		return nil
	}
	if _, isInterface := n.Underlying().(*types.Interface); !isInterface {
		return nil
	}
	return []Violation{{
		Rule: contractRule, From: file, To: "formats." + n.Obj().Name() + " in " + owner,
		Detail: "a capability every format has is a method of the contract; an optional one is read by formats.CapabilitiesOf alone, not by a type assertion to an interface of internal/formats",
	}}
}
