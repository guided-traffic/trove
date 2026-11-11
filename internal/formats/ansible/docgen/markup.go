package docgen

import (
	"html"
	"strings"
	"unicode/utf8"
)

// The converter of the Ansible markup of a documentation string to HTML (R-ANS-039, Q-ANS-59). Every character of the text is escaped before it
// is written; the macros only decide which fixed tags surround the escaped text. The caller still runs the result through the Host's
// sanitizer, so the converter is not the one weak point.

// MaxMarkupInput is the longest text one conversion reads; more is cut (R-ANS-039).
const MaxMarkupInput = 64 << 10

// maxMarkupDepth is how deeply emphasis macros nest before the rest is text.
const maxMarkupDepth = 4

// linkRel is the rel of every link the converter makes.
const linkRel = "nofollow noopener noreferrer"

// RenderMarkup converts the Ansible markup of a documentation string to HTML: I(x) and E(x) emphasis, B(x) strong, C(x), O(x), V(x), RV(x),
// M(x), P(x) and R(text,ref) code, U(url) and L(text,url) a link when the URL is http or https and the text otherwise, HORIZONTALLINE a rule,
// and the backslash escapes of the markup. A macro that does not close, or nests deeper than 4, is text. The input is cut at
// [MaxMarkupInput] bytes. The caller sanitizes the result with the policy of R-UI-008.
func RenderMarkup(text string) string {
	var b strings.Builder
	r := renderer{out: &b}
	r.run(cut(text), 0)
	return b.String()
}

// PlainText is the text of a documentation string without its markup: the content of every macro, no tags. It builds the search text of an
// entry.
func PlainText(text string) string {
	var b strings.Builder
	r := renderer{out: &b, plain: true}
	r.run(cut(text), 0)
	return b.String()
}

func cut(s string) string {
	if len(s) <= MaxMarkupInput {
		return s
	}
	s = s[:MaxMarkupInput]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

type renderer struct {
	out   *strings.Builder
	plain bool
}

// macroTag returns the tag of a macro ("" for the macros that write a link or text only) and whether its argument is literal text.
func macroTag(name string) (tag string, code, ok bool) {
	switch name {
	case "I", "E":
		return "em", false, true
	case "B":
		return "strong", false, true
	case "C", "O", "V", "RV", "M", "P", "R":
		return "code", true, true
	case "U", "L":
		return "", true, true
	}
	return "", false, false
}

func (r *renderer) text(s string) {
	if r.plain {
		r.out.WriteString(s)
		return
	}
	r.out.WriteString(html.EscapeString(s))
}

// run writes s, finding the macros in it. depth is the nesting of emphasis macros.
func (r *renderer) run(s string, depth int) {
	i, last := 0, 0
	flush := func(to int) {
		if to > last {
			r.text(unescapeMarkup(s[last:to]))
		}
	}
	for i < len(s) {
		c := s[i]
		if c == '\\' {
			i += 2
			continue
		}
		if !isUpper(c) || i > 0 && isWordByte(s[i-1]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isUpper(s[j]) {
			j++
		}
		name := s[i:j]
		if name == "HORIZONTALLINE" && (j >= len(s) || !isWordByte(s[j])) {
			flush(i)
			if r.plain {
				r.out.WriteByte(' ')
			} else {
				r.out.WriteString("<hr>")
			}
			i, last = j, j
			continue
		}
		tag, code, ok := macroTag(name)
		if !ok || j >= len(s) || s[j] != '(' {
			i = j
			continue
		}
		arg, end, closed := macroArgument(s, j+1, !code || name == "U" || name == "L" || name == "R")
		if !closed || !code && depth >= maxMarkupDepth {
			i = j
			continue
		}
		flush(i)
		r.macro(name, tag, code, arg, depth)
		i, last = end, end
	}
	flush(len(s))
}

// macroArgument returns the text between the parenthesis that opens at start-1 and the first closing one that is not escaped, and the
// index after it. With nest the parentheses inside count, so an emphasis macro can hold another macro and a URL can hold parentheses.
func macroArgument(s string, start int, nest bool) (arg string, end int, closed bool) {
	level := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '(':
			if nest {
				level++
			}
		case ')':
			if level == 0 {
				return s[start:i], i + 1, true
			}
			level--
		}
	}
	return "", 0, false
}

func (r *renderer) macro(name, tag string, code bool, arg string, depth int) {
	switch name {
	case "R", "L":
		text, ref := splitArgument(arg)
		if name == "R" {
			r.wrap("code", unescapeMarkup(text))
			return
		}
		url := strings.TrimSpace(unescapeMarkup(ref))
		if r.plain || !httpURL(url) {
			r.text(unescapeMarkup(text))
			return
		}
		r.out.WriteString(`<a href="` + html.EscapeString(url) + `" rel="` + linkRel + `">`)
		r.text(unescapeMarkup(text))
		r.out.WriteString("</a>")
	case "U":
		url := strings.TrimSpace(unescapeMarkup(arg))
		if r.plain || !httpURL(url) {
			r.text(url)
			return
		}
		r.out.WriteString(`<a href="` + html.EscapeString(url) + `" rel="` + linkRel + `">`)
		r.text(url)
		r.out.WriteString("</a>")
	default:
		if code {
			r.wrap(tag, unescapeMarkup(arg))
			return
		}
		if !r.plain {
			r.out.WriteString("<" + tag + ">")
		}
		r.run(arg, depth+1)
		if !r.plain {
			r.out.WriteString("</" + tag + ">")
		}
	}
}

func (r *renderer) wrap(tag, text string) {
	if r.plain {
		r.text(text)
		return
	}
	r.out.WriteString("<" + tag + ">")
	r.text(text)
	r.out.WriteString("</" + tag + ">")
}

// splitArgument splits "text,ref" at the first comma that is not escaped.
func splitArgument(arg string) (text, ref string) {
	for i := 0; i < len(arg); i++ {
		switch arg[i] {
		case '\\':
			i++
		case ',':
			return arg[:i], arg[i+1:]
		}
	}
	return arg, ""
}

// unescapeMarkup removes the backslash of the escapes of the markup: \\, \(, \), \, and a backslash before any other character stays.
func unescapeMarkup(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte(`\(),`, s[i+1]) >= 0 {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// httpURL reports whether u is an absolute http or https URL without white space or control characters.
func httpURL(u string) bool {
	l := strings.ToLower(u)
	if !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") || len(u) <= len("http://") {
		return false
	}
	for _, c := range u {
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }
func isWordByte(c byte) bool {
	return isUpper(c) || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'
}
