package iface_test

import (
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// The guard of R-BAS-009 (Q-BAS-20, Q-BAS-26). The surface of this package, which is the whole
// interface, is held equal to testdata/interface.txt: its first line is the version, the lines
// below are the routes, headers, marking values, feed kinds, subject kinds, problem codes and the
// JSON members of every body, one line per member, as the declarations spell them. A change of
// any line without a change of the version fails TestR_BAS_009_TheInterfaceChangesOnlyWithItsVersion,
// and the regeneration refuses to write a changed surface under an unchanged version:
//
//	go test ./internal/bastion/iface -run TestR_BAS_009 -update-interface
var updateInterface = flag.Bool("update-interface", false, "rewrite testdata/interface.txt from the surface of package iface; refused while the surface changed and the version did not")

const surfaceFile = "testdata/interface.txt"

// versionLine is the first line of the surface file.
func versionLine(v int) string { return "interface version " + strconv.Itoa(v) }

// surfaceOf lists the exported declarations of the package in dir, its tests left out, one line
// each. The line of InterfaceVersion itself is the file's first line, not part of the surface.
func surfaceOf(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			out = append(out, declLines(fset, decl)...)
		}
	}
	sort.Strings(out)
	return out, nil
}

func text(fset *token.FileSet, n ast.Node) string {
	var b strings.Builder
	if err := printer.Fprint(&b, fset, n); err != nil {
		return "?"
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func declLines(fset *token.FileSet, decl ast.Decl) []string {
	var out []string
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if !d.Name.IsExported() {
			return nil
		}
		recv := ""
		if d.Recv != nil && len(d.Recv.List) == 1 {
			t := text(fset, d.Recv.List[0].Type)
			if !ast.IsExported(strings.TrimPrefix(t, "*")) {
				return nil
			}
			recv = "(" + t + ") "
		}
		out = append(out, "func "+recv+d.Name.Name+strings.TrimPrefix(text(fset, d.Type), "func"))
	case *ast.GenDecl:
		for place, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if s.Name.IsExported() {
					out = append(out, typeLines(fset, s)...)
				}
			case *ast.ValueSpec:
				out = append(out, valueLines(fset, d.Tok, s, place)...)
			}
		}
	}
	return out
}

func typeLines(fset *token.FileSet, s *ast.TypeSpec) []string {
	head := "type " + s.Name.Name
	st, ok := s.Type.(*ast.StructType)
	if !ok {
		it, isInterface := s.Type.(*ast.InterfaceType)
		if !isInterface {
			return []string{head + " " + text(fset, s.Type)}
		}
		out := []string{head + " interface"}
		for _, m := range it.Methods.List {
			out = append(out, head+": method "+text(fset, m))
		}
		return out
	}
	out := []string{head + " struct"}
	for _, f := range st.Fields.List {
		tag := ""
		if f.Tag != nil {
			tag = " " + f.Tag.Value
		}
		for _, n := range f.Names {
			if n.IsExported() {
				out = append(out, head+": field "+n.Name+" "+text(fset, f.Type)+tag)
			}
		}
	}
	return out
}

func valueLines(fset *token.FileSet, tok token.Token, s *ast.ValueSpec, place int) []string {
	var out []string
	for i, n := range s.Names {
		if !n.IsExported() || n.Name == "InterfaceVersion" {
			continue
		}
		l := strings.ToLower(tok.String()) + " " + n.Name
		if s.Type != nil {
			l += " " + text(fset, s.Type)
		}
		if i < len(s.Values) {
			l += " = " + text(fset, s.Values[i])
		} else {
			l += fmt.Sprintf(" = (repeated, place %d of its group)", place)
		}
		out = append(out, l)
	}
	return out
}

// render writes the surface file: the version line, then the surface.
func render(version int, lines []string) string {
	return versionLine(version) + "\n" + strings.Join(lines, "\n") + "\n"
}

// parseSurface reads a surface file back into its version and lines.
func parseSurface(raw string) (version int, lines []string, err error) {
	all := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	n, ok := strings.CutPrefix(all[0], "interface version ")
	if !ok {
		return 0, nil, fmt.Errorf("the first line is %q, not %q", all[0], versionLine(0))
	}
	version, err = strconv.Atoi(n)
	if err != nil || version < 1 {
		return 0, nil, fmt.Errorf("the version %q is not a positive integer", n)
	}
	return version, all[1:], nil
}

// parseSurfaceOK is parseSurface for a caller that only asks whether the file is readable.
func parseSurfaceOK(raw string) (version int, lines []string, ok bool) {
	version, lines, err := parseSurface(raw)
	return version, lines, err == nil
}

// diff lists the lines that are in one surface and not in the other, - for removed and + for added.
func diff(want, got []string) []string {
	var out []string
	for _, l := range want {
		if !slices.Contains(got, l) {
			out = append(out, "- "+l)
		}
	}
	for _, l := range got {
		if !slices.Contains(want, l) {
			out = append(out, "+ "+l)
		}
	}
	return out
}

// errRefused is the regeneration's refusal.
var errRefused = errors.New("the surface of the interface changed but InterfaceVersion did not")

// checkUpdate decides whether the file may be rewritten: a surface that differs from the recorded
// one needs a different version; a new file or an unchanged surface is always fine.
func checkUpdate(old string, haveOld bool, version int, lines []string) error {
	if !haveOld {
		return nil
	}
	oldVersion, oldLines, readable := parseSurfaceOK(old)
	if !readable {
		return nil // an unreadable file is rewritten
	}
	if d := diff(oldLines, lines); len(d) > 0 && oldVersion == version {
		return fmt.Errorf("%w (version %d):\n  %s", errRefused, version, strings.Join(d, "\n  "))
	}
	return nil
}

// TestR_BAS_009_TheInterfaceChangesOnlyWithItsVersion: the surface file and the code agree, and the
// file's version is the code's. Adding a field to the check answer without raising the version fails
// here, and -update-interface refuses to regenerate.
func TestR_BAS_009_TheInterfaceChangesOnlyWithItsVersion(t *testing.T) {
	lines, err := surfaceOf(".")
	if err != nil {
		t.Fatal(err)
	}
	if *updateInterface {
		old, readErr := os.ReadFile(surfaceFile)
		if err := checkUpdate(string(old), readErr == nil, iface.InterfaceVersion, lines); err != nil {
			t.Fatalf("refused to write %s: %v\nraise InterfaceVersion in version.go (and say so in the release notes), then regenerate", surfaceFile, err)
		}
		if err := os.MkdirAll(filepath.Dir(surfaceFile), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(surfaceFile, []byte(render(iface.InterfaceVersion, lines)), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(surfaceFile)
	if err != nil {
		t.Fatalf("%v: generate the surface file with -update-interface", err)
	}
	version, want, err := parseSurface(string(raw))
	if err != nil {
		t.Fatalf("%s: %v", surfaceFile, err)
	}
	if version != iface.InterfaceVersion {
		t.Fatalf("%s records version %d, the code says %d: regenerate it with -update-interface in the change that raises the version", surfaceFile, version, iface.InterfaceVersion)
	}
	if d := diff(want, lines); len(d) > 0 {
		t.Fatalf("the Bastion interface changed (%d lines) and InterfaceVersion is still %d (R-BAS-009):\n  %s\n"+
			"Every change of the interface raises the version, whatever its kind: raise InterfaceVersion, then "+
			"go test ./internal/bastion/iface -run TestR_BAS_009 -update-interface", len(d), iface.InterfaceVersion, strings.Join(d, "\n  "))
	}
}

// TestR_BAS_009_TheRegenerationRefusesAChangedSurfaceUnderTheSameVersion: the refusal of
// -update-interface, on a recorded surface, an added field and a raised version.
func TestR_BAS_009_TheRegenerationRefusesAChangedSurfaceUnderTheSameVersion(t *testing.T) {
	base := []string{"type CheckAnswer struct", "type CheckAnswer: field Subject Subject `json:\"subject\"`"}
	added := append(slices.Clone(base), "type CheckAnswer: field Extra string `json:\"extra\"`")
	recorded := render(1, base)
	cases := []struct {
		name    string
		old     string
		haveOld bool
		version int
		lines   []string
		refused bool
	}{
		{"no file yet", "", false, 1, added, false},
		{"unchanged surface, same version", recorded, true, 1, base, false},
		{"added field, same version", recorded, true, 1, added, true},
		{"removed field, same version", render(1, added), true, 1, base, true},
		{"added field, raised version", recorded, true, 2, added, false},
		{"unreadable file", "garbage", true, 1, added, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkUpdate(c.old, c.haveOld, c.version, c.lines)
			if refused := errors.Is(err, errRefused); refused != c.refused {
				t.Fatalf("refused = %v (err %v), want %v", refused, err, c.refused)
			}
		})
	}
}
