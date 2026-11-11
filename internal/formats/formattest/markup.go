package formattest

import (
	"html"

	"github.com/guided-traffic/trove/internal/formats"
)

// StandInMarkup is the kit's deterministic stand-in for the Host's renderer (ADR-196): it escapes the text and wraps it, so a format's
// unit test sees what went through the capability without the policy of the README renderer, which the format may not import. Markdown
// wraps the escaped source in a paragraph and SanitizeHTML escapes the HTML, so no markup survives either call.
type StandInMarkup struct{}

var _ formats.Markup = StandInMarkup{}

// Markdown implements [formats.Markup].
func (StandInMarkup) Markdown(src string) (string, bool) {
	return "<p>" + html.EscapeString(src) + "</p>", false
}

// SanitizeHTML implements [formats.Markup].
func (StandInMarkup) SanitizeHTML(h string) string { return html.EscapeString(h) }
