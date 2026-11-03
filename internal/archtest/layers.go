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
	layerLeaf   layer = iota // config, errs, buildinfo, observability, cache, meta, backup/tables, tlsfiles, formats/catalog, formats/nuget/{id,version,nupkg,nuspec}, formats/<id>/rows, auth/scope, ci/{reason,claims,subject}, auth/jws, docs/manual: usable from every layer
	layerInfra               // meta/postgres, blob, cluster, outbound, secrets
	layerDomain              // repository, proxy, virtual, formatdata, authz, project, auth, jobs, audit, backup, manual
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
		// The NuGet rule libraries import only errs, the standard library and each other. They are leaves so that
		// the format (edge), its data port (domain) and the jobs that reindex can all use one parser (R-NUGET-003, R-NUGET-005).
		{"internal/formats/nuget/id", layerLeaf},
		{"internal/formats/nuget/version", layerLeaf},
		{"internal/formats/nuget/nupkg", layerLeaf},
		{"internal/formats/nuget/nuspec", layerLeaf},
		{"internal/auth/scope", layerLeaf},
		// The CI federation's pure parts (R-CI-004, R-CI-010 to R-CI-014): reason names, the claim engine
		// and the subject-token validation import only the standard library and each other, so that the
		// authorization engine and the token package can carry their types. internal/auth/jws is the
		// shared compact-JWS verifier that internal/auth/oidc and the subject validation both use (Q-CI-19).
		{"internal/ci/reason", layerLeaf},
		{"internal/ci/claims", layerLeaf},
		{"internal/ci/subject", layerLeaf},
		{"internal/auth/jws", layerLeaf},
		{"docs/manual", layerLeaf}, // the embedded files of the manual, nothing but embed (R-API-054)
		// The trove Bastion (R-BAS-001, Q-BAS-51): the wire contract, the answers' validity rules and the metrics are
		// leaves that the main installation's side and the Bastion's share or read; they import nothing of the module.
		{"internal/bastion/iface", layerLeaf},
		{"internal/bastion/answers", layerLeaf},
		{"internal/bastion/metrics", layerLeaf},

		{"internal/meta/postgres", layerInfra},
		{"internal/blob", layerInfra},
		{"internal/cluster", layerInfra},
		{"internal/outbound", layerInfra},
		{"internal/secrets", layerInfra},
		// The Bastion's clients and stores: the main installation's client, the cache backends, the shared state.
		{"internal/bastion/mainclient", layerInfra},
		{"internal/bastion/cache", layerInfra},
		{"internal/bastion/shared", layerInfra},

		{"internal/repository", layerDomain},
		{"internal/files", layerDomain},
		{"internal/items", layerDomain},
		{"internal/packages", layerDomain},
		{"internal/manual", layerDomain}, // the embedded manual of the help area (R-API-054)
		{"internal/proxy", layerDomain},
		{"internal/virtual", layerDomain},
		{"internal/formatdata", layerDomain},
		{"internal/authz", layerDomain},
		{"internal/project", layerDomain}, // projects, the unit of delegated administration (R-AUTHZ-026)
		{"internal/ci", layerDomain},      // the GitLab CI OIDC federation (R-CI-001 to R-CI-030)
		{"internal/auth", layerDomain},
		{"internal/jobs", layerDomain},
		{"internal/audit", layerDomain},
		{"internal/backup", layerDomain},
		// The main installation's side of the Bastion (seams, recognition, registrations, marking) and the Bastion's
		// guard and download buffer.
		{"internal/bastionmain", layerDomain},
		{"internal/bastion/guard", layerDomain},
		{"internal/bastion/downloads", layerDomain},

		{"internal/httpserver", layerEdge},
		{"internal/api", layerEdge},
		{"internal/webui", layerEdge},
		{"internal/formats", layerEdge},
		{"internal/formathost", layerEdge},
		// The Bastion's request flow and its role: they answer HTTP.
		{"internal/bastion/engine", layerEdge},
		{"internal/bastion/server", layerEdge},

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
