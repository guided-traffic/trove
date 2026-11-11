package docgen

import (
	"bytes"
	"unicode/utf8"
)

// The literal scanner (R-ANS-034, Q-ANS-58): it reads Python source as bytes and finds the module-level assignments of DOCUMENTATION,
// EXAMPLES and RETURN whose value is one string literal. It evaluates nothing and is no Python parser: it knows the strings, the comments, the
// brackets and the line starts, which is what it takes to tell a module-level statement from text inside a string, a call or a function. It
// uses no regular expression and visits every byte once.

// Status of a scanned literal.
type literalStatus uint8

const (
	// litOK is a literal that was read.
	litOK literalStatus = iota + 1
	// litTooLarge is a literal whose decoded text exceeds the bound.
	litTooLarge
	// litUnterminated is a literal that does not close before the end of the source.
	litUnterminated
)

// literal is the value of one assignment.
type literal struct {
	text   string
	status literalStatus
}

// docNames are the three module-level names that are read.
var docNames = [...]string{"DOCUMENTATION", "EXAMPLES", "RETURN"}

func isDocName(b []byte) (string, bool) {
	for _, n := range docNames {
		if string(b) == n {
			return n, true
		}
	}
	return "", false
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
func isIdentByte(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

// scanLiterals returns the last literal assigned to each of the three names. maxText bounds the decoded text of one literal.
func scanLiterals(src []byte, maxText int) map[string]literal {
	out := map[string]literal{}
	n := len(src)
	i, depth, lineStart := 0, 0, true
	for i < n {
		c := src[i]
		if lineStart && depth == 0 {
			lineStart = false
			if isIdentStart(c) {
				j := i
				for j < n && isIdentByte(src[j]) {
					j++
				}
				if name, ok := isDocName(src[i:j]); ok {
					if next, lit, read := assignment(src, j, maxText); read {
						out[name] = lit
						i = next
						continue
					}
				}
			}
		}
		switch c {
		case '\n':
			i++
			if depth == 0 {
				lineStart = true
			}
		case '\\':
			// A backslash joins the next line to this one: no line start follows.
			i++
			if i < n && src[i] == '\r' {
				i++
			}
			if i < n && src[i] == '\n' {
				i++
			}
		case '#':
			for i < n && src[i] != '\n' {
				i++
			}
		case '\'', '"':
			i, _, _ = literalAt(src, i, maxText, true)
		case '(', '[', '{':
			depth++
			i++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
			i++
		default:
			i++
		}
	}
	return out
}

// assignment reads "= <literal>[ <literal>...]" after a name that ends at j, with an optional pair of parentheses around the literals. read
// is false for anything else (a call, an operator, a name, an f-string or bytes literal): the statement is then walked like any other.
func assignment(src []byte, j, maxText int) (next int, lit literal, read bool) {
	n := len(src)
	k := skipBlank(src, j, false)
	if k >= n || src[k] != '=' || k+1 < n && src[k+1] == '=' {
		return 0, literal{}, false
	}
	k = skipBlank(src, k+1, false)
	paren := k < n && src[k] == '('
	if paren {
		k = skipBlank(src, k+1, true)
	}
	var buf bytes.Buffer
	status := litOK
	count := 0
	for {
		end, text, st := literalAt(src, k, maxText-buf.Len(), false)
		if st == 0 {
			break // not a literal
		}
		count++
		switch {
		case st == litUnterminated:
			return end, literal{status: litUnterminated}, true
		case st == litTooLarge || buf.Len()+len(text) > maxText:
			status = litTooLarge
		default:
			buf.WriteString(text)
		}
		k = skipBlank(src, end, paren)
	}
	if count == 0 {
		return 0, literal{}, false
	}
	if paren {
		if k >= n || src[k] != ')' {
			return 0, literal{}, false
		}
		k++
	}
	// The statement must end here: a call, an operator or a format ("".join(...), "a" % b) is a value the scanner does not read.
	rest := skipBlank(src, k, false)
	if rest < n && src[rest] != '\n' && src[rest] != '\r' && src[rest] != '#' && src[rest] != ';' {
		return 0, literal{}, false
	}
	if status != litOK {
		return k, literal{status: status}, true
	}
	return k, literal{text: buf.String(), status: litOK}, true
}

// skipBlank skips spaces and tabs, backslash-newline continuations, and, inside parentheses, newlines and comments.
func skipBlank(src []byte, i int, multiline bool) int {
	n := len(src)
	for i < n {
		switch src[i] {
		case ' ', '\t', '\f':
			i++
		case '\\':
			j := i + 1
			if j < n && src[j] == '\r' {
				j++
			}
			if j < n && src[j] == '\n' {
				i = j + 1
				continue
			}
			return i
		case '\r', '\n':
			if !multiline {
				return i
			}
			i++
		case '#':
			if !multiline {
				return i
			}
			for i < n && src[i] != '\n' {
				i++
			}
		default:
			return i
		}
	}
	return i
}

// literalAt reads a string literal that starts at i, with its prefix. st is 0 when there is none, or when the prefix makes it one that is not
// read (bytes, f-string; the literal is then still skipped, with st litOK and an empty text, when skipOnly is set). The decoded text is cut
// at maxText: a longer literal is still consumed to its end and reported as litTooLarge.
func literalAt(src []byte, i, maxText int, skipOnly bool) (end int, text string, st literalStatus) {
	n := len(src)
	start := i
	raw, bad := false, false
	for i < n && i-start < 2 {
		switch src[i] {
		case 'r', 'R':
			raw = true
		case 'u', 'U':
		case 'b', 'B', 'f', 'F':
			bad = true
		default:
			goto prefixDone
		}
		i++
	}
prefixDone:
	if i >= n || src[i] != '\'' && src[i] != '"' {
		return start, "", 0
	}
	q := src[i]
	triple := i+2 < n && src[i+1] == q && src[i+2] == q
	if triple {
		i += 3
	} else {
		i++
	}
	var buf []byte
	collect := !skipOnly && !bad
	over := false
	emit := func(p ...byte) {
		if !collect || over {
			return
		}
		if len(buf)+len(p) > maxText {
			over = true
			return
		}
		buf = append(buf, p...)
	}
	for i < n {
		c := src[i]
		switch {
		case c == '\\':
			if i+1 >= n {
				return n, "", litUnterminated
			}
			if raw || skipOnly || bad {
				emit('\\', src[i+1])
				i += 2
				continue
			}
			i = escape(src, i, emit)
		case c == q:
			if !triple {
				return finish(i+1, buf, over, bad, skipOnly)
			}
			if i+2 < n && src[i+1] == q && src[i+2] == q {
				return finish(i+3, buf, over, bad, skipOnly)
			}
			emit(c)
			i++
		case c == '\n' && !triple:
			return i, "", litUnterminated
		default:
			emit(c)
			i++
		}
	}
	return n, "", litUnterminated
}

func finish(end int, buf []byte, over, bad, skipOnly bool) (int, string, literalStatus) {
	switch {
	case skipOnly:
		return end, "", litOK
	case bad:
		return end, "", 0
	case over:
		return end, "", litTooLarge
	}
	return end, string(buf), litOK
}

// escape decodes the escape sequence of a non-raw literal that starts at the backslash at i and returns the index after it. An unknown
// escape keeps the backslash and the character, as Python does.
func escape(src []byte, i int, emit func(...byte)) int {
	n := len(src)
	c := src[i+1]
	switch c {
	case '\n':
		return i + 2
	case '\r':
		if i+2 < n && src[i+2] == '\n' {
			return i + 3
		}
		return i + 2
	case '\\', '\'', '"':
		emit(c)
	case 'n':
		emit('\n')
	case 't':
		emit('\t')
	case 'r':
		emit('\r')
	case 'a':
		emit(7)
	case 'b':
		emit(8)
	case 'f':
		emit(12)
	case 'v':
		emit(11)
	case 'x', 'u', 'U':
		digits := map[byte]int{'x': 2, 'u': 4, 'U': 8}[c]
		if v, ok := hexValue(src, i+2, digits); ok {
			if v >= 0 && v <= utf8.MaxRune {
				emit(utf8.AppendRune(nil, v)...)
			}
			return i + 2 + digits
		}
		emit('\\', c)
	case '0', '1', '2', '3', '4', '5', '6', '7':
		v, j := rune(c-'0'), i+2
		for k := 0; k < 2 && j < n && src[j] >= '0' && src[j] <= '7'; k++ {
			v, j = v*8+rune(src[j]-'0'), j+1
		}
		emit(utf8.AppendRune(nil, v)...)
		return j
	default:
		emit('\\', c)
	}
	return i + 2
}

func hexValue(src []byte, i, digits int) (rune, bool) {
	if i+digits > len(src) {
		return 0, false
	}
	var v rune
	for _, c := range src[i : i+digits] {
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, true
}
