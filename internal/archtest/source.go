package archtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// patterns are built from pieces so that this file does not itself contain the
// literal text the outbound check greps for (R-ARCH-002's acceptance criterion
// is a grep over internal/ that must match only internal/outbound).
func outboundPatterns() []string {
	return []string{"http." + "Client{", "net." + "Dial"}
}

// walkSources calls fn for every non-test .go file below root/dir (and, if
// withSQL, every .sql file), skipping testdata directories.
func walkSources(root, dir string, withSQL bool, fn func(rel string, content []byte)) error {
	base := filepath.Join(root, dir)
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		isGo := strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
		isSQL := withSQL && strings.HasSuffix(name, ".sql")
		if !isGo && !isSQL {
			return nil
		}
		content, err := os.ReadFile(path) //nolint:gosec // G304: path comes from walking the module's own source tree
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(root, path)
		fn(filepath.ToSlash(r), content)
		return nil
	})
}

// CheckOutbound implements the grep of R-ARCH-002 and R-ARCH-022: an HTTP client
// literal or a dial appears only in internal/outbound and in _test.go files.
func CheckOutbound(root string) ([]Violation, error) {
	var out []Violation
	err := walkSources(root, "internal", false, func(rel string, content []byte) {
		if under(rel, "internal/outbound") {
			return
		}
		for _, pat := range outboundPatterns() {
			if strings.Contains(string(content), pat) {
				out = append(out, Violation{
					Rule: "R-ARCH-022", From: rel, To: pat,
					Detail: "every outbound connection is made by a client from internal/outbound",
				})
			}
		}
	})
	sortViolations(out)
	return out, err
}

// sourceBan forbids a regular expression in the sources below some trees.
type sourceBan struct {
	rule    string
	dirs    []string
	re      *regexp.Regexp
	why     string
	withSQL bool
	// except lists trees below dirs that the rule does not cover.
	except []string
}

func sourceBans() []sourceBan {
	return []sourceBan{
		{
			rule: "R-STOR-046",
			dirs: []string{"internal/blob"},
			// A session-level advisory lock: pg_advisory_lock, pg_try_advisory_lock,
			// their _shared forms and the matching unlocks. The transaction-level
			// pg_advisory_xact_lock does not match.
			re:      regexp.MustCompile(`\bpg_(try_)?advisory_(lock|unlock)(_shared|_all)?\s*\(`),
			why:     "storage correctness rests on row locks; no session-level advisory lock below internal/blob",
			withSQL: true,
		},
		{
			rule:    "R-ARCH-033",
			dirs:    []string{"internal/proxy", "internal/virtual"},
			re:      regexp.MustCompile(`\bpg_(try_)?advisory`),
			why:     "the remote fetch engine and the virtual engine use the Valkey flight lock, never a PostgreSQL advisory lock",
			withSQL: true,
		},
	}
}

// CheckSourceBans implements the source rules of R-STOR-046 and R-ARCH-033.
func CheckSourceBans(root string) ([]Violation, error) {
	var out []Violation
	for _, ban := range sourceBans() {
		for _, dir := range ban.dirs {
			err := walkSources(root, dir, ban.withSQL, func(rel string, content []byte) {
				for _, ex := range ban.except {
					if under(rel, ex) {
						return
					}
				}
				if m := ban.re.Find(content); m != nil {
					out = append(out, Violation{Rule: ban.rule, From: rel, To: string(m), Detail: ban.why})
				}
			})
			if err != nil {
				return nil, err
			}
		}
	}
	sortViolations(out)
	return out, nil
}
