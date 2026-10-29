package archtest

import (
	"fmt"
	"sort"
)

// layer orders the packages of R-ARCH-002. A package may import packages of its
// own layer or a lower one; a lower layer never imports a higher one.
type layer int

const (
	layerLeaf   layer = iota // config, errs, buildinfo, observability, cache, meta, backup/tables, tlsfiles: usable from every layer
	layerInfra               // meta/postgres, blob, cluster, outbound, secrets
	layerDomain              // repository, proxy, authz, auth, jobs, audit, backup
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

		{"internal/meta/postgres", layerInfra},
		{"internal/blob", layerInfra},
		{"internal/cluster", layerInfra},
		{"internal/outbound", layerInfra},
		{"internal/secrets", layerInfra},

		{"internal/repository", layerDomain},
		{"internal/files", layerDomain},
		{"internal/proxy", layerDomain},
		{"internal/authz", layerDomain},
		{"internal/auth", layerDomain},
		{"internal/jobs", layerDomain},
		{"internal/audit", layerDomain},
		{"internal/backup", layerDomain},

		{"internal/httpserver", layerEdge},
		{"internal/api", layerEdge},
		{"internal/webui", layerEdge},
		{"internal/formats", layerEdge},

		{"internal/app", layerApp},
		{"cmd", layerCmd},
	}
}

// unchecked lists the trees the layer rules do not look at: this package, the
// repository's own tooling and the tests that sit outside the production tree.
func unchecked() []string {
	return []string{"internal/archtest", "internal/tools", "test", "tools", "perf", "web", "deploy"}
}

// layerOf returns the layer of a package by its module-relative path.
func layerOf(relPath string) (layer, bool) {
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

func sortViolations(vs []Violation) {
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].String() < vs[j].String() })
}
