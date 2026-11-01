//go:build compat

package compat_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestR_ARCH_025_NoBreakingChangeAgainstLatestRelease: the api/openapi.yaml of this tree has no error-level
// breaking change against the latest release (R-ARCH-025, acceptance: oasdiff breaking, fails on a change within v1).
func TestR_ARCH_025_NoBreakingChangeAgainstLatestRelease(t *testing.T) {
	root := repoRoot(t)
	bin := oasdiffBinary(t, root)
	tag := baselineTag(t, root)
	base := filepath.Join(t.TempDir(), "openapi-"+tag+".yaml")
	if err := os.WriteFile(base, []byte(git(t, root, "show", tag+":api/openapi.yaml")), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := breaking(t, bin, base, filepath.Join(root, "api", "openapi.yaml")); code != 0 {
		t.Fatalf("api/openapi.yaml breaks %s (exit %d):\n%s", tag, code, out)
	}
}

// TestR_ARCH_025_RemovedOperationFails: the check fails on the fixture that removes POST /api/v1/things, and
// names that operation in its output (a parse error would exit 1 too, so the output is read as well).
func TestR_ARCH_025_RemovedOperationFails(t *testing.T) {
	bin := oasdiffBinary(t, repoRoot(t))
	code, out := breaking(t, bin, filepath.Join("testdata", "openapi", "base.yaml"),
		filepath.Join("testdata", "openapi", "removed-operation.yaml"))
	if code != 1 || !strings.Contains(out, "POST /api/v1/things") {
		t.Fatalf("exit %d, want 1 and the removed operation named:\n%s", code, out)
	}
}

// TestR_ARCH_025_AddedOperationPasses: an added operation is not breaking, so the check passes on the fixture
// that adds DELETE /api/v1/things/{id}. Together with the test above this shows the check reads the change.
func TestR_ARCH_025_AddedOperationPasses(t *testing.T) {
	bin := oasdiffBinary(t, repoRoot(t))
	if code, out := breaking(t, bin, filepath.Join("testdata", "openapi", "base.yaml"),
		filepath.Join("testdata", "openapi", "added-operation.yaml")); code != 0 {
		t.Fatalf("exit %d for an additive change:\n%s", code, out)
	}
}

// TestR_ARCH_025_CheckerIsThePinnedVersion: the binary every check above runs is the module version that
// tools/versions.env pins (OASDIFF_VERSION), read from its build information.
func TestR_ARCH_025_CheckerIsThePinnedVersion(t *testing.T) {
	_ = oasdiffBinary(t, repoRoot(t))
}
