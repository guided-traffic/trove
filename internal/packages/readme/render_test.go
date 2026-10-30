package readme

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func renderer(t *testing.T, origins ...string) *Renderer {
	t.Helper()
	r, err := New(Options{MaxSourceBytes: 512 << 10, ImageOrigins: origins, OwnOrigins: []string{"https://trove.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestR_UI_008_XSSCorpusRendersNothingExecutable: every vector of the corpus renders
// to HTML that holds none of its executable forms.
func TestR_UI_008_XSSCorpusRendersNothingExecutable(t *testing.T) {
	corpus := map[string]string{
		"script tag":         `<script>alert(1)</script>`,
		"script in a para":   "hello <script>alert(1)</script> world",
		"event handler":      `<img src="https://img.example/x.png" onerror="alert(1)">`,
		"event on a link":    `<a href="https://example.com" onclick="alert(1)">x</a>`,
		"javascript link":    `[x](javascript:alert(1))`,
		"javascript raw":     `<a href="javascript:alert(1)">x</a>`,
		"vbscript link":      `<a href="vbscript:msgbox(1)">x</a>`,
		"data link":          `<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">x</a>`,
		"svg with script":    `<svg><script>alert(1)</script></svg>`,
		"svg onload":         `<svg onload="alert(1)"></svg>`,
		"form":               `<form action="https://evil.example"><input name="a"></form>`,
		"base":               `<base href="https://evil.example/">`,
		"style tag":          `<style>body{display:none}</style>`,
		"style attribute":    `<p style="background:url(https://evil.example)">x</p>`,
		"iframe":             `<iframe src="https://evil.example"></iframe>`,
		"object":             `<object data="https://evil.example"></object>`,
		"embed":              `<embed src="https://evil.example">`,
		"meta refresh":       `<meta http-equiv="refresh" content="0;url=https://evil.example">`,
		"link stylesheet":    `<link rel="stylesheet" href="https://evil.example/a.css">`,
		"math":               `<math><mi xlink:href="javascript:alert(1)">x</mi></math>`,
		"proto id":           `<p id="__proto__">x</p>`,
		"clobbering name":    `<a name="getElementById">x</a>`,
		"clobbering id":      `<h1 id="cookie">x</h1>`,
		"unclosed attribute": `<a href="https://example.com" onmouseover=alert(1) x>x</a>`,
		"mixed case":         `<ScRiPt>alert(1)</sCrIpT>`,
		"entity encoded":     `<a href="&#106;avascript:alert(1)">x</a>`,
		"image data source":  `<img src="data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=">`,
	}
	forbidden := regexp.MustCompile(`(?i)<script|<style|<iframe|<object|<embed|<form|<base|<meta|<link|<svg|<math|\bon[a-z]+\s*=|javascript:|vbscript:|data:|\bstyle\s*=|id="__proto__"|id="cookie"|name="getElementById"`)
	r := renderer(t)
	for name, src := range corpus {
		t.Run(name, func(t *testing.T) {
			res := r.Render(src)
			if loc := forbidden.FindString(res.HTML); loc != "" {
				t.Fatalf("%q survived in %q", loc, res.HTML)
			}
		})
	}
}

// TestR_UI_008_RawHTMLPassesThroughTheSanitizer: raw HTML in the Markdown stays where
// the allowlist has it.
func TestR_UI_008_RawHTMLPassesThroughTheSanitizer(t *testing.T) {
	res := renderer(t).Render("<details open><summary>More</summary><kbd>Ctrl</kbd></details>")
	for _, want := range []string{"<details open=\"\">", "<summary>More</summary>", "<kbd>Ctrl</kbd>"} {
		if !strings.Contains(res.HTML, want) {
			t.Errorf("%q is missing in %q", want, res.HTML)
		}
	}
}

// TestR_UI_008_GitHubStyleMarkdown: tables, strikethrough, autolinks and task lists.
func TestR_UI_008_GitHubStyleMarkdown(t *testing.T) {
	src := "| a | b |\n|---|:-:|\n| 1 | 2 |\n\n~~gone~~ https://example.com/x\n\n- [x] done\n- [ ] open\n"
	res := renderer(t).Render(src)
	for _, want := range []string{"<table>", "<del>gone</del>", `href="https://example.com/x"`, `type="checkbox"`, `disabled=""`} {
		if !strings.Contains(res.HTML, want) {
			t.Errorf("%q is missing in %q", want, res.HTML)
		}
	}
	if strings.Contains(res.HTML, "<input") && !strings.Contains(res.HTML, "disabled") {
		t.Errorf("a checkbox is not disabled: %q", res.HTML)
	}
}

// TestR_UI_008_RawCheckboxIsDisabled: a checkbox an author writes is made read-only,
// and an input of another type is dropped.
func TestR_UI_008_RawCheckboxIsDisabled(t *testing.T) {
	res := renderer(t).Render(`<input type="checkbox"> <input type="text" value="x"> <input type="password">`)
	if strings.Count(res.HTML, "<input") != 1 || !strings.Contains(res.HTML, "disabled") {
		t.Fatalf("got %q", res.HTML)
	}
}

// TestR_UI_008_LinksGetRelAndNoTarget: a link that leaves trove carries the rel of the
// spec and no target; a fragment stays; a relative link is reduced to its text.
func TestR_UI_008_LinksGetRelAndNoTarget(t *testing.T) {
	res := renderer(t).Render(`<a href="https://example.com" target="_blank" rel="opener">ext</a> [frag](#user-content-usage) [rel](docs/guide.md) [abs](/api/v1/users) [mail](mailto:a@example.com) [pr](//evil.example/x)`)
	if !strings.Contains(res.HTML, `href="https://example.com" rel="nofollow noopener noreferrer"`) {
		t.Errorf("external link: %q", res.HTML)
	}
	if strings.Contains(res.HTML, "target=") {
		t.Errorf("a target survived: %q", res.HTML)
	}
	if !strings.Contains(res.HTML, `href="#user-content-usage"`) || !strings.Contains(res.HTML, `href="mailto:a@example.com"`) {
		t.Errorf("fragment or mail link: %q", res.HTML)
	}
	for _, text := range []string{"rel", "abs", "pr"} {
		if !strings.Contains(res.HTML, text) {
			t.Errorf("the text %q of a reduced link is missing: %q", text, res.HTML)
		}
	}
	for _, href := range []string{"docs/guide.md", "/api/v1/users", "//evil.example"} {
		if strings.Contains(res.HTML, `href="`+href) {
			t.Errorf("link target %q survived: %q", href, res.HTML)
		}
	}
}

// TestR_UI_008_ExternalImagesPerPolicy: a listed https origin keeps its image, every
// other external image becomes a link and is counted, and an image of trove's own
// origin or a relative one is dropped without a link.
func TestR_UI_008_ExternalImagesPerPolicy(t *testing.T) {
	src := strings.Join([]string{
		`![build status](https://img.shields.example/badge.svg)`,
		`![kept](https://gitlab.example.com/logo.png)`,
		`![x](/api/npm/npm-remote/left-pad)`,
		`<img src="https://trove.example.com/files/generic-local/x.png">`,
		`<img src="https://TROVE.example.com:443/files/generic-local/y.png">`,
		`![plain http](http://gitlab.example.com/logo.png)`,
		`[![badge](https://img.shields.example/b.svg)](https://example.com)`,
	}, "\n\n")
	res := renderer(t, "https://gitlab.example.com").Render(src)
	if res.ExternalImagesBlocked != 3 {
		t.Errorf("externalImagesBlocked = %d, want 3 (%q)", res.ExternalImagesBlocked, res.HTML)
	}
	if !strings.Contains(res.HTML, `>External image: build status</a>`) {
		t.Errorf("no link for the unlisted badge: %q", res.HTML)
	}
	if strings.Count(res.HTML, "<img") != 1 || !strings.Contains(res.HTML, `src="https://gitlab.example.com/logo.png"`) {
		t.Errorf("only the listed image stays: %q", res.HTML)
	}
	for _, s := range []string{"left-pad", "trove.example.com", "TROVE"} {
		if strings.Contains(res.HTML, s) {
			t.Errorf("%q survived: %q", s, res.HTML)
		}
	}
	if strings.Contains(res.HTML, "<a href=\"https://example.com\" rel=\"nofollow noopener noreferrer\"><a") {
		t.Errorf("a link was nested in a link: %q", res.HTML)
	}
}

// TestR_UI_008_EmptyOriginListBlocksEveryImage: with no origin listed, no img element
// stays.
func TestR_UI_008_EmptyOriginListBlocksEveryImage(t *testing.T) {
	res := renderer(t).Render("![a](https://img.example/a.png) ![b](https://other.example/b.png)")
	if strings.Contains(res.HTML, "<img") || res.ExternalImagesBlocked != 2 {
		t.Fatalf("got %d blocked, %q", res.ExternalImagesBlocked, res.HTML)
	}
}

// TestR_UI_008_OnlyExactHTTPSOriginsAreAccepted: the list is validated when the
// renderer is built.
func TestR_UI_008_OnlyExactHTTPSOriginsAreAccepted(t *testing.T) {
	for _, bad := range []string{"http://img.example", "https://*.example.com", "https://img.example/path", "img.example", "https://user@img.example", "https://img.example?x=1", ""} {
		if _, err := New(Options{MaxSourceBytes: 1, ImageOrigins: []string{bad}}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := New(Options{MaxSourceBytes: 1, ImageOrigins: []string{"https://img.example", "https://img.example:8443"}}); err != nil {
		t.Fatal(err)
	}
}

// TestR_UI_008_AnOriginOfTroveItselfCannotBeListed: a list that names trove's own origin
// is refused when the renderer is built.
func TestR_UI_008_AnOriginOfTroveItselfCannotBeListed(t *testing.T) {
	_, err := New(Options{MaxSourceBytes: 1, ImageOrigins: []string{"https://trove.example.com"}, OwnOrigins: []string{"https://TROVE.example.com:443/"}})
	if err == nil {
		t.Fatal("accepted")
	}
}

// TestR_UI_008_PolicyHashFollowsTheOriginList: the hash that is part of the ETag changes
// with the list and not with its order.
func TestR_UI_008_PolicyHashFollowsTheOriginList(t *testing.T) {
	a := renderer(t, "https://a.example", "https://b.example").PolicyHash()
	b := renderer(t, "https://b.example", "https://a.example").PolicyHash()
	c := renderer(t, "https://a.example").PolicyHash()
	if a != b || a == c || a == renderer(t).PolicyHash() {
		t.Fatalf("hashes %s %s %s", a, b, c)
	}
}

// TestR_UI_008_HeadingAnchorsFollowTheSlugRule: the anchors are user-content-<slug>
// with the rule of the UI, repeated names numbered.
func TestR_UI_008_HeadingAnchorsFollowTheSlugRule(t *testing.T) {
	res := renderer(t).Render("# Usage\n\n## Usage\n\n### Über uns & mehr!\n\n## Usage\n\n<h2>Raw <em>Head</em></h2>\n")
	for _, want := range []string{`id="user-content-usage"`, `id="user-content-usage-1"`, `id="user-content-usage-2"`, `id="user-content-über-uns--mehr"`, `id="user-content-raw-head"`} {
		if !strings.Contains(res.HTML, want) {
			t.Errorf("%q is missing in %q", want, res.HTML)
		}
	}
}

// slugCorpus is the corpus of the heading rule. The same table is in
// web/src/app/features/packages/package-readme.spec.ts, and the expected anchors are
// what JavaScript's trim, toLowerCase and \s produce (Q-UI-76); a change to either
// table is made in both.
var slugCorpus = [][2]string{
	{"Usage", "usage"},
	{"  Getting Started  ", "getting-started"},
	{"What's new? (v2)", "whats-new-v2"},
	{"snake_case and-dash", "snake_case-and-dash"},
	{"Über uns & mehr!", "über-uns--mehr"},
	{"日本語 の 見出し", "日本語-の-見出し"},
	{"A\u00a0B", "a-b"},
	{"A\u2003B", "a-b"},
	{"A\u0085B", "ab"},
	{"A\ufeffB", "a-b"},
	{"\ufeffX\ufeff", "x"},
	{"Tab\tTab", "tab-tab"},
	{"x\u2028y", "x-y"},
	{"İstanbul", "istanbul"},
	{"ΟΔΟΣ", "οδος"},
	{"ΟΔΟΣ ΣΑΣ", "οδος-σας"},
	{"Σ", "σ"},
	{"ΑΣ.", "ας"},
	{"Aσς", "aσς"},
	{"ǅ Title", "ǆ-title"},
	{"Ⅻ roman", "ⅻ-roman"},
	{"x²", "x²"},
	{"٣ arabic", "٣-arabic"},
	{"???", ""},
	{"!!!", ""},
	{"a\u200bb", "ab"},
	{"\u180eX", "x"},
	{"ﬃ ligature", "ﬃ-ligature"},
	{"Ǆ", "ǆ"},
	{"ᾈ", "ᾀ"},
	{"e\u0301", "e"},
	{"😀 emoji", "-emoji"},
	{"ΑΣ\u0301", "ας"},
	{"Σ'Α", "σα"},
	{"ΑΣ\u2019", "ας"},
}

// TestR_UI_008_HeadingSlugMatchesTheUIRule: every heading of the corpus has the anchor
// the browser derives for it.
func TestR_UI_008_HeadingSlugMatchesTheUIRule(t *testing.T) {
	for _, c := range slugCorpus {
		if got := HeadingSlug(c[0], map[string]int{}); got != c[1] {
			t.Errorf("HeadingSlug(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	seen := map[string]int{}
	for i, want := range []string{"usage", "usage-1", "usage-2", "other", "usage-3"} {
		heading := "Usage"
		if want == "other" {
			heading = "Other"
		}
		if got := HeadingSlug(heading, seen); got != want {
			t.Errorf("repeat %d: %q, want %q", i, got, want)
		}
	}
	// A heading without letters or digits has the empty anchor, and the next one is numbered.
	empty := map[string]int{}
	if a, b := HeadingSlug("???", empty), HeadingSlug("!!!", empty); a != "" || b != "-1" {
		t.Errorf("empty anchors: %q %q", a, b)
	}
}

// TestR_UI_008_SourceLimitTruncates: a source above ui.readme.max_source_bytes is cut
// and flagged.
func TestR_UI_008_SourceLimitTruncates(t *testing.T) {
	r, err := New(Options{MaxSourceBytes: 20})
	if err != nil {
		t.Fatal(err)
	}
	res := r.Render("# Title\n\n" + strings.Repeat("ä", 100))
	if !res.Truncated || len(res.HTML) > 200 {
		t.Fatalf("truncated=%v html=%q", res.Truncated, res.HTML)
	}
}

// TestR_UI_008_HTMLLimitTruncates: a rendering above 1 MiB is cut by cutting the
// source, never the HTML, so that no tag is left open.
func TestR_UI_008_HTMLLimitTruncates(t *testing.T) {
	r, err := New(Options{MaxSourceBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	r.timeout = time.Minute // the size limit is the subject here; the time limit has a test of its own
	src := strings.Repeat("- x\n", 150_000)
	if len(src) < 512<<10 {
		t.Fatalf("source too small: %d", len(src))
	}
	start := time.Now()
	res := r.Render(src)
	if !res.Truncated || len(res.HTML) > MaxHTMLBytes || !strings.Contains(res.HTML, "<li>x</li>") {
		t.Fatalf("truncated=%v len=%d", res.Truncated, len(res.HTML))
	}
	if strings.Count(res.HTML, "<ul>") != strings.Count(res.HTML, "</ul>") {
		t.Errorf("unbalanced list tags")
	}
	t.Logf("rendered in %s", time.Since(start))
}

// TestR_UI_008_CodeBlockLanguageClass: only language-* survives on code.
func TestR_UI_008_CodeBlockLanguageClass(t *testing.T) {
	res := renderer(t).Render("```js\nlet a = 1 < 2;\n```\n\n<code class=\"evil\">x</code>")
	if !strings.Contains(res.HTML, `class="language-js"`) || strings.Contains(res.HTML, "evil") {
		t.Fatalf("got %q", res.HTML)
	}
	if !strings.Contains(res.HTML, "&lt;") {
		t.Errorf("code is not escaped: %q", res.HTML)
	}
}

// TestR_UI_008_FallbackFitsTheLimit: the escaped-text fallback of a rendering that is
// abandoned is itself below the limit for a rendered document, whatever the source.
func TestR_UI_008_FallbackFitsTheLimit(t *testing.T) {
	for _, src := range []string{strings.Repeat(`"`, 5<<20), strings.Repeat("<&>", 3<<20), strings.Repeat("ä", 4<<20), "short"} {
		got := plain(src)
		if len(got) > MaxHTMLBytes || !strings.HasPrefix(got, "<pre>") || !strings.HasSuffix(got, "</pre>") {
			t.Fatalf("len %d", len(got))
		}
		if strings.ContainsAny(strings.TrimSuffix(strings.TrimPrefix(got, "<pre>"), "</pre>"), "<>\"") {
			t.Fatal("an unescaped character")
		}
	}
}

// TestR_UI_008_TimeLimitFallsBackToEscapedText: a rendering that takes longer than its
// limit is abandoned and the answer is the escaped source, flagged as truncated.
func TestR_UI_008_TimeLimitFallsBackToEscapedText(t *testing.T) {
	r := renderer(t)
	r.timeout = time.Nanosecond
	res := r.Render("# Title\n\n<script>alert(1)</script>\n" + strings.Repeat("- x\n", 100_000))
	if !res.Truncated || len(res.HTML) > MaxHTMLBytes || strings.Contains(res.HTML, "<script") || strings.Contains(res.HTML, "<h1") {
		t.Fatalf("truncated=%v len=%d", res.Truncated, len(res.HTML))
	}
}
