package nupkg

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxNameBytes bounds one entry name.
const MaxNameBytes = 4096

// normalize applies the rules of R-NUGET-005 step 2 to one central-directory
// name and returns the two spellings the rules are applied to: name, with
// backslashes read as slashes, and path, the same with percent escapes decoded
// once. A NuGet client resolves package parts by their unescaped names, so both
// spellings have to stay inside the package and name the same place. dir is true
// for a directory entry.
func normalize(raw string) (name, path string, dir bool, err error) {
	switch {
	case raw == "":
		return "", "", false, invalidName("an entry name is empty")
	case len(raw) > MaxNameBytes:
		return "", "", false, invalidName(fmt.Sprintf("an entry name is longer than %d bytes", MaxNameBytes))
	case !utf8.ValidString(raw):
		return "", "", false, invalidName(fmt.Sprintf("entry name %q is not valid UTF-8", shorten(raw)))
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", "", false, invalidName(fmt.Sprintf("entry name %q holds a control character or NUL", shorten(raw)))
		}
	}
	name = strings.ReplaceAll(raw, `\`, "/")
	path, ok := unescape(name)
	if !ok {
		return "", "", false, invalidName(fmt.Sprintf("entry name %q holds an escape that decodes to an invalid name", shorten(raw)))
	}
	if strings.Count(path, "/") != strings.Count(name, "/") || strings.Contains(path, `\`) {
		return "", "", false, invalidName(fmt.Sprintf("entry name %q hides a path separator in an escape", shorten(raw)))
	}
	for _, form := range []string{name, path} {
		if err := checkPath(raw, form); err != nil {
			return "", "", false, err
		}
	}
	return name, path, strings.HasSuffix(name, "/"), nil
}

// checkPath refuses a form that is absolute, names a drive, or holds an empty,
// dot or double-dot segment (also with trailing dots or spaces, which Windows
// ignores).
func checkPath(raw, form string) error {
	if strings.HasPrefix(form, "/") {
		return invalidName(fmt.Sprintf("entry name %q is absolute", shorten(raw)))
	}
	if len(form) >= 2 && form[1] == ':' && (form[0]|0x20 >= 'a' && form[0]|0x20 <= 'z') {
		return invalidName(fmt.Sprintf("entry name %q starts with a drive letter", shorten(raw)))
	}
	for seg := range strings.SplitSeq(strings.TrimSuffix(form, "/"), "/") {
		if strings.Trim(seg, ". ") == "" {
			return invalidName(fmt.Sprintf("entry name %q has an empty, dot or double-dot segment", shorten(raw)))
		}
	}
	return nil
}

// unescape decodes each valid %XX once. An invalid escape stays as it is. The
// result must be valid UTF-8 without control characters, or ok is false.
func unescape(s string) (string, bool) {
	if !strings.Contains(s, "%") {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if h, l := unhex(s[i+1]), unhex(s[i+2]); h < 16 && l < 16 {
				b.WriteByte(h<<4 | l)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	out := b.String()
	if !utf8.ValidString(out) {
		return "", false
	}
	for _, r := range out {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return out, true
}

// unhex returns the value of a hex digit, or 255 for any other byte.
func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 255
}

// foldKey is the key two names are compared by for the duplicate rule:
// case-insensitive, and stable across the case mappings that differ between
// upper and lower casing.
func foldKey(path string) string {
	return strings.ToLower(strings.ToUpper(strings.TrimSuffix(path, "/")))
}

func shorten(s string) string {
	if len(s) > 80 {
		s = s[:80] + "..."
	}
	return s
}
