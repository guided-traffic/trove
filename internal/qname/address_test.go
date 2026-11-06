package qname

import "testing"

func TestR_REPO_001_ParseAddress(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want Address
		ok   bool
	}{
		{"protocol path", "/api/npm/web/npm-remote/left-pad", Address{FamilyAPI, "npm", Repo{"web", "npm-remote"}, false, "left-pad"}, true},
		{"protocol repository root", "/api/npm/web/npm-remote", Address{FamilyAPI, "npm", Repo{"web", "npm-remote"}, false, ""}, true},
		{"trailing slash", "/api/npm/web/npm-remote/", Address{FamilyAPI, "npm", Repo{"web", "npm-remote"}, false, ""}, true},
		{"rest stays escaped", "/api/npm/web/x1/%40scope%2Fpkg/-/pkg-1.0.0.tgz", Address{FamilyAPI, "npm", Repo{"web", "x1"}, false, "%40scope%2Fpkg/-/pkg-1.0.0.tgz"}, true},
		{"cache view", "/api/npm/web/npm-remote~cache/pkg", Address{FamilyAPI, "npm", Repo{"web", "npm-remote"}, true, "pkg"}, true},
		{"nuget", "/api/nuget/ops/feed/v3/index.json", Address{FamilyAPI, "nuget", Repo{"ops", "feed"}, false, "v3/index.json"}, true},
		{"raw files", "/files/web/generic-local/a/b.txt", Address{FamilyFiles, "", Repo{"web", "generic-local"}, false, "a/b.txt"}, true},
		{"raw files cache view", "/files/web/generic-remote~cache/a", Address{FamilyFiles, "", Repo{"web", "generic-remote"}, true, "a"}, true},
		{"raw files root", "/files/web/generic-local", Address{FamilyFiles, "", Repo{"web", "generic-local"}, false, ""}, true},

		{"one segment is no address", "/api/npm/web", Address{}, false},
		{"one segment with slash is no address", "/api/npm/web/", Address{}, false},
		{"files one segment", "/files/web", Address{}, false},
		{"files one segment with slash", "/files/web/", Address{}, false},
		{"format only", "/api/npm", Address{}, false},
		{"format only with slash", "/api/npm/", Address{}, false},
		{"api root", "/api", Address{}, false},
		{"management API is no format", "/api/v1/projects/web/repositories", Address{}, false},
		{"a later API version is no format", "/api/v2/web/npm", Address{}, false},
		{"relative", "api/npm/web/x1", Address{}, false},
		{"other family", "/other/npm/web/x1", Address{}, false},
		{"empty", "", Address{}, false},
		{"root", "/", Address{}, false},
		{"empty project", "/api/npm//npm-remote", Address{}, false},
		{"empty key", "/api/npm/web//x", Address{}, false},
		{"upper-case project", "/api/npm/Web/npm-remote", Address{}, false},
		{"project starting with a digit", "/api/npm/1web/npm-remote", Address{}, false},
		{"short key", "/api/npm/web/n", Address{}, false},
		{"key with dots", "/api/npm/web/a..b", Address{}, false},
		{"key ending in a dot", "/api/npm/web/ab./x", Address{}, false},
		{"cache suffix on the project", "/api/npm/web~cache/npm-remote", Address{}, false},
		{"double cache suffix", "/api/npm/web/npm-remote~cache~cache", Address{}, false},
		{"only the suffix", "/api/npm/web/~cache", Address{}, false},

		{"percent in the family", "/%61pi/npm/web/x1", Address{}, false},
		{"percent in the format", "/api/%6Epm/web/x1", Address{}, false},
		{"percent in the project", "/api/npm/w%65b/x1", Address{}, false},
		{"percent slash in the project", "/api/npm/web%2Fx1", Address{}, false},
		{"percent in the key", "/api/npm/web/x%31", Address{}, false},
		{"percent slash joins project and key", "/api/npm/web%2Fnpm-remote/x", Address{}, false},
		{"percent in the files project", "/files/w%65b/x1", Address{}, false},
		{"percent in the files key", "/files/web/x%31/a", Address{}, false},
		{"percent in the families name", "/fil%65s/web/x1", Address{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseAddress(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Errorf("ParseAddress(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestR_REPO_001_DotSegments(t *testing.T) {
	for in, want := range map[string]bool{
		"/api/npm/web/x1/a/../b":  true,
		"/api/npm/web/x1/./b":     true,
		"/api/npm/web/x1/a/..":    true,
		"/api/npm/web/x1/a/b":     false,
		"/api/npm/web/x1":         false,
		"/api/npm/web/x1/.hidden": false,
		"/files/web/x1/a/b..c":    false,
	} {
		a, ok := ParseAddress(in)
		if !ok {
			t.Fatalf("%q is not an address", in)
		}
		if got := a.DotSegments(); got != want {
			t.Errorf("DotSegments(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestR_REPO_001_ProtocolBase(t *testing.T) {
	r := Repo{"web", "npm-remote"}
	for _, tc := range []struct{ tmpl, want string }{
		{"/api/npm/{project}/{key}/", "/api/npm/web/npm-remote/"},
		{"/api/nuget/{project}/{key}/v3/index.json", "/api/nuget/web/npm-remote/v3/index.json"},
		{"/files/{project}/{key}/", "/files/web/npm-remote/"},
		{"/static/", "/static/"},
	} {
		if got := ProtocolBase(tc.tmpl, r); got != tc.want {
			t.Errorf("ProtocolBase(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
	if got := FilesBase(r); got != "/files/web/npm-remote/" {
		t.Errorf("FilesBase = %q", got)
	}
	// A slash in a value is escaped, never a second segment.
	if got := ProtocolBase("/api/npm/{project}/{key}/", Repo{"a/b", "c"}); got != "/api/npm/a%2Fb/c/" {
		t.Errorf("ProtocolBase with a slash = %q", got)
	}
}

func TestR_REPO_001_BaseParsesBack(t *testing.T) {
	r := Repo{"web", "a.b_c-d"}
	for _, base := range []string{ProtocolBase("/api/npm/{project}/{key}/", r), FilesBase(r)} {
		a, ok := ParseAddress(base + "x/y")
		if !ok || a.Repo != r || a.Rest != "x/y" {
			t.Errorf("ParseAddress(%q) = %+v, %v", base+"x/y", a, ok)
		}
	}
}

func FuzzParseAddress(f *testing.F) {
	for _, s := range []string{"/api/npm/web/x1/a", "/files/web/x1", "/api/npm/web", "/api/%6Epm/web/x1", "//", "/api/npm/web/x1~cache/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, ok := ParseAddress(s)
		if !ok {
			return
		}
		if !a.Repo.Valid() {
			t.Fatalf("%q parsed to the invalid repository %+v", s, a.Repo)
		}
		for _, part := range []string{a.Format, a.Repo.Project, a.Repo.Key} {
			if part == "%" || len(part) > 0 && containsPercent(part) {
				t.Fatalf("%q parsed with a percent in %q", s, part)
			}
		}
	})
}

func containsPercent(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			return true
		}
	}
	return false
}
