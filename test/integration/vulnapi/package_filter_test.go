//go:build integration

package vulnapi

import (
	"slices"
	"testing"
)

// TestR_VULN_020_ThePackageFilterFollowsTheNameRuleOfTheFormat: a NuGet ID is ASCII case-insensitive (R-NUGET-003) and the
// stored name is lowercase, so the filter finds it in any case; an npm name is lowercase by rule and the filter stays exact.
func TestR_VULN_020_ThePackageFilterFollowsTheNameRuleOfTheFormat(t *testing.T) {
	e := newEnv(t)
	e.addFinding(finding{Repo: "nuget-local", Package: "newtonsoft.json", Severity: "high", Detected: "now()"})
	e.addFinding(finding{Repo: "npm-local", Package: "lodash", Severity: "high", Detected: "now()"})
	for _, name := range []string{"Newtonsoft.Json", "newtonsoft.json", "NEWTONSOFT.JSON"} {
		if got := e.list(e.r, e.tokRita, q("package", name)).packages(); !slices.Equal(got, []string{"newtonsoft.json"}) {
			t.Errorf("package=%s: %v", name, got)
		}
	}
	if got := e.list(e.r, e.tokRita, q("package", "Lodash")).packages(); len(got) != 0 {
		t.Errorf("an npm name is compared exactly: %v", got)
	}
	if got := e.list(e.r, e.tokRita, q("package", "lodash")).packages(); len(got) != 1 {
		t.Errorf("package=lodash: %v", got)
	}
}
