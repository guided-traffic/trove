package version

import (
	"strings"
	"unicode"

	"github.com/guided-traffic/trove/internal/errs"
)

// CodeInvalidRange is the error code of every version range this package refuses.
const CodeInvalidRange = errs.CodeNuGetInvalidVersionRange

// MaxRangeLength bounds the string of a range: two versions and the notation around them.
const MaxRangeLength = 2*MaxLength + 8

// Range is a parsed NuGet version range (the interval notation of dependency versions).
// A nil bound is unbounded; an unbounded side is never inclusive. The zero value is the
// unbounded range, which Parse never returns and which is not a valid dependency range.
type Range struct {
	Min, Max                   *Version
	MinInclusive, MaxInclusive bool
}

// ParseRange parses a dependency version range with NuGet's grammar, without floating
// versions (Q-NUGET-20). White space around the string, the brackets, the comma and the bounds
// is ignored.
//
//   - a bare version is a minimum, inclusive: "2.0" is [2.0.0, );
//   - "[1.0]" is exactly 1.0; no other bracket form takes a single version;
//   - "[1.0,2.0)", "(1.0,2.0]", "(,1.0]", "[1.0,)" and so on: "[" and "]" include the bound,
//     "(" and ")" exclude it, an empty side is unbounded, and a side without a bound is not
//     inclusive whatever bracket is written;
//   - at least one bound is required, the minimum must not be above the maximum, and equal
//     bounds need the same bracket kind on both sides: "[1.0,1.0]" is exactly 1.0, "(1.0,1.0)"
//     is valid and matches nothing, "[1.0,1.0)" is refused.
func ParseRange(s string) (Range, error) {
	if len(s) > MaxRangeLength {
		return Range{}, invalidRange(s, "the range is too long")
	}
	t := strings.TrimFunc(s, unicode.IsSpace)
	if t == "" {
		return Range{}, invalidRange(s, "the range is empty")
	}

	first := t[0]
	if first != '[' && first != '(' {
		v, err := Parse(t)
		if err != nil {
			return Range{}, invalidRange(s, "not a version or an interval")
		}
		return Range{Min: &v, MinInclusive: true}, nil
	}
	last := t[len(t)-1]
	if len(t) < 2 || (last != ']' && last != ')') {
		return Range{}, invalidRange(s, "the interval is not closed by \"]\" or \")\"")
	}
	r := Range{MinInclusive: first == '[', MaxInclusive: last == ']'}

	inner := t[1 : len(t)-1]
	lo, hi, two := strings.Cut(inner, ",")
	if !two {
		// A single version: only [x] is valid and means exactly x.
		if !r.MinInclusive || !r.MaxInclusive {
			return Range{}, invalidRange(s, "a single version needs \"[\" and \"]\"")
		}
		v, err := Parse(inner)
		if err != nil {
			return Range{}, invalidRange(s, "the version is invalid")
		}
		r.Min, r.Max = &v, &v
		return r, nil
	}
	if strings.Contains(hi, ",") {
		return Range{}, invalidRange(s, "an interval has two bounds at most")
	}
	var err error
	if r.Min, err = bound(lo); err != nil {
		return Range{}, invalidRange(s, "the minimum is invalid")
	}
	if r.Max, err = bound(hi); err != nil {
		return Range{}, invalidRange(s, "the maximum is invalid")
	}
	if r.Min == nil {
		r.MinInclusive = false
	}
	if r.Max == nil {
		r.MaxInclusive = false
	}
	switch {
	case r.Min == nil && r.Max == nil:
		return Range{}, invalidRange(s, "the interval has no bound")
	case r.Min != nil && r.Max != nil:
		switch c := Compare(*r.Min, *r.Max); {
		case c > 0:
			return Range{}, invalidRange(s, "the minimum is above the maximum")
		case c == 0 && r.MinInclusive != r.MaxInclusive:
			return Range{}, invalidRange(s, "equal bounds need the same bracket kind on both sides")
		}
	}
	return r, nil
}

// bound parses one side of an interval; an empty side is unbounded (nil).
func bound(s string) (*Version, error) {
	if strings.TrimFunc(s, unicode.IsSpace) == "" {
		return nil, nil
	}
	v, err := Parse(s)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// Satisfies reports whether v lies inside the range. Build metadata does not take part, and a
// pre-release version satisfies a range like any other version (floating ranges, the only
// place where NuGet treats pre-releases differently, are not accepted).
func (r Range) Satisfies(v Version) bool {
	if r.Min != nil {
		c := Compare(v, *r.Min)
		if c < 0 || (c == 0 && !r.MinInclusive) {
			return false
		}
	}
	if r.Max != nil {
		c := Compare(v, *r.Max)
		if c > 0 || (c == 0 && !r.MaxInclusive) {
			return false
		}
	}
	return true
}

// IsSemver2 reports whether one of the bounds is itself SemVer 2.0.0-specific, which makes the
// package that declares the dependency SemVer 2.0.0-specific (R-NUGET-004).
func (r Range) IsSemver2() bool {
	return (r.Min != nil && r.Min.IsSemver2()) || (r.Max != nil && r.Max.IsSemver2())
}

// String returns the normalized interval, as NuGet writes it: "[1.0.0, 2.0.0)", "(, 1.0.0]",
// "[1.0.0, )". Bounds are written in their normalized form without build metadata.
func (r Range) String() string {
	var b strings.Builder
	if r.MinInclusive {
		b.WriteByte('[')
	} else {
		b.WriteByte('(')
	}
	if r.Min != nil {
		b.WriteString(r.Min.Normalized())
	}
	b.WriteString(", ")
	if r.Max != nil {
		b.WriteString(r.Max.Normalized())
	}
	if r.MaxInclusive {
		b.WriteByte(']')
	} else {
		b.WriteByte(')')
	}
	return b.String()
}

func invalidRange(s, reason string) *errs.Error {
	return errs.E(errs.Invalid, CodeInvalidRange, "version range "+quote(s)+": "+reason)
}
