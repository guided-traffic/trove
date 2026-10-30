package corpus

import (
	"strconv"
	"strings"
)

// parsed is a semantic version split into its parts.
type parsed struct {
	major, minor, patch int
	pre                 []string
}

func parseVersion(v string) (parsed, bool) {
	core, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return parsed{}, false
	}
	var n [3]int
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return parsed{}, false
		}
		n[i] = x
	}
	out := parsed{major: n[0], minor: n[1], patch: n[2]}
	if hasPre {
		out.pre = strings.Split(pre, ".")
	}
	return out, true
}

// CompareVersions orders two semantic versions as SemVer 2.0.0 does: the numbers first, a
// prerelease before its release, prerelease identifiers one by one (numeric below alphanumeric).
// A string that is not a version sorts below every version.
func CompareVersions(a, b string) int {
	pa, okA := parseVersion(a)
	pb, okB := parseVersion(b)
	switch {
	case !okA && !okB:
		return strings.Compare(a, b)
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for _, d := range []int{pa.major - pb.major, pa.minor - pb.minor, pa.patch - pb.patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case len(pa.pre) == 0 && len(pb.pre) == 0:
		return 0
	case len(pa.pre) == 0:
		return 1
	case len(pb.pre) == 0:
		return -1
	}
	for i := 0; i < len(pa.pre) && i < len(pb.pre); i++ {
		if c := compareIdentifier(pa.pre[i], pb.pre[i]); c != 0 {
			return c
		}
	}
	return sign(len(pa.pre) - len(pb.pre))
}

func compareIdentifier(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return sign(na - nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// IsPrerelease reports whether a version carries a prerelease part.
func IsPrerelease(v string) bool {
	p, ok := parseVersion(v)
	return ok && len(p.pre) > 0
}
