package oci

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeededItemSurface: the door for tests that seed items around the protocol (seed_integration.go) exists only with the integration build
// tag. No other non-test file of the package is tag-gated, and the files of the build without the tag declare no SeededItem and no
// admitSeeded but the one that answers false.
func TestSeededItemSurface(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(string(src), "\n")
		if strings.Contains(first, "go:build integration") {
			if name != "seed_integration.go" {
				t.Errorf("%s is compiled only with the integration tag; only seed_integration.go may be", name)
			}
			continue
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					if ts, ok := sp.(*ast.TypeSpec); ok && ts.Name.Name == "SeededItem" {
						t.Errorf("%s declares SeededItem in the build without the integration tag", name)
					}
				}
			case *ast.FuncDecl:
				if d.Name.Name == "admitSeeded" && name != "seed_default.go" {
					t.Errorf("%s declares admitSeeded outside seed_default.go", name)
				}
			}
		}
	}
}
