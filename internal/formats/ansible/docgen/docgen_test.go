package docgen_test

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/docgen"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// TestR_ANS_031_TheDefaultLimitsAreTheBoundsOfTheSpec: the generator reads under the bounds of R-ANS-031.
func TestR_ANS_031_TheDefaultLimitsAreTheBoundsOfTheSpec(t *testing.T) {
	want := docgen.Limits{
		Literal: 512 << 10, Source: 2 << 20, SourceBudget: 256 << 20,
		Entry: 1 << 20, EntriesPerVersion: 5000, BytesPerVersion: 16 << 20, File: 512 << 10, Files: 64,
	}
	if got := docgen.DefaultLimits(); got != want {
		t.Errorf("DefaultLimits = %+v, want %+v", got, want)
	}
}

// TestR_ANS_034_AGeneratorBuiltFromTheContractReadsNothing: until the step that reads the members lands, a generator wants no member and
// builds an empty generated set that names the generator version; a member handed to it is not an error.
func TestR_ANS_034_AGeneratorBuiltFromTheContractReadsNothing(t *testing.T) {
	g := docgen.NewGenerator(docgen.DefaultLimits())
	if _, ok := g.Wants("plugins/modules/x.py"); ok {
		t.Error("the contract's generator wants a member")
	}
	g.Add(docgen.Member{Path: "README.md", Body: []byte("# x")})
	set := g.Build("README.md")
	if set.Source != rows.DocSourceGenerated || set.Generator != docgen.Version || len(set.Entries) != 0 || set.Readme != nil {
		t.Errorf("Build = %+v", set)
	}
}

// TestR_ANS_039_MarkupIsEscapedBeforeAnythingElse: the converter never lets a character of the text reach the HTML as markup.
func TestR_ANS_039_MarkupIsEscapedBeforeAnythingElse(t *testing.T) {
	if got := docgen.RenderMarkup(`C(<img src=x onerror=1>) & "q"`); got != `C(&lt;img src=x onerror=1&gt;) &amp; &#34;q&#34;` {
		t.Errorf("RenderMarkup = %q", got)
	}
}
