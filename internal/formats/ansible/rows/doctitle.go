package rows

import "strings"

// MaxDocTitleRunes bounds the title of a file in the tree (R-ANS-036).
const MaxDocTitleRunes = 120

// maxHeadingScan is how much of a Markdown source a title reads, so that a title costs no scan of a large file.
const maxHeadingScan = 16 << 10

// DocFileTitle is the title of a file of the tree (R-ANS-036): for a Markdown file the text of its first heading, at most
// [MaxDocTitleRunes] characters, and for any other file, or a file without a heading, its name without the extension.
func DocFileTitle(f DocFile) string {
	if f.Format == DocFormatMarkdown {
		if t := firstHeading(f.Body); t != "" {
			return cutRunes(t, MaxDocTitleRunes)
		}
	}
	name := f.Name
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		name = name[:i]
	}
	return cutRunes(name, MaxDocTitleRunes)
}

// firstHeading returns the text of the first ATX heading of a Markdown source outside a code fence, without the marks of emphasis
// and code.
func firstHeading(body string) string {
	if len(body) > maxHeadingScan {
		body = body[:maxHeadingScan]
	}
	inFence := false
	for body != "" {
		line := body
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			line, body = body[:i], body[i+1:]
		} else {
			body = ""
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || len(line) < 2 || line[0] != '#' {
			continue
		}
		n := 0
		for n < len(line) && line[n] == '#' {
			n++
		}
		if n > 6 || n >= len(line) || line[n] != ' ' {
			continue
		}
		text := strings.TrimRight(strings.TrimSpace(line[n:]), "#")
		var out []rune
		for _, r := range text {
			if r != '`' && r != '*' {
				out = append(out, r)
			}
		}
		if t := strings.TrimSpace(string(out)); t != "" {
			return t
		}
	}
	return ""
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
