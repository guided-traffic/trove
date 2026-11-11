package app

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/packages/readme"
)

// TestR_ANS_039_TheHostMarkupRunsThePolicyOfTheREADMERenderer: the Markup the composition root gives the format host renders Markdown and
// sanitizes HTML under the README policy of R-UI-008.
func TestR_ANS_039_TheHostMarkupRunsThePolicyOfTheREADMERenderer(t *testing.T) {
	rd, err := readme.New(readme.Options{MaxSourceBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	m := readmeMarkup{r: rd}
	html, truncated := m.Markdown("# Title\n\n<script>alert(1)</script>")
	if truncated || !strings.Contains(html, "Title") || strings.Contains(html, "<script") {
		t.Errorf("Markdown = %q (truncated %v)", html, truncated)
	}
	if got := m.SanitizeHTML(`<p onclick="x()">y</p>`); got != "<p>y</p>" {
		t.Errorf("SanitizeHTML = %q", got)
	}
}
