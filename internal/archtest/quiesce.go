package archtest

import (
	"fmt"
	"go/types"

	"golang.org/x/tools/go/packages"
)

// CheckQuiesceMarkers implements the marker rule of R-ARCH-024 step 4 (Q-ARCH-318): a type that implements app.Quiescer and has a
// struct field whose type implements app.QuiescesFirst must implement QuiescesFirst itself. The shutdown reads the marker on the
// registered component; a marker on a type held in a named field is not in the holder's method set (only an embedded field promotes
// it), and the holder would then be quiesced in the reverse start order, under the work that stands on it.
func CheckQuiesceMarkers(module string, pkgs []*packages.Package) []Violation {
	var quiescer, first *types.Interface
	for _, p := range pkgs {
		if p.Types == nil || p.PkgPath != module+"/internal/app" {
			continue
		}
		quiescer = lookupInterface(p.Types, "Quiescer")
		first = lookupInterface(p.Types, "QuiescesFirst")
	}
	if quiescer == nil || first == nil {
		// The rule would check nothing: a renamed or moved interface must fail it, not pass it silently.
		return []Violation{{
			Rule:   "R-ARCH-024",
			From:   module + "/internal/app",
			Detail: "the interfaces Quiescer and QuiescesFirst were not found; the marker rule cannot run",
		}}
	}
	implements := func(t types.Type, it *types.Interface) bool {
		return types.Implements(t, it) || types.Implements(types.NewPointer(t), it)
	}
	var out []Violation
	for _, p := range pkgs {
		if p.Types == nil {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			st, ok := tn.Type().Underlying().(*types.Struct)
			if !ok || !implements(tn.Type(), quiescer) || implements(tn.Type(), first) {
				continue
			}
			for i := range st.NumFields() {
				f := st.Field(i)
				if implements(f.Type(), first) {
					out = append(out, Violation{
						Rule:   "R-ARCH-024",
						From:   rel(module, p.PkgPath) + "." + name,
						To:     f.Name(),
						Detail: fmt.Sprintf("the field %s has type %s, which quiesces first, but %s does not implement QuiescesFirst (a named field does not promote it)", f.Name(), f.Type(), name),
					})
				}
			}
		}
	}
	sortViolations(out)
	return out
}

// lookupInterface returns the interface type declared under name in the package, or nil.
func lookupInterface(pkg *types.Package, name string) *types.Interface {
	tn, ok := pkg.Scope().Lookup(name).(*types.TypeName)
	if !ok {
		return nil
	}
	it, _ := tn.Type().Underlying().(*types.Interface)
	return it
}
