package claims_test

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/ci/claims"
)

func TestR_CI_010_TheGlobTable(t *testing.T) {
	// The first eight rows are the table of R-CI-010, in its order; the rest pin each token.
	rows := []struct {
		pattern, value string
		want           bool
	}{
		{"acme/*", "acme/app", true},
		{"acme/*", "acme/billing/app", false},
		{"acme/**", "acme/billing/app", true},
		{"acme/**", "acme", false},
		{"acme/**/app", "acme/app", true},
		{"acme/app-*", "acme/app-ui", true},
		{"acme*/**", "acme-evil/app", true},
		{`release/\*`, "release/*", true},

		{`release/\*`, "release/x", false},
		{"acme/*", "acme/", true},
		{"acme/*", "acme", false},
		{"acme/**", "acme/", true},
		{"acme/**", "acme/app", true},
		{"acme/**/app", "acme/billing/app", true},
		{"acme/**/app", "acme/a/b/c/app", true},
		{"acme/**/app", "acme/a/b/c/ap", false},
		{"acme/**/app", "acmeapp", false},
		{"**/app", "app", true},
		{"**/app", "acme/billing/app", true},
		{"**/app", "acme/billing/apps", false},
		{"**", "anything/at/all", true},
		{"**", "", true},
		{"a/**/**/b", "a/b", true},
		{"a/**/b/**", "a/x/b", false},
		{"a/**/b/**", "a/x/b/y", true},
		{"?", "a", true},
		{"?", "", false},
		{"?", "/", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"a?c", "a/c", false},
		{"?", "é", true},
		{"*", "", true},
		{"*", "a/b", false},
		{"*-*-*", "a-b-c", true},
		{"*a*a*b", "aaaaaaaaaaaaaaaaaaaaaaaaab", true},
		{"*a*a*b", "aaaaaaaaaaaaaaaaaaaaaaaaaa", false},
		{`\?`, "?", true},
		{`\?`, "a", false},
		{`\\`, `\`, true},
		{`\[x\]`, "[x]", true},
		{`\{a,b\}`, "{a,b}", true},
		{`a\/b`, "a/b", true},
		{"v1.*", "v1.4.0", true},
		{"v1.*", "v10", false},
		{"", "", true},
		{"", "a", false},
	}
	for _, r := range rows {
		t.Run("R-CI-010 glob "+r.pattern+" against "+r.value, func(t *testing.T) {
			if err := claims.GlobValid(r.pattern); err != nil {
				t.Fatalf("GlobValid: %v", err)
			}
			if got := claims.GlobMatch(r.pattern, r.value, false); got != r.want {
				t.Fatalf("match %v, want %v", got, r.want)
			}
		})
	}
	t.Run("R-CI-010 ignoreCase folds the glob's literal characters", func(t *testing.T) {
		if !claims.GlobMatch("ACME/**", "acme/billing", true) || claims.GlobMatch("ACME/**", "acme/billing", false) {
			t.Fatal("ignoreCase")
		}
		if !claims.GlobMatch("Σ*", "ς-x", true) {
			t.Fatal("the simple folding orbit of Σ holds σ and ς")
		}
	})
}

func TestR_CI_010_InvalidGlobsAreRefusedOnSave(t *testing.T) {
	for _, p := range []string{"a**", "**b", "a/**b/c", "a/b**", "***", "a[bc]", "a]b", "{a,b}", "a}", `trailing\`, strings.Repeat("a", 1025)} {
		t.Run("R-CI-010 "+p[:min(len(p), 20)]+" is refused", func(t *testing.T) {
			if claims.GlobValid(p) == nil {
				t.Fatal("valid")
			}
		})
	}
	for _, p := range []string{"**", "a/**", "**/b", "a/**/b", `a\**`, `\*\*`, `\[\]\{\}`, "a/*/b?"} {
		t.Run("R-CI-010 "+p+" is valid", func(t *testing.T) {
			if err := claims.GlobValid(p); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// globReference translates a valid glob into an RE2 expression with the semantics of R-CI-010: the
// independent second implementation the fuzz target compares GlobMatch with.
func globReference(pattern string, fold bool) *regexp.Regexp {
	var segs []string
	var cur strings.Builder
	escaped := false
	for _, r := range pattern {
		switch {
		case escaped && r == '/':
			segs = append(segs, cur.String())
			cur.Reset()
			escaped = false
		case escaped:
			cur.WriteString(`\` + string(r))
			escaped = false
		case r == '\\':
			escaped = true
		case r == '/':
			segs = append(segs, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	segs = append(segs, cur.String())
	return regexp.MustCompile(buildReference(segs, fold))
}

// buildReference writes the expression: a segment as its tokens, a globstar in the middle or at the
// start as zero or more "segment/" groups, a trailing globstar as "/" and anything (one or more
// segments), alone as anything.
func buildReference(segs []string, fold bool) string {
	var re strings.Builder
	if fold {
		re.WriteString("(?i)")
	}
	re.WriteString("^")
	needSep := false
	for i, s := range segs {
		sep := ""
		if needSep {
			sep = "/"
		}
		switch {
		case s == "**" && i == len(segs)-1:
			re.WriteString(sep + "(?s:.*)")
		case s == "**":
			re.WriteString(sep + "(?:[^/]*/)*")
			needSep = false
			continue
		default:
			re.WriteString(sep + segmentRegex(s))
		}
		needSep = true
	}
	re.WriteString("$")
	return re.String()
}

func segmentRegex(s string) string {
	var b strings.Builder
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			b.WriteString(regexp.QuoteMeta(string(r)))
			escaped = false
		case r == '\\':
			escaped = true
		case r == '*':
			b.WriteString("[^/]*")
		case r == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

// FuzzR_CI_010_Glob compares GlobMatch with an RE2 translation of the glob semantics of R-CI-010
// on every valid pattern and value, with and without ignoreCase.
//
//	go test ./internal/ci/claims -run '^$' -fuzz '^FuzzR_CI_010_Glob$' -fuzztime 1m
func FuzzR_CI_010_Glob(f *testing.F) {
	for _, s := range [][2]string{
		{"acme/*", "acme/app"},
		{"acme/**", "acme"},
		{"acme/**/app", "acme/x/app"},
		{"**/b", "a/b"},
		{`release/\*`, "release/*"},
		{"a?c", "abc"},
		{"*a*a*b", "aaab"},
		{"**", ""},
		{"a/**/**/b", "a/b"},
		{"Σ*", "ς"},
		{`a\/b`, "a/b"},
	} {
		f.Add(s[0], s[1], false)
		f.Add(s[0], s[1], true)
	}
	f.Fuzz(func(t *testing.T, pattern, value string, fold bool) {
		if !utf8.ValidString(pattern) || !utf8.ValidString(value) || len(pattern) > 64 || len(value) > 256 {
			return
		}
		if claims.GlobValid(pattern) != nil {
			return
		}
		want := globReference(pattern, fold).MatchString(value)
		if got := claims.GlobMatch(pattern, value, fold); got != want {
			t.Fatalf("GlobMatch(%q, %q, %v) = %v, the reference says %v", pattern, value, fold, got, want)
		}
	})
}
