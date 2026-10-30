package testformat_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/formattest/testformat"
)

// TestR_ARCH_010_TheTestFormatPassesTheKit: the conforming variant of the test
// format passes all ten checks, with a handler that tries to delete the content
// headers as well (R-ARCH-007).
func TestR_ARCH_010_TheTestFormatPassesTheKit(t *testing.T) {
	t.Run("conforming", func(t *testing.T) {
		formattest.Run(t, testformat.New(), testformat.Fixture())
	})
	t.Run("a handler that deletes the content headers still sends them", func(t *testing.T) {
		formattest.Run(t, testformat.With(testformat.StripHeaders), testformat.Fixture())
	})
}

// TestR_ARCH_010_Defects: each defect fails the check the spec names for it and
// no other check, which is the acceptance of R-ARCH-010 and R-ARCH-007: routes
// that are not told apart fail check 1, the tarball route registered as Volatile
// fails check 7, and a tag-changing Admin route that raises no revision fails
// check 9.
func TestR_ARCH_010_Defects(t *testing.T) {
	cases := []struct {
		name   string
		defect testformat.Defect
		check  formattest.Check
		match  string // part of the failure message
		// also are the checks that fail with it because the defect breaks what they
		// stand on: routes that are not told apart cannot be served either.
		also []formattest.Check
	}{
		{
			"R-ARCH-010 /{package} and /{scope}/{name} without a disambiguating rule fail check 1", testformat.AmbiguousRoutes, formattest.CheckAmbiguity, "ambiguous routes",
			[]formattest.Check{formattest.CheckHeaders, formattest.CheckNames},
		},
		{"R-ARCH-010 a Match that cleans the path itself fails check 2", testformat.CleaningMatch, formattest.CheckTraversal, "resolves to route", nil},
		{"R-ARCH-010 a package document classified as content fails check 3", testformat.ContentMetadata, formattest.CheckClassify, "says content", nil},
		{"R-ARCH-010 a rewrite that is not deterministic fails check 4", testformat.NondeterministicRewrite, formattest.CheckRewrite, "not deterministic", nil},
		{"R-ARCH-010 a rewrite that keeps an upstream link fails check 4", testformat.UpstreamLeak, formattest.CheckRewrite, "still links to the upstream", nil},
		{"R-ARCH-010 a merge in which another member wins a version fails check 5", testformat.MergeLastWins, formattest.CheckMerge, "per-version source rule", nil},
		{"R-ARCH-010 an Inspect that accepts crafted input fails check 6", testformat.InspectAccepts, formattest.CheckInspect, "accepted the input", nil},
		{"R-ARCH-010 an Inspect that answers 400 for a bound crossed fails check 6", testformat.InspectWrongKind, formattest.CheckInspect, "want 413 or 422", nil},
		{"R-ARCH-010 an Inspect that holds 640 MiB before it refuses fails check 6", testformat.InspectHeap, formattest.CheckInspect, "the heap grew", nil},
		{
			"R-ARCH-007 the npm-style tarball route registered as Volatile fails check 7", testformat.TarballVolatile, formattest.CheckHeaders, "served a stored item",
			[]formattest.Check{formattest.CheckClassify},
		},
		{"R-ARCH-010 a setup template with a credential option fails check 8", testformat.TemplateCredential, formattest.CheckSetup, "spells a credential", nil},
		{"R-ARCH-010 a client without an install template fails check 8", testformat.NoInstallTemplate, formattest.CheckSetup, "no install template", nil},
		{"R-ARCH-009 a tag-changing Admin route that raises no revision fails check 9", testformat.TagWithoutRevision, formattest.CheckRevision, "must raise it", nil},
		{
			"R-ARCH-010 a name rule that keeps the case of a spelling fails check 10", testformat.NamesCaseSensitive, formattest.CheckNames, "normalize to",
			[]formattest.Check{formattest.CheckClassify},
		},
		{"R-ARCH-010 a document path no GET route resolves fails check 10", testformat.WrongDocumentPath, formattest.CheckNames, "does not match a route", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := formattest.Evaluate(testformat.With(c.defect), testformat.Fixture())
			want := append([]formattest.Check{c.check}, c.also...)
			slices.Sort(want)
			if failed := rep.Failed(); !slices.Equal(failed, want) {
				t.Fatalf("failed checks = %v, want %v:\n%v", failed, want, rep)
			}
			if got := strings.Join(rep.Result(c.check).Failures, "\n"); !strings.Contains(got, c.match) {
				t.Fatalf("failures do not say %q:\n%s", c.match, got)
			}
		})
	}
	t.Run("R-ARCH-010 the ambiguous variant is also refused by Register's own check", func(t *testing.T) {
		err := formats.Validate(testformat.With(testformat.AmbiguousRoutes))
		if err == nil || !strings.Contains(err.Error(), "ambiguous routes") {
			t.Fatalf("Validate = %v", err)
		}
	})
}

func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, testformat.New()) }

func FuzzR_ARCH_028_Inspect(f *testing.F) {
	formattest.FuzzInspect(f, testformat.New(), "left-pad/-/left-pad-1.0.0.tgz", testformat.Tarball("left-pad", "1.0.0"))
}
