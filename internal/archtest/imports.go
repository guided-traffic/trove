package archtest

import (
	"slices"
	"sort"
	"strings"
)

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

// moduleBan forbids an edge inside this module that the layer rule allows: the
// packages below from may not import the packages below forbidden. It names a
// boundary two neighbouring packages keep on purpose (R-ARCH-002), where the
// layers alone would let the import through.
type moduleBan struct {
	rule      string
	from      string   // module-relative package prefix
	forbidden []string // module-relative package prefixes
	// exact are module-relative packages forbidden by their exact path, not their subtree: a leaf below a forbidden
	// package stays importable.
	exact []string
	// transitive extends the ban from the direct imports of the packages below from to everything they depend on inside the
	// module, so an allowed import cannot bring a forbidden package along.
	transitive bool
	why        string
}

func moduleBans() []moduleBan {
	return []moduleBan{
		{
			rule: "R-ARCH-002", from: "internal/items", forbidden: []string{"internal/authz"},
			why: "the item store makes no decision and calls no decision point: whoever commits or removes decided before, and hands the store its checks as callbacks (Q-ARCH-207)",
		},
		{
			rule: "R-ARCH-002", from: "internal/files", forbidden: []string{"internal/blob", "internal/meta/postgres"},
			why: "the generic layer reaches the object store and the items tables only through internal/items (Q-ARCH-206)",
		},
		{
			rule: "R-ARCH-002", from: "internal/api/rawfiles", forbidden: []string{"internal/api/v1"},
			why: "the raw /files/ routes share their conversions and content responses through internal/api/edge, not through the management API (Q-ARCH-206)",
		},
		{
			rule: "R-ARCH-002", from: "internal/api/edge", forbidden: []string{"internal/api/v1"},
			why: "internal/api/edge is what the entry points share; it imports none of them (Q-ARCH-205)",
		},
		{
			rule: "R-ARCH-002", from: "internal/api/login", forbidden: []string{"internal/api/v1"},
			why: "the browser login routes are an entry point of their own; they share conversions through internal/api/edge and never import the management API (Q-AUTH-175)",
		},
		{
			rule: "R-ARCH-002", from: "internal/api/dispatch", forbidden: []string{"internal/api/v1"},
			why: "the format dispatcher is an entry point of its own; it shares conversions through internal/api/edge and never imports the management API (Q-ARCH-205)",
		},
		{
			rule: "R-ARCH-002", from: "internal/formathost", forbidden: []string{"internal/api/v1"},
			why: "the host of the format contract bridges to the domain and shares conversions through internal/api/edge; it never imports the management API (Q-ARCH-205)",
		},
		{
			rule: "R-ARCH-002", from: "internal/mirror", forbidden: []string{"internal/proxy"},
			why: "the mirror engine reaches the remote engine only through mirror.Host and takes the background share from its options; the composition root binds proxy.BackgroundShare (Q-ARCH-322)",
		},
		{
			rule: "R-ARCH-002", from: "internal/formathost", forbidden: []string{"internal/formatdata"},
			why: "the data port of a format is bound by the composition root; the host reaches it only through Host.FormatData (Q-ARCH-193)",
		},
		{
			rule: "R-ARCH-002", from: "internal/api/dispatch", transitive: true,
			exact:     []string{"internal/auth", "internal/api/registry"},
			forbidden: []string{"internal/auth/session", "internal/auth/registrytoken", "internal/auth/oidc"},
			why: "the dispatcher's match below /v2/ is shared with the registry's enforcement point through the leaf internal/api/registry/route; " +
				"the dispatcher depends on neither the registry root nor the authentication stage, so the registry root may import it without a cycle (Q-OCI-56)",
		},
	}
}

// CheckModuleBans implements the boundaries of moduleBans over the package graph. A transitive ban follows the module's
// imports of the given packages; a package outside the given set ends the walk.
func CheckModuleBans(module string, pkgs []Package) []Violation {
	imports := map[string][]string{}
	for _, p := range pkgs {
		for _, imp := range p.Imports {
			if target := rel(module, imp); target != imp {
				imports[p.Rel] = append(imports[p.Rel], target)
			}
		}
	}
	var out []Violation
	for _, p := range pkgs {
		for _, ban := range moduleBans() {
			if !under(p.Rel, ban.from) {
				continue
			}
			for _, target := range reached(imports, p.Rel, ban.transitive) {
				if underAny(target, ban.forbidden) || slices.Contains(ban.exact, target) {
					out = append(out, Violation{Rule: ban.rule, From: p.Rel, To: target, Detail: ban.why})
				}
			}
		}
	}
	sortViolations(out)
	return out
}

// reached returns the module packages from imports directly, or, with transitive, every one it depends on, sorted.
func reached(imports map[string][]string, from string, transitive bool) []string {
	if !transitive {
		return imports[from]
	}
	seen := map[string]bool{}
	queue := append([]string(nil), imports[from]...)
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if seen[next] || next == from {
			continue
		}
		seen[next] = true
		queue = append(queue, imports[next]...)
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// formatAllowed is what a format package may import from this module (R-ARCH-008,
// R-ARCH-009): the format contract (internal/formats itself), its own tree, the
// format catalog (the name rule and the exposure versioning it declares), the
// untrusted-content limits and readers (R-ARCH-028), the leaf types of the
// vulnerability scanning (ADR-150) and of the mirror's discovery (ADR-152), the error kinds, the
// configuration types it reads, the observability primitives and the repository name (internal/qname,
// the leaf that spells project and key and builds the address of a repository). Everything else
// a format needs comes through Host. The match is the exact package, so a sibling
// format, the object store, the job queue, the cache registry, the SQL
// transaction type and the infrastructure are all refused.
//
// A format that needs a table registered for backup (internal/backup/tables) or a
// revision column (internal/meta) gets that row added here, with the requirement
// that justifies it.
func formatAllowed() []string {
	return []string{
		"internal/formats", "internal/formats/catalog", "internal/formats/untrusted",
		"internal/vuln/advisory", "internal/vuln/match", "internal/mirror/discover",
		"internal/errs", "internal/config", "internal/observability", "internal/qname",
	}
}

// contractAllowed is what the contract package itself (internal/formats) may
// import: a format's list plus the core types it spells for the formats as
// aliases (the decision point's subject, index and actions, the repository model
// and its canonical path, the opaque transaction) and the content-header wrapper
// that ServeRoute runs every content route through (Q-ARCH-189). A format
// package sees none of them: it names core types through the contract's aliases.
func contractAllowed() []string {
	return append(formatAllowed(), "internal/authz", "internal/repository", "internal/meta", "internal/httpserver/content", "internal/servefacts")
}

// kitAllowed is what the conformance kit (internal/formats/formattest) may
// import: what the contract imports, so that check 7 runs the real ServeRoute and
// the in-memory Host can hold a transaction and a subject (Q-ARCH-216). It still
// refuses the storage implementation, the object store and the rest of the
// infrastructure: the kit runs against an in-memory Host.
func kitAllowed() []string { return contractAllowed() }

// allowedFor returns the allowlist of a package of the formats tree.
func allowedFor(rel string) []string {
	switch {
	case rel == formatRoot:
		return contractAllowed()
	case under(rel, formatKit):
		return kitAllowed()
	}
	return formatAllowed()
}

// formatRoot is the tree of the format plug-ins and their contract.
const formatRoot = "internal/formats"

// formatKit is the conformance kit and its test format.
const formatKit = formatRoot + "/formattest"

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
		allowed := allowedFor(p.Rel)
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
				Detail: "a format reaches the core only through Host; it may import the format contract, the catalog, the untrusted-content limits, the leaf types of the vulnerability scanning, errs, config types and observability primitives, and nothing else",
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
