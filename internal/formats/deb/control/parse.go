package control

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// maxLine bounds one physical line: a field of at most 1 MiB, its name and a little more.
const maxLine = MaxFieldBytes + 256

// Reader reads the paragraphs of a deb822 document one after the other: a Packages or Sources index, a Release. It applies the syntax
// of the package comment to every paragraph and holds the bounds of R-DEB-034 per paragraph; the number of paragraphs is the caller's
// to bound (1,000,000 for an index, R-DEB-023). Blank lines between paragraphs are skipped, and a line that starts with '#' is a comment
// when comments are allowed.
type Reader struct {
	br       *bufio.Reader
	comments bool
}

// NewReader returns a reader of paragraphs. comments lets lines starting with '#' pass, as dpkg does; the strict reading of an upload
// refuses them.
func NewReader(r io.Reader, comments bool) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 64<<10), comments: comments}
}

// line returns the next physical line without its newline; io.EOF at the end of the input (a last line without a newline is returned
// first). A line longer than the bound is the 413 of R-DEB-034.
func (r *Reader) line() (string, error) {
	var buf []byte
	for {
		part, err := r.br.ReadSlice('\n')
		buf = append(buf, part...)
		if len(buf) > maxLine {
			return "", tooLarge("a line of a control document is longer than 1 MiB")
		}
		switch {
		case err == nil:
			return strings.TrimSuffix(string(buf), "\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(buf) == 0 {
				return "", io.EOF
			}
			return string(buf), nil
		default:
			return "", err
		}
	}
}

// Next returns the next paragraph, io.EOF after the last one. Anything else is a *Error or an error of the input.
func (r *Reader) Next() (Paragraph, error) {
	p, err := r.next()
	if err != nil {
		return Paragraph{}, err
	}
	for _, f := range p.Fields {
		if why := validValue(f.Value); why != "" {
			return Paragraph{}, &Error{Field: f.Name, Reason: why}
		}
	}
	return p, nil
}

func (r *Reader) next() (Paragraph, error) {
	var (
		p    Paragraph
		cur  = -1
		size int
		seen = map[string]bool{}
	)
	for {
		l, err := r.line()
		if errors.Is(err, io.EOF) {
			if len(p.Fields) == 0 {
				return Paragraph{}, io.EOF
			}
			return p, nil
		}
		if err != nil {
			return Paragraph{}, err
		}
		if l == "" {
			if len(p.Fields) == 0 {
				continue // blank lines between paragraphs
			}
			return p, nil
		}
		if l[0] == '#' && r.comments {
			continue
		}
		if l[0] == ' ' || l[0] == '\t' {
			if cur < 0 {
				return Paragraph{}, &Error{Reason: "a continuation line has no field before it"}
			}
			rest := l[1:]
			if strings.TrimSpace(rest) == "" {
				return Paragraph{}, &Error{Field: p.Fields[cur].Name, Reason: "a continuation line is blank (an empty line of a long value is a single dot)"}
			}
			if size += len(rest) + 1; size > MaxFieldBytes {
				return Paragraph{}, tooLarge("the field " + p.Fields[cur].Name + " is longer than 1 MiB")
			}
			p.Fields[cur].Value += "\n" + strings.TrimRight(rest, " \t")
			continue
		}
		name, value, ok := strings.Cut(l, ":")
		if !ok {
			return Paragraph{}, &Error{Reason: "a line is neither a field nor a continuation line"}
		}
		if !nameRE.MatchString(name) {
			return Paragraph{}, &Error{Field: shorten(name), Reason: "the field name is not valid"}
		}
		if seen[strings.ToLower(name)] {
			return Paragraph{}, &Error{Field: name, Reason: "the field appears more than once"}
		}
		seen[strings.ToLower(name)] = true
		if len(p.Fields) >= MaxFields {
			return Paragraph{}, tooLarge("a paragraph has more than 512 fields")
		}
		value = strings.TrimRight(strings.TrimLeft(value, " \t"), " \t")
		p.Fields = append(p.Fields, Field{Name: name, Value: value})
		cur, size = len(p.Fields)-1, len(value)
		if size > MaxFieldBytes {
			return Paragraph{}, tooLarge("the field " + name + " is longer than 1 MiB")
		}
	}
}

func shorten(s string) string {
	if len(s) > 32 {
		return s[:32] + "..."
	}
	return s
}

// Parse reads the one paragraph of a control file strictly (R-DEB-007): exactly one paragraph, no comments, valid values. Trailing blank
// lines are allowed; a second paragraph is a violation.
func Parse(data []byte) (Paragraph, error) {
	if bytes.HasPrefix(data, []byte("\n")) {
		return Paragraph{}, &Error{Reason: "the control data starts with a blank line"}
	}
	r := NewReader(bytes.NewReader(data), false)
	p, err := r.Next()
	if errors.Is(err, io.EOF) {
		return Paragraph{}, &Error{Reason: "the control data holds no paragraph"}
	}
	if err != nil {
		return Paragraph{}, err
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		if err != nil {
			return Paragraph{}, err
		}
		return Paragraph{}, &Error{Reason: "the control data holds more than one paragraph"}
	}
	return p, nil
}
