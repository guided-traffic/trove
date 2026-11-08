// Package version orders Debian package versions the way dpkg does (deb-version(7), R-DEB-002): the epoch numerically, then the
// upstream version and the revision, each by alternating runs of non-digits (letters before other characters, '~' before everything
// including the end of the string) and digits (numerically). It is a leaf: the format, its data port and the virtual merge share it.
package version

import "strings"

// Version is a version split into its three parts.
type Version struct {
	Epoch    string
	Upstream string
	Revision string
}

// Parse splits s into epoch, upstream version and revision. The epoch is the part before the first colon; the revision is the part after
// the last hyphen. It validates nothing: [Compare] orders any string, and the grammar of R-DEB-002 is the format's.
func Parse(s string) Version {
	var v Version
	if i := strings.IndexByte(s, ':'); i >= 0 {
		v.Epoch, s = s[:i], s[i+1:]
	}
	if i := strings.LastIndexByte(s, '-'); i >= 0 {
		v.Upstream, v.Revision = s[:i], s[i+1:]
	} else {
		v.Upstream = s
	}
	return v
}

// Compare orders two versions: negative when a is older than b, zero when they are equal ("2.10-3" equals "0:2.10-3"), positive when a
// is newer. A string with an epoch that is no number sorts as epoch 0, as dpkg treats it after refusing it; the caller validates first.
func Compare(a, b string) int {
	va, vb := Parse(a), Parse(b)
	if c := cmpInt(va.Epoch, vb.Epoch); c != 0 {
		return c
	}
	if c := verrevcmp(va.Upstream, vb.Upstream); c != 0 {
		return c
	}
	return verrevcmp(va.Revision, vb.Revision)
}

// Less reports whether a is older than b.
func Less(a, b string) bool { return Compare(a, b) < 0 }

// Equal reports whether a and b are the same version.
func Equal(a, b string) bool { return Compare(a, b) == 0 }

// cmpInt compares two epochs as decimal numbers of any length: leading zeros do not count and an empty string is zero.
func cmpInt(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	for _, s := range []*string{&a, &b} {
		for i := 0; i < len(*s); i++ {
			if (*s)[i] < '0' || (*s)[i] > '9' {
				*s = (*s)[:i]
				break
			}
		}
	}
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// order is the weight of a character in a non-digit run: '~' sorts before everything, the end of the string and digits before
// letters, letters before the other characters.
func order(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return 0
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return int(c)
	case c == '~':
		return -1
	case c != 0:
		return int(c) + 256
	}
	return 0
}

// verrevcmp compares an upstream version or a revision as dpkg does.
func verrevcmp(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		first := 0
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			var ac, bc int
			if i < len(a) {
				ac = order(a[i])
			}
			if j < len(b) {
				bc = order(b[j])
			}
			if ac != bc {
				if ac < bc {
					return -1
				}
				return 1
			}
			i++
			j++
		}
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		for i < len(a) && isDigit(a[i]) && j < len(b) && isDigit(b[j]) {
			if first == 0 {
				first = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		if i < len(a) && isDigit(a[i]) {
			return 1
		}
		if j < len(b) && isDigit(b[j]) {
			return -1
		}
		if first != 0 {
			if first < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
