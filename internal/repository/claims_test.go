package repository

import (
	"log/slog"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// npmNames is the name rule of npm-like formats: names compare as written, so a
// pattern and a name are folded by doing nothing (R-AUTHZ-005).
type npmNames struct{}

func (npmNames) Normalize(name string) (string, bool) { return name, name != "" }

// npmCatalog holds a format with npm's name semantics, scoped names and priority
// members, registered the way a real format registers itself. R-NPM-033 is tested
// against it because the repository model cannot import the npm format.
func npmCatalog() *catalog.Registry {
	r := catalog.NewRegistry()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "npmlike", Classes: []Class{Local, Remote, Virtual}, Layout: "npmlike", ProtocolBasePath: "/api/npmlike/{key}/",
		PatternSubject: SubjectName, PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
	}, Names: npmNames{}})
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "pathfmt", Classes: []Class{Local, Remote, Virtual}, Layout: "pathfmt", ProtocolBasePath: "/files/{key}/",
		PatternSubject: SubjectPath,
	}})
	return r
}

func mustPattern(t *testing.T, p string) ClaimPattern {
	t.Helper()
	c, err := ParseClaimPattern(p)
	if err != nil {
		t.Fatalf("ParseClaimPattern(%q): %v", p, err)
	}
	return c
}

func TestClaimPatternForms(t *testing.T) {
	valid := map[string]string{ // pattern -> the folded form that is stored
		"left-pad":        "left-pad",
		"@acme/util":      "@acme/util",
		"@acme/*":         "@acme/*",
		"@acme/**":        "@acme/**",
		"acme-*":          "acme-*",
		"*":               "*",
		"**":              "**",
		"***":             "**",
		`a\*b`:            `a\*b`,
		`a\?b*`:           `a\?b*`,
		`a\\b`:            `a\\b`,
		"Acme.Internal.*": "Acme.Internal.*",
	}
	for in, want := range valid {
		t.Run("R-REPO-023 accepts "+in, func(t *testing.T) {
			if got := mustPattern(t, in).String(); got != want {
				t.Fatalf("String() = %q, want %q", got, want)
			}
		})
	}
	for _, in := range []string{
		"", "@*/*", "a*b", "*a", "a?b", "?", "a/*/b", "*/x", "a**b", `a\`, "@acme/*x", "a\x01*",
	} {
		t.Run("R-REPO-023 R-NPM-033 refuses "+in, func(t *testing.T) {
			if _, err := ParseClaimPattern(in); err == nil {
				t.Fatalf("%q was accepted; claim and pin-rule patterns are an exact name or a literal and one trailing * or **", in)
			}
		})
	}
	t.Run("R-REPO-023 a pattern of 257 characters is refused", func(t *testing.T) {
		long := make([]byte, 257)
		for i := range long {
			long[i] = 'a'
		}
		if _, err := ParseClaimPattern(string(long)); err == nil {
			t.Fatal("257 characters accepted")
		}
		if _, err := ParseClaimPattern(string(long[:256])); err != nil {
			t.Fatalf("256 characters refused: %v", err)
		}
	})
}

// TestMostSpecificIsTotal checks, over every pattern of the two forms and every
// name of a small alphabet, that two different patterns that cover one name
// always differ in rank: "most specific wins" is then a total order and a name
// has one deciding pattern (R-REPO-023, Terms: Most specific wins).
func TestMostSpecificIsTotal(t *testing.T) {
	alphabet := []string{"a", "b", "/"}
	var names []string
	var gen func(prefix string, depth int)
	gen = func(prefix string, depth int) {
		if prefix != "" {
			names = append(names, prefix)
		}
		if depth == 0 {
			return
		}
		for _, c := range alphabet {
			gen(prefix+c, depth-1)
		}
	}
	gen("", 5)
	literals := append([]string{""}, names...)
	var patterns []ClaimPattern
	for _, l := range literals {
		for _, suffix := range []string{"*", "**"} {
			if c, err := ParseClaimPattern(l + suffix); err == nil {
				patterns = append(patterns, c)
			}
		}
	}
	for _, n := range names {
		if c, err := ParseClaimPattern(n); err == nil {
			patterns = append(patterns, c)
		}
	}
	checked := 0
	for _, name := range names {
		var hits []ClaimPattern
		for _, p := range patterns {
			if p.Match(name) {
				hits = append(hits, p)
			}
		}
		for i := range hits {
			for j := i + 1; j < len(hits); j++ {
				checked++
				if hits[i].sameRank(hits[j]) && hits[i].String() != hits[j].String() {
					t.Fatalf("name %q: %q and %q match with the same rank", name, hits[i], hits[j])
				}
				// Overlaps must agree with the matcher: both match this name.
				if !hits[i].Overlaps(hits[j]) || !hits[j].Overlaps(hits[i]) {
					t.Fatalf("name %q: %q and %q both match but Overlaps says no", name, hits[i], hits[j])
				}
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d pairs checked", checked)
	}
	t.Run("R-REPO-023 the documented order", func(t *testing.T) {
		order := []string{"@acme/billing-core", "@acme/billing-*", "@acme/*", "@acme/**", "*", "**"}
		// "*" does not cover a scoped name; the order is for the patterns that do,
		// and "*" and "**" compare by the same rule for an unscoped one.
		for i := 0; i < 4-1; i++ {
			a, b := mustPattern(t, order[i]), mustPattern(t, order[i+1])
			if !a.moreSpecific(b) {
				t.Errorf("%q is not more specific than %q", a, b)
			}
		}
		if !mustPattern(t, "*").moreSpecific(mustPattern(t, "**")) {
			t.Error(`"*" is not more specific than "**"`)
		}
		if !mustPattern(t, "left-pad").moreSpecific(mustPattern(t, "left-*")) {
			t.Error("an exact name is not more specific than a prefix")
		}
	})
	t.Run("R-REPO-023 patterns that cannot cover one name do not overlap", func(t *testing.T) {
		cases := []struct {
			a, b string
			want bool
		}{
			{"@acme/*", "@acme/billing-*", true},
			{"@acme/*", "@acme/a/b", false}, // * stops at /
			{"@acme/**", "@acme/a/b", true},
			{"@acme/*", "@other/*", false},
			{"@acme/*", "left-pad", false},
			{"*", "@acme/*", false},
			{"**", "@acme/*", true},
			{"left-pad", "left-pad", true},
			{"left-*", "left-pad", true},
			{"left-*", "right-*", false},
		}
		for _, c := range cases {
			if got := mustPattern(t, c.a).Overlaps(mustPattern(t, c.b)); got != c.want {
				t.Errorf("Overlaps(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
			}
		}
	})
}

func claimRows(t *testing.T, rows ...q.ListNamespaceClaimsRow) *claimIndex {
	t.Helper()
	return buildClaimIndex(rows, slog.New(slog.DiscardHandler))
}

func TestClaimIndexLookup(t *testing.T) {
	x := claimRows(t,
		q.ListNamespaceClaimsRow{RepoID: 1, Format: "npmlike", Pattern: "@acme/*", Source: SourceManual, Scope: ScopeGlobal},
		q.ListNamespaceClaimsRow{RepoID: 2, Format: "npmlike", Pattern: "@acme/billing-*", Source: SourceManual, Scope: ScopeMembers},
		q.ListNamespaceClaimsRow{RepoID: 3, Format: "npmlike", Pattern: "@acme/billing-core", Source: SourceAuto, Scope: ScopeMembers},
		q.ListNamespaceClaimsRow{RepoID: 4, Format: "npmlike", Pattern: "@acme/**", Source: SourceManual, Scope: ScopeMembers},
		q.ListNamespaceClaimsRow{RepoID: 1, Format: "npmlike", Pattern: "*", Source: SourceManual, Scope: ScopeMembers},
		q.ListNamespaceClaimsRow{RepoID: 9, Format: "otherfmt", Pattern: "@acme/*", Source: SourceManual, Scope: ScopeGlobal},
	)
	type hit struct {
		repo    int64
		pattern string
	}
	got := func(format, name string) []hit {
		var out []hit
		for _, h := range x.lookup(format, name) {
			out = append(out, hit{h.repoID, h.pattern.String()})
		}
		return out
	}
	cases := []struct {
		name string
		want []hit
	}{
		{"@acme/billing-core", []hit{{3, "@acme/billing-core"}, {2, "@acme/billing-*"}, {1, "@acme/*"}, {4, "@acme/**"}}},
		{"@acme/billing-ui", []hit{{2, "@acme/billing-*"}, {1, "@acme/*"}, {4, "@acme/**"}}},
		{"@acme/a/b", []hit{{4, "@acme/**"}}},
		{"left-pad", []hit{{1, "*"}}},
		{"@other/x", nil},
		{"", nil},
	}
	for _, c := range cases {
		t.Run("R-REPO-023 R-NPM-033 lookup "+c.name, func(t *testing.T) {
			if g := got("npmlike", c.name); !slices.Equal(g, c.want) {
				t.Fatalf("lookup = %v, want %v", g, c.want)
			}
		})
	}
	t.Run("R-REPO-023 a claim of another format is not seen", func(t *testing.T) {
		if g := got("otherfmt", "@acme/x"); len(g) != 1 || g[0].repo != 9 {
			t.Fatalf("lookup = %v", g)
		}
	})
	t.Run("R-REPO-023 equal patterns of two repositories decide together", func(t *testing.T) {
		x := claimRows(t,
			q.ListNamespaceClaimsRow{RepoID: 5, Format: "npmlike", Pattern: "@acme/*", Scope: ScopeMembers, Source: SourceManual},
			q.ListNamespaceClaimsRow{RepoID: 2, Format: "npmlike", Pattern: "@acme/*", Scope: ScopeMembers, Source: SourceManual},
			q.ListNamespaceClaimsRow{RepoID: 7, Format: "npmlike", Pattern: "@acme/**", Scope: ScopeMembers, Source: SourceManual},
		)
		top := mostSpecific(x.lookup("npmlike", "@acme/x"))
		if len(top) != 2 || top[0].repoID != 2 || top[1].repoID != 5 {
			t.Fatalf("most specific = %+v", top)
		}
	})
	t.Run("R-REPO-023 a stored claim that does not parse protects nothing and does not stop the others", func(t *testing.T) {
		x := claimRows(t,
			q.ListNamespaceClaimsRow{RepoID: 1, Format: "npmlike", Pattern: "a*b", Scope: ScopeGlobal, Source: SourceManual},
			q.ListNamespaceClaimsRow{RepoID: 2, Format: "npmlike", Pattern: "ab", Scope: ScopeGlobal, Source: SourceManual},
		)
		if h := x.lookup("npmlike", "ab"); len(h) != 1 || h[0].repoID != 2 {
			t.Fatalf("hits = %+v", h)
		}
	})
}

func TestPinIndexBest(t *testing.T) {
	rules := []PinRule{
		{Packages: []string{"@partner/*"}, Members: []string{"partner-remote"}},
		{Packages: []string{"@partner/sdk", "@vendor/**"}, Members: []string{"sdk-local"}},
		{Packages: []string{"**"}, Members: []string{"everything"}},
	}
	p := newPinIndex(npmCatalog(), "npmlike", rules, slog.New(slog.DiscardHandler), "v")
	cases := []struct {
		name, member, pattern string
		ok                    bool
	}{
		{"@partner/sdk", "sdk-local", "@partner/sdk", true},
		{"@partner/other", "partner-remote", "@partner/*", true},
		{"@vendor/a/b", "sdk-local", "@vendor/**", true},
		{"left-pad", "everything", "**", true},
	}
	for _, c := range cases {
		t.Run("R-REPO-023 the most specific pattern selects the rule: "+c.name, func(t *testing.T) {
			m, ok := p.best(c.name)
			if ok != c.ok || !m.names(c.member) || m.pattern.String() != c.pattern {
				t.Fatalf("best = %+v %v, want %s via %s", m, ok, c.member, c.pattern)
			}
		})
	}
	if _, ok := (*pinIndex)(nil).best("x"); ok {
		t.Fatal("a repository without pin rules selected a rule")
	}
}
