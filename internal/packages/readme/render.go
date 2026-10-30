package readme

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	mdhtml "github.com/yuin/goldmark/renderer/html"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Version is the renderer version. It is part of the ETag of a README answer
// (R-API-029) and is raised whenever the renderer or the sanitizer policy changes
// what a source renders to.
const Version = 1

// The limits of R-UI-008 that are not configuration.
const (
	// MaxHTMLBytes is the largest rendered document.
	MaxHTMLBytes = 1 << 20
	// RenderTimeout is the longest a rendering may take.
	RenderTimeout = 2 * time.Second
)

// UserContent is the prefix of every id and name a README keeps.
const UserContent = "user-content-"

// ExternalRel is the rel of every link that leaves trove.
const ExternalRel = "nofollow noopener noreferrer"

// Options configure [New].
type Options struct {
	// MaxSourceBytes is ui.readme.max_source_bytes; a longer source is cut.
	MaxSourceBytes int
	// ImageOrigins are the exact https origins whose images stay
	// (ui.readme.external_image_origins).
	ImageOrigins []string
	// OwnOrigins are the origins of trove itself (server.base_url and the endpoint
	// base URLs): an image from one of them is dropped, so that a README cannot make
	// the viewer's browser send authenticated requests to package or file endpoints
	// (R-AUTHZ-008).
	OwnOrigins []string
}

// Renderer renders READMEs. It is safe for concurrent use.
type Renderer struct {
	maxSource int
	allowed   map[string]bool
	own       map[string]bool
	policy    *bluemonday.Policy
	md        goldmark.Markdown
	hash      string
	// timeout is RenderTimeout; a test of the size limit gives a slow machine more.
	timeout time.Duration
}

// New builds a renderer. An origin that is not an exact https origin is an error.
func New(o Options) (*Renderer, error) {
	if o.MaxSourceBytes < 1 {
		return nil, fmt.Errorf("readme: max source bytes must be positive, got %d", o.MaxSourceBytes)
	}
	r := &Renderer{maxSource: o.MaxSourceBytes, allowed: map[string]bool{}, own: map[string]bool{}, timeout: RenderTimeout}
	for _, s := range o.ImageOrigins {
		origin, err := ExactOrigin(s)
		if err != nil {
			return nil, fmt.Errorf("readme: image origin %q: %w", s, err)
		}
		r.allowed[origin] = true
	}
	for _, s := range o.OwnOrigins {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			r.own[originOf(u)] = true
		}
	}
	for origin := range r.allowed {
		if r.own[origin] {
			return nil, fmt.Errorf("readme: image origin %q is an origin of this installation", origin)
		}
	}
	r.policy = newPolicy()
	r.md = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(mdhtml.WithUnsafe()),
	)
	origins := make([]string, 0, len(r.allowed))
	for k := range r.allowed {
		origins = append(origins, k)
	}
	sort.Strings(origins)
	sum := sha256.Sum256([]byte("origins\n" + strings.Join(origins, "\n")))
	r.hash = hex.EncodeToString(sum[:8])
	return r, nil
}

// ExactOrigin checks the form of an external image origin of R-UI-008: scheme
// https, a host, an optional port, and nothing else. It returns the origin in the
// form it is compared in.
func ExactOrigin(s string) (string, error) {
	if strings.ContainsAny(s, "*; ,'\"\r\n") {
		return "", fmt.Errorf("contains a character that is not allowed in an origin")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("must be an https origin of the form https://host[:port]")
	}
	return originOf(u), nil
}

func originOf(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return strings.ToLower(u.Scheme) + "://" + host
}

// PolicyHash identifies what, besides the source and the version, decides the
// rendered HTML: the list of external image origins. It is part of the ETag of a
// README answer.
func (r *Renderer) PolicyHash() string { return r.hash }

// Result is a rendered README.
type Result struct {
	// HTML is sanitized.
	HTML string
	// Truncated is true when the source or the rendered HTML exceeded a limit, or
	// the rendering took too long.
	Truncated bool
	// ExternalImagesBlocked counts the images replaced by links.
	ExternalImagesBlocked int32
}

// Render renders a Markdown source. It always returns sanitized HTML: a source above
// the limit is cut, and a rendering that is too large or too slow falls back to the
// escaped text, with Truncated set.
func (r *Renderer) Render(source string) Result {
	truncated := false
	if len(source) > r.maxSource {
		source, truncated = cutRunes(source, r.maxSource), true
	}
	for range 8 {
		res, ok := r.renderWithin(source)
		switch {
		case !ok:
			return Result{HTML: plain(source), Truncated: true}
		case len(res.HTML) <= MaxHTMLBytes:
			res.Truncated = res.Truncated || truncated
			return res
		}
		// Too large: keep the share of the source that fits, with a tenth to spare, so
		// that the next pass is the last in all but a degenerate case.
		source, truncated = cutRunes(source, int(int64(len(source))*MaxHTMLBytes/int64(len(res.HTML))*9/10)), true
	}
	return Result{HTML: plain(source), Truncated: true}
}

// renderWithin runs the pipeline with the time limit. A rendering that takes longer
// is abandoned and ok is false.
func (r *Renderer) renderWithin(source string) (Result, bool) {
	done := make(chan Result, 1)
	go func() { done <- r.pipeline(source) }()
	t := time.NewTimer(r.timeout)
	defer t.Stop()
	select {
	case res := <-done:
		return res, true
	case <-t.C:
		return Result{}, false
	}
}

func (r *Renderer) pipeline(source string) Result {
	var buf bytes.Buffer
	if err := r.md.Convert([]byte(source), &buf); err != nil {
		return Result{HTML: plain(source), Truncated: true}
	}
	clean := r.policy.SanitizeBytes(buf.Bytes())
	out, blocked := r.finish(clean)
	return Result{HTML: out, ExternalImagesBlocked: blocked}
}

// plain is the fallback: the source as escaped text, cut so that the escaped text fits
// the limit for the rendered document. Escaping grows a character by at most a factor
// of six (&#34;), so a source of a sixth of the limit always fits.
func plain(source string) string {
	return "<pre>" + html.EscapeString(cutRunes(source, MaxHTMLBytes/6-16)) + "</pre>"
}

func cutRunes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

var (
	langClass   = regexp.MustCompile(`^language-[A-Za-z0-9_+.#-]{1,40}$`)
	userContent = regexp.MustCompile(`^user-content-[A-Za-z0-9_.:\p{L}\p{N}%-]{0,200}$`)
	alignValue  = regexp.MustCompile(`^(left|center|right)$`)
	numeric     = regexp.MustCompile(`^[0-9]{1,4}$`)
)

// newPolicy is the allowlist of R-UI-008. Elements and attributes outside it are
// removed; the content of the elements that carry code or foreign documents is
// removed with them.
func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("h1", "h2", "h3", "h4", "h5", "h6", "p", "br", "hr", "blockquote", "ul", "ol", "li",
		"dl", "dt", "dd", "pre", "code", "kbd", "samp", "em", "strong", "del", "ins", "sup", "sub", "a", "img",
		"table", "thead", "tbody", "tfoot", "tr", "th", "td", "details", "summary", "input")
	p.SkipElementsContent("script", "style", "iframe", "object", "embed", "svg", "math", "noscript", "template",
		"form", "select", "textarea", "title")
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(true)
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("src", "alt").OnElements("img")
	p.AllowAttrs("width", "height").Matching(numeric).OnElements("img")
	p.AllowAttrs("id", "name").Matching(userContent).Globally()
	p.AllowAttrs("class").Matching(langClass).OnElements("code")
	p.AllowAttrs("align").Matching(alignValue).OnElements("th", "td")
	p.AllowAttrs("colspan", "rowspan").Matching(numeric).OnElements("th", "td")
	p.AllowAttrs("open").OnElements("details")
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("checked", "disabled").OnElements("input")
	return p
}

// finish applies what the allowlist cannot say to the sanitized fragment.
func (r *Renderer) finish(clean []byte) (string, int32) {
	root := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := xhtml.ParseFragment(bytes.NewReader(clean), root)
	if err != nil {
		return plain(string(clean)), 0
	}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	f := &finisher{r: r, seen: map[string]int{}}
	f.walk(root)
	var out bytes.Buffer
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if err := xhtml.Render(&out, c); err != nil {
			return plain(string(clean)), 0
		}
	}
	return out.String(), f.blocked
}

type finisher struct {
	r       *Renderer
	seen    map[string]int
	blocked int32
}

// walk visits the children before the element, so that an anchor is judged after
// the images inside it and an unwrapped anchor leaves no unvisited child behind.
func (f *finisher) walk(n *xhtml.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		f.walk(c)
		if c.Type == xhtml.ElementNode {
			f.element(c)
		}
		c = next
	}
}

func (f *finisher) element(n *xhtml.Node) {
	switch n.DataAtom {
	case atom.A:
		f.anchor(n)
	case atom.Img:
		f.image(n)
	case atom.Input:
		f.checkbox(n)
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		if slug := HeadingSlug(textOf(n), f.seen); slug != "" {
			setAttr(n, "id", UserContent+slug)
		}
	}
}

// anchor keeps a link to a fragment, an http or https address or a mail address.
// Any other target is removed; an anchor left with neither target nor name is
// reduced to its text.
func (f *finisher) anchor(n *xhtml.Node) {
	href := strings.TrimSpace(attr(n, "href"))
	switch {
	case href == "":
	case strings.HasPrefix(href, "#"):
		setAttr(n, "href", href)
	case absolute(href):
		setAttr(n, "href", href)
		setAttr(n, "rel", ExternalRel)
	default:
		delAttr(n, "href")
	}
	delAttr(n, "target")
	if attr(n, "href") == "" && attr(n, "id") == "" && attr(n, "name") == "" {
		unwrap(n)
	}
}

// absolute reports whether s is an absolute http, https or mailto address.
func absolute(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return true
	}
	return false
}

// image applies the image policy of Q-UI-6: an https image of a listed origin
// stays, an image of trove's own origin and a relative or other source is dropped,
// and every other image becomes a link, or only its text inside a link.
func (f *finisher) image(n *xhtml.Node) {
	src := strings.TrimSpace(attr(n, "src"))
	u, err := url.Parse(src)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		remove(n)
		return
	}
	origin := originOf(u)
	switch {
	case f.r.own[origin]:
		remove(n)
	case u.Scheme == "https" && f.r.allowed[origin]:
		delAttr(n, "srcset")
	default:
		f.blocked++
		label := "External image"
		if alt := strings.TrimSpace(attr(n, "alt")); alt != "" {
			label += ": " + alt
		}
		text := &xhtml.Node{Type: xhtml.TextNode, Data: label}
		if insideAnchor(n) {
			replace(n, text)
			return
		}
		a := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.A, Data: "a", Attr: []xhtml.Attribute{
			{Key: "href", Val: src}, {Key: "rel", Val: ExternalRel},
		}}
		a.AppendChild(text)
		replace(n, a)
	}
}

// checkbox keeps the checkboxes of task lists and makes them read-only.
func (f *finisher) checkbox(n *xhtml.Node) {
	if !strings.EqualFold(attr(n, "type"), "checkbox") {
		remove(n)
		return
	}
	setAttr(n, "disabled", "")
}

func insideAnchor(n *xhtml.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.DataAtom == atom.A {
			return true
		}
	}
	return false
}

func attr(n *xhtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *xhtml.Node, key, val string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, xhtml.Attribute{Key: key, Val: val})
}

func delAttr(n *xhtml.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != key {
			out = append(out, a)
		}
	}
	n.Attr = out
}

func remove(n *xhtml.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

func replace(n, with *xhtml.Node) {
	if n.Parent != nil {
		n.Parent.InsertBefore(with, n)
		n.Parent.RemoveChild(n)
	}
}

// unwrap replaces an element by its children.
func unwrap(n *xhtml.Node) {
	p := n.Parent
	if p == nil {
		return
	}
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		n.RemoveChild(c)
		p.InsertBefore(c, n)
		c = next
	}
	p.RemoveChild(n)
}

func textOf(n *xhtml.Node) string {
	var b strings.Builder
	var rec func(*xhtml.Node)
	rec = func(x *xhtml.Node) {
		if x.Type == xhtml.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return b.String()
}

// HeadingSlug is the anchor name of a heading: trimmed, lower case, every character
// that is no letter, digit, white space, underscore or hyphen removed, white space
// to hyphens. seen counts a repeated name so that the second "Usage" is "usage-1".
// It is the rule of headingSlug in web/src/app/features/packages/package-readme.ts
// and the two are kept identical (Q-UI-76): the component derives the anchors again
// from the text of the headings, so that the links of a table of contents that this
// renderer wrote resolve. The text functions are those of JavaScript's trim,
// toLowerCase and \s, not Go's, because the browser runs the other half.
func HeadingSlug(heading string, seen map[string]int) string {
	var b strings.Builder
	for _, r := range jsLower(strings.TrimFunc(heading, jsSpace)) {
		switch {
		case jsSpace(r):
			b.WriteByte('-')
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-':
			b.WriteRune(r)
		}
	}
	base := b.String()
	n := seen[base]
	seen[base] = n + 1
	if n == 0 {
		return base
	}
	return base + "-" + strconv.Itoa(n)
}

// jsSpace is what JavaScript calls white space (the regular expression class \s and
// String.prototype.trim): the ECMAScript WhiteSpace and LineTerminator productions.
// Go's unicode.IsSpace differs: it counts U+0085 and lacks U+FEFF.
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// jsLower is String.prototype.toLowerCase: the simple mapping of Unicode, the one
// unconditional special case (U+0130 becomes "i" and a combining dot) and the
// context rule for the final sigma, which strings.ToLower does not know.
func jsLower(s string) string {
	rs := []rune(s)
	out := make([]rune, 0, len(rs))
	for i, r := range rs {
		switch {
		case r == 0x130:
			out = append(out, 'i', 0x307)
		case r == 'Σ' && finalSigma(rs, i):
			out = append(out, 'ς')
		default:
			out = append(out, unicode.ToLower(r))
		}
	}
	return string(out)
}

// finalSigma is the Final_Sigma condition: a cased letter before the sigma, with
// only case-ignorable characters between, and none after it.
func finalSigma(rs []rune, i int) bool {
	before := false
	for j := i - 1; j >= 0; j-- {
		if caseIgnorable(rs[j]) {
			continue
		}
		before = cased(rs[j])
		break
	}
	if !before {
		return false
	}
	for j := i + 1; j < len(rs); j++ {
		if caseIgnorable(rs[j]) {
			continue
		}
		return !cased(rs[j])
	}
	return true
}

func cased(r rune) bool { return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) }

func caseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) ||
		strings.ContainsRune("'.:·‘\u2019․‧︓﹒﹕＇．：", r)
}
