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
	layerLeaf   layer = iota // config, errs, buildinfo, qname, observability, cache, meta, backup/tables, backup/target, tlsfiles, formats/catalog, formats/untrusted, servefacts, formats/nuget/{id,version,nupkg,nuspec}, formats/deb/{control,debfile,version,index}, formats/<id>/rows, auth/scope, ci/{reason,claims,subject}, auth/jws, vuln/{advisory,match,osv,metrics}, mirror/discover, docs/manual: usable from every layer
	layerInfra               // meta/postgres, blob, cluster, outbound, secrets
	layerDomain              // repository, proxy, virtual, formatdata, authz, project, auth, jobs, audit, backup, manual, mirror
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
		// The identity of a repository and of the objects of a project, and the one parser of protocol and raw-file
		// paths (Q-REPO-212): standard library only, so that the authorization engine, the formats, the edge and the
		// Bastion spell one address.
		{"internal/qname", layerLeaf},
		{"internal/observability", layerLeaf},
		{"internal/cache", layerLeaf},
		{"internal/meta", layerLeaf},
		{"internal/backup/tables", layerLeaf},
		// The port of the backup target and its in-memory fake: standard library only, so that the S3 client of internal/blob/s3
		// implements it without importing a domain package (R-BAK-002, Q-BAK-15).
		{"internal/backup/target", layerLeaf},
		// The on-target format of backups (IDs, layout, manifest, blob lists): standard library and zstd only, a leaf so that the
		// S3 sink of a restore (internal/blob/s3) can name its digest type without importing a domain package (R-BAK-021).
		{"internal/backup/format", layerLeaf},
		{"internal/tlsfiles", layerLeaf},
		// The OpenPGP signer and verifier of the Debian repositories (R-DEB-015, R-DEB-016, R-DEB-020, Q-ARCH-383): the only importer
		// of the OpenPGP module, standard library otherwise. A leaf so that the format (edge), its data port (domain) and the
		// repository service's creation hook can all use it.
		{"internal/openpgp", layerLeaf},
		{"internal/formats/catalog", layerLeaf},
		// The rows a serve-time check decides on, read by the item lookup (R-ARCH-027): plain types, no imports. A leaf so that
		// the item store, the format contract and the remote engine spell one type.
		{"internal/servefacts", layerLeaf},
		// The untrusted-content limits and bounded readers import only errs and the standard library. They are a leaf since
		// the vulnerability scanning (Q-VULN-23): the OSV decoder (internal/vuln/osv, a leaf like the other pure parsers)
		// reads the advisory archives through them, and a leaf may not import an edge package.
		{"internal/formats/untrusted", layerLeaf},
		// The NuGet rule libraries import only errs, the standard library and each other. They are leaves so that
		// the format (edge), its data port (domain) and the jobs that reindex can all use one parser (R-NUGET-003, R-NUGET-005).
		{"internal/formats/nuget/id", layerLeaf},
		{"internal/formats/nuget/version", layerLeaf},
		{"internal/formats/nuget/nupkg", layerLeaf},
		{"internal/formats/nuget/nuspec", layerLeaf},
		// The npm version order imports only errs and the standard library. It is a leaf so that the format (edge) and its data
		// port (domain) apply one dist-tag rule: the restore's journal replay withholds a version and moves the tags off it
		// (R-NPM-016, R-NPM-047).
		{"internal/formats/npm/semver", layerLeaf},
		// The Debian rule libraries import only errs, internal/formats/untrusted, the standard library and each other: the deb822
		// parser and writer (control), the package archive reader (debfile), dpkg's version order (version) and the index renderers
		// (index). They are leaves so that the format (edge), its data port and its jobs share one parser (R-DEB-002, R-DEB-007,
		// R-DEB-011, Q-ARCH-383). The values of the data port are internal/formats/deb/rows, a leaf by the rule below.
		{"internal/formats/deb/control", layerLeaf},
		{"internal/formats/deb/debfile", layerLeaf},
		{"internal/formats/deb/version", layerLeaf},
		{"internal/formats/deb/index", layerLeaf},
		// The reader of a Release document of an APT archive (Date, names, checksum section): control and rows only, shared by the
		// remote's format, its jobs and the builds that merge remote members (R-DEB-019, R-DEB-021).
		{"internal/formats/deb/release", layerLeaf},
		// The Ansible Galaxy rule libraries import only errs, internal/formats/untrusted, the standard library, the YAML module (one
		// scalar of meta/runtime.yml) and each other: semantic versions and their sort key (version), the collection archive reader
		// (collection, with collectiontest, its builder of archives for tests), the Galaxy documents (galaxydoc) and the pure merge of
		// version lists (merge). They are leaves so that the format (edge), its data port and the importer share one reader and one
		// renderer (R-ANS-002, R-ANS-004, R-ANS-010, R-ANS-022). The values of the data port are internal/formats/ansible/rows, a leaf
		// by the rule below.
		{"internal/formats/ansible/version", layerLeaf},
		{"internal/formats/ansible/collection", layerLeaf},
		{"internal/formats/ansible/galaxydoc", layerLeaf},
		{"internal/formats/ansible/merge", layerLeaf},
		{"internal/auth/scope", layerLeaf},
		// The CI federation's pure parts (R-CI-004, R-CI-010 to R-CI-014): reason names, the claim engine
		// and the subject-token validation import only the standard library and each other, so that the
		// authorization engine and the token package can carry their types. internal/auth/jws is the
		// shared compact-JWS verifier that internal/auth/oidc and the subject validation both use (Q-CI-19).
		{"internal/ci/reason", layerLeaf},
		{"internal/ci/claims", layerLeaf},
		{"internal/ci/subject", layerLeaf},
		{"internal/auth/jws", layerLeaf},
		// The vulnerability scanning's pure parts (R-VULN-006 to R-VULN-009, Q-VULN-23): the normalized advisory
		// records, the matcher over a format's version order and the OSV and KEV decoders import only the standard
		// library, errs, the untrusted-content limits and each other, so that the format contract can hand advisories to
		// a format and the format's adapter can be matched without the format importing the scanning's domain.
		{"internal/vuln/advisory", layerLeaf},
		{"internal/vuln/match", layerLeaf},
		{"internal/vuln/osv", layerLeaf},
		{"internal/vuln/metrics", layerLeaf}, // the collectors and names of R-VULN-024: the Prometheus client only
		// The mirror's discovery contract (R-REPO-050 to R-REPO-055, ADR-152): the types a format's lister and the mirror
		// engine share, standard library only, so the contract and the formats speak them and the engine imports no edge.
		{"internal/mirror/discover", layerLeaf},
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
		// The in-process main installation of the Bastion's tests (and the spool double of the download buffer's).
		{"internal/bastion/maintest", layerInfra},

		{"internal/repository", layerDomain},
		{"internal/files", layerDomain},
		{"internal/items", layerDomain},
		{"internal/packages", layerDomain},
		{"internal/uploads", layerDomain}, // the multi-request upload sessions (R-STOR-011)
		{"internal/manual", layerDomain},  // the embedded manual of the help area (R-API-054)
		{"internal/avatar", layerDomain},  // the user avatar: processing of the upload and the store (R-API-055)
		{"internal/proxy", layerDomain},
		{"internal/virtual", layerDomain},
		{"internal/formatdata", layerDomain},
		{"internal/authz", layerDomain},
		{"internal/project", layerDomain}, // projects, the unit of delegated administration (R-AUTHZ-026)
		{"internal/ci", layerDomain},      // the GitLab CI OIDC federation (R-CI-001 to R-CI-030)
		// The vulnerability scanning (R-VULN-001 to R-VULN-029): the shared domain types and ports at the root, and the
		// store, sync, evaluate, triage and report packages below it (the pure parts are leaves above).
		{"internal/vuln", layerDomain},
		// The mirror engine of remote repositories (R-REPO-053 to R-REPO-058): dispatch, passes, throttling, status.
		{"internal/mirror", layerDomain},
		// The import tooling (R-IMP-001 to R-IMP-010): a client of a source registry and of a trove repository's publish protocol.
		{"internal/importer", layerDomain},
		{"internal/auth", layerDomain},
		{"internal/jobs", layerDomain},
		{"internal/audit", layerDomain},
		{"internal/backup", layerDomain},
		{"internal/maintenance", layerDomain}, // the deletion pause: pause-deletions, resume-deletions (R-HA-025)
		{"internal/sysstate", layerDomain},    // the banner and the read-only maintenance mode in system_state (R-OPS-024, R-OPS-025)
		{"internal/journal", layerDomain},     // the revocation journal: object, ports, replay handlers (R-HA-054)
		// The main installation's side of the Bastion (seams, recognition, registrations, marking) and the Bastion's
		// guard and download buffer.
		{"internal/bastionmain", layerDomain},
		{"internal/bastion/guard", layerDomain},
		{"internal/bastion/downloads", layerDomain},
		// The replica's standing connection to the main installation (handshake, feed, reachability).
		{"internal/bastion/link", layerDomain},

		// The vocabulary the dispatcher and the registry root share about a request below /v2/ (Q-OCI-56): the standard
		// library and internal/qname only, so the dispatcher needs neither the registry root nor the authentication stage.
		{"internal/api/registry/route", layerLeaf},

		{"internal/httpserver", layerEdge},
		{"internal/api", layerEdge},
		{"internal/webui", layerEdge},
		{"internal/formats", layerEdge},
		{"internal/formathost", layerEdge},
		// The Bastion's request flow and its role: they answer HTTP.
		{"internal/bastion/engine", layerEdge},
		{"internal/bastion/server", layerEdge},
		// The scenario of the whole role in one process (R-BAS-048): it starts the server.
		{"internal/bastion/roletest", layerEdge},

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
