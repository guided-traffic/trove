package formats

import "testing"

// TestR_ANS_039_NoMarkupReturnsNothingNeverTheInput: the Markup of a host that has not wired the renderer cannot hand unsanitized text on.
func TestR_ANS_039_NoMarkupReturnsNothingNeverTheInput(t *testing.T) {
	m := NoMarkup()
	if html, truncated := m.Markdown("<script>x</script> # y"); html != "" || !truncated {
		t.Errorf("Markdown = %q %v, want nothing and truncated", html, truncated)
	}
	if got := m.SanitizeHTML("<script>x</script><p>y</p>"); got != "" {
		t.Errorf("SanitizeHTML = %q, want nothing", got)
	}
}
