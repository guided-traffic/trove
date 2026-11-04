package formats_test

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
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
	// values are the values of the constants and variables, as valueString writes them.
	values map[string]string
	// methods are the exported methods of exported types: type -> method lines.
	methods map[string][]string
}

func newDecls() *decls {
	return &decls{
		types: map[string]*ast.TypeSpec{}, funcs: map[string]*ast.FuncDecl{}, consts: map[string]string{},
		values: map[string]string{}, methods: map[string][]string{},
	}
}

// valueString writes the value of the i-th name of a value spec: its expression, or, for a
// constant that repeats the expression of the spec before it (iota), its place in the group;
// an expression that reads iota carries its place too, so a reordering that changes a value shows.
func valueString(tok token.Token, s *ast.ValueSpec, i, place int) string {
	switch {
	case i < len(s.Values):
		// the printer writes a composite literal whole, where ExprString abbreviates it
		var b strings.Builder
		if err := printer.Fprint(&b, token.NewFileSet(), s.Values[i]); err != nil {
			return "= " + types.ExprString(s.Values[i])
		}
		v := "= " + strings.Join(strings.Fields(b.String()), " ")
		if tok == token.CONST && strings.Contains(v, "iota") {
			v += fmt.Sprintf(" (place %d of its group)", place)
		}
		return v
	case tok == token.CONST:
		return fmt.Sprintf("= (repeated, place %d of its group)", place)
	}
	return ""
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
			for si, s := range x.Specs {
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
					for i, n := range s.Names {
						if n.IsExported() {
							d.consts[n.Name] = typ
							d.values[n.Name] = valueString(x.Tok, s, i, si)
						}
					}
				}
			}
		}
	}
}

func codeDecls(t *testing.T) *decls {
	t.Helper()
	return dirDecls(t, ".")
}

// dirDecls are the declarations of the Go files of a directory, its tests left out.
func dirDecls(t *testing.T, dir string) *decls {
	t.Helper()
	entries, err := os.ReadDir(dir)
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
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
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
	code, words := codeDecls(t), specWords(t)
	var missing []string
	for _, name := range sortedKeys(code.types) {
		if !words[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("exported types of package formats that no page of docs/spec names: %s\nwrite them into the contract blocks of R-ARCH-008/009", strings.Join(missing, ", "))
	}
}

// specWords are the identifiers on every page of docs/spec, the format pages included: the
// words a name must be one of to count as named, as a word-bounded match would find it. The
// pages are read and split once per test binary.
func specWords(t *testing.T) map[string]bool {
	t.Helper()
	specWordSet.once.Do(func() { specWordSet.words, specWordSet.err = readSpecWords() })
	if specWordSet.err != nil {
		t.Fatal(specWordSet.err)
	}
	return specWordSet.words
}

var specWordSet struct {
	once  sync.Once
	words map[string]bool
	err   error
}

func readSpecWords() (map[string]bool, error) {
	files, err := filepath.Glob("../../docs/spec/*.md")
	switch {
	case err != nil:
		return nil, err
	case len(files) == 0:
		return nil, fmt.Errorf("no spec pages in %s", filepath.Dir(specPath))
	}
	formatsPages, _ := filepath.Glob("../../docs/spec/formats/*.md")
	words := map[string]bool{}
	notWord := func(r rune) bool {
		return r != '_' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z')
	}
	for _, f := range append(files, formatsPages...) {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, w := range strings.FieldsFunc(string(b), notWord) {
			words[w] = true
		}
	}
	return words, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The frozen contract (E3.3, ADR-141, Q-ARCH-280). The exported surface of package formats and of
// the catalog leaf a descriptor embeds, one line per member, is held equal to testdata/contract.txt,
// whose first line is ContractVersion. Any change of the surface - an addition as much as a removal
// or a changed signature - fails TestR_ARCH_025_TheFrozenContractChangesOnlyWithItsSurfaceFile
// until the file is regenerated, which is done in the change that carries the change's ADR:
//
//	go test ./internal/formats -run TestR_ARCH_025 -update-contract
var updateContract = flag.Bool("update-contract", false, "rewrite testdata/contract.txt from the exported surface of the format contract")

const surfacePath = "testdata/contract.txt"

// surfaceOf writes what a package exports: every type with its exported fields, interface methods
// and methods, every function with its signature, every constant and variable with its type and value.
func surfaceOf(pkg string, d *decls) []string {
	var out []string
	for _, n := range sortedKeys(d.types) {
		lines := append(members(d.types[n]), d.methods[n]...)
		sort.Strings(lines)
		for _, l := range lines {
			out = append(out, pkg+"."+n+": "+l)
		}
	}
	for _, n := range sortedKeys(d.funcs) {
		out = append(out, pkg+"."+n+": func"+strings.TrimPrefix(typeString(d.funcs[n].Type), "func"))
	}
	for _, n := range sortedKeys(d.consts) {
		l := pkg + "." + n + ": value"
		for _, part := range []string{d.consts[n], d.values[n]} {
			if part != "" {
				l += " " + part
			}
		}
		out = append(out, l)
	}
	return out
}

// leafSurfaceOf writes the types a leaf package contributes to the contract (ADR-150): the types with their exported fields and
// methods, and, when withConsts is set, the constants (the values of an enumeration). The leaf's functions are not part of
// the contract: a format calls match.Affects, but its signature is the matcher's own.
func leafSurfaceOf(pkg string, d *decls, withConsts bool) []string {
	trimmed := newDecls()
	trimmed.types, trimmed.methods = d.types, d.methods
	if withConsts {
		trimmed.consts, trimmed.values = d.consts, d.values
	}
	return surfaceOf(pkg, trimmed)
}

// TestR_ARCH_025_TheFrozenContractChangesOnlyWithItsSurfaceFile: the contract is frozen at version 1
// since the end of M3, and from then on every change of it is an ADR (E3.3). The test cannot tell an
// addition the rule of R-ARCH-025 allows from a breaking change; it makes every change visible in
// the diff of testdata/contract.txt, next to the ADR that decides it and, for a breaking change, the
// raised version on the file's first line.
func TestR_ARCH_025_TheFrozenContractChangesOnlyWithItsSurfaceFile(t *testing.T) {
	lines := []string{fmt.Sprintf("contract version %d", formats.ContractVersion)}
	lines = append(lines, surfaceOf("formats", codeDecls(t))...)
	lines = append(lines, surfaceOf("catalog", dirDecls(t, "catalog"))...)
	// The leaves the contract speaks in (ADR-150): a field added to an advisory or to the matcher's result changes what a
	// format reads, so it is a change of the contract too.
	lines = append(lines, leafSurfaceOf("advisory", dirDecls(t, "../vuln/advisory"), true)...)
	lines = append(lines, leafSurfaceOf("match", dirDecls(t, "../vuln/match"), false)...)
	// The mirror's discovery contract (ADR-152): MirrorLister is an alias of discover.Lister, and its types are what a
	// format's lister reads and returns.
	lines = append(lines, leafSurfaceOf("discover", dirDecls(t, "../mirror/discover"), true)...)
	got := strings.Join(lines, "\n") + "\n"
	if *updateContract {
		if err := os.MkdirAll(filepath.Dir(surfacePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(surfacePath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(surfacePath)
	if err != nil {
		t.Fatalf("%v: generate the surface file with -update-contract", err)
	}
	want := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	var diffs []string
	for _, l := range want {
		if !slices.Contains(lines, l) {
			diffs = append(diffs, "- "+l)
		}
	}
	for _, l := range lines {
		if !slices.Contains(want, l) {
			diffs = append(diffs, "+ "+l)
		}
	}
	if len(diffs) > 0 {
		t.Fatalf("the format contract changed (%d lines):\n  %s\n"+
			"It is frozen at version %d (E3.3, ADR-141): every change is an ADR of its own. An addition (an optional interface "+
			"that CapabilitiesOf finds, a default in Base or RemoteBase, a service of the Host) keeps ContractVersion; a changed "+
			"meaning or a removal raises it and moves every format (R-ARCH-025). Regenerate the file in the change that carries "+
			"the ADR: go test ./internal/formats -run TestR_ARCH_025 -update-contract", len(diffs), strings.Join(diffs, "\n  "), formats.ContractVersion)
	}
}

// TestR_ARCH_008_EveryExportedFunctionAndValueOfTheContractIsNamedInTheSpec: what holds for the types
// holds for the functions, constants and variables of package formats (E3.3): none joins the frozen
// contract without a page of docs/spec naming it.
func TestR_ARCH_008_EveryExportedFunctionAndValueOfTheContractIsNamedInTheSpec(t *testing.T) {
	code, words := codeDecls(t), specWords(t)
	var missing []string
	for _, name := range append(sortedKeys(code.funcs), sortedKeys(code.consts)...) {
		if !words[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("exported functions and values of package formats that no page of docs/spec names: %s\n"+
			"name them in R-ARCH-008 (the blocks or the paragraph of supporting names)", strings.Join(missing, ", "))
	}
}
