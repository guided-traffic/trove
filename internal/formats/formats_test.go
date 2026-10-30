package formats

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

type fake struct{ d Descriptor }

func (f fake) Descriptor() Descriptor                        { return f.d }
func (fake) Describe(string, string, bool) RequestDescriptor { return RequestDescriptor{} }

func valid(id string) Descriptor {
	return Descriptor{Facts: catalog.Facts{ID: ID(id), Classes: []Class{Local}, Layout: id, PatternSubject: SubjectPath}}
}

type identity struct{}

func (identity) Normalize(name string) (string, bool) { return name, name != "" }

func TestRegister(t *testing.T) {
	bad := map[string]Descriptor{
		"upper case":       valid("Generic"),
		"one character":    valid("x"),
		"api version":      valid("v1"),
		"no class":         {Facts: catalog.Facts{ID: "nocls", Layout: "x", PatternSubject: SubjectPath}},
		"no layout":        {Facts: catalog.Facts{ID: "nolay", Classes: []Class{Local}, PatternSubject: SubjectPath}},
		"no subject":       {Facts: catalog.Facts{ID: "nosub", Classes: []Class{Local}, Layout: "x"}},
		"class twice":      {Facts: catalog.Facts{ID: "twice", Classes: []Class{Local, Local}, Layout: "x", PatternSubject: SubjectPath}},
		"class out of set": {Facts: catalog.Facts{ID: "oos", Classes: []Class{"mirror"}, Layout: "x", PatternSubject: SubjectPath}},
	}
	for name, d := range bad {
		t.Run("R-ARCH-010 Register panics on "+name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			Register(fake{d})
		})
	}
	t.Run("R-ARCH-010 a duplicate ID panics, a lookup finds the format", func(t *testing.T) {
		Register(fake{valid("probe-fmt")})
		if f, ok := Lookup("probe-fmt"); !ok || f.Descriptor().ID != "probe-fmt" {
			t.Fatal("not found")
		}
		defer func() {
			if recover() == nil {
				t.Fatal("no panic")
			}
		}()
		Register(fake{valid("probe-fmt")})
	})
	t.Run("R-REPO-003 Descriptors lists the registered formats sorted by ID", func(t *testing.T) {
		Register(fake{valid("probe-aaa")})
		ds := Descriptors()
		for i := 1; i < len(ds); i++ {
			if ds[i-1].ID >= ds[i].ID {
				t.Fatalf("not sorted: %v", ds)
			}
		}
	})
}

// TestR_REPO_003_RegisterFillsTheCatalog: the facts a format declares are the
// facts the repository model and the decision point read, with nothing copied by
// hand in between (Q-ARCH-188).
func TestR_REPO_003_RegisterFillsTheCatalog(t *testing.T) {
	d := valid("probe-cat")
	d.PackageNames, d.PatternSubject, d.Names = true, SubjectName, identity{}
	d.Exposure = catalog.Exposure{Exposable: true, Packages: true}
	Register(fake{d})
	e, ok := catalog.Default().Lookup("probe-cat")
	if !ok {
		t.Fatal("Register did not fill catalog.Default")
	}
	if e.ID != "probe-cat" || !e.Supports(Local) || e.Supports(Remote) || e.PatternSubject != catalog.SubjectName ||
		e.Names == nil || !e.Exposure.Exposable || !e.Exposure.Packages {
		t.Fatalf("catalog entry = %+v", e)
	}
}

func catalogClass(catalog.Class) catalog.Class                     { return Local }
func catalogKind(catalog.RequestKind) catalog.RequestKind          { return KindWrite }
func catalogSubject(catalog.PatternSubject) catalog.PatternSubject { return SubjectName }

// TestR_REPO_003_TheAliasesAreTheCatalogTypes: a value of the contract's name is
// a value of the catalog's type, with the catalog's spelling.
func TestR_REPO_003_TheAliasesAreTheCatalogTypes(t *testing.T) {
	if c, k, s := catalogClass(Local), catalogKind(KindWrite), catalogSubject(SubjectName); string(c) != "local" || string(k) != "write" || string(s) != "name" {
		t.Fatal("the spelling of a class, a kind or a subject changed")
	}
}
