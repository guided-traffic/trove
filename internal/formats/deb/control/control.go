// Package control reads and writes deb822 paragraphs, the text format of Debian control files, Packages and Sources indexes and Release
// documents (Debian Policy, chapter 5; R-DEB-007, R-DEB-034). It is a leaf.
//
// trove copies uploaded paragraphs into the indexes it signs, so the syntax is strict (R-DEB-007): a field name matches
// ^[A-Za-z][A-Za-z0-9-]{0,63}$ and appears once, compared without case; a value is UTF-8 without control characters other than a tab;
// a continuation line starts with a space or a tab and is not blank after it (" ." stands for an empty line); a paragraph has at most 512
// fields of at most 1 MiB. Whatever the writer emits is built from parsed fields, never from bytes copied unparsed, so a value cannot
// start a new paragraph or add a field (R-DEB-034, "index injection").
package control

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
)

// The bounds of R-DEB-034.
const (
	MaxFields     = 512
	MaxFieldBytes = 1 << 20
)

var nameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)

// Field is one field of a paragraph. Value holds the first line and, after "\n", each continuation line without its leading space or
// tab; an empty line of a long value is the line ".".
type Field struct {
	Name  string
	Value string
}

// Paragraph is the fields of one paragraph in their order.
type Paragraph struct {
	Fields []Field
}

// Get returns the value of a field, compared without case.
func (p Paragraph) Get(name string) (string, bool) {
	for _, f := range p.Fields {
		if strings.EqualFold(f.Name, name) {
			return f.Value, true
		}
	}
	return "", false
}

// Value returns the value of a field, "" when there is none.
func (p Paragraph) Value(name string) string {
	v, _ := p.Get(name)
	return v
}

// Without returns the paragraph without the named fields (compared without case): the fields trove computes for an index are taken out
// of an uploaded paragraph this way.
func (p Paragraph) Without(names ...string) Paragraph {
	out := Paragraph{Fields: make([]Field, 0, len(p.Fields))}
	for _, f := range p.Fields {
		drop := false
		for _, n := range names {
			if strings.EqualFold(f.Name, n) {
				drop = true
				break
			}
		}
		if !drop {
			out.Fields = append(out.Fields, f)
		}
	}
	return out
}

// With returns the paragraph with the field set: replaced in place when it is there, else appended.
func (p Paragraph) With(name, value string) Paragraph {
	out := Paragraph{Fields: append([]Field(nil), p.Fields...)}
	for i, f := range out.Fields {
		if strings.EqualFold(f.Name, name) {
			out.Fields[i] = Field{Name: f.Name, Value: value}
			return out
		}
	}
	out.Fields = append(out.Fields, Field{Name: name, Value: value})
	return out
}

// Error is a violation of the syntax; it names the field it concerns ("" for the paragraph as a whole).
type Error struct {
	Field  string
	Reason string
}

func (e *Error) Error() string {
	if e.Field == "" {
		return "control: " + e.Reason
	}
	return "control: field " + e.Field + ": " + e.Reason
}

// Refusal converts a violation to the 422 answer of R-DEB-007; a bound crossed is the 413 of R-DEB-034.
func Refusal(err error) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok { //nolint:errorlint // the parser returns *Error itself
		msg := "The control data is not accepted: " + e.Reason + "."
		if e.Field != "" {
			msg = "The control field " + e.Field + " is not accepted: " + e.Reason + "."
		}
		return errs.E(errs.Unprocessable, errs.CodeDebInvalidControl, msg, errs.WithFields(errs.FieldError{Pointer: "/control/" + e.Field, Code: errs.CodeDebInvalidControl, Detail: e.Reason}))
	}
	return err
}

// validValue reports why a value is not accepted: invalid UTF-8 or a control character other than a tab in a line.
func validValue(v string) string {
	if !utf8.ValidString(v) {
		return "the value is not valid UTF-8"
	}
	for _, r := range v {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return fmt.Sprintf("the value holds the control character U+%04X", r)
		}
	}
	return ""
}

// Validate checks the paragraph against the syntax rules and the bounds: what a writer is about to emit.
func (p Paragraph) Validate() error {
	if len(p.Fields) > MaxFields {
		return tooLarge("a paragraph has more than 512 fields")
	}
	seen := make(map[string]bool, len(p.Fields))
	for _, f := range p.Fields {
		if !nameRE.MatchString(f.Name) {
			return &Error{Field: f.Name, Reason: "the field name is not valid"}
		}
		key := strings.ToLower(f.Name)
		if seen[key] {
			return &Error{Field: f.Name, Reason: "the field appears more than once"}
		}
		seen[key] = true
		if len(f.Value) > MaxFieldBytes {
			return tooLarge("the field " + f.Name + " is longer than 1 MiB")
		}
		if why := validValue(f.Value); why != "" {
			return &Error{Field: f.Name, Reason: why}
		}
		if strings.HasPrefix(f.Value, " ") || strings.HasPrefix(f.Value, "\t") {
			return &Error{Field: f.Name, Reason: "the value starts with white space"}
		}
		for i, line := range strings.Split(f.Value, "\n") {
			if i > 0 && strings.TrimSpace(line) == "" && line != "" {
				return &Error{Field: f.Name, Reason: "a continuation line is blank"}
			}
		}
	}
	return nil
}

func tooLarge(detail string) error {
	return errs.E(errs.TooLarge, errs.CodeTooLarge, detail)
}

// Bytes renders the paragraph: "Name: first line", then each continuation line prefixed with one space, an empty line as " .", and a
// final newline. The paragraph is validated first, so the output holds exactly the fields given and no blank line.
func (p Paragraph) Bytes() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	for _, f := range p.Fields {
		lines := strings.Split(f.Value, "\n")
		b.WriteString(f.Name)
		b.WriteString(":")
		if lines[0] != "" {
			b.WriteString(" ")
			b.WriteString(lines[0])
		}
		b.WriteString("\n")
		for _, l := range lines[1:] {
			b.WriteString(" ")
			if l == "" {
				l = "."
			}
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	return b.Bytes(), nil
}

// String renders the paragraph and panics on a paragraph that does not validate; it is for tests.
func (p Paragraph) String() string {
	b, err := p.Bytes()
	if err != nil {
		panic(err)
	}
	return string(b)
}
