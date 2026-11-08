package control

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

const hello = "Package: hello\nVersion: 2.10-3\nArchitecture: amd64\nMaintainer: A B <a@example.org>\nDescription: the greeter\n A longer text.\n .\n Second paragraph of the text.\nHomepage: https://example.org/\n"

func TestParseRoundTrip(t *testing.T) {
	p, err := Parse([]byte(hello))
	if err != nil {
		t.Fatal(err)
	}
	if p.Value("package") != "hello" || p.Value("Description") != "the greeter\nA longer text.\n.\nSecond paragraph of the text." {
		t.Fatalf("fields = %+v", p.Fields)
	}
	if got := p.String(); got != hello {
		t.Fatalf("round trip:\n%s\nwant:\n%s", got, hello)
	}
	if names := fieldNames(p); strings.Join(names, ",") != "Package,Version,Architecture,Maintainer,Description,Homepage" {
		t.Errorf("order = %v", names)
	}
}

func fieldNames(p Paragraph) []string {
	var out []string
	for _, f := range p.Fields {
		out = append(out, f.Name)
	}
	return out
}

// The violations of R-DEB-007 each name their field.
func TestParseRefuses(t *testing.T) {
	cases := []struct{ name, in, field string }{
		{"two paragraphs", hello + "\nPackage: other\n", ""},
		{"a blank continuation line", "Package: a1\nDescription: x\n \nVersion: 1\n", "Description"},
		{"a tab-only continuation line", "Package: a1\nDescription: x\n\t\n", "Description"},
		{"a field twice", "Package: hello\nPackage: again\n", "Package"},
		{"a field twice with another case", "Package: hello\npackage: again\n", "package"},
		{"a field name with a space", "Pack age: hello\n", "Pack age"},
		{"a field name that starts with a digit", "1Package: hello\n", "1Package"},
		{"a line that is no field", "Package hello\n", ""},
		{"a continuation before any field", " x\nPackage: a\n", ""},
		{"a control character", "Package: hel\x01lo\n", "Package"},
		{"a carriage return", "Package: hello\r\n", "Package"},
		{"invalid UTF-8", "Package: hel\xfflo\n", "Package"},
		{"a leading blank line", "\nPackage: hello\n", ""},
		{"a comment", "# comment\nPackage: hello\n", ""},
		{"nothing", "", ""},
		{"only blank lines", "\n\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.in))
			var ce *Error
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want a violation", err)
			}
			if ce.Field != c.field {
				t.Errorf("field = %q, want %q", ce.Field, c.field)
			}
			if r := Refusal(err); errs.KindOf(r) != errs.Unprocessable {
				t.Errorf("refusal = %v", r)
			}
		})
	}
}

func TestParseAcceptsAFilenameThatIsNoPath(t *testing.T) {
	// A declared Filename is data: the index lists the canonical one (R-DEB-007).
	p, err := Parse([]byte("Package: hello\nFilename: ../../etc/passwd\n"))
	if err != nil || p.Value("Filename") != "../../etc/passwd" {
		t.Fatalf("%v %v", p, err)
	}
	if n := fieldNames(p.Without("filename", "SIZE")); strings.Join(n, ",") != "Package" {
		t.Errorf("Without = %v", n)
	}
}

func TestBounds(t *testing.T) {
	var many strings.Builder
	for i := 0; i < MaxFields+1; i++ {
		many.WriteString("F")
		many.WriteString(strings.Repeat("a", 3))
		many.WriteString(strings.Repeat("-", 0))
		many.WriteString(itoa(i))
		many.WriteString(": x\n")
	}
	if _, err := Parse([]byte(many.String())); !tooLargeErr(err) {
		t.Errorf("513 fields: %v", err)
	}
	long := "Description: " + strings.Repeat("a", MaxFieldBytes+1) + "\n"
	if _, err := Parse([]byte(long)); !tooLargeErr(err) {
		t.Errorf("a field above 1 MiB: %v", err)
	}
	cont := "Description: x\n" + strings.Repeat(" "+strings.Repeat("b", 1000)+"\n", 1100)
	if _, err := Parse([]byte(cont)); !tooLargeErr(err) {
		t.Errorf("continuation lines above 1 MiB: %v", err)
	}
	huge := "Description: " + strings.Repeat("a", maxLine+10)
	if _, err := Parse([]byte(huge)); !tooLargeErr(err) {
		t.Errorf("a line above the bound: %v", err)
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{digits[i%10]}, b...)
	}
	return string(b)
}

func tooLargeErr(err error) bool { return errs.KindOf(err) == errs.TooLarge }

func TestReaderStreamsParagraphs(t *testing.T) {
	doc := "# generated\nPackage: a\nVersion: 1\n\n\n\nPackage: b\nDescription: x\n y\n\nPackage: c\n"
	r := NewReader(strings.NewReader(doc), true)
	var got []string
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, p.Value("Package"))
	}
	if strings.Join(got, "") != "abc" {
		t.Fatalf("paragraphs = %v", got)
	}
	// Strict mode refuses the comment.
	if _, err := NewReader(strings.NewReader(doc), false).Next(); err == nil {
		t.Error("a comment passed in strict mode")
	}
}

// Index injection (R-DEB-034): a value cannot start a paragraph or add a field when it is written.
func TestWriterCannotInject(t *testing.T) {
	p := Paragraph{Fields: []Field{{Name: "Package", Value: "x"}, {Name: "Description", Value: "one\n\nPackage: evil\nFilename: pool/x"}}}
	out, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatalf("the output does not parse as one paragraph: %v\n%s", err, out)
	}
	if len(back.Fields) != 2 || back.Value("Package") != "x" {
		t.Fatalf("injected fields: %+v", back.Fields)
	}
	for _, bad := range []Paragraph{
		{Fields: []Field{{Name: "Bad Name", Value: "x"}}},
		{Fields: []Field{{Name: "A", Value: "x"}, {Name: "a", Value: "y"}}},
		{Fields: []Field{{Name: "A", Value: "x\x00y"}}},
		{Fields: []Field{{Name: "A", Value: "x\n  \ny"}}},
		{Fields: []Field{{Name: "A", Value: " leading"}}},
	} {
		if _, err := bad.Bytes(); err == nil {
			t.Errorf("%+v was written", bad.Fields)
		}
	}
}

func TestWith(t *testing.T) {
	p := Paragraph{Fields: []Field{{Name: "Package", Value: "a"}}}.With("package", "b").With("Size", "10")
	if p.Value("Package") != "b" || p.Value("Size") != "10" || len(p.Fields) != 2 {
		t.Fatalf("%+v", p.Fields)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(hello))
	f.Add([]byte("Package: a\n b\n .\n c\n"))
	f.Add([]byte("A:\n\n\nB: x\n"))
	f.Add([]byte("#x\nA: b\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Parse(data)
		if err != nil {
			return
		}
		// Whatever parses writes back to something that parses to the same fields.
		out, err := p.Bytes()
		if err != nil {
			t.Fatalf("parsed paragraph does not write: %v", err)
		}
		q, err := Parse(out)
		if err != nil {
			t.Fatalf("written paragraph does not parse: %v\n%q", err, out)
		}
		if len(q.Fields) != len(p.Fields) {
			t.Fatalf("fields %d -> %d", len(p.Fields), len(q.Fields))
		}
		for i := range p.Fields {
			if p.Fields[i] != q.Fields[i] {
				t.Fatalf("field %d: %+v -> %+v", i, p.Fields[i], q.Fields[i])
			}
		}
	})
}
