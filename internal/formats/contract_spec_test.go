package formats_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The contract the spec writes down (the Go blocks of R-ARCH-008 and R-ARCH-009 in
// docs/spec/architecture.md) and the contract the code exports are one thing, and this test
// holds them together (Q-ARCH-254): every type, function and constant a block declares exists in
// package formats with the same exported members, field and method names and types, and every
// exported type of the package is named in the spec. A field or method added to the code and not
// written into the block fails here with the difference, not at the freeze.

const (
	specPath    = "../../docs/spec/architecture.md"
	specFrom    = "### R-ARCH-008 · Format plug-in contract"
	specThrough = "### R-ARCH-011 ·"
)

// specBlocks returns the Go code blocks of the sections R-ARCH-008 to R-ARCH-010.
func specBlocks(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	from, through := strings.Index(text, specFrom), strings.Index(text, specThrough)
	if from < 0 || through < from {
		t.Fatalf("the spec has no section %q before %q", specFrom, specThrough)
	}
	var blocks []string
	rest := text[from:through]
	for {
		i := strings.Index(rest, "```go\n")
		if i < 0 {
			break
		}
		rest = rest[i+len("```go\n"):]
		j := strings.Index(rest, "\n```")
		if j < 0 {
			t.Fatal("a Go block of the spec is not closed")
		}
		blocks = append(blocks, rest[:j])
		rest = rest[j+len("\n```"):]
	}
	if len(blocks) == 0 {
		t.Fatal("the spec has no Go block in the contract sections")
	}
	return blocks
}

// decls are the declarations of a Go source, by name.
type decls struct {
	types  map[string]*ast.TypeSpec
	funcs  map[string]*ast.FuncDecl
	consts map[string]string // name -> explicit type, "" when none
	// methods are the exported methods of exported types: type -> method lines.
	methods map[string][]string
}

func newDecls() *decls {
	return &decls{types: map[string]*ast.TypeSpec{}, funcs: map[string]*ast.FuncDecl{}, consts: map[string]string{}, methods: map[string][]string{}}
}

// receiver names the type a method belongs to: "Request" for (rq *Request).
func receiver(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return ""
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok && id.IsExported() {
		return id.Name
	}
	return ""
}

func (d *decls) add(f *ast.File) {
	for _, decl := range f.Decls {
		switch x := decl.(type) {
		case *ast.FuncDecl:
			if x.Recv == nil && x.Name.IsExported() {
				d.funcs[x.Name.Name] = x
			}
			if r := receiver(x); r != "" && x.Name.IsExported() {
				d.methods[r] = append(d.methods[r], "method "+x.Name.Name+strings.TrimPrefix(typeString(x.Type), "func"))
			}
		case *ast.GenDecl:
			for _, s := range x.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						d.types[s.Name.Name] = s
					}
				case *ast.ValueSpec:
					typ := ""
					if s.Type != nil {
						typ = typeString(s.Type)
					}
					for _, n := range s.Names {
						if n.IsExported() {
							d.consts[n.Name] = typ
						}
					}
				}
			}
		}
	}
}

func codeDecls(t *testing.T) *decls {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	d := newDecls()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		d.add(f)
	}
	return d
}

func specDecls(t *testing.T, blocks []string) *decls {
	t.Helper()
	d := newDecls()
	for i, b := range blocks {
		if !strings.HasPrefix(strings.TrimSpace(b), "package ") {
			b = "package formats\n\n" + b
		}
		f, err := parser.ParseFile(token.NewFileSet(), fmt.Sprintf("spec block %d", i+1), b, 0)
		if err != nil {
			t.Fatalf("Go block %d of the spec does not parse: %v", i+1, err)
		}
		d.add(f)
	}
	return d
}

// typeString writes a type expression without the names of parameters, so that a spec that names
// them differently still states the same signature.
func typeString(e ast.Expr) string {
	switch x := e.(type) {
	case nil:
		return ""
	case *ast.FuncType:
		return "func(" + fieldTypes(x.Params) + ")" + resultTypes(x.Results)
	case *ast.StarExpr:
		return "*" + typeString(x.X)
	case *ast.ArrayType:
		return "[" + typeString(x.Len) + "]" + typeString(x.Elt)
	case *ast.MapType:
		return "map[" + typeString(x.Key) + "]" + typeString(x.Value)
	case *ast.Ellipsis:
		return "..." + typeString(x.Elt)
	case *ast.ChanType:
		return "chan " + typeString(x.Value)
	case *ast.InterfaceType:
		if x.Methods == nil || len(x.Methods.List) == 0 {
			return "any"
		}
	case *ast.Ident:
		if x.Name == "interface{}" {
			return "any"
		}
	}
	return types.ExprString(e)
}

func fieldTypes(fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	var out []string
	for _, f := range fl.List {
		n := max(len(f.Names), 1)
		for range n {
			out = append(out, typeString(f.Type))
		}
	}
	return strings.Join(out, ", ")
}

func resultTypes(fl *ast.FieldList) string {
	s := fieldTypes(fl)
	switch {
	case s == "":
		return ""
	case strings.Contains(s, ","):
		return " (" + s + ")"
	}
	return " " + s
}

// members lists what a type declaration states, one line per exported member: "field Name Type",
// "embedded Type" or "method Name(params) results".
func members(s *ast.TypeSpec) []string {
	var out []string
	switch x := s.Type.(type) {
	case *ast.StructType:
		for _, f := range x.Fields.List {
			if len(f.Names) == 0 {
				out = append(out, "embedded "+typeString(f.Type))
				continue
			}
			for _, n := range f.Names {
				if n.IsExported() {
					out = append(out, "field "+n.Name+" "+typeString(f.Type))
				}
			}
		}
	case *ast.InterfaceType:
		for _, m := range x.Methods.List {
			if len(m.Names) == 0 {
				out = append(out, "embedded "+typeString(m.Type))
				continue
			}
			ft, _ := m.Type.(*ast.FuncType)
			for _, n := range m.Names {
				if n.IsExported() && ft != nil {
					out = append(out, "method "+n.Name+strings.TrimPrefix(typeString(ft), "func"))
				}
			}
		}
	default:
		prefix := "type "
		if s.Assign.IsValid() {
			prefix = "alias "
		}
		out = append(out, prefix+typeString(s.Type))
	}
	sort.Strings(out)
	return out
}

func diff(name string, spec, code []string) []string {
	var out []string
	for _, l := range spec {
		if !slices.Contains(code, l) {
			out = append(out, fmt.Sprintf("%s: the spec states %q, the code has no such member", name, l))
		}
	}
	for _, l := range code {
		if !slices.Contains(spec, l) {
			out = append(out, fmt.Sprintf("%s: the code has %q, the spec block does not state it", name, l))
		}
	}
	return out
}

// TestR_ARCH_008_TheWrittenContractEqualsTheCode compares every declaration of the contract blocks
// with package formats. It fails with one line per difference.
func TestR_ARCH_008_TheWrittenContractEqualsTheCode(t *testing.T) {
	spec, code := specDecls(t, specBlocks(t)), codeDecls(t)
	var problems []string
	for _, name := range sortedKeys(spec.types) {
		c, ok := code.types[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("type %s: the spec declares it, package formats does not", name))
			continue
		}
		problems = append(problems, diff("type "+name, members(spec.types[name]), members(c))...)
	}
	// A type with methods in the code states them in the spec block as method declarations
	// (func (rq *Request) Addressed() string), so a method added to the code shows here.
	for _, name := range sortedKeys(spec.types) {
		sm, cm := slices.Clone(spec.methods[name]), slices.Clone(code.methods[name])
		if _, isInterface := spec.types[name].Type.(*ast.InterfaceType); isInterface {
			continue
		}
		sort.Strings(sm)
		sort.Strings(cm)
		problems = append(problems, diff("methods of "+name, sm, cm)...)
	}
	for _, name := range sortedKeys(spec.funcs) {
		c, ok := code.funcs[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("func %s: the spec declares it, package formats does not", name))
			continue
		}
		if s, g := typeString(spec.funcs[name].Type), typeString(c.Type); s != g {
			problems = append(problems, fmt.Sprintf("func %s: the spec states %s, the code has %s", name, s, g))
		}
	}
	for _, name := range sortedKeys(spec.consts) {
		typ, ok := code.consts[name]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("const %s: the spec declares it, package formats does not", name))
		case spec.consts[name] != "" && typ != "" && spec.consts[name] != typ:
			problems = append(problems, fmt.Sprintf("const %s: the spec types it %s, the code %s", name, spec.consts[name], typ))
		}
	}
	if len(problems) > 0 {
		t.Fatalf("the contract blocks of R-ARCH-008 and R-ARCH-009 in docs/spec/architecture.md differ from package formats (%d):\n  %s\n"+
			"update the block (and its prose) in the same change that changes the contract", len(problems), strings.Join(problems, "\n  "))
	}
}

// TestR_ARCH_008_EveryExportedTypeOfTheContractIsNamedInTheSpec: a type that joins the exported surface
// of package formats is written into the contract blocks or named in the prose of the spec; it does
// not appear without the spec knowing it.
func TestR_ARCH_008_EveryExportedTypeOfTheContractIsNamedInTheSpec(t *testing.T) {
	code := codeDecls(t)
	files, err := filepath.Glob("../../docs/spec/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no spec pages: %v", err)
	}
	var all strings.Builder
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		all.WriteByte('\n')
	}
	formatsPages, _ := filepath.Glob("../../docs/spec/formats/*.md")
	for _, f := range formatsPages {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		all.WriteByte('\n')
	}
	text := all.String()
	var missing []string
	for _, name := range sortedKeys(code.types) {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(text) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("exported types of package formats that no page of docs/spec names: %s\nwrite them into the contract blocks of R-ARCH-008/009", strings.Join(missing, ", "))
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
