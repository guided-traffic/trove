package repository

import "testing"

// TestR_REPO_012_TheQueryMatchesTheQualifiedNameAndTheDescription: q of the repository list is a case-insensitive substring of
// <project>/<key>, which holds the key, or of the description (Q-API-279), so the results page of the search holds every
// repository the suggestions name (R-API-062).
func TestR_REPO_012_TheQueryMatchesTheQualifiedNameAndTheDescription(t *testing.T) {
	for _, c := range []struct {
		needle string
		want   bool
	}{
		{"", true},
		{"npm", true},
		{"web/npm", true},
		{"web/npm-local", true},
		{"b/np", true},
		{"build outputs", true},
		{"mobile/npm", false},
		{"web/docs", false},
	} {
		if got := queryMatches(c.needle, "web", "npm-local", "Build Outputs of the web team"); got != c.want {
			t.Errorf("R-REPO-012 q=%q: %v, want %v", c.needle, got, c.want)
		}
	}
}
