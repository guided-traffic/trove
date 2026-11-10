package ansiblestub

import (
	"fmt"
	"strconv"
	"strings"
)

// semver is a version of Semantic Versioning 2.0.0. The stub compares versions by itself, independently of trove's own
// implementation, so that a defect there does not hide in the order of the stub's lists.
type semver struct {
	major, minor, patch uint64
	pre                 []string
}

func parseSemver(v string) (semver, error) {
	core, _, _ := strings.Cut(v, "+")
	core, pre, hasPre := strings.Cut(core, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("version %q: want major.minor.patch", v)
	}
	var n [3]uint64
	for i, p := range parts {
		x, err := strconv.ParseUint(p, 10, 63)
		if err != nil || (len(p) > 1 && p[0] == '0') {
			return semver{}, fmt.Errorf("version %q: %q is no number", v, p)
		}
		n[i] = x
	}
	s := semver{major: n[0], minor: n[1], patch: n[2]}
	if hasPre {
		if pre == "" {
			return semver{}, fmt.Errorf("version %q: empty pre-release", v)
		}
		s.pre = strings.Split(pre, ".")
	}
	return s, nil
}

// compare orders by precedence: -1, 0 or 1.
func (a semver) compare(b semver) int {
	for _, p := range [][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		xn, xerr := strconv.ParseUint(x, 10, 63)
		yn, yerr := strconv.ParseUint(y, 10, 63)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		case x != y:
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}
	return 0
}

// compareVersions orders two version strings; an invalid string sorts before a valid one.
func compareVersions(a, b string) int {
	sa, ea := parseSemver(a)
	sb, eb := parseSemver(b)
	switch {
	case ea != nil && eb != nil:
		return strings.Compare(a, b)
	case ea != nil:
		return -1
	case eb != nil:
		return 1
	}
	return sa.compare(sb)
}
