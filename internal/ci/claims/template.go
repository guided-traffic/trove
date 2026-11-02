package claims

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/ci/reason"
)

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

// The grammar of R-CI-014:
//
//	template   = *( literal / "{{" ws expr ws "}}" )
//	expr       = source *( ws "|" ws filter )
//	source     = claim-name / string        ; claim-name: [a-z_][a-z0-9_]* (top-level claims only)
//	filter     = "lower" / "slug"
//	           / "replace" ws string ws string
//	           / "trim_prefix" ws string / "trim_suffix" ws string
//	           / "segment" ws integer       ; 0-based "/" segment, negative counts from the end
//	           / "require" ws string        ; RE2 full match, else render error
//	string     = DQUOTE *( %x20-21 / %x23-5B / %x5D-10FFFF / "\" ( DQUOTE / "\" ) ) DQUOTE
//
// ws is any run of spaces and tabs. Literal text is any text without "{{"; it holds no control
// character, and "}}" in it is literal. A literal "{{" is written {{ "{{" }}.

// Template is a parsed template of the language of R-CI-014 (a literal text with {{ source | filter … }}
// substitutions; the filters lower, slug, replace, trim_prefix, trim_suffix, segment and require).
// The zero value is the empty literal. A Template is immutable and safe for concurrent use.
type Template struct {
	src    string
	pieces []piece
}

// piece is a literal text or one substitution.
type piece struct {
	literal string
	expr    *expression
}

// expression is one {{ … }}: a claim or a string, and its filters.
type expression struct {
	claim   string // "" for a string source
	str     string
	filters []filter
}

type filter struct {
	name string
	args []string
	n    int
	re   *regexp.Regexp
}

// templateInvalid is the refusal of a template's syntax (422 template_invalid); the mapping service
// sets the pointer of the pattern.
func templateInvalid(pos int, detail string) *Error {
	return refuse(reason.TemplateInvalid, "", "at byte "+strconv.Itoa(pos+1)+": "+detail)
}

// ParseTemplate parses a template (422 template_invalid with the position on a syntax error or an
// unknown filter, source or argument). A string without "{{" is a literal template.
//
// Beyond the grammar it refuses a control character in the literal text, `replace` with an empty
// search string (which would insert its replacement between every character), a `require`
// expression that does not compile anchored or is longer than 512 characters (the limit of a
// condition's regex), and literal text that alone is longer than MaxRenderedLength (Q-CI-29).
func ParseTemplate(src string) (*Template, error) {
	p := templateParser{src: src}
	if err := p.parse(); err != nil {
		return nil, err
	}
	return &Template{src: src, pieces: p.pieces}, nil
}

type templateParser struct {
	src    string
	i      int
	pieces []piece
}

func (p *templateParser) parse() error {
	literalRunes := 0
	for p.i < len(p.src) {
		j := strings.Index(p.src[p.i:], "{{")
		if j < 0 {
			j = len(p.src) - p.i
		}
		if j > 0 {
			lit := p.src[p.i : p.i+j]
			if k := strings.IndexFunc(lit, isControl); k >= 0 {
				return templateInvalid(p.i+k, "a control character in the literal text")
			}
			literalRunes += utf8.RuneCountInString(lit)
			p.pieces = append(p.pieces, piece{literal: lit})
			p.i += j
		}
		if p.i >= len(p.src) {
			break
		}
		p.i += 2
		e, err := p.expression()
		if err != nil {
			return err
		}
		p.pieces = append(p.pieces, piece{expr: e})
	}
	if literalRunes > MaxRenderedLength {
		return templateInvalid(0, "the literal text alone is longer than 1024 characters")
	}
	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

func (p *templateParser) skipWS() {
	for p.i < len(p.src) && (p.src[p.i] == ' ' || p.src[p.i] == '\t') {
		p.i++
	}
}

func (p *templateParser) expression() (*expression, error) {
	p.skipWS()
	e := &expression{}
	switch {
	case p.i < len(p.src) && p.src[p.i] == '"':
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		e.str = s
	default:
		name := p.word(isClaimStart, isClaimChar)
		if name == "" {
			return nil, templateInvalid(p.i, "a substitution starts with a claim name ([a-z_][a-z0-9_]*) or a string")
		}
		e.claim = name
	}
	for {
		p.skipWS()
		switch {
		case strings.HasPrefix(p.src[p.i:], "}}"):
			p.i += 2
			return e, nil
		case p.i < len(p.src) && p.src[p.i] == '|':
			p.i++
			p.skipWS()
			f, err := p.filter()
			if err != nil {
				return nil, err
			}
			e.filters = append(e.filters, f)
		case p.i >= len(p.src):
			return nil, templateInvalid(p.i, "the substitution is not closed with }}")
		default:
			return nil, templateInvalid(p.i, "expected | or }}")
		}
	}
}

func isClaimStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') }
func isClaimChar(c byte) bool  { return isClaimStart(c) || (c >= '0' && c <= '9') }

func (p *templateParser) word(start, rest func(byte) bool) string {
	b := p.i
	if p.i >= len(p.src) || !start(p.src[p.i]) {
		return ""
	}
	p.i++
	for p.i < len(p.src) && rest(p.src[p.i]) {
		p.i++
	}
	return p.src[b:p.i]
}

// str reads a string of the grammar: printable characters, \" and \\ as the only escapes.
func (p *templateParser) str() (string, error) {
	start := p.i
	p.i++ // the opening quote
	var b strings.Builder
	for p.i < len(p.src) {
		r, size := utf8.DecodeRuneInString(p.src[p.i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			return "", templateInvalid(p.i, "a string holds bytes that are not UTF-8")
		case r == '"':
			p.i++
			return b.String(), nil
		case r == '\\':
			if p.i+1 >= len(p.src) || (p.src[p.i+1] != '"' && p.src[p.i+1] != '\\') {
				return "", templateInvalid(p.i, `a string escapes only \" and \\`)
			}
			b.WriteByte(p.src[p.i+1])
			p.i += 2
			continue
		case r < 0x20:
			return "", templateInvalid(p.i, "a control character in a string")
		}
		b.WriteRune(r)
		p.i += size
	}
	return "", templateInvalid(start, "the string is not closed")
}

func (p *templateParser) filter() (filter, error) {
	at := p.i
	name := p.word(isClaimStart, isClaimChar)
	f := filter{name: name}
	switch name {
	case "lower", "slug":
		return f, nil
	case "replace":
		args, err := p.strings(2)
		if err != nil {
			return f, err
		}
		if args[0] == "" {
			return f, templateInvalid(at, "replace needs a non-empty search string")
		}
		f.args = args
	case "trim_prefix", "trim_suffix":
		args, err := p.strings(1)
		if err != nil {
			return f, err
		}
		f.args = args
	case "segment":
		p.skipWS()
		b := p.i
		if p.i < len(p.src) && p.src[p.i] == '-' {
			p.i++
		}
		for p.i < len(p.src) && p.src[p.i] >= '0' && p.src[p.i] <= '9' {
			p.i++
		}
		n, err := strconv.Atoi(p.src[b:p.i])
		if err != nil {
			return f, templateInvalid(b, "segment needs an integer")
		}
		f.n = n
	case "require":
		args, err := p.strings(1)
		if err != nil {
			return f, err
		}
		if utf8.RuneCountInString(args[0]) > DefaultLimits().MaxRegexLength {
			return f, templateInvalid(at, "the require expression is longer than 512 characters")
		}
		re, err := compileAnchored(args[0], false)
		if err != nil {
			return f, templateInvalid(at, "the require expression does not compile in RE2: "+err.Error())
		}
		f.args, f.re = args, re
	case "":
		return f, templateInvalid(at, "a filter name follows |")
	default:
		return f, templateInvalid(at, "unknown filter "+strconv.Quote(name)+" (lower, slug, replace, trim_prefix, trim_suffix, segment, require)")
	}
	return f, nil
}

func (p *templateParser) strings(n int) ([]string, error) {
	out := make([]string, 0, n)
	for range n {
		p.skipWS()
		if p.i >= len(p.src) || p.src[p.i] != '"' {
			return nil, templateInvalid(p.i, "the filter needs a string argument")
		}
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// String returns the source text.
func (t *Template) String() string {
	if t == nil {
		return ""
	}
	return t.src
}

// IsLiteral reports whether the template has no substitution.
func (t *Template) IsLiteral() bool {
	if t == nil {
		return true
	}
	for _, p := range t.pieces {
		if p.expr != nil {
			return false
		}
	}
	return true
}

// ClaimNames returns the claims the template substitutes, in order of first use. The save-time
// warning for a claim that is neither required by the issuer nor guarded uses it.
func (t *Template) ClaimNames() []string {
	if t == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range t.pieces {
		if p.expr != nil && p.expr.claim != "" && !seen[p.expr.claim] {
			seen[p.expr.claim] = true
			out = append(out, p.expr.claim)
		}
	}
	return out
}

// Anchored reports whether every substitution has literal text next to it, the rule of the
// `repositories` context (R-CI-014). Only a claim substitution needs an anchor (a string source is
// fixed), and the anchor is literal text right before or after it that holds a character other than
// the wildcards `*` and `?`: `{{ x }}*` lets the claim value alone select a repository (Q-CI-29).
func (t *Template) Anchored() bool {
	if t == nil {
		return true
	}
	for i, p := range t.pieces {
		if p.expr == nil || p.expr.claim == "" {
			continue
		}
		if !t.anchorAt(i-1) && !t.anchorAt(i+1) {
			return false
		}
	}
	return true
}

func (t *Template) anchorAt(i int) bool {
	if i < 0 || i >= len(t.pieces) || t.pieces[i].expr != nil {
		return false
	}
	return strings.ContainsFunc(t.pieces[i].literal, func(r rune) bool { return r != '*' && r != '?' })
}

// unconstrained reports whether a claim substitution has no `require` filter, the warning
// repository_template_unconstrained of R-CI-010.
func (t *Template) unconstrained() bool {
	if t == nil {
		return false
	}
	for _, p := range t.pieces {
		if p.expr == nil || p.expr.claim == "" {
			continue
		}
		required := false
		for _, f := range p.expr.filters {
			required = required || f.name == "require"
		}
		if !required {
			return true
		}
	}
	return false
}

// classLike reports whether a template with a substitution starts with literal text "@": it would
// render a class selector, which a template never produces (R-AUTHZ-004).
func (t *Template) classLike() bool {
	return t != nil && !t.IsLiteral() && len(t.pieces) > 0 && t.pieces[0].expr == nil && strings.HasPrefix(t.pieces[0].literal, "@")
}

// renderFailed is a render error (grant_render_failed); the detail names the claim, never its value.
func renderFailed(detail string) *Error { return refuse(reason.GrantRenderFailed, "", detail) }

// Render renders the template for the claims in a context. An array, object, missing or null claim,
// a failed `require`, an empty `slug`, a control character, a value the context refuses or a result
// over MaxRenderedLength is a render error: an *Error with reason grant_render_failed. There is no
// partial or no-op outcome.
//
// So that no claim widens a pattern, an empty substituted value and a `segment` index outside the
// value are render errors in every context too (Q-CI-29); a number that is not an integer is no value.
func (t *Template) Render(c Claims, ctx Context) (string, error) {
	if t == nil {
		return "", nil
	}
	switch ctx {
	case ContextRepositories, ContextPackages, ContextPaths:
	default:
		return "", renderFailed("unknown rendering context " + strconv.Quote(string(ctx)))
	}
	var b strings.Builder
	for _, p := range t.pieces {
		if p.expr == nil {
			b.WriteString(p.literal)
			continue
		}
		v, err := p.expr.value(c)
		if err != nil {
			return "", err
		}
		v, err = substitute(v, ctx, p.expr.label())
		if err != nil {
			return "", err
		}
		b.WriteString(v)
	}
	out := b.String()
	if utf8.RuneCountInString(out) > MaxRenderedLength {
		return "", renderFailed("the rendered pattern is longer than 1024 characters")
	}
	return out, nil
}

// label names a substitution in a detail: its claim, or "a string".
func (e *expression) label() string {
	if e.claim != "" {
		return "the claim " + e.claim
	}
	return "a string source"
}

// value is the substitution's value after its filters.
func (e *expression) value(c Claims) (string, error) {
	v := e.str
	if e.claim != "" {
		raw, ok := c[e.claim]
		if !ok || raw == nil {
			return "", renderFailed("the claim " + e.claim + " is missing or null")
		}
		switch raw.(type) {
		case []any:
			return "", renderFailed("the claim " + e.claim + " is an array, which a grant template does not substitute")
		case map[string]any:
			return "", renderFailed("the claim " + e.claim + " is an object, which a grant template does not substitute")
		}
		s, isScalar := scalar(raw)
		if !isScalar {
			return "", renderFailed("the claim " + e.claim + " is a number that is not an integer")
		}
		v = s
	}
	for _, f := range e.filters {
		var err error
		if v, err = f.apply(v, e.label()); err != nil {
			return "", err
		}
	}
	return v, nil
}

func (f filter) apply(v, label string) (string, error) {
	switch f.name {
	case "lower":
		return strings.ToLower(v), nil
	case "slug":
		s := slug(v)
		if s == "" {
			return "", renderFailed("the slug of " + label + " is empty")
		}
		return s, nil
	case "replace":
		return strings.ReplaceAll(v, f.args[0], f.args[1]), nil
	case "trim_prefix":
		return strings.TrimPrefix(v, f.args[0]), nil
	case "trim_suffix":
		return strings.TrimSuffix(v, f.args[0]), nil
	case "segment":
		segs := strings.Split(v, "/")
		i := f.n
		if i < 0 {
			i += len(segs)
		}
		if i < 0 || i >= len(segs) {
			return "", renderFailed("segment " + strconv.Itoa(f.n) + " of " + label + " does not exist")
		}
		return segs[i], nil
	case "require":
		if !f.re.MatchString(v) {
			return "", renderFailed("the value of " + label + " does not match the require filter")
		}
		return v, nil
	}
	return "", renderFailed("unknown filter " + strconv.Quote(f.name))
}

// slug lowercases and turns every run of characters outside [a-z0-9] into one "-", without a
// leading or trailing "-".
func slug(v string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(v) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

var repositoryValue = regexp.MustCompile(`^[a-z0-9._-]+$`)

// globMeta are the metacharacters a substituted value has escaped in packages and paths.
const globMeta = `\*?[]{}`

// substitute applies the context's rule to one substituted value (R-CI-014).
func substitute(v string, ctx Context, label string) (string, error) {
	if v == "" {
		return "", renderFailed("the value substituted for " + label + " is empty")
	}
	if strings.ContainsFunc(v, isControl) {
		return "", renderFailed("the value substituted for " + label + " holds a control character")
	}
	switch ctx {
	case ContextRepositories:
		if !repositoryValue.MatchString(v) {
			return "", renderFailed("the value substituted for " + label + " is not of [a-z0-9._-], as a repositories template needs")
		}
		return v, nil
	case ContextPackages:
		if strings.Contains(v, "/") {
			return "", renderFailed("the value substituted for " + label + " holds a \"/\", which a packages template does not accept")
		}
	case ContextPaths:
		if strings.HasPrefix(v, "/") || strings.Contains(v, "..") || strings.Contains(v, "//") || strings.HasSuffix(v, "/") {
			return "", renderFailed("the value substituted for " + label + " starts with \"/\", holds \"..\" or an empty segment, which a paths template does not accept")
		}
	}
	var b strings.Builder
	for _, r := range v {
		if strings.ContainsRune(globMeta, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String(), nil
}
