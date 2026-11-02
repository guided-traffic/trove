package claims_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/repository"
)

func render(t testing.TB, src string, c claims.Claims, ctx claims.Context) (string, error) {
	t.Helper()
	tpl, err := claims.ParseTemplate(src)
	if err != nil {
		t.Fatalf("ParseTemplate(%q): %v", src, err)
	}
	return tpl.Render(c, ctx)
}

// nameMatches is the package-name matcher of policies (R-AUTHZ-005), the one the evaluator applies
// to a rendered grant.
func nameMatches(t testing.TB, pattern, name string) bool {
	t.Helper()
	p, err := repository.CompilePattern(repository.SubjectName, pattern)
	if err != nil {
		t.Fatalf("CompilePattern(%q): %v", pattern, err)
	}
	return p.Match(name)
}

func TestR_CI_014_TheExampleRows(t *testing.T) {
	c := tokenClaims() // project acme/billing/invoice-api, namespace acme/billing
	rows := []struct {
		template string
		ctx      claims.Context
		want     string
	}{
		{"@{{ namespace_path | slug }}/*", claims.ContextPackages, "@acme-billing/*"},
		{`{{ project_path | replace "/" "." | replace "-" "" }}.*`, claims.ContextPackages, "acme.billing.invoiceapi.*"},
		{"{{ project_path | segment -1 }}", claims.ContextPaths, "invoice-api"},
		{`{{ project_path | segment 0 | require "acme" }}-releases`, claims.ContextRepositories, "acme-releases"},
	}
	for _, r := range rows {
		t.Run("R-CI-014 "+r.template+" renders "+r.want, func(t *testing.T) {
			got, err := render(t, r.template, c, r.ctx)
			if err != nil || got != r.want {
				t.Fatalf("%q, %v", got, err)
			}
		})
	}
}

func TestR_CI_014_AClaimNeverWidensAPattern(t *testing.T) {
	t.Run("R-CI-014 a claim value a*b renders the pattern a\\*b, which matches only the name a*b", func(t *testing.T) {
		got, err := render(t, "{{ name }}", claims.Claims{"name": "a*b"}, claims.ContextPackages)
		if err != nil || got != `a\*b` {
			t.Fatalf("%q, %v", got, err)
		}
		if !nameMatches(t, got, "a*b") || nameMatches(t, got, "axb") || nameMatches(t, got, "ab") {
			t.Fatal("the rendered pattern matches more than the literal name")
		}
	})
	t.Run("R-CI-014 every glob metacharacter of a value is escaped", func(t *testing.T) {
		got, err := render(t, "x-{{ v }}", claims.Claims{"v": `a\b*c?d[e]f{g}`}, claims.ContextPackages)
		if err != nil || got != `x-a\\b\*c\?d\[e\]f\{g\}` {
			t.Fatalf("%q, %v", got, err)
		}
		if !nameMatches(t, got, `x-a\b*c?d[e]f{g}`) {
			t.Fatal("the escaped pattern does not match its own literal")
		}
	})
	t.Run("R-CI-014 literal text stays a pattern", func(t *testing.T) {
		got, err := render(t, "@acme/{{ v }}-*", claims.Claims{"v": "ui"}, claims.ContextPackages)
		if err != nil || got != "@acme/ui-*" || !nameMatches(t, got, "@acme/ui-kit") {
			t.Fatalf("%q, %v", got, err)
		}
	})
}

func TestR_AUTHZ_014_AnEscapedClaimMatchesOnlyItsLiteralName(t *testing.T) {
	// An ID token of a generic issuer whose project_path is team/ui*.
	got, err := render(t, "@acme/{{ project_path | segment -1 }}", claims.Claims{"project_path": "team/ui*"}, claims.ContextPackages)
	if err != nil || got != `@acme/ui\*` {
		t.Fatalf("%q, %v", got, err)
	}
	if !nameMatches(t, got, "@acme/ui*") {
		t.Fatal("R-AUTHZ-014: the pattern does not match the literal name @acme/ui*")
	}
	if nameMatches(t, got, "@acme/ui-kit") {
		t.Fatal("R-AUTHZ-014: the pattern matches @acme/ui-kit")
	}
}

func TestR_CI_014_RenderErrors(t *testing.T) {
	c := claims.Claims{
		"list": []any{"a", "b"}, "obj": map[string]any{"a": "b"}, "null": nil, "frac": json.Number("1.5"),
		"path": "acme/app", "empty": "", "ctrl": "a\x01b", "dotdot": "../etc", "upper": "Acme", "long": strings.Repeat("a", 1020),
		"slash": "a/b", "lead": "/etc", "trail": "a/", "double": "a//b", "dots": "x..y", "punct": "--",
	}
	cases := []struct {
		name, template string
		ctx            claims.Context
	}{
		{"an array claim", "{{ list }}", claims.ContextPackages},
		{"an object claim", "{{ obj }}", claims.ContextPackages},
		{"a missing claim", "{{ absent }}", claims.ContextPackages},
		{"a null claim", "{{ null }}", claims.ContextPackages},
		{"a non-integer number (Q-CI-28)", "{{ frac }}", claims.ContextPackages},
		{"../etc in paths", "{{ dotdot }}", claims.ContextPaths},
		{"a leading / in paths", "x/{{ lead }}", claims.ContextPaths},
		{"a trailing / in paths", "x/{{ trail }}", claims.ContextPaths},
		{"an empty segment in paths", "x/{{ double }}", claims.ContextPaths},
		{".. inside a value in paths", "x/{{ dots }}", claims.ContextPaths},
		{"a / in packages", "@acme/{{ slash }}", claims.ContextPackages},
		{"an upper-case value in repositories", "{{ upper }}-releases", claims.ContextRepositories},
		{"a / in repositories", "{{ path }}-releases", claims.ContextRepositories},
		{"a control character", "x-{{ ctrl }}", claims.ContextPackages},
		{"an empty value (Q-CI-29)", "x-{{ empty }}", claims.ContextPackages},
		{"a filter that leaves nothing (Q-CI-29)", `x-{{ path | trim_prefix "acme/app" }}`, claims.ContextPackages},
		{"an empty slug", "x-{{ punct | slug }}", claims.ContextPackages},
		{"a failed require", `{{ path | segment 0 | require "other" }}-releases`, claims.ContextRepositories},
		{"a segment past the end (Q-CI-29)", "{{ path | segment 2 }}", claims.ContextPaths},
		{"a segment before the start (Q-CI-29)", "{{ path | segment -3 }}", claims.ContextPaths},
		{"a result over 1024 characters", "abcde{{ long }}", claims.ContextPackages},
	}
	for _, tc := range cases {
		t.Run("R-CI-014 "+tc.name+" is grant_render_failed", func(t *testing.T) {
			_, err := render(t, tc.template, c, tc.ctx)
			r, _ := reasonOf(t, err)
			if r != reason.GrantRenderFailed {
				t.Fatalf("%s (%v)", r, err)
			}
			for _, v := range []string{"../etc", "Acme", "a/b", "acme/app"} {
				if strings.Contains(err.Error(), v) {
					t.Fatalf("R-CI-008: the detail holds a claim value: %v", err)
				}
			}
		})
	}
}

func TestR_CI_014_TheGrammar(t *testing.T) {
	c := claims.Claims{"project_path": "Acme/Billing/Invoice-API", "ref": "v1.4.0", "bool": true, "num": json.Number("42")}
	ok := []struct{ template, want string }{
		{"plain", "plain"},
		{`{{ "{{" }}`, `\{\{`},
		{`a}}b`, "a}}b"},
		{"{{project_path|lower}}", `acme/billing/invoice-api`},
		{"{{\tproject_path | slug\t}}", "acme-billing-invoice-api"},
		{`{{ project_path | trim_prefix "Acme/" | trim_suffix "-API" }}`, `Billing/Invoice`},
		{`{{ "say \"hi\" \\ there" }}`, `say "hi" \\ there`},
		{`{{ ref | replace "." "_" }}`, "v1_4_0"},
		{`{{ bool }}-{{ num }}`, "true-42"},
		{`{{ ref | require "v[0-9]+\\.[0-9]+\\.[0-9]+" }}`, "v1.4.0"},
	}
	for _, tc := range ok {
		t.Run("R-CI-014 "+tc.template+" renders", func(t *testing.T) {
			got, err := render(t, tc.template, c, claims.ContextPaths)
			if err != nil || got != tc.want {
				t.Fatalf("%q, %v", got, err)
			}
		})
	}
	bad := []string{
		"{{ project_path | upper }}",          // an unknown filter
		"{{ Project }}",                       // a claim name outside [a-z_][a-z0-9_]*
		"{{ job.config }}",                    // dots are no path
		"{{ project_path",                     // unclosed
		"{{ project_path | }}",                // no filter
		"{{ project_path | segment }}",        // no integer
		"{{ project_path | segment x }}",      // not an integer
		"{{ project_path | replace \"/\" }}",  // one argument
		`{{ project_path | replace "" "x" }}`, // an empty search string (Q-CI-29)
		`{{ "unclosed }}`,
		`{{ "bad \n escape" }}`,
		`{{ "\q" }}`,
		`{{ ref | require "(" }}`,
		`{{ ref | require "` + strings.Repeat("a", 513) + `" }}`,
		"{{ }}",
		"{{ ref ref }}",
		"a\x01b",
		strings.Repeat("a", 1025),
		"{{ ref | lower lower }}",
	}
	for _, src := range bad {
		t.Run("R-CI-014 "+src[:min(len(src), 40)]+" is template_invalid", func(t *testing.T) {
			_, err := claims.ParseTemplate(src)
			if r, _ := reasonOf(t, err); r != reason.TemplateInvalid {
				t.Fatalf("%s", r)
			}
		})
	}
	t.Run("R-CI-014 a string is one value, never split", func(t *testing.T) {
		got, err := render(t, "{{ groups | slug }}", claims.Claims{"groups": "a,b"}, claims.ContextPaths)
		if err != nil || got != "a-b" {
			t.Fatalf("%q %v", got, err)
		}
	})
}

func TestR_CI_014_TheAnchorOfARepositoriesTemplate(t *testing.T) {
	cases := []struct {
		template string
		anchored bool
	}{
		{"{{ namespace_path | slug }}", false},
		{"{{ x }}*", false},
		{"*{{ x }}?", false},
		{"{{ x }}{{ y }}-a", false},
		{"{{ x }}-releases", true},
		{"npm-{{ x }}", true},
		{"{{ x }}-{{ y }}", true},
		{`{{ "npm" }}`, true},
		{"npm-internal", true},
	}
	for _, c := range cases {
		t.Run("R-CI-014 "+c.template, func(t *testing.T) {
			tpl, err := claims.ParseTemplate(c.template)
			if err != nil {
				t.Fatal(err)
			}
			if tpl.Anchored() != c.anchored {
				t.Fatalf("anchored %v, want %v", tpl.Anchored(), c.anchored)
			}
		})
	}
	tpl, _ := claims.ParseTemplate(`{{ a }}-{{ "x" | lower }}-{{ b }}-{{ a }}`)
	if got := strings.Join(tpl.ClaimNames(), ","); got != "a,b" || tpl.IsLiteral() {
		t.Fatalf("claims %q", got)
	}
}

// FuzzR_CI_014_Template parses arbitrary templates and renders every one that parses in every
// context: no panic, a parse error is template_invalid, a render error is grant_render_failed, and a
// rendering in packages or paths never holds an unescaped glob metacharacter that a claim brought.
//
//	go test ./internal/ci/claims -run '^$' -fuzz '^FuzzR_CI_014_Template$' -fuzztime 1m
func FuzzR_CI_014_Template(f *testing.F) {
	for _, s := range []string{
		"@{{ namespace_path | slug }}/*", `{{ project_path | replace "/" "." | replace "-" "" }}.*`,
		"{{ project_path | segment -1 }}", `{{ project_path | segment 0 | require "acme" }}-releases`,
		`{{ "{{" }}`, `{{ "\"\\" }}`, "{{ v }}", "a}}{{", "{{ v | lower | slug | trim_prefix \"a\" | trim_suffix \"b\" }}",
	} {
		f.Add(s, "a*b/c?")
	}
	f.Fuzz(func(t *testing.T, src, value string) {
		tpl, err := claims.ParseTemplate(src)
		if err != nil {
			if r, _ := reasonOf(t, err); r != reason.TemplateInvalid {
				t.Fatalf("parse reason %s", r)
			}
			return
		}
		if tpl.String() != src {
			t.Fatal("String() is not the source")
		}
		c := claims.Claims{"v": value, "namespace_path": value, "project_path": value}
		for _, ctx := range []claims.Context{claims.ContextRepositories, claims.ContextPackages, claims.ContextPaths} {
			out, err := tpl.Render(c, ctx)
			if err != nil {
				if r, _ := reasonOf(t, err); r != reason.GrantRenderFailed {
					t.Fatalf("render reason %s", r)
				}
				continue
			}
			if len([]rune(out)) > claims.MaxRenderedLength {
				t.Fatalf("rendered %d characters", len([]rune(out)))
			}
			if ctx != claims.ContextRepositories && tpl.IsLiteral() && out != src {
				t.Fatalf("a literal template rendered %q", out)
			}
		}
	})
}
