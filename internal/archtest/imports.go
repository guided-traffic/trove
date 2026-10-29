package archtest

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

// moduleImportBan forbids a package tree from importing module-internal trees.
type moduleImportBan struct {
	rule      string
	from      []string
	forbidden []string // module-relative prefixes
	why       string
}

func moduleImportBans() []moduleImportBan {
	return []moduleImportBan{
		{
			rule:      "R-ARCH-008",
			from:      []string{"internal/formats"},
			forbidden: []string{"internal/meta/postgres", "internal/blob/s3", "internal/cluster", "internal/outbound", "internal/secrets"},
			why:       "format packages reach the core only through Host and import no infrastructure",
		},
	}
}

// CheckModuleImportBans checks bans between trees of this module, such as the
// formats importing infrastructure.
func CheckModuleImportBans(module string, pkgs []Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		for _, ban := range moduleImportBans() {
			if !underAny(p.Rel, ban.from) {
				continue
			}
			for _, imp := range p.Imports {
				target := rel(module, imp)
				if target != imp && underAny(target, ban.forbidden) {
					out = append(out, Violation{Rule: ban.rule, From: p.Rel, To: target, Detail: ban.why})
				}
			}
		}
	}
	sortViolations(out)
	return out
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
