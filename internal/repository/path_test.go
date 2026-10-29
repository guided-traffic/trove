package repository

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestCleanPath(t *testing.T) {
	ok := []struct {
		name, in string
		want     Cleaned
	}{
		{"plain file", "tools/widget.bin", Cleaned{Path: "tools/widget.bin"}},
		{"leading slash is the route's", "/tools/widget.bin", Cleaned{Path: "tools/widget.bin"}},
		{"trailing slash marks a folder", "a/b/", Cleaned{Path: "a/b", Folder: true}},
		{"root", "", Cleaned{Folder: true}},
		{"root slash", "/", Cleaned{Folder: true}},
		{"escapes decode once", "a%20b/c%2Bd", Cleaned{Path: "a b/c+d"}},
		{"an encoded slash becomes a separator", "@acme%2futil/-/util-1.0.tgz", Cleaned{Path: "@acme/util/-/util-1.0.tgz"}},
		{"double encoding stays one literal level", "a/%252e%252e", Cleaned{Path: "a/%2e%2e"}},
		{"case is kept", "Report.pdf", Cleaned{Path: "Report.pdf"}},
		{"dots inside names are names", "a/.hidden/b..c", Cleaned{Path: "a/.hidden/b..c"}},
		{"non-ASCII", "r%C3%A4ume/%E2%82%AC.txt", Cleaned{Path: "räume/€.txt"}},
	}
	for _, c := range ok {
		t.Run("R-GENERIC-002 R-ARCH-005 accepts "+c.name, func(t *testing.T) {
			got, err := CleanPath(c.in)
			if err != nil || got != c.want {
				t.Fatalf("CleanPath(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
			}
		})
	}
	bad := map[string]string{
		"dot segment":             "a/./b",
		"dot-dot segment":         "a/../b",
		"encoded dot-dot":         "a/%2e%2e/x",
		"encoded dot-dot upper":   "a/%2E%2E/x",
		"encoded dot-dot slash":   "a%2f..%2fx",
		"empty segment":           "a//b",
		"leading empty segment":   "//a",
		"encoded leading slash":   "%2fa",
		"NUL":                     "a%00b",
		"encoded backslash":       "a%5Cb",
		"literal backslash":       `a\b`,
		"control character":       "a%0Ab",
		"DEL":                     "a%7Fb",
		"C1 control":              "a%C2%85b",
		"malformed escape":        "a%zzb",
		"truncated escape":        "a%2",
		"invalid UTF-8":           "a%FFb",
		"segment of 256 bytes":    strings.Repeat("a", 256),
		"path of 4097 bytes":      strings.Repeat(strings.Repeat("a", 100)+"/", 41),
		"only a dot":              ".",
		"only dot-dot with slash": "../",
	}
	for name, in := range bad {
		t.Run("R-GENERIC-002 R-ARCH-005 refuses "+name, func(t *testing.T) {
			_, err := CleanPath(in)
			e, isErr := errs.As(err)
			if !isErr || e.Code != errs.CodeInvalidPath || e.Status() != 400 {
				t.Fatalf("CleanPath(%q) error = %v; want 400 invalid_path", in, err)
			}
		})
	}
	t.Run("R-GENERIC-002 limits are inclusive", func(t *testing.T) {
		seg := strings.Repeat("a", MaxSegmentBytes)
		if _, err := CleanPath(seg); err != nil {
			t.Fatalf("255-byte segment: %v", err)
		}
		var parts []string
		for len(strings.Join(parts, "/")) < MaxPathBytes-MaxSegmentBytes-1 {
			parts = append(parts, seg)
		}
		long := strings.Join(parts, "/")
		long += "/" + strings.Repeat("b", MaxPathBytes-len(long)-1)
		if len(long) != MaxPathBytes {
			t.Fatalf("test path is %d bytes", len(long))
		}
		if _, err := CleanPath(long); err != nil {
			t.Fatalf("4096-byte path: %v", err)
		}
		if _, err := CleanPath(long + "c"); err == nil {
			t.Fatal("4097-byte path accepted")
		}
	})
}
