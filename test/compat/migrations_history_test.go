//go:build compat

package compat_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// historicalEdit is one commit on main that changed a migration file after the file's first commit, before the
// rule of R-ARCH-026 existed. The entry names the commit, so a later change of the same file is not covered by it.
type historicalEdit struct {
	File   string // the file name in migrationsDir
	Commit string // the full hash of the commit on main that changed the file
	Why    string // why the change is allowed; it is history, not a licence for the next one
}

// historicalEdits are the only changes of a migration file on main after its first commit. They were found with
// `git log --no-renames --name-status -- internal/meta/postgres/migrations` on 2026-11-02; each is the sole change
// of its file after the first commit (`git log --follow` shows no rename).
var historicalEdits = []historicalEdit{
	{
		// 00009_api.sql was first committed in 77408465 and got ten lines at its end in dbb8d18f (the job column
		// ran_by and two indexes). The rule did not exist then. The original version is in no tag; v0.10.0 and the
		// later tags hold the appended one.
		// The owner's dev cluster reported the checksum error for 00019 and not for 00009; the runner checks the
		// applied steps in ascending order and stops at the first mismatch (verifyApplied), so its applied 00009
		// matched the file. Not verified for any other database.
		File:   "00009_api.sql",
		Commit: "dbb8d18f495c72b916dbac492d58512585abc3a1",
		Why:    "appended before the rule; the owner's dev cluster applied it unchanged (verifyApplied order)",
	},
	{
		// 00019_projects.sql was first committed in 007c3dfc as one file and rewritten in f5273866 into the expand
		// steps 00019 to 00025. The dev cluster had applied the first version, and its checksum error is the case
		// of this rule; its database was reset. The rewrite is the file that v0.11.0, v0.11.1 and v0.12.0 hold (all
		// three are commits of main, and they hold no 00019 before the rewrite). The first version is in no tag.
		File:   "00019_projects.sql",
		Commit: "f527386646ca9b5345a3ee05124d146e615a8db0",
		Why:    "the rewrite into expand steps, before the rule; the cause of the dev-cluster reset (the first version is in no tag)",
	},
}

// mainRef is the branch main. A clone without a local main (a pull request checkout) uses origin/main, which the
// compat job has because it fetches the whole history.
func mainRef(t *testing.T, dir string) string {
	t.Helper()
	for _, ref := range []string{"main", "origin/main"} {
		if _, code := gitCode(t, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); code == 0 {
			return ref
		}
	}
	t.Fatal("neither main nor origin/main exists: fetch the branch (fetch-depth 0)")
	return ""
}

// gitCode runs git in dir and returns its standard output and exit code. Exit codes 0 and 1 are answers of the
// commands that use them (ancestry, diff --quiet, rev-parse --quiet); any other failure stops the test.
func gitCode(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err == nil {
		return string(out), 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return string(out), 1
	}
	t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	return "", -1
}

// commitInfo is one line of `git log --format=%H%x09%h%x09%s`.
type commitInfo struct {
	Hash, Short, Subject string
}

// commitsOf lists the commits of a revision range that changed path, oldest first. --full-history keeps a merge
// that changed the file even when the simplified history would skip it.
func commitsOf(t *testing.T, dir, revs, path string) []commitInfo {
	t.Helper()
	out := git(t, dir, "log", "--full-history", "--no-renames", "--format=%H%x09%h%x09%s", revs, "--", path)
	var list []commitInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			t.Fatalf("unexpected git log line %q", line)
		}
		list = append(list, commitInfo{Hash: f[0], Short: f[1], Subject: f[2]})
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list
}

// migrationHistoryProblems returns the reasons the migration files of headRef break R-ARCH-026 against the
// history of mainRef. For every file that main has ever held, its first commit on main is the anchor. A commit
// after the anchor that is reachable from headRef and changes the file is a problem, unless the allowlist names
// it; a removed file is a change. A file whose anchor is not in headRef's history is not yet part of it and is
// not checked. A file that exists only on headRef (a migration of a branch that is not on main) may change.
func migrationHistoryProblems(t *testing.T, dir, mainRef, headRef string, allowed []historicalEdit) []string {
	t.Helper()
	if strings.TrimSpace(git(t, dir, "rev-parse", "--is-shallow-repository")) == "true" {
		t.Fatal("the clone is shallow: the history of main is needed (fetch-depth 0)")
	}
	var problems []string

	// Every migration path that main has ever held, a rename included as its removal and its addition.
	seen := map[string]bool{}
	var paths []string
	for _, p := range strings.Fields(git(t, dir, "log", mainRef, "--no-renames", "--format=", "--name-only", "--", migrationsDir)) {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	allowedFor := map[string]map[string]bool{} // path -> commit hashes allowed for it
	for _, e := range allowed {
		p := migrationsDir + "/" + e.File
		if allowedFor[p] == nil {
			allowedFor[p] = map[string]bool{}
		}
		allowedFor[p][e.Commit] = true
	}

	for _, p := range paths {
		adds := strings.Fields(git(t, dir, "log", mainRef, "--no-merges", "--no-renames", "--full-history", "--diff-filter=A", "--format=%H", "--", p))
		if len(adds) == 0 {
			problems = append(problems, fmt.Sprintf("%s: main has it but no commit of main adds it", p))
			continue
		}
		anchor := adds[len(adds)-1] // the oldest addition on main
		if _, code := gitCode(t, dir, "merge-base", "--is-ancestor", anchor, headRef); code == 1 {
			continue // headRef predates the file on main
		}
		anchorInfo := commitsOf(t, dir, anchor+"^!", p) // the anchor itself, for its subject and short hash
		anchorShort := anchor[:8]
		if len(anchorInfo) > 0 {
			anchorShort = anchorInfo[0].Short
		}

		before := len(problems)
		headFile := strings.TrimSpace(git(t, dir, "ls-tree", "--name-only", headRef, "--", p))
		verb := "changed"
		if headFile == "" {
			verb = "removed"
		}
		for _, c := range commitsOf(t, dir, anchor+".."+headRef, p) {
			if allowedFor[p][c.Hash] {
				continue
			}
			problems = append(problems, fmt.Sprintf(
				"%s: %s by %s (%s) after its first commit on %s (%s); a migration on main is never edited, not even in a comment: fix forward with a new migration (R-ARCH-026)",
				p, verb, c.Short, c.Subject, mainRef, anchorShort))
		}
		if len(problems) == before && len(allowedFor[p]) == 0 {
			if _, code := gitCode(t, dir, "diff", "--quiet", anchor, headRef, "--", p); code == 1 {
				problems = append(problems, fmt.Sprintf(
					"%s: its content at %s differs from its first commit on %s (%s); a migration on main is never edited: fix forward with a new migration (R-ARCH-026)",
					p, headRef, mainRef, anchorShort))
			}
		}
	}

	// An allowlist entry must name a commit of main that changes its file, so that the list cannot outlive its history.
	for _, e := range allowed {
		p := migrationsDir + "/" + e.File
		// Newest first: the last line is the file's first commit, which is an addition and never an edit.
		touching := strings.Fields(git(t, dir, "log", mainRef, "--full-history", "--no-renames", "--format=%H", "--", p))
		found := false
		for i, hash := range touching {
			if hash == e.Commit && i < len(touching)-1 {
				found = true
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("allowlist entry %s of %s names no commit of %s that edits it after its first commit: remove the entry", e.Commit, p, mainRef))
		}
	}
	return problems
}

// TestR_ARCH_026_MigrationsOnMainAreNeverEdited: no migration file changed on main after its first commit, except
// the historical edits named in historicalEdits, and no migration of main was removed or renamed. The check runs on
// the checked-out HEAD, so a pull request and a push to main are both covered (the compat job).
func TestR_ARCH_026_MigrationsOnMainAreNeverEdited(t *testing.T) {
	root := repoRoot(t)
	if problems := migrationHistoryProblems(t, root, mainRef(t, root), "HEAD", historicalEdits); len(problems) > 0 {
		t.Fatalf("migrations changed on main:\n%s", strings.Join(problems, "\n"))
	}
}

// fixtureRepo is a temporary repository with a branch main, for the tests of the history check.
type fixtureRepo struct {
	t   *testing.T
	dir string
}

func newFixtureRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	f := &fixtureRepo{t: t, dir: t.TempDir()}
	git(t, f.dir, "init", "-q", "-b", "main")
	return f
}

// write creates or replaces a file of the migration directory.
func (f *fixtureRepo) write(name, content string) {
	f.t.Helper()
	p := filepath.Join(f.dir, migrationsDir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// remove deletes a file of the migration directory.
func (f *fixtureRepo) remove(name string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.dir, migrationsDir, name)); err != nil {
		f.t.Fatal(err)
	}
}

// commit stages every change and commits it; the identity is the fixture's own.
func (f *fixtureRepo) commit(subject string) string {
	f.t.Helper()
	git(f.t, f.dir, "add", "-A")
	git(f.t, f.dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false",
		"commit", "-q", "--no-verify", "-m", subject)
	return strings.TrimSpace(git(f.t, f.dir, "rev-parse", "HEAD"))
}

func (f *fixtureRepo) checkout(args ...string) {
	f.t.Helper()
	git(f.t, f.dir, append([]string{"checkout", "-q"}, args...)...)
}

// migration is the content of a fixture migration, with the header the runner requires.
func migration(body string) string {
	return "-- trove:phase expand\n" + body + "\n"
}

func expectProblems(t *testing.T, problems []string, wantSubstrings ...string) {
	t.Helper()
	if len(wantSubstrings) == 0 {
		if len(problems) > 0 {
			t.Fatalf("no problem expected, got:\n%s", strings.Join(problems, "\n"))
		}
		return
	}
	if len(problems) == 0 {
		t.Fatalf("a problem was expected containing %q, got none", wantSubstrings)
	}
	joined := strings.Join(problems, "\n")
	for _, want := range wantSubstrings {
		if !strings.Contains(joined, want) {
			t.Fatalf("problems do not contain %q:\n%s", want, joined)
		}
	}
}

// TestR_ARCH_026_HistoryEditAfterFirstCommitFails: a migration of main edited by a later commit of main fails, and
// the message names the file, the commit that changed it and the rule.
func TestR_ARCH_026_HistoryEditAfterFirstCommitFails(t *testing.T) {
	f := newFixtureRepo(t)
	f.write("00001_base.sql", migration("CREATE TABLE a (id int);"))
	f.commit("feat: the first migration")
	f.write("00001_base.sql", migration("CREATE TABLE a (id bigint);"))
	edit := f.commit("fix: a column type")
	problems := migrationHistoryProblems(t, f.dir, "main", "HEAD", nil)
	expectProblems(t, problems, "00001_base.sql", "changed by "+edit[:7], "fix forward with a new migration", "R-ARCH-026")
}

// TestR_ARCH_026_HistoryNewMigrationPasses: additions of new migrations on main pass, and so does a migration that
// is only on the branch being checked, which may still change because main does not have it yet.
func TestR_ARCH_026_HistoryNewMigrationPasses(t *testing.T) {
	f := newFixtureRepo(t)
	f.write("00001_base.sql", migration("CREATE TABLE a (id int);"))
	f.commit("feat: the first migration")
	f.write("00002_next.sql", migration("CREATE TABLE b (id int);"))
	f.commit("feat: the second migration")
	expectProblems(t, migrationHistoryProblems(t, f.dir, "main", "HEAD", nil))

	f.checkout("-b", "feature")
	f.write("00003_branch.sql", migration("CREATE TABLE c (id int);"))
	f.commit("feat: a migration of the branch")
	f.write("00003_branch.sql", migration("CREATE TABLE c (id bigint);"))
	f.commit("fix: a migration that main does not have yet")
	expectProblems(t, migrationHistoryProblems(t, f.dir, "main", "HEAD", nil))
}

// TestR_ARCH_026_HistoryRemovedMigrationFails: a migration of main that a later commit removes fails.
func TestR_ARCH_026_HistoryRemovedMigrationFails(t *testing.T) {
	f := newFixtureRepo(t)
	f.write("00001_base.sql", migration("CREATE TABLE a (id int);"))
	f.commit("feat: the first migration")
	f.remove("00001_base.sql")
	removal := f.commit("chore: remove the migration")
	expectProblems(t, migrationHistoryProblems(t, f.dir, "main", "HEAD", nil), "00001_base.sql", "removed by "+removal[:7], "R-ARCH-026")
}

// TestR_ARCH_026_HistoryRenamedMigrationFails: a rename on main is a removal of the old name, which fails.
func TestR_ARCH_026_HistoryRenamedMigrationFails(t *testing.T) {
	f := newFixtureRepo(t)
	f.write("00001_base.sql", migration("CREATE TABLE a (id int);"))
	f.commit("feat: the first migration")
	content := migration("CREATE TABLE a (id int);")
	f.remove("00001_base.sql")
	f.write("00001_renamed.sql", content)
	rename := f.commit("chore: rename the migration")
	expectProblems(t, migrationHistoryProblems(t, f.dir, "main", "HEAD", nil), "00001_base.sql", "removed by "+rename[:7])
}

// TestR_ARCH_026_HistoryAllowlistedEditPasses: the commit an allowlist entry names is accepted for its file; a later
// commit that changes the same file is not covered by the entry and fails.
func TestR_ARCH_026_HistoryAllowlistedEditPasses(t *testing.T) {
	f := newFixtureRepo(t)
	f.write("00001_base.sql", migration("CREATE TABLE a (id int);"))
	f.commit("feat: the first migration")
	f.write("00001_base.sql", migration("CREATE TABLE a (id bigint);"))
	allowedEdit := f.commit("fix: a historical edit")
	allowed := []historicalEdit{{File: "00001_base.sql", Commit: allowedEdit, Why: "test"}}
	expectProblems(t, migrationHistoryProblems(t, f.dir, "main", "HEAD", allowed))

	f.write("00001_base.sql", migration("CREATE TABLE a (id numeric);"))
	later := f.commit("fix: a second edit that the entry does not cover")
	expectProblems(t, migrationHistoryProblems(t, f.dir, "main", "HEAD", allowed), "changed by "+later[:7])
}

// TestR_ARCH_026_HistoryEditOnBranchFails: a pull request checkout is HEAD of a branch that is not on main. An edit
// there to a migration of main fails, and main itself is not the one that is checked.
func TestR_ARCH_026_HistoryEditOnBranchFails(t *testing.T) {
	f := newFixtureRepo(t)
	f.write("00001_base.sql", migration("CREATE TABLE a (id int);"))
	f.commit("feat: the first migration")
	f.checkout("-b", "pull-request")
	f.write("00001_base.sql", migration("CREATE TABLE a (id bigint);"))
	edit := f.commit("fix: edit a migration of main in a pull request")
	expectProblems(t, migrationHistoryProblems(t, f.dir, "main", "HEAD", nil), "00001_base.sql", "changed by "+edit[:7])
	expectProblems(t, migrationHistoryProblems(t, f.dir, "main", "main", nil))
}

// TestR_ARCH_026_HistoryStaleAllowlistFails: an allowlist entry that names no commit changing its file on main fails,
// so the list cannot keep an exception that the history no longer needs.
func TestR_ARCH_026_HistoryStaleAllowlistFails(t *testing.T) {
	f := newFixtureRepo(t)
	f.write("00001_base.sql", migration("CREATE TABLE a (id int);"))
	first := f.commit("feat: the first migration")
	allowed := []historicalEdit{{File: "00001_base.sql", Commit: first, Why: "names the addition, not an edit"}}
	expectProblems(t, migrationHistoryProblems(t, f.dir, "main", "HEAD", allowed), "allowlist entry "+first, "remove the entry")
}

// TestR_ARCH_026_HistoryBranchBeforeMigrationPasses: a branch that does not contain a migration of main is not
// checked against it, and a branch that contains it is checked, so a clone behind main does not fail on a file it
// has not yet received.
func TestR_ARCH_026_HistoryBranchBeforeMigrationPasses(t *testing.T) {
	f := newFixtureRepo(t)
	f.write("00001_base.sql", migration("CREATE TABLE a (id int);"))
	f.commit("feat: the first migration")
	f.checkout("-b", "behind")
	f.checkout("main")
	f.write("00002_next.sql", migration("CREATE TABLE b (id int);"))
	f.commit("feat: the second migration")
	expectProblems(t, migrationHistoryProblems(t, f.dir, "main", "behind", nil))
}
