package archtest

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// registryUncleanedRule is the requirement the check enforces.
const registryUncleanedRule = "R-HA-041"

// CheckRegistryUncleaned implements the part of R-HA-041 that the route table cannot see: the prefixes of
// httpserver.Router.HandleRegistryUncleaned serve the uncleaned paths below /v2/ and are admitted by name, so only the
// registry root's enforcement point (internal/api/registry) may register one. The check resolves what an identifier is, so a
// method value or an alias does not get around it. Test files are not loaded.
func CheckRegistryUncleaned(module string, pkgs []*packages.Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		r := rel(module, p.PkgPath)
		if p.TypesInfo == nil || under(r, "internal/httpserver") || under(r, "internal/api/registry") {
			continue
		}
		seen := map[string]bool{}
		for _, f := range p.Syntax {
			file := r + "/" + fileBase(p, f)
			ast.Inspect(f, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				fn, ok := p.TypesInfo.Uses[id].(*types.Func)
				if !ok || fn.Name() != "HandleRegistryUncleaned" || !fromHTTPServer(fn.Pkg()) || seen[file] {
					return true
				}
				seen[file] = true
				out = append(out, Violation{
					Rule: registryUncleanedRule, From: file, To: "httpserver.Router.HandleRegistryUncleaned",
					Detail: "only internal/api/registry registers an uncleaned prefix below /v2/; mount through registry.Enforcer.Mount",
				})
				return true
			})
		}
	}
	sortViolations(out)
	return out
}
