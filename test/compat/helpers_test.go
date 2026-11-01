//go:build compat

// Package compat_test checks trove's compatibility promises against the latest release: the OpenAPI
// document with the pinned oasdiff (R-ARCH-025) and the migration files (R-ARCH-026). Both read the git
// history and the release tags, so the checkout needs them: the compat job of release.yml uses
// fetch-depth 0 and fetch-tags.
//
// Run it with `make compat-check`, or with `go test -tags compat ./test/compat/...` when oasdiff at the
// pin of tools/versions.env is on PATH or named by TROVE_OASDIFF.
package compat_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// releaseTagPattern selects the release tags of major version 0 (ADR-124). The newest one by version order
// is the baseline of both checks.
const releaseTagPattern = "v0.*"

// migrationsDir is the directory of the embedded migration files (R-ARCH-026).
const migrationsDir = "internal/meta/postgres/migrations"

// oasdiffModule is the Go module of the breaking-change checker; its pin is OASDIFF_VERSION.
const oasdiffModule = "github.com/oasdiff/oasdiff"

// git runs git in dir and returns its standard output. A failure stops the test with git's own message.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			msg = strings.TrimSpace(string(exit.Stderr))
		}
		t.Fatalf("git %s: %s", strings.Join(args, " "), msg)
	}
	return string(out)
}

// repoRoot is the top of the working tree the test runs in. A git worktree has its own top.
func repoRoot(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(git(t, ".", "rev-parse", "--show-toplevel"))
}

// pin reads KEY from tools/versions.env, the one file of the tool pins (ADR-125).
func pin(t *testing.T, root, key string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "tools", "versions.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return value
		}
	}
	t.Fatalf("tools/versions.env has no %s", key)
	return ""
}

// baselineTag returns the newest release tag whose commit is not HEAD's. On the release commit itself HEAD
// would be compared with itself, so the check then compares with the release before it.
func baselineTag(t *testing.T, root string) string {
	t.Helper()
	head := strings.TrimSpace(git(t, root, "rev-parse", "HEAD^{commit}"))
	for _, tag := range strings.Fields(git(t, root, "tag", "--list", releaseTagPattern, "--sort=-v:refname")) {
		if strings.TrimSpace(git(t, root, "rev-parse", tag+"^{commit}")) != head {
			return tag
		}
	}
	t.Fatalf("no release tag %s other than HEAD: fetch the tags (fetch-depth 0 and fetch-tags: true)", releaseTagPattern)
	return ""
}

// oasdiffBinary returns the checker: TROVE_OASDIFF (set by make compat-check), or oasdiff on PATH. It checks
// the module version of the binary that runs, not the pin alone: a module install reports "main" from
// `oasdiff --version`, so the version comes from `go version -m`.
func oasdiffBinary(t *testing.T, root string) string {
	t.Helper()
	bin := os.Getenv("TROVE_OASDIFF")
	if bin == "" {
		found, err := exec.LookPath("oasdiff")
		if err != nil {
			t.Fatal("oasdiff is neither in TROVE_OASDIFF nor on PATH: run make compat-check")
		}
		bin = found
	}
	// go test runs in the package directory, so a path relative to the repository root is made absolute first.
	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	bin = abs
	want := pin(t, root, "OASDIFF_VERSION")
	got := ""
	info, err := exec.Command("go", "version", "-m", bin).Output()
	if err != nil {
		t.Fatalf("go version -m %s: %v", bin, err)
	}
	for _, line := range strings.Split(string(info), "\n") {
		if fields := strings.Fields(line); len(fields) >= 3 && fields[0] == "mod" && fields[1] == oasdiffModule {
			got = fields[2]
		}
	}
	if got != want {
		t.Fatalf("%s is built from %q, tools/versions.env pins %s: run make compat-check", bin, got, want)
	}
	return bin
}

// breaking runs `oasdiff breaking` with --fail-on ERR. It returns the exit code and the output: 1 is the
// verdict that an error-level change exists, and the caller reads the output to see which one.
func breaking(t *testing.T, bin, base, revised string) (int, string) {
	t.Helper()
	out, err := exec.Command(bin, "breaking", base, revised, "--fail-on", "ERR").CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), string(out)
	}
	t.Fatalf("oasdiff breaking %s %s: %v\n%s", base, revised, err, out)
	return -1, ""
}
