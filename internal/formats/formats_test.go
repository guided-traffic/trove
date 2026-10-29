package formats

import "testing"

type fake struct{ d Descriptor }

func (f fake) Descriptor() Descriptor                        { return f.d }
func (fake) Describe(string, string, bool) RequestDescriptor { return RequestDescriptor{} }

func valid(id string) Descriptor {
	return Descriptor{ID: ID(id), Classes: []Class{Local}, Layout: id, PatternSubject: SubjectPath}
}

func TestRegister(t *testing.T) {
	bad := map[string]Descriptor{
		"upper case":       valid("Generic"),
		"one character":    valid("x"),
		"api version":      valid("v1"),
		"no class":         {ID: "nocls", Layout: "x", PatternSubject: SubjectPath},
		"no layout":        {ID: "nolay", Classes: []Class{Local}, PatternSubject: SubjectPath},
		"no subject":       {ID: "nosub", Classes: []Class{Local}, Layout: "x"},
		"class twice":      {ID: "twice", Classes: []Class{Local, Local}, Layout: "x", PatternSubject: SubjectPath},
		"class out of set": {ID: "oos", Classes: []Class{Class(9)}, Layout: "x", PatternSubject: SubjectPath},
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
	t.Run("R-REPO-003 Supports follows the class list", func(t *testing.T) {
		d := valid("x1")
		if !d.Supports(Local) || d.Supports(Remote) {
			t.Fatal("Supports")
		}
		if Local.String() != "local" || Remote.String() != "remote" || Virtual.String() != "virtual" {
			t.Fatal("class spelling")
		}
	})
}
