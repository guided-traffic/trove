package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The SQL source rules of R-ARCH-018 and R-ARCH-019. They read every non-test Go
// file (string literals and selected identifiers) and every .sql file below
// internal/ and cmd/, so a statement in a sqlc query, in a migration or in a Go
// constant is looked at the same way.

// sqlTrees are the trees the SQL rules scan.
func sqlTrees() []string { return []string{"internal", "cmd"} }

// sqlSkipped are trees whose sources are the rules' own subject matter.
func sqlSkipped(rel string) bool {
	return underAny(rel, []string{"internal/archtest", "internal/tools"})
}

// sessionClass reports whether a source runs on a session-class connection, where
// session-level statements are legal: migrations run on the migrate class (R-ARCH-026).
func sessionClass(rel string) bool {
	return strings.Contains(rel, "/migrations/") || rel == "internal/meta/postgres/migrate.go"
}

// isolationExempt are the trees R-ARCH-019 lets run at REPEATABLE READ READ ONLY:
// the backup's snapshot (R-BAK-005) and the grant-index build (R-AUTHZ-028).
func isolationExempt(rel string) bool {
	return underAny(rel, []string{"internal/backup", "internal/authz"})
}

// sqlBuilders are the only places that assemble SQL text at run time: the
// confined list/search builder, and the named catalog exception that reads
// referencing columns from the catalog (R-STOR-024 rule 3). A file is listed by
// exact path; the builder by its tree.
func sqlBuilders() (trees, files []string) {
	return []string{"internal/meta/postgres/listq"}, []string{"internal/meta/postgres/blobrefs.go"}
}

// sqlStatementStart matches the start of a statement in a literal: the start of
// the text or after a semicolon.
const sqlStatementStart = `(?:\A|;)\s*`

// statementKeyword is the statement start for the keywords that are also
// ordinary English words (SET, RESET): upper case anywhere a statement can start,
// any case only at the very start of a literal. A log message that reads
// "...; set the key to..." is no statement.
func statementKeyword(word string) string {
	return `(?:\A\s*(?i:` + word + `)|;\s*` + word + `)`
}

type sqlRule struct {
	rule   string
	re     *regexp.Regexp
	why    string
	exempt func(rel string) bool
	// allow, when set, receives the submatches of a match and returns true for a
	// match that is fine after all (RE2 has no look-ahead).
	allow func(sub []string) bool
}

func sqlRules() []sqlRule {
	never := func(string) bool { return false }
	return []sqlRule{
		{
			rule:   "R-ARCH-018",
			re:     regexp.MustCompile(`(?is)` + sqlStatementStart + `(?:LISTEN|UNLISTEN|NOTIFY)\s+(?:"[^"]+"|[a-z_][a-z0-9_]*)\s*(?:,\s*'[^']*')?\s*;?\s*\z`),
			why:    "trove issues no LISTEN or NOTIFY (Q-ARCH-15)",
			exempt: never,
		},
		{
			rule:   "R-ARCH-018",
			re:     regexp.MustCompile(`(?i)\bpg_notify\s*\(`),
			why:    "trove issues no pg_notify (Q-ARCH-15)",
			exempt: never,
		},
		{
			rule: "R-ARCH-018",
			re:   regexp.MustCompile(`(?is)\b(?:insert\s+into|update|delete\s+from|truncate(?:\s+table)?|merge\s+into)\s+(?:only\s+)?(?:"?public"?\.)?"?(?:cluster_epoch|cluster_change)\b`),
			why:  "only internal/cluster writes cluster_epoch and cluster_change; its sqlc statements are the one other place, and migrations create the rows",
			exempt: func(rel string) bool {
				return under(rel, "internal/cluster") || strings.Contains(rel, "/migrations/") || clusterSQLFile(rel)
			},
		},
		{
			rule: "R-ARCH-019",
			re:   regexp.MustCompile(`(?s)` + statementKeyword("SET") + `\s+((?i:SESSION\s+|LOCAL\s+))?([a-zA-Z_][a-zA-Z0-9_.]*)\s*(?:=|(?i:\bTO\b))\s*(?:'[^']*'|"[^"]*"|\$\d+|[A-Za-z0-9_.+\-]+)\s*(?:[;,]|\z)`),
			// SET LOCAL is the transaction-scoped form and is fine.
			allow:  func(sub []string) bool { return strings.HasPrefix(strings.ToUpper(sub[1]), "LOCAL") },
			why:    "session-level SET never works behind a transaction-mode pooler; use SET LOCAL",
			exempt: sessionClass,
		},
		{
			rule:   "R-ARCH-019",
			re:     regexp.MustCompile(`(?s)` + statementKeyword("RESET") + `\s+(?:ALL|[a-zA-Z_][a-zA-Z0-9_.]*)\s*;?\s*\z`),
			why:    "session-level RESET never works behind a transaction-mode pooler",
			exempt: sessionClass,
		},
		{
			rule:   "R-ARCH-019",
			re:     regexp.MustCompile(`(?i)\bset_config\s*\([^)]*,\s*false\s*\)`),
			why:    "set_config(..., false) is a session-level SET; use SET LOCAL or set_config(..., true)",
			exempt: sessionClass,
		},
		{
			rule:   "R-ARCH-019",
			re:     regexp.MustCompile(`(?i)\bCREATE\s+(?:GLOBAL\s+|LOCAL\s+)?(?:TEMP|TEMPORARY)\b`),
			why:    "temporary tables are session state and never work behind a transaction-mode pooler",
			exempt: sessionClass,
		},
		{
			rule:   "R-ARCH-019",
			re:     regexp.MustCompile(`(?i)\bISOLATION\s+LEVEL\s+(?:REPEATABLE\s+READ|SERIALIZABLE|READ\s+UNCOMMITTED)\b`),
			why:    "transactions run at READ COMMITTED; REPEATABLE READ READ ONLY is allowed only for the backup snapshot and the grant-index build",
			exempt: isolationExempt,
		},
		{
			rule:   "R-ARCH-019",
			re:     regexp.MustCompile(`(?i)\bWITH\s+HOLD\b`),
			why:    "WITH HOLD cursors are session state and never work behind a transaction-mode pooler",
			exempt: sessionClass,
		},
	}
}

// clusterSQLFile are the sqlc files of the change log: sqlc generates the typed
// access that internal/cluster calls, and the generator needs the statements in
// the meta/postgres tree.
func clusterSQLFile(rel string) bool {
	return rel == "internal/meta/postgres/queries/cluster.sql" || rel == "internal/meta/postgres/q/cluster.sql.go"
}

var (
	sqlLike = regexp.MustCompile(`(?is)\bselect\b.+\bfrom\b|\binsert\s+into\b|\bupdate\s+\S+\s+set\b|\bdelete\s+from\b|\bwith\s+\w+\s+as\s*\(|\bfor\s+update\b`)
	nameTag = regexp.MustCompile(`(?m)^--\s*name:\s*(\w+)\s+:\w+\s*$`)
)

// commentRE strips SQL comments so a comment that names a forbidden statement is not a statement.
var commentRE = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)

// CheckSQLSources implements the SQL source rules of R-ARCH-018 and R-ARCH-019
// on the tree at root: no LISTEN, NOTIFY or pg_notify; no write to cluster_epoch
// or cluster_change outside internal/cluster; no session-level SET, temporary
// table, non-default isolation level or WITH HOLD cursor; and no SQL assembled
// with fmt.Sprintf, fmt.Fprintf or string concatenation outside the confined
// builders. Each violation names the file, the line where there is one, and the
// offending text.
func CheckSQLSources(root string) ([]Violation, error) {
	var out []Violation
	rules := sqlRules()
	var clusterWriters []string
	var goFiles []string

	for _, tree := range sqlTrees() {
		base := filepath.Join(root, tree)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") || sqlSkipped(rel) {
					return fs.SkipDir
				}
				return nil
			}
			switch {
			case strings.HasSuffix(rel, ".sql"):
				b, err := os.ReadFile(path) //nolint:gosec // G304: walking the module's own source tree
				if err != nil {
					return err
				}
				text := string(b)
				out = append(out, scanSQL(rules, rel, 0, commentRE.ReplaceAllString(text, " "))...)
				if strings.Contains(rel, "/queries/") {
					clusterWriters = append(clusterWriters, writerNames(text)...)
				}
			case strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go"):
				goFiles = append(goFiles, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(goFiles)
	for _, rel := range goFiles {
		vs, err := scanGo(root, rel, rules)
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	sort.Strings(clusterWriters)
	for _, rel := range goFiles {
		if under(rel, "internal/cluster") || under(rel, "internal/meta/postgres/q") {
			continue
		}
		vs, err := callsOfWriters(root, rel, clusterWriters)
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	sortViolations(out)
	return out, nil
}

// scanSQL applies the text rules to one piece of SQL.
func scanSQL(rules []sqlRule, rel string, line int, text string) []Violation {
	var out []Violation
	for _, r := range rules {
		if r.exempt(rel) {
			continue
		}
		for _, sub := range r.re.FindAllStringSubmatch(text, -1) {
			if r.allow != nil && r.allow(sub) {
				continue
			}
			from := rel
			if line > 0 {
				from = fmt.Sprintf("%s:%d", rel, line)
			}
			out = append(out, Violation{Rule: r.rule, From: from, To: strings.Join(strings.Fields(sub[0]), " "), Detail: r.why})
		}
	}
	return out
}

// writerNames returns the names of the sqlc statements of one query file that
// write cluster_epoch or cluster_change.
func writerNames(file string) []string {
	var out []string
	write := regexp.MustCompile(`(?is)\b(?:insert\s+into|update|delete\s+from|truncate(?:\s+table)?)\s+(?:only\s+)?"?(?:cluster_epoch|cluster_change)\b`)
	idx := nameTag.FindAllStringSubmatchIndex(file, -1)
	for i, m := range idx {
		end := len(file)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		if write.MatchString(commentRE.ReplaceAllString(file[m[1]:end], " ")) {
			out = append(out, file[m[2]:m[3]])
		}
	}
	return out
}

// scanGo checks one Go file: its string literals as SQL, the calls and
// concatenations that assemble SQL, and the pgx notification API.
func scanGo(root, rel string, rules []sqlRule) ([]Violation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", rel, err)
	}
	if isGenerated(f) && !strings.HasSuffix(rel, ".sql.go") {
		return nil, nil
	}
	var out []Violation
	trees, files := sqlBuilders()
	builder := underAny(rel, trees)
	for _, p := range files {
		builder = builder || rel == p
	}
	at := func(p token.Pos) int { return fset.Position(p).Line }

	// Ranges of literals that belong to a flagged assembly are reported once, by the assembly.
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			out = append(out, scanSQL(rules, rel, at(n.Pos()), commentRE.ReplaceAllString(s, " "))...)
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "WaitForNotification" {
				out = append(out, Violation{
					Rule: "R-ARCH-018", From: fmt.Sprintf("%s:%d", rel, at(n.Pos())), To: "WaitForNotification",
					Detail: "trove issues no LISTEN, so nothing waits for a notification (Q-ARCH-15)",
				})
			}
			if builder {
				return true
			}
			if name, ok := formatCall(n); ok && len(n.Args) > 0 {
				idx := 0
				if name == "Fprintf" || name == "Appendf" {
					idx = 1
				}
				if idx < len(n.Args) {
					if s, ok := stringConst(n.Args[idx]); ok && sqlLike.MatchString(s) {
						out = append(out, Violation{
							Rule: "R-ARCH-019", From: fmt.Sprintf("%s:%d", rel, at(n.Pos())), To: "fmt." + name,
							Detail: "SQL is built by fmt." + name + "; only internal/meta/postgres/listq and the named catalog exception assemble SQL",
						})
					}
				}
			}
		case *ast.BinaryExpr:
			if builder || n.Op != token.ADD {
				return true
			}
			if lit, other := constAndDynamic(n); lit != "" && sqlLike.MatchString(lit) && other {
				out = append(out, Violation{
					Rule: "R-ARCH-019", From: fmt.Sprintf("%s:%d", rel, at(n.Pos())), To: "+",
					Detail: "SQL is built by string concatenation; only internal/meta/postgres/listq and the named catalog exception assemble SQL",
				})
			}
		}
		return true
	})
	// Isolation level through the pgx API.
	if !isolationExempt(rel) {
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "pgx" {
					switch sel.Sel.Name {
					case "RepeatableRead", "Serializable", "ReadUncommitted":
						out = append(out, Violation{
							Rule: "R-ARCH-019", From: fmt.Sprintf("%s:%d", rel, at(sel.Pos())), To: "pgx." + sel.Sel.Name,
							Detail: "transactions run at READ COMMITTED; REPEATABLE READ READ ONLY is allowed only for the backup snapshot and the grant-index build",
						})
					}
				}
			}
			return true
		})
	}
	return dedupe(out), nil
}

// callsOfWriters reports a call of a generated function that writes the change log
// from outside internal/cluster.
func callsOfWriters(root, rel string, writers []string) ([]Violation, error) {
	if len(writers) == 0 {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", rel, err)
	}
	set := map[string]bool{}
	for _, w := range writers {
		set[w] = true
	}
	var out []Violation
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && set[sel.Sel.Name] {
			out = append(out, Violation{
				Rule: "R-ARCH-018", From: fmt.Sprintf("%s:%d", rel, fset.Position(sel.Pos()).Line), To: sel.Sel.Name,
				Detail: "this generated statement writes cluster_epoch or cluster_change; only internal/cluster calls it",
			})
		}
		return true
	})
	return out, nil
}

func dedupe(vs []Violation) []Violation {
	seen := map[string]bool{}
	out := vs[:0]
	for _, v := range vs {
		if k := v.String(); !seen[k] {
			seen[k] = true
			out = append(out, v)
		}
	}
	return out
}

// formatCall reports a call of fmt.Sprintf, fmt.Fprintf or fmt.Appendf.
func formatCall(c *ast.CallExpr) (string, bool) {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "fmt" {
		return "", false
	}
	switch sel.Sel.Name {
	case "Sprintf", "Fprintf", "Appendf":
		return sel.Sel.Name, true
	}
	return "", false
}

// stringConst returns the value of a string literal or of a concatenation of literals.
func stringConst(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			s, err := strconv.Unquote(e.Value)
			return s, err == nil
		}
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			l, lok := stringConst(e.X)
			r, rok := stringConst(e.Y)
			if lok && rok {
				return l + r, true
			}
		}
	case *ast.ParenExpr:
		return stringConst(e.X)
	}
	return "", false
}

// constAndDynamic returns the literal text on the left-most chain of a
// concatenation and whether any operand of the chain is not a constant.
func constAndDynamic(b *ast.BinaryExpr) (lit string, dynamic bool) {
	var walk func(e ast.Expr)
	walk = func(e ast.Expr) {
		if bb, ok := e.(*ast.BinaryExpr); ok && bb.Op == token.ADD {
			walk(bb.X)
			walk(bb.Y)
			return
		}
		if s, ok := stringConst(e); ok {
			lit += s
			return
		}
		dynamic = true
	}
	walk(b)
	return lit, dynamic
}
