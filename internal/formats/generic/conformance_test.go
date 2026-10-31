package generic

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// conformance is the fixture of the generic format: it has no routes (its protocol
// is /files/, Q-GENERIC-7), no remote or virtual class and no package names, so only
// the checks that do not need those run: Match must still refuse every traversal
// input (check 2), Classify must still say what a stored file, a folder and the root
// are (check 3), and the setup templates and the guide of curl must hold (check 8,
// R-GENERIC-016). Every skip carries its reason, and the kit refuses a skip the
// format contradicts, so a generic remote class (R-GENERIC-009) or a generic virtual
// class (R-GENERIC-010) cannot keep passing without a fixture.
func conformance() formattest.Fixture {
	return formattest.Fixture{
		Rejects: []string{"a/../b.bin", "folder/%2e%2e/file", "file.bin%00"},
		Paths: []formattest.PathCase{
			{Path: "file.bin", Want: formats.PathInfo{Kind: formats.Content}},
			{Path: "folder/sub/file.tar.gz", Want: formats.PathInfo{Kind: formats.Content}},
			{Path: "folder/", Want: formats.PathInfo{Kind: formats.PathComputed}},
			{Path: "", Want: formats.PathInfo{Kind: formats.PathComputed}},
		},
		OptionNames: []string{"TROVE_TOKEN"},
		Skip: map[formattest.Check]string{
			formattest.CheckAmbiguity: "no routes: /files/ is the protocol, served by the generic layer and tested by rawfiles (Q-GENERIC-7, R-GENERIC-002)",
			formattest.CheckRewrite:   "local class only: no remote adapter, no upstream document to rewrite",
			formattest.CheckMerge:     "local class only: no virtual adapter",
			formattest.CheckInspect:   "Inspect reads no archive: an upload is an opaque file",
			formattest.CheckHeaders:   "no routes: the content headers of /files/ are tested with the generic layer (R-STOR-012)",
			formattest.CheckRevision:  "no package rows: the format writes none and keeps no hook",
			formattest.CheckNames:     "no package names",
		},
	}
}

// TestR_ARCH_010_Conformance runs the conformance kit over the generic format.
func TestR_ARCH_010_Conformance(t *testing.T) {
	formattest.Run(t, Format{}, conformance())
}

// TestR_ARCH_010_ConformanceSkipsAreHonest: the skips above hold for the format
// as it is, and the checks it does not skip pass. A format that gains routes, a
// class or package names fails them.
func TestR_ARCH_010_ConformanceSkipsAreHonest(t *testing.T) {
	rep := formattest.Evaluate(Format{}, conformance())
	for c := formattest.CheckAmbiguity; c <= formattest.CheckNames; c++ {
		res := rep.Result(c)
		switch c {
		case formattest.CheckTraversal, formattest.CheckClassify, formattest.CheckSetup:
			if !res.Passed() {
				t.Errorf("%v: %v", c, res)
			}
		default:
			if res.Skipped == "" || len(res.Failures) != 0 {
				t.Errorf("%v: %+v", c, res)
			}
		}
	}
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never
// panics, with the traversal inputs of the kit as seeds.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, Format{}) }
