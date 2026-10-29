// Package archtest enforces the architecture rules of the specification as
// tests: the layer rules and the import bans of R-ARCH-002, the outbound
// client rule of R-ARCH-022, the no-state rule of R-ARCH-032, the migration
// driver ban of R-ARCH-026, and the source rules of R-STOR-046 and R-ARCH-033.
//
// The checks are plain functions over a package graph or a source tree, so each
// has a test that feeds it a synthetic violation and asserts that the offending
// edge is named. The test that runs them on the real tree is in
// architecture_test.go.
package archtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// Package is the part of `go list -json` the rules need.
type Package struct {
	ImportPath string   // full import path
	Rel        string   // path relative to the module root ("internal/proxy")
	Imports    []string // direct imports, full import paths
}

// Violation is one broken rule. It names the rule, the offending package and,
// for an import, the edge.
type Violation struct {
	Rule   string // requirement ID, e.g. "R-ARCH-002"
	From   string // the offending package or file
	To     string // the import or the matched text; empty if none
	Detail string
}

// String renders "R-ARCH-002: internal/formats/npm -> internal/meta/postgres: detail".
func (v Violation) String() string {
	s := v.Rule + ": " + v.From
	if v.To != "" {
		s += " -> " + v.To
	}
	if v.Detail != "" {
		s += ": " + v.Detail
	}
	return s
}

// Strings renders violations in a stable order.
func Strings(vs []Violation) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.String()
	}
	sort.Strings(out)
	return out
}

type listedPackage struct {
	ImportPath string
	Imports    []string
	DepOnly    bool
	Module     *struct {
		Path string
		Main bool
	}
}

// ListPackages runs `go list -deps -json ./...` in dir and returns the packages
// of the main module with their direct imports, and the module path.
func ListPackages(dir string) (module string, pkgs []Package, err error) {
	cmd := exec.CommandContext(context.Background(), "go", "list", "-deps", "-json=ImportPath,Imports,DepOnly,Module", "./...")
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("go list -deps -json ./... in %s: %w\n%s", dir, err, errb.String())
	}
	dec := json.NewDecoder(&out)
	for {
		var lp listedPackage
		if err := dec.Decode(&lp); err == io.EOF {
			break
		} else if err != nil {
			return "", nil, fmt.Errorf("decoding go list output: %w", err)
		}
		if lp.Module == nil || !lp.Module.Main || lp.DepOnly {
			continue
		}
		module = lp.Module.Path
		pkgs = append(pkgs, Package{ImportPath: lp.ImportPath, Rel: rel(module, lp.ImportPath), Imports: lp.Imports})
	}
	if module == "" {
		return "", nil, fmt.Errorf("go list in %s returned no package of the main module", dir)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ImportPath < pkgs[j].ImportPath })
	return module, pkgs, nil
}

// rel returns the path of an import relative to the module ("" for the root).
func rel(module, importPath string) string {
	if importPath == module {
		return ""
	}
	if r, ok := strings.CutPrefix(importPath, module+"/"); ok {
		return r
	}
	return importPath
}

// under reports whether path equals prefix or lies below it.
func under(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func underAny(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if under(path, p) {
			return true
		}
	}
	return false
}
