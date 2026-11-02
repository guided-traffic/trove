package claims

// Context is where a template is rendered (R-CI-014): the context decides the escaping and the
// validation of each substituted value.
type Context string

// The rendering contexts.
const (
	// ContextRepositories: the value must match ^[a-z0-9._-]+$, nothing is escaped, and the template
	// must contain a literal character next to every substitution (422 template_unanchored).
	ContextRepositories Context = "repositories"
	// ContextPackages: glob metacharacters (\ * ? [ ] { }) are backslash-escaped; the value must not
	// contain "/".
	ContextPackages Context = "packages"
	// ContextPaths: glob metacharacters are escaped; the value must not be empty, contain "..", an
	// empty segment, or start with "/".
	ContextPaths Context = "paths"
)

// MaxRenderedLength is the longest rendered pattern (R-CI-014, IAM-021).
const MaxRenderedLength = 1024

// Template is a parsed template of the language of R-CI-014 (a literal text with {{ source | filter … }}
// substitutions; the filters lower, slug, replace, trim_prefix, trim_suffix, segment and require).
// The zero value is the empty literal. A Template is immutable and safe for concurrent use.
type Template struct {
	src string
}

// ParseTemplate parses a template (422 template_invalid with the position on a syntax error or an
// unknown filter, source or argument). A string without "{{" is a literal template.
func ParseTemplate(_ string) (*Template, error) {
	panic("not implemented: W2")
}

// String returns the source text.
func (t *Template) String() string { return t.src }

// IsLiteral reports whether the template has no substitution.
func (t *Template) IsLiteral() bool {
	panic("not implemented: W2")
}

// ClaimNames returns the claims the template substitutes, in order of first use. The save-time
// warning for a claim that is neither required by the issuer nor guarded uses it.
func (t *Template) ClaimNames() []string {
	panic("not implemented: W2")
}

// Anchored reports whether every substitution has literal text next to it, the rule of the
// `repositories` context (R-CI-014).
func (t *Template) Anchored() bool {
	panic("not implemented: W2")
}

// Render renders the template for the claims in a context. An array, object, missing or null claim,
// a failed `require`, an empty `slug`, a control character, a value the context refuses or a result
// over MaxRenderedLength is a render error: an *Error with reason grant_render_failed. There is no
// partial or no-op outcome.
func (t *Template) Render(_ Claims, _ Context) (string, error) {
	panic("not implemented: W2")
}
