package view

import "testing"

// TestR_API_062_APackageSuggestionRanksByItsName: 0 for a name equal to the text, 1 for a name that starts with it, 2 for a match
// by words, whichever statement found the name: `@acme/ui` suggests `@acme/ui-kit` before `@acme/kit-ui`.
func TestR_API_062_APackageSuggestionRanksByItsName(t *testing.T) {
	for _, c := range []struct {
		text, name string
		want       int
	}{
		{"@acme/ui", "@acme/ui", 0},
		{"@acme/ui", "@acme/ui-kit", 1},
		{"@acme/ui", "@acme/kit-ui", 2},
		{"kit", "@acme/ui-kit", 2},
		{"microsoft.ext", "Microsoft.Extensions.Logging", 1},
	} {
		if got := suggestionRank(c.text, c.name); got != c.want {
			t.Errorf("R-API-062 %s for %s: rank %d, want %d", c.name, c.text, got, c.want)
		}
	}
}
