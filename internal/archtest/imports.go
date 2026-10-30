package archtest

import "strings"

// importBan forbids an import everywhere except in the listed packages.
type importBan struct {
	rule      string
	forbidden []string // import path prefixes (external modules or standard packages)
	allowedIn []string // module-relative package prefixes that may import them
	why       string
}

func importBans() []importBan {
	return []importBan{
		{
			rule:      "R-ARCH-002",
			forbidden: []string{"github.com/jackc/pgx", "github.com/jackc/pgconn", "github.com/jackc/pgtype", "github.com/jackc/puddle"},
			allowedIn: []string{"internal/meta/postgres", "internal/cluster"},
			why:       "only internal/meta/postgres and internal/cluster import pgx",
		},
		{
			rule:      "R-ARCH-002",
			forbidden: []string{"github.com/valkey-io/valkey-go"},
			allowedIn: []string{"internal/cluster"},
			why:       "only internal/cluster imports the Valkey client (R-ARCH-031)",
		},
		{
			rule:      "R-ARCH-002",
			forbidden: []string{"github.com/aws/aws-sdk-go-v2", "github.com/aws/aws-sdk-go", "github.com/aws/smithy-go"},
			allowedIn: []string{"internal/blob/s3"},
			why:       "only internal/blob/s3 imports the AWS SDK",
		},
		{
			rule:      "R-ARCH-026",
			forbidden: []string{"database/sql", "github.com/jackc/pgx/v5/stdlib"},
			allowedIn: nil,
			why:       "no package imports database/sql or the pgx stdlib adapter",
		},
	}
}

// pathForbids reports whether imp is one of the forbidden import paths or lies below one.
func pathForbids(imp string, forbidden []string) bool {
	return underAny(imp, forbidden)
}

// CheckImportBans implements the import bans of R-ARCH-002 and R-ARCH-026 for
// every package outside the unchecked trees.
func CheckImportBans(pkgs []Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		if underAny(p.Rel, unchecked()) {
			continue
		}
		for _, imp := range p.Imports {
			for _, ban := range importBans() {
				if pathForbids(imp, ban.forbidden) && !underAny(p.Rel, ban.allowedIn) {
					out = append(out, Violation{Rule: ban.rule, From: p.Rel, To: imp, Detail: ban.why})
				}
			}
		}
	}
	sortViolations(out)
	return out
}

// formatAllowed is what a format package may import from this module (R-ARCH-008,
// R-ARCH-009): the format contract (internal/formats itself), its own tree, the
// format catalog (the name rule and the exposure versioning it declares), the
// error kinds, the configuration types it reads, and the observability
// primitives. Everything else a format needs comes through Host. The match is the
// exact package, so a sibling format, the object store, the job queue, the cache
// registry, the SQL transaction type and the infrastructure are all refused.
//
// A format that needs a table registered for backup (internal/backup/tables) or a
// revision column (internal/meta) gets that row added here, with the requirement
// that justifies it.
func formatAllowed() []string {
	return []string{"internal/formats", "internal/formats/catalog", "internal/errs", "internal/config", "internal/observability"}
}

// formatRoot is the tree of the format plug-ins and their contract.
const formatRoot = "internal/formats"

// formatAll is the package that registers every format at compile time
// (R-ARCH-010).
const formatAll = formatRoot + "/all"

// formatSelf returns the tree of the format package rel ("internal/formats/npm"
// for "internal/formats/npm/parse"), or the contract itself.
func formatSelf(rel string) string {
	rest := strings.TrimPrefix(rel, formatRoot+"/")
	if rest == rel {
		return formatRoot
	}
	id, _, _ := strings.Cut(rest, "/")
	return formatRoot + "/" + id
}

// CheckModuleImportBans implements the format import rule of R-ARCH-008 as an
// allowlist: a package of the formats tree imports from this module only the
// packages of formatAllowed and its own subpackages. Any other edge, including a
// package that did not exist when the rule was written, is a violation that names
// the edge.
func CheckModuleImportBans(module string, pkgs []Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		if !under(p.Rel, formatRoot) {
			continue
		}
		self := formatSelf(p.Rel)
		allowed := formatAllowed()
		for _, imp := range p.Imports {
			target := rel(module, imp)
			if target == imp { // not a package of this module
				continue
			}
			// The registration package imports every format (R-ARCH-010), and only
			// formats: its one job is to call formats.Register for each of them.
			if p.Rel == formatAll && under(target, formatRoot) {
				continue
			}
			// The contract package has no tree of its own to import from.
			own := under(target, self) && (self != formatRoot || target == formatRoot)
			if inList(target, allowed) || own {
				continue
			}
			out = append(out, Violation{
				Rule: "R-ARCH-008", From: p.Rel, To: target,
				Detail: "a format reaches the core only through Host; it may import the format contract, errs, config types and observability primitives, and nothing else",
			})
		}
	}
	sortViolations(out)
	return out
}

func inList(s string, list []string) bool {
	for _, x := range list {
		if s == x {
			return true
		}
	}
	return false
}

// CheckStorageValkey implements the import half of R-STOR-046: nothing below
// internal/blob imports the Valkey client. (CheckImportBans already keeps the
// client inside internal/cluster; this check names R-STOR-046 for the blob
// tree so that its failure message points at the storage requirement.)
func CheckStorageValkey(pkgs []Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		if !under(p.Rel, "internal/blob") {
			continue
		}
		for _, imp := range p.Imports {
			if under(imp, "github.com/valkey-io/valkey-go") {
				out = append(out, Violation{
					Rule: "R-STOR-046", From: p.Rel, To: imp,
					Detail: "storage uses Valkey only through the cluster package's callers, never directly",
				})
			}
		}
	}
	sortViolations(out)
	return out
}

// moduleBan forbids the packages under from to import the packages under
// forbidden, both module-relative. importBans matches external paths only; this
// is the list for edges inside the module that the layer table cannot express
// because both ends lie in the same layer.
type moduleBan struct {
	rule      string
	from      []string
	forbidden []string
	why       string
}

func moduleBans() []moduleBan {
	return []moduleBan{
		{
			rule:      "R-ARCH-002",
			from:      []string{"internal/api/login"},
			forbidden: []string{"internal/api/v1"},
			why:       "the browser login routes are an entry point of their own; they share conversions through internal/api/edge and never import the management API",
		},
	}
}

// CheckModuleBans implements moduleBans: it names every import of a forbidden
// package from a package under a banned tree.
func CheckModuleBans(module string, pkgs []Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		for _, ban := range moduleBans() {
			if !underAny(p.Rel, ban.from) {
				continue
			}
			for _, imp := range p.Imports {
				if target := rel(module, imp); target != imp && underAny(target, ban.forbidden) {
					out = append(out, Violation{Rule: ban.rule, From: p.Rel, To: target, Detail: ban.why})
				}
			}
		}
	}
	sortViolations(out)
	return out
}
