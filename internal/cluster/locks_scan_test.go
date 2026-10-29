package cluster

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// advisoryCall matches a call of an advisory-lock function and captures its
// arguments. Arguments may contain one level of parentheses (casts, calls).
var advisoryCall = regexp.MustCompile(`(?i)\bpg_(try_)?advisory_(xact_)?(lock|unlock)(_shared|_all)?\s*\(((?:[^()]|\([^()]*\))*)\)`)

type lockViolation struct{ file, call, why string }

// checkAdvisoryCalls implements the source test of R-ARCH-017: every advisory
// lock takes the two-argument form, its first argument is a category constant
// (a literal of a registered category or a placeholder bound to a constant of
// locks.go), the retired category 2 is never used, and the shared form appears
// only in the backup export code.
func checkAdvisoryCalls(file string, src string) []lockViolation {
	var out []lockViolation
	for _, m := range advisoryCall.FindAllStringSubmatch(src, -1) {
		call, shared, all, args := m[0], strings.EqualFold(m[4], "_shared"), strings.EqualFold(m[4], "_all"), m[5]
		bad := func(why string) { out = append(out, lockViolation{file, strings.Join(strings.Fields(call), " "), why}) }
		if all {
			continue // pg_advisory_unlock_all() takes nothing and names no lock
		}
		parts := splitArgs(args)
		if len(parts) != 2 {
			bad("the single-bigint form (or a wrong argument count); use the two-int4 form with a category")
			continue
		}
		first := strings.TrimSpace(parts[0])
		switch {
		case regexp.MustCompile(`^\d+$`).MatchString(first):
			n, _ := strconv.Atoi(first)
			switch int32(n) {
			case LockLease, LockMigration, LockConfig:
			case LockFetchRetired:
				bad("category 2 is retired and never reused")
			default:
				bad("first argument is not a registered category")
			}
		case regexp.MustCompile(`^(\$\d+|%d|@\w+)(::\w+)?$`).MatchString(first):
			// bound to a category constant of locks.go by the caller
		default:
			bad("first argument must be a category constant of internal/cluster/locks.go")
		}
		if shared && !strings.HasPrefix(filepath.ToSlash(file), "internal/backup/") {
			bad("the shared form is only for the backup export code")
		}
	}
	return out
}

func splitArgs(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func TestAdvisoryLockCategories(t *testing.T) {
	t.Run("R-ARCH-017 the checker rejects one-argument, retired and ad-hoc forms", func(t *testing.T) {
		bad := []string{
			"SELECT pg_advisory_lock(42)",
			"SELECT pg_try_advisory_lock(hashtext('x'))",
			"SELECT pg_advisory_xact_lock(7)",
			"SELECT pg_try_advisory_lock(2, 1)",
			"SELECT pg_try_advisory_lock(99, 1)",
			"SELECT pg_advisory_lock(hashtext('a'), 1)",
			"SELECT pg_advisory_lock_shared(3, 0)",
			"SELECT pg_advisory_unlock_shared(3, 0)",
			"SELECT PG_TRY_ADVISORY_LOCK(5)",
		}
		for _, src := range bad {
			if len(checkAdvisoryCalls("internal/x/x.go", src)) == 0 {
				t.Errorf("%q accepted", src)
			}
		}
		good := []string{
			"SELECT pg_try_advisory_lock($1::int, $2::int)",
			"SELECT pg_try_advisory_lock($1::int, 0)",
			"SELECT pg_advisory_unlock(3, 0)",
			"SELECT pg_advisory_xact_lock(4, 2)",
			"SELECT pg_advisory_xact_lock($1::int, $2::int)",
			"SELECT pg_advisory_unlock_all()",
		}
		for _, src := range good {
			if v := checkAdvisoryCalls("internal/x/x.go", src); len(v) != 0 {
				t.Errorf("%q rejected: %v", src, v)
			}
		}
		if v := checkAdvisoryCalls("internal/backup/export.go", "SELECT pg_advisory_lock_shared(3, 0)"); len(v) != 0 {
			t.Errorf("the shared form in the backup export code was rejected: %v", v)
		}
	})

	t.Run("R-ARCH-017 no Go source and no query of the repository breaks the category rule", func(t *testing.T) {
		root := repoRoot(t)
		var seen int
		for _, dir := range []string{"internal", "cmd"} {
			_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					if d != nil && d.IsDir() && (d.Name() == "testdata" || d.Name() == "node_modules") {
						return filepath.SkipDir
					}
					return nil
				}
				name := d.Name()
				isGo := strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
				if !isGo && !strings.HasSuffix(name, ".sql") {
					return nil
				}
				if strings.Contains(filepath.ToSlash(path), "/internal/archtest/") || strings.HasSuffix(name, ".sql.go") {
					return nil
				}
				b, err := os.ReadFile(path)
				if err != nil {
					t.Error(err)
					return nil
				}
				rel, _ := filepath.Rel(root, path)
				if strings.HasSuffix(name, ".go") && strings.Contains(string(b), "Code generated") {
					return nil
				}
				for _, v := range checkAdvisoryCalls(rel, string(b)) {
					t.Errorf("%s: %s: %s", v.file, v.call, v.why)
				}
				seen += len(advisoryCall.FindAllString(string(b), -1))
				return nil
			})
		}
		if seen == 0 {
			t.Fatal("the scan found no advisory-lock call at all; it does not look at the right files")
		}
	})

	t.Run("R-ARCH-017 the migration runner uses category 3 as this package registers it", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "meta", "postgres", "migrate.go"))
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`const lockMigration int32 = (\d+)`).FindSubmatch(b)
		if m == nil {
			t.Fatal("lockMigration not found in internal/meta/postgres/migrate.go")
		}
		if n, _ := strconv.Atoi(string(m[1])); int32(n) != LockMigration {
			t.Fatalf("migrate.go has category %s, locks.go has %d", m[1], LockMigration)
		}
	})
}
