package docgen_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/docgen"
)

// TestR_ANS_039_TheMarkupTableHasACasePerMacro: each macro of the Ansible markup becomes its tag, the text is escaped, and what is not a closed
// macro stays text.
func TestR_ANS_039_TheMarkupTableHasACasePerMacro(t *testing.T) {
	const rel = ` rel="nofollow noopener noreferrer"`
	for in, want := range map[string]string{
		"plain":                               "plain",
		"I(x) and E(y)":                       "<em>x</em> and <em>y</em>",
		"B(bold)":                             "<strong>bold</strong>",
		"C(a), O(b), V(c), RV(d), M(e), P(f)": "<code>a</code>, <code>b</code>, <code>c</code>, <code>d</code>, <code>e</code>, <code>f</code>",
		"R(the page,ref)":                     "<code>the page</code>",
		"U(https://example.com/x)":            `<a href="https://example.com/x"` + rel + `>https://example.com/x</a>`,
		"L(docs,http://example.com)":          `<a href="http://example.com"` + rel + `>docs</a>`,
		"L(x,javascript:alert(1))":            "x",
		"U(javascript:alert(1))":              "javascript:alert(1)",
		"L(x,ftp://example.com)":              "x",
		"HORIZONTALLINE":                      "<hr>",
		"a HORIZONTALLINE b":                  "a <hr> b",
		"C(a\\)b)":                            "<code>a)b</code>",
		"C(a\\\\b)":                           `<code>a\b</code>`,
		"L(a\\, b,https://e.com)":             `<a href="https://e.com"` + rel + `>a, b</a>`,
		"I(unclosed":                          "I(unclosed",
		"XC(x)":                               "XC(x)",
		"B(I(C(x)))":                          "<strong><em><code>x</code></em></strong>",
		"B(I(B(I(B(x)))))":                    "<strong><em><strong><em>B(x)</em></strong></em></strong>",
		"C(<img src=x onerror=1>)":            "<code>&lt;img src=x onerror=1&gt;</code>",
		"<script>alert(1)</script>":           "&lt;script&gt;alert(1)&lt;/script&gt;",
		"a & b":                               "a &amp; b",
	} {
		if got := docgen.RenderMarkup(in); got != want {
			t.Errorf("RenderMarkup(%q) = %q, want %q", in, got, want)
		}
	}
	if got := docgen.PlainText("Connects to C(host) and I(waits). L(x,https://e.com) HORIZONTALLINE"); got != "Connects to host and waits. x  " {
		t.Errorf("PlainText = %q", got)
	}
	if got := docgen.RenderMarkup(strings.Repeat("a", docgen.MaxMarkupInput+100)); len(got) != docgen.MaxMarkupInput {
		t.Errorf("the input is cut at the bound, got %d bytes", len(got))
	}
}

var (
	tagRE   = regexp.MustCompile(`<(/?)([a-zA-Z][a-zA-Z0-9]*)`)
	allowed = map[string]bool{"em": true, "strong": true, "code": true, "a": true, "hr": true}
)

func FuzzR_ANS_039_RenderMarkup(f *testing.F) {
	for _, s := range []string{"C(<script>)", "L(a,javascript:x)", "I(B(I(B(I(x)))))", `L(a\,b,https://e.com)`, "HORIZONTALLINE", "U(", `\`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := docgen.RenderMarkup(in)
		for _, m := range tagRE.FindAllStringSubmatch(out, -1) {
			if !allowed[m[2]] {
				t.Fatalf("RenderMarkup(%q) = %q holds the tag %s", in, out, m[2])
			}
		}
		if strings.Contains(out, "<script") || regexp.MustCompile(`(?i)href="(?:[^h]|h[^t])`).MatchString(out) {
			t.Fatalf("RenderMarkup(%q) = %q", in, out)
		}
	})
}
