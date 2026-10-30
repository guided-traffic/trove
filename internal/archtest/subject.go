package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// subjectRule is the requirement the subject-construction check enforces.
const subjectRule = "R-EP-013"

// CheckSubjectConstruction implements R-EP-013 on typed syntax (Q-EP-20 (b)): the
// subject authorization decides on carries the endpoint whose listener accepted
// the request, and with it the exposure ceiling of every decision. Only
// internal/authz and the one conversion in internal/api/edge make an
// authz.Subject; any other package that built one by hand, or edited a field of
// one it was handed, could decide a request without its ceiling.
//
// The check resolves what an expression is, not how it is spelled, so an import
// alias and a local type alias do not get around it. It flags a composite
// literal, new(authz.Subject), a variable declared without a value (the zero
// subject has no endpoint and so no ceiling) and an assignment to a field of a
// subject. Passing a subject on, as a parameter, a field, a result or the value
// of edge.SubjectFrom, is what every package does and is not flagged. Test files
// are not loaded.
func CheckSubjectConstruction(module string, pkgs []*packages.Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		r := rel(module, p.PkgPath)
		if p.TypesInfo == nil || under(r, "internal/authz") || under(r, "internal/api/edge") {
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
				Rule: subjectRule, From: file, To: what,
				Detail: "only internal/authz and internal/api/edge build an authz.Subject; take the subject of a request from edge.SubjectFrom",
			})
		}
		for _, f := range p.Syntax {
			file := r + "/" + fileBase(p, f)
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CompositeLit:
					if isSubject(p.TypesInfo.TypeOf(x)) {
						add(file, "authz.Subject{}")
					}
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok {
						if _, builtin := p.TypesInfo.Uses[id].(*types.Builtin); builtin && id.Name == "new" {
							if ptr, ok := p.TypesInfo.TypeOf(x).(*types.Pointer); ok && isSubject(ptr.Elem()) {
								add(file, "new(authz.Subject)")
							}
						}
					}
				case *ast.ValueSpec:
					// var s authz.Subject: the zero subject, which has no endpoint.
					if len(x.Values) == 0 && x.Type != nil && isSubject(p.TypesInfo.TypeOf(x.Type)) {
						add(file, "a zero value of authz.Subject")
					}
				case *ast.AssignStmt:
					if x.Tok == token.DEFINE {
						return true
					}
					for _, lhs := range x.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && isSubject(p.TypesInfo.TypeOf(sel.X)) {
							add(file, "an assignment to authz.Subject."+sel.Sel.Name)
						}
					}
				}
				return true
			})
		}
	}
	sortViolations(out)
	return out
}

// isSubject reports whether t is the authz.Subject type itself or a pointer to it.
func isSubject(t types.Type) bool {
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		t = ptr.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return false
	}
	return n.Obj().Name() == "Subject" && strings.HasSuffix(n.Obj().Pkg().Path(), "/internal/authz")
}
