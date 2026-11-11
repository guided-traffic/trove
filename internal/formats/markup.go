package formats

// Markup is what a format may use to render and sanitize the text of its documentation (ADR-196, R-ANS-039). A format may import neither
// the README renderer nor the sanitizer behind it (R-ARCH-002), so the Host hands it the one implementation: the policy of R-UI-008, the
// same that renders the README of a package. Host.Markup returns it.
type Markup interface {
	// Markdown renders a Markdown source to sanitized HTML. truncated is true when the source or the result exceeded a limit of the
	// renderer, and the HTML is then what fits.
	Markdown(src string) (html string, truncated bool)
	// SanitizeHTML applies the allowlist of the policy to HTML that came from somebody else, such as an upstream's documentation.
	SanitizeHTML(html string) string
}

// NoMarkup returns the Markup of a host that has not wired the renderer: every call returns the empty string and Markdown reports
// truncated, never the input, so a format that runs on such a host cannot hand unsanitized text to a browser. It is what Host.Markup
// returns before the composition root wires the renderer.
func NoMarkup() Markup { return noMarkup{} }

type noMarkup struct{}

var _ Markup = noMarkup{}

// Markdown implements [Markup]: the result is empty and truncated.
func (noMarkup) Markdown(string) (string, bool) { return "", true }

// SanitizeHTML implements [Markup]: the result is empty.
func (noMarkup) SanitizeHTML(string) string { return "" }
