package repository

import "testing"

func TestPathPatterns(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"releases/**", "releases/1.0/x.bin", true},
		{"releases/**", "releases", true},
		{"releases/**", "tmp/x", false},
		{"**/*.pdb", "a/b/x.pdb", true},
		{"**/*.pdb", "x.pdb", true},
		{"**/*.pdb", "x.pdb.txt", false},
		{"*", "x", true},
		{"*", "a/x", false},
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/b/b/c", false},
		{"a/**/c", "a/c", true},
		{"a/**/c", "a/x/y/c", true},
		{"?.bin", "a.bin", true},
		{"?.bin", "ab.bin", false},
		{`a\*b`, "a*b", true},
		{`a\*b`, "axb", false},
		{"Report.*", "report.pdf", false},
		{"**", "a/b/c", true},
		{"a/b", "a/b/c", false},
	}
	for _, c := range cases {
		t.Run("R-REPO-005 R-GENERIC-007 "+c.pat+" vs "+c.path, func(t *testing.T) {
			p, err := CompilePattern(SubjectPath, c.pat)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Match(c.path); got != c.want {
				t.Fatalf("Match = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNamePatterns(t *testing.T) {
	// The cases of R-AUTHZ-005 for npm names: the matcher a repository evaluates
	// name patterns with.
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"@acme/*", "@acme/ui", true},
		{"@acme/*", "@acme-tools/ui", false},
		{"@acme/*", "left-pad", false},
		{"*", "left-pad", true},
		{"*", "@acme/ui", false},
		{"@*/*", "@acme/ui", true},
		{"**", "@acme/ui", true},
		{"acme-*", "acme-core", true},
		{"@acme/secret-*", "@acme/secret-keys", true},
	}
	for _, c := range cases {
		t.Run("R-REPO-005 name "+c.pat+" vs "+c.name, func(t *testing.T) {
			p, err := CompilePattern(SubjectName, c.pat)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Match(c.name); got != c.want {
				t.Fatalf("Match = %v, want %v", got, c.want)
			}
		})
	}
}

func TestPatternSyntax(t *testing.T) {
	bad := map[string][2]string{ // name -> subject, pattern
		"empty":                    {"path", ""},
		"leading slash":            {"path", "/a"},
		"empty segment":            {"path", "a//b"},
		"double star in a segment": {"path", "a**b"},
		"lone backslash":           {"path", `a\`},
		"control character":        {"path", "a\x01b"},
		"too long":                 {"path", string(make([]byte, MaxPatternLength+1))},
	}
	for name, c := range bad {
		t.Run("R-REPO-005 refuses "+name, func(t *testing.T) {
			if _, err := CompilePattern(PatternSubject(c[0]), c[1]); err == nil {
				t.Fatalf("pattern %q compiled", c[1])
			}
		})
	}
}

func TestAdmission(t *testing.T) {
	adm := func(subject PatternSubject, inc, exc []string) *Admission {
		a, err := NewAdmission(subject, inc, exc)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	t.Run("R-REPO-005 R-GENERIC-007 empty lists admit everything", func(t *testing.T) {
		if !adm(SubjectPath, nil, nil).Admits("any/thing") || !(*Admission)(nil).Admits("x") {
			t.Fatal("not admitted")
		}
	})
	t.Run("R-REPO-005 R-GENERIC-007 include restricts, exclude beats include", func(t *testing.T) {
		a := adm(SubjectPath, []string{"releases/**"}, []string{"**/*.pdb"})
		for path, want := range map[string]bool{
			"releases/1.0/x.bin": true, "releases/1.0/x.pdb": false, "tmp/x.bin": false,
		} {
			if got := a.Admits(path); got != want {
				t.Errorf("Admits(%q) = %v, want %v", path, got, want)
			}
		}
	})
	t.Run("R-GENERIC-007 a folder is admitted when something below it could be", func(t *testing.T) {
		a := adm(SubjectPath, []string{"releases/*/dist/**"}, []string{"releases/old/**"})
		for folder, want := range map[string]bool{
			"": true, "releases": true, "releases/1.0": true, "releases/1.0/dist": true, "releases/1.0/dist/x": true,
			"tmp": false, "releases/1.0/src": false, "releases/old": false,
		} {
			if got := a.AdmitsFolder(folder); got != want {
				t.Errorf("AdmitsFolder(%q) = %v, want %v", folder, got, want)
			}
		}
	})
	t.Run("R-REPO-005 Repository.Admits applies the compiled lists", func(t *testing.T) {
		r := Repository{adm: adm(SubjectPath, []string{"a/**"}, nil)}
		if !r.Admits("a/b", false) || r.Admits("b", false) || !r.Admits("a", true) || r.Admits("b", true) {
			t.Fatal("Repository.Admits does not follow the lists")
		}
		if !(Repository{}).Admits("anything", false) {
			t.Fatal("a repository without lists must admit everything")
		}
	})
}
