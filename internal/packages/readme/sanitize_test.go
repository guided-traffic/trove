package readme

import (
	"regexp"
	"strings"
	"testing"
)

// TestR_ANS_039_SanitizeHTMLAppliesTheREADMEPolicyToForeignHTML: the HTML of an upstream's documentation runs through the allowlist and
// the link and image rules of R-UI-008, so none of the executable forms of the corpus survives and the harmless markup does.
func TestR_ANS_039_SanitizeHTMLAppliesTheREADMEPolicyToForeignHTML(t *testing.T) {
	r := renderer(t)
	forbidden := regexp.MustCompile(`(?i)<script|<style|<iframe|<object|<embed|<form|<svg|<math|\bon[a-z]+\s*=|javascript:|data:|\bstyle\s*=`)
	for name, src := range map[string]string{
		"script":          `<h1>x</h1><script>alert(1)</script>`,
		"event handler":   `<img src="https://img.example/x.png" onerror="alert(1)">`,
		"javascript link": `<a href="javascript:alert(1)">x</a>`,
		"iframe":          `<iframe src="https://evil.example"></iframe>`,
		"style attribute": `<p style="background:url(https://evil.example)">x</p>`,
		"svg":             `<svg onload="alert(1)"></svg>`,
		"mixed case":      `<ScRiPt>alert(1)</sCrIpT>`,
	} {
		if got := r.SanitizeHTML(src); forbidden.MatchString(got) {
			t.Errorf("%s: %q survived in %q", name, forbidden.FindString(got), got)
		}
	}
	got := r.SanitizeHTML(`<h1>Title</h1><p>See <a href="https://example.com/x">the page</a> and <code>x</code>.</p>`)
	if !strings.Contains(got, ">Title</h1>") || !strings.Contains(got, `href="https://example.com/x"`) || !strings.Contains(got, `rel="nofollow noopener noreferrer"`) {
		t.Errorf("harmless HTML = %q", got)
	}
	if got := r.SanitizeHTML(`<img src="https://img.example/x.png" alt="a">`); strings.Contains(got, "<img") {
		t.Errorf("an external image stays under an empty origin list: %q", got)
	}
	big := r.SanitizeHTML(strings.Repeat("<p>x</p>", MaxHTMLBytes/8+10))
	if !strings.HasPrefix(big, "<pre>") || len(big) > MaxHTMLBytes {
		t.Errorf("an input above the limit is not parsed: %d bytes, starts %q", len(big), big[:min(len(big), 12)])
	}
}
