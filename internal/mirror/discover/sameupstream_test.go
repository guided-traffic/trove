package discover

import (
	"net/url"
	"testing"
)

// R-REPO-052 (Q-REPO-194): the one predicate for "this is the format's well-known upstream".
func TestSameUpstream(t *testing.T) {
	const npm, nuget = "https://registry.npmjs.org/", "https://api.nuget.org/v3/index.json"
	for _, tc := range []struct {
		base, known string
		want        bool
	}{
		{"https://registry.npmjs.org/", npm, true},
		{"https://registry.npmjs.org", npm, true},
		{"HTTPS://Registry.NPMJS.org:443/", npm, true},
		{"https://registry.npmjs.org/?", npm, false},
		{"https://registry.npmjs.org/?x=1", npm, false},
		{"https://registry.npmjs.org/#f", npm, false},
		{"https://u:p@registry.npmjs.org/", npm, false},
		{"http://registry.npmjs.org/", npm, false},
		{"https://registry.npmjs.org:8443/", npm, false},
		{"https://registry.npmjs.org/sub/", npm, false},
		{"https://api.nuget.org/v3/index.json", nuget, true},
		{"https://api.nuget.org/v3/index.json?x=1", nuget, false},
		{"https://api.nuget.org/v3/index.json?", nuget, false},
		{"https://api.nuget.org/v3/other.json", nuget, false},
	} {
		u, err := url.Parse(tc.base)
		if err != nil {
			t.Fatal(err)
		}
		if got := SameUpstream(u, tc.known); got != tc.want {
			t.Errorf("SameUpstream(%q, %q) = %v, want %v", tc.base, tc.known, got, tc.want)
		}
	}
	if SameUpstream(nil, npm) {
		t.Error("a nil URL is no upstream")
	}
}
