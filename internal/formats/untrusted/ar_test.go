package untrusted

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

type arM struct {
	name string
	data string
	size string // overrides the size field
}

func buildAr(ms ...arM) []byte {
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	for _, m := range ms {
		size := m.size
		if size == "" {
			size = fmt.Sprint(len(m.data))
		}
		fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8s%-10s`\n", m.name, 0, 0, 0, "100644", size)
		b.WriteString(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

func readAll(t *testing.T, data []byte) (names []string, bodies []string, err error) {
	t.Helper()
	a, err := NewAr(bytes.NewReader(data), int64(len(data)), Limits{})
	if err != nil {
		return nil, nil, err
	}
	for {
		m, err := a.Next()
		if errors.Is(err, io.EOF) {
			return names, bodies, nil
		}
		r, _ := a.Open()
		body, _ := io.ReadAll(r)
		names, bodies = append(names, m.Name), append(bodies, string(body))
	}
}

func TestArReadsMembersInOrder(t *testing.T) {
	data := buildAr(arM{name: "debian-binary", data: "2.0\n"}, arM{name: "control.tar.gz", data: "abc"}, arM{name: "data.tar.xz", data: "defg"})
	names, bodies, err := readAll(t, data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "debian-binary,control.tar.gz,data.tar.xz" || strings.Join(bodies, "|") != "2.0\n|abc|defg" {
		t.Fatalf("names %v bodies %q", names, bodies)
	}
	// An odd last member without its padding byte is accepted: dpkg-deb writes it.
	short := buildAr(arM{name: "a", data: "xyz"})
	short = short[:len(short)-1]
	if _, bodies, err := readAll(t, short); err != nil || bodies[0] != "xyz" {
		t.Errorf("unpadded last member: %v %v", bodies, err)
	}
}

// The crafted archives of R-DEB-034.
func TestArRefuses(t *testing.T) {
	nine := make([]arM, 9)
	for i := range nine {
		nine[i] = arM{name: fmt.Sprintf("m%d", i), data: "x"}
	}
	ok := buildAr(arM{name: "a", data: "xy"})
	cases := []struct {
		name string
		data []byte
		kind errs.Kind
	}{
		{"empty", nil, errs.Unprocessable},
		{"no magic", []byte("not an archive at all, not at all"), errs.Unprocessable},
		{"only the magic and junk", append([]byte("!<arch>\n"), []byte("short")...), errs.Unprocessable},
		{"nine members", buildAr(nine...), errs.TooLarge},
		{"a long name", buildAr(arM{name: "#1/20", data: "x"}), errs.Unprocessable},
		{"a symbol table", buildAr(arM{name: "/", data: "x"}), errs.Unprocessable},
		{"a size beyond the archive", buildAr(arM{name: "a", data: "x", size: "999"}), errs.Unprocessable},
		{"a size that is no number", buildAr(arM{name: "a", data: "x", size: "12x"}), errs.Unprocessable},
		{"a negative size", buildAr(arM{name: "a", data: "x", size: "-1"}), errs.Unprocessable},
		{"a name with a slash inside", buildAr(arM{name: "a/b", data: "x"}), errs.Unprocessable},
		{"trailing bytes", append(append([]byte(nil), ok...), []byte("tail")...), errs.Unprocessable},
		{"a bad terminator", func() []byte { b := append([]byte(nil), ok...); b[8+58] = 'x'; return b }(), errs.Unprocessable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewAr(bytes.NewReader(c.data), int64(len(c.data)), Limits{})
			if err == nil || errs.KindOf(err) != c.kind {
				t.Fatalf("err = %v (kind %v), want kind %v", err, errs.KindOf(err), c.kind)
			}
		})
	}
}

func FuzzAr(f *testing.F) {
	f.Add(buildAr(arM{name: "debian-binary", data: "2.0\n"}, arM{name: "control.tar.gz", data: "abc"}))
	f.Add([]byte("!<arch>\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		a, err := NewAr(bytes.NewReader(data), int64(len(data)), Limits{})
		if err != nil {
			return
		}
		if a.Len() > arMaxMembers {
			t.Fatalf("%d members", a.Len())
		}
		var total int64
		for {
			m, err := a.Next()
			if err != nil {
				break
			}
			total += m.Size
			r, _ := a.Open()
			n, _ := io.Copy(io.Discard, r)
			if n != m.Size {
				t.Fatalf("member %q: read %d of %d", m.Name, n, m.Size)
			}
		}
		if total > int64(len(data)) {
			t.Fatalf("members add up to %d in %d bytes", total, len(data))
		}
	})
}
