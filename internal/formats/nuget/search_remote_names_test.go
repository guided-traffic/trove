package nuget

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/nuget/search"
)

// TestR_NUGET_016_TheWithheldRuleAsksAboutEveryNameAQueryCarries: the names the withheld-query
// rule hands to KeepsAway (Q-NUGET-80): a term's value without its field, a phrase word by word,
// and inside a word every run of the characters an ID may hold, with and without separators at
// its ends, so that `acme!`, `acme*`, `"acme,"` and `unknown:acme` ask about `acme`.
func TestR_NUGET_016_TheWithheldRuleAsksAboutEveryNameAQueryCarries(t *testing.T) {
	for q, want := range map[string][]string{
		"acme":                    {"acme"},
		"packageid:Acme.Payments": {"acme.payments"},
		`"Tools for ACME"`:        {"tools", "for", "acme"},
		"acme!":                   {"acme!", "acme"},
		"acme*":                   {"acme*", "acme"},
		`"acme, inc"`:             {"acme,", "acme", "inc"},
		"unknown:acme":            {"unknown:acme", "unknown", "acme"},
		"acme.":                   {"acme.", "acme"},
		"version:1.0.0":           {"1.0.0"},
	} {
		terms, _ := search.Parse(q)
		if got := withheldNames(terms); !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", q, got, want)
		}
	}
}
