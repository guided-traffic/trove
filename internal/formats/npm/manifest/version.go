package manifest

import (
	"regexp"
	"strconv"
	"strings"
)

// maxSafeInteger and maxVersionLength are node-semver's limits.
const (
	maxSafeInteger   = 1<<53 - 1
	maxVersionLength = 256
)

// looseVersion is node-semver's LOOSE expression without the leading
// "[v=\s]*", which CleanVersion strips by hand.
var looseVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)` +
	`(?:-?((?:\d+|\d*[a-zA-Z-][a-zA-Z0-9-]*)(?:\.(?:\d+|\d*[a-zA-Z-][a-zA-Z0-9-]*))*))?` +
	`(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// CleanVersion is semver's clean() with loose parsing: surrounding space and a
// leading "v" or "=" are removed, the build metadata is dropped, and numbers
// are written without leading zeros. It reports false for a string that is not
// a version, which is then left as it is.
func CleanVersion(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "=v")
	s = strings.TrimLeft(s, "v= \t\n\r\f\v")
	if len(s) > maxVersionLength {
		return "", false
	}
	m := looseVersion.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	var parts [3]string
	for i := range parts {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil || n > maxSafeInteger {
			return "", false
		}
		parts[i] = strconv.FormatUint(n, 10)
	}
	out := strings.Join(parts[:], ".")
	if m[4] != "" {
		ids := strings.Split(m[4], ".")
		for i, id := range ids {
			if isDigits(id) {
				if n, err := strconv.ParseUint(id, 10, 64); err == nil && n <= maxSafeInteger {
					ids[i] = strconv.FormatUint(n, 10)
				}
			}
		}
		out += "-" + strings.Join(ids, ".")
	}
	return out, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
