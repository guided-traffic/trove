//go:build compat

package compat_test

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// migrationName is the file name of a migration: NNNNN_<name>.sql, the rule of the runner (steps.go).
var migrationName = regexp.MustCompile(`^(\d{5})_[a-z0-9][a-z0-9_]*\.sql$`)

// TestR_ARCH_026_MigrationsOnlyAddedSinceRelease: since the latest release, every change of the migration
// directory is an addition, and the additions continue the numbering (R-ARCH-026, acceptance: a file of the
// latest release of the same major that is missing, renamed or changed fails the check). The diff runs with
// --no-renames, so a rename shows as a deletion and an addition.
func TestR_ARCH_026_MigrationsOnlyAddedSinceRelease(t *testing.T) {
	root := repoRoot(t)
	tag := baselineTag(t, root)
	baseline := strings.Fields(git(t, root, "ls-tree", "--name-only", tag+":"+migrationsDir))
	diff := git(t, root, "diff", "--name-status", "--no-renames", tag, "HEAD", "--", migrationsDir)
	if problems := checkMigrationChanges(diff, baseline); len(problems) > 0 {
		t.Fatalf("migrations changed since %s:\n%s", tag, strings.Join(problems, "\n"))
	}
}

// fixtureBaseline is the migration directory of a release that ends at 00016, with two of its names.
var fixtureBaseline = []string{"00015_virtual_claims_ownership.sql", "00016_npm.sql"}

// TestR_ARCH_026_ChangedFilesFail: an edited, a renamed or a removed migration of the release is a problem.
// The rename is shown both as git prints it with renames (R100) and as --no-renames prints it (D and A).
func TestR_ARCH_026_ChangedFilesFail(t *testing.T) {
	const dir = migrationsDir + "/"
	cases := []struct {
		name string
		diff string
	}{
		{"edited", "M\t" + dir + "00016_npm.sql\n"},
		{"renamed with renames", "R100\t" + dir + "00016_npm.sql\t" + dir + "00016_npm_v2.sql\n"},
		{"renamed without renames", "D\t" + dir + "00016_npm.sql\nA\t" + dir + "00016_npm_v2.sql\n"},
		{"missing", "D\t" + dir + "00015_virtual_claims_ownership.sql\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if problems := checkMigrationChanges(c.diff, fixtureBaseline); len(problems) == 0 {
				t.Fatalf("no problem reported for the %s file:\n%s", c.name, c.diff)
			}
		})
	}
}

// TestR_ARCH_026_NumberingContinuesTheSequence: additions pass when they continue the numbering after the
// release's highest number, and fail on a gap, on a number in use, on a duplicate, on a name that is not a
// migration and on a file outside the directory.
func TestR_ARCH_026_NumberingContinuesTheSequence(t *testing.T) {
	const dir = migrationsDir + "/"
	cases := []struct {
		name string
		diff string
		ok   bool
	}{
		{"the next number", "A\t" + dir + "00017_nuget.sql\n", true},
		{"two in sequence", "A\t" + dir + "00017_a.sql\nA\t" + dir + "00018_b.sql\n", true},
		{"no change", "", true},
		{"a gap", "A\t" + dir + "00018_nuget.sql\n", false},
		{"a number in use", "A\t" + dir + "00016_other.sql\n", false},
		{"a duplicate number", "A\t" + dir + "00017_a.sql\nA\t" + dir + "00017_b.sql\n", false},
		{"a name that is not a migration", "A\t" + dir + "notes.sql\n", false},
		{"a file in a subdirectory", "A\t" + dir + "old/00017_a.sql\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := checkMigrationChanges(c.diff, fixtureBaseline)
			if (len(problems) == 0) != c.ok {
				t.Fatalf("problems %q, want ok=%v", problems, c.ok)
			}
		})
	}
}

// checkMigrationChanges returns the reasons the diff of the migration directory since the release breaks
// R-ARCH-026: a line that is not an addition (a change, a rename or a removal), an addition that is not a
// migration file of the directory, and additions that do not continue the numbering after the release's
// highest number. An empty diff has no problem.
func checkMigrationChanges(nameStatus string, baseline []string) []string {
	var problems []string
	last := 0
	for _, name := range baseline {
		n, ok := migrationNumber(name)
		if !ok {
			problems = append(problems, "the release has a file that is not a migration: "+name)
			continue
		}
		last = max(last, n)
	}
	var added []int
	for _, line := range strings.Split(nameStatus, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if fields[0] != "A" || len(fields) != 2 {
			problems = append(problems, fmt.Sprintf("%q: only additions are allowed since the release; an applied migration is never edited, renamed or removed", line))
			continue
		}
		if path.Dir(fields[1]) != migrationsDir {
			problems = append(problems, fmt.Sprintf("%q: not a file of %s", fields[1], migrationsDir))
			continue
		}
		n, ok := migrationNumber(path.Base(fields[1]))
		if !ok {
			problems = append(problems, fmt.Sprintf("%q: not a migration file name NNNNN_<name>.sql", fields[1]))
			continue
		}
		added = append(added, n)
	}
	sort.Ints(added)
	for i, n := range added {
		if want := last + 1 + i; n != want {
			problems = append(problems, fmt.Sprintf("added migration %05d: the numbering continues with %05d", n, want))
		}
	}
	return problems
}

// migrationNumber returns the number of a migration file name, and false for any other name.
func migrationNumber(name string) (int, bool) {
	m := migrationName.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}
