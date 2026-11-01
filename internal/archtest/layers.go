package archtest

import (
	"fmt"
	"sort"
	"strings"
)

// layer orders the packages of R-ARCH-002. A package may import packages of its
// own layer or a lower one; a lower layer never imports a higher one.
type layer int

const (
	layerLeaf   layer = iota // config, errs, buildinfo, observability, cache, meta, backup/tables, tlsfiles, formats/catalog, formats/<id>/rows, auth/scope, docs/manual: usable from every layer
	layerInfra               // meta/postgres, blob, cluster, outbound, secrets
	layerDomain              // repository, proxy, virtual, formatdata, authz, auth, jobs, audit, backup, manual
	layerEdge                // httpserver, api/v1, webui, formats
	layerApp                 // the composition root
	layerCmd                 // the binaries
)

func (l layer) String() string {
	switch l {
	case layerLeaf:
		return "leaf"
	case layerInfra:
		return "infrastructure"
	case layerDomain:
		return "domain"
	case layerEdge:
		return "edge"
	case layerApp:
		return "composition root"
	case layerCmd:
		return "command"
	}
	return fmt.Sprintf("layer(%d)", int(l))
}

type layerRule struct {
	prefix string
	layer  layer
}

// layerTable assigns a layer by the longest matching path prefix. A package
// under internal/ that matches none fails the test, so a new package cannot
// escape the rules: add it here (and to R-ARCH-003).
func layerTable() []layerRule {
	return []layerRule{
		{"internal/config", layerLeaf},
		{"internal/errs", layerLeaf},
		{"internal/buildinfo", layerLeaf},
		{"internal/observability", layerLeaf},
		{"internal/cache", layerLeaf},
		{"internal/meta", layerLeaf},
		{"internal/backup/tables", layerLeaf},
		{"internal/tlsfiles", layerLeaf},
		{"internal/formats/catalog", layerLeaf},
		{"internal/auth/scope", layerLeaf},
		{"docs/manual", layerLeaf}, // the embedded files of the manual, nothing but embed (R-API-054)

		{"internal/meta/postgres", layerInfra},
		{"internal/blob", layerInfra},
		{"internal/cluster", layerInfra},
		{"internal/outbound", layerInfra},
		{"internal/secrets", layerInfra},

		{"internal/repository", layerDomain},
		{"internal/files", layerDomain},
		{"internal/items", layerDomain},
		{"internal/packages", layerDomain},
		{"internal/manual", layerDomain}, // the embedded manual of the help area (R-API-054)
		{"internal/proxy", layerDomain},
		{"internal/virtual", layerDomain},
		{"internal/formatdata", layerDomain},
		{"internal/authz", layerDomain},
		{"internal/auth", layerDomain},
		{"internal/jobs", layerDomain},
		{"internal/audit", layerDomain},
		{"internal/backup", layerDomain},

		{"internal/httpserver", layerEdge},
		{"internal/api", layerEdge},
		{"internal/webui", layerEdge},
		{"internal/formats", layerEdge},
		{"internal/formathost", layerEdge},

		{"internal/app", layerApp},
		{"cmd", layerCmd},
	}
}

// unchecked lists the trees the layer rules do not look at: this package, the
// repository's own tooling and the tests that sit outside the production tree.
func unchecked() []string {
	return []string{"internal/archtest", "internal/tools", "test", "tools", "perf", "web", "deploy"}
}

// isFormatRows reports whether a module-relative path is the rows leaf of a
// format, internal/formats/<id>/rows: the values of that format's data port,
// shared by the format and its internal/formatdata/<id> package (Q-ARCH-193). The
// leaf is a rule, not a list entry (WP3.0r): every format that has a data port
// gets one, and CheckLayers lets it import nothing but internal/errs and the
// standard library.
func isFormatRows(relPath string) bool {
	parts := strings.Split(relPath, "/")
	return len(parts) == 4 && parts[0] == "internal" && parts[1] == "formats" && parts[3] == "rows"
}

// layerOf returns the layer of a package by its module-relative path.
func layerOf(relPath string) (layer, bool) {
	if isFormatRows(relPath) {
		return layerLeaf, true
	}
	best, bestLen, found := layerLeaf, -1, false
	for _, r := range layerTable() {
		if under(relPath, r.prefix) && len(r.prefix) > bestLen {
			best, bestLen, found = r.layer, len(r.prefix), true
		}
	}
	return best, found
}

// CheckLayers implements R-ARCH-002's layer rule: no lower layer imports a
// higher one, and every package under internal/ has a layer.
func CheckLayers(module string, pkgs []Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		if underAny(p.Rel, unchecked()) {
			continue
		}
		if isFormatRows(p.Rel) {
			out = append(out, checkFormatRows(module, p)...)
		}
		from, ok := layerOf(p.Rel)
		if !ok {
			out = append(out, Violation{
				Rule: "R-ARCH-002", From: p.Rel,
				Detail: "package has no layer; add it to layerTable in internal/archtest/layers.go and to R-ARCH-003",
			})
			continue
		}
		for _, imp := range p.Imports {
			if len(imp) <= len(module) || imp[:len(module)] != module || imp[len(module)] != '/' {
				continue
			}
			target := rel(module, imp)
			if underAny(target, unchecked()) {
				continue
			}
			to, known := layerOf(target)
			if known && to > from {
				out = append(out, Violation{
					Rule: "R-ARCH-002", From: p.Rel, To: target,
					Detail: fmt.Sprintf("%s layer must not import the %s layer", from, to),
				})
			}
		}
	}
	sortViolations(out)
	return out
}

// checkFormatRows holds a rows leaf to its rule: it imports the standard library
// and internal/errs and nothing else, so that both the format (an edge package) and
// its domain data package can import it without a cycle or a layer breach.
func checkFormatRows(module string, p Package) []Violation {
	var out []Violation
	for _, imp := range p.Imports {
		if first, _, _ := strings.Cut(imp, "/"); !strings.Contains(first, ".") {
			continue // standard library
		}
		if imp == module+"/internal/errs" {
			continue
		}
		out = append(out, Violation{
			Rule: "R-ARCH-002", From: p.Rel, To: rel(module, imp),
			Detail: "a format's rows leaf imports only internal/errs and the standard library",
		})
	}
	return out
}

func sortViolations(vs []Violation) {
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].String() < vs[j].String() })
}
