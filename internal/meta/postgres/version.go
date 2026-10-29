package postgres

import (
	"fmt"
	"strconv"
	"strings"
)

// version is a trove release number: major.minor.patch with an optional
// pre-release. The build "dev" (go run, tests) is newer than every release, so
// a development binary passes the compatibility gate.
type version struct {
	major, minor, patch int
	pre                 string
	dev                 bool
}

// parseVersion reads "1.2.3", "v1.2.3", "1.2.3-rc1" and "dev". Build metadata
// after "+" is ignored.
func parseVersion(s string) (version, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "dev" || s == "" {
		return version{dev: true}, nil
	}
	core, _, _ := strings.Cut(s, "+")
	core, pre, _ := strings.Cut(core, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, fmt.Errorf("version %q is not major.minor.patch", s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return version{}, fmt.Errorf("version %q is not major.minor.patch", s)
		}
		n[i] = v
	}
	return version{major: n[0], minor: n[1], patch: n[2], pre: pre}, nil
}

// compare returns -1, 0 or 1. A pre-release sorts below its release.
func (a version) compare(b version) int {
	switch {
	case a.dev && b.dev:
		return 0
	case a.dev:
		return 1
	case b.dev:
		return -1
	}
	for _, d := range [3][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	case a.pre < b.pre:
		return -1
	}
	return 1
}

// zeroVersion is the compat floor of a database without a contract step.
const zeroVersion = "0.0.0"
