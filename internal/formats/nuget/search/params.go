package search

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// Limits are the bounds of the paging parameters, the nuget.search settings.
type Limits struct {
	DefaultTake, MaxTake, MaxSkip int
}

// Params is a search request after the validation of R-NUGET-010.
type Params struct {
	Terms []rows.SearchTerm
	// Skip and Take are the page; Take is already clamped to the largest take.
	Skip, Take int
	// Prerelease says that pre-release versions are visible; SemVer2 that SemVer 2.0.0-specific
	// versions are (R-NUGET-004).
	Prerelease, SemVer2 bool
	// PackageType is the lowercased type a package's latest visible version must declare, ""
	// for no filter. NoMatch is set when the parameter is no package type name: nothing
	// matches then.
	PackageType string
	NoMatch     bool
}

// MaxPackageTypeBytes bounds the packageType parameter.
const MaxPackageTypeBytes = 256

// ParseParams validates the query string of a search request (R-NUGET-010): skip and take
// are non-negative integers, skip is at most MaxSkip, take is clamped to MaxTake and is
// DefaultTake when absent; q is at most [MaxQueryBytes] bytes and holds at most
// [MaxTerms] terms. A parameter that is absent or empty has its default. Other parameters
// are ignored: clients send ones this server does not use. The first value of a repeated
// parameter counts. The errors are 400.
func ParseParams(v url.Values, l Limits) (Params, error) {
	p := Params{Take: l.DefaultTake}
	q := v.Get("q")
	if len(q) > MaxQueryBytes {
		return Params{}, bad("The query q is longer than " + strconv.Itoa(MaxQueryBytes) + " bytes.")
	}
	var more bool
	if p.Terms, more = Parse(q); more {
		return Params{}, bad("The query q holds more than " + strconv.Itoa(MaxTerms) + " terms.")
	}
	if s := v.Get("skip"); s != "" {
		n, ok := atoi(s)
		if !ok || n > l.MaxSkip {
			return Params{}, bad("skip must be an integer from 0 to " + strconv.Itoa(l.MaxSkip) + ".")
		}
		p.Skip = n
	}
	if s := v.Get("take"); s != "" {
		n, ok := atoi(s)
		if !ok {
			return Params{}, bad("take must be a non-negative integer.")
		}
		p.Take = min(n, l.MaxTake)
	}
	p.Prerelease = strings.EqualFold(v.Get("prerelease"), "true")
	p.SemVer2 = semVerLevel2(v.Get("semVerLevel"))
	if t := v.Get("packageType"); t != "" {
		if validTypeName(t) {
			p.PackageType = strings.ToLower(t)
		} else {
			p.NoMatch = true
		}
	}
	return p, nil
}

// atoi reads a non-negative decimal integer written with digits only. A value too large for
// an int comes back as the largest int, which every bound refuses or clamps.
func atoi(s string) (int, bool) {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return int(^uint(0) >> 1), true
	}
	return n, true
}

var semver2 = version.MustParse("2.0.0")

// semVerLevel2 reports whether the semVerLevel parameter asks for SemVer 2.0.0-specific
// versions: it is parsed as a version, and an absent or unparseable value means 1.0.0
// (R-NUGET-004).
func semVerLevel2(s string) bool {
	v, err := version.Parse(s)
	return err == nil && version.Compare(v, semver2) >= 0
}

// validTypeName reports whether a text can be a package type name: not empty, at most
// [MaxPackageTypeBytes] bytes, without white space and control characters.
func validTypeName(s string) bool {
	if s == "" || len(s) > MaxPackageTypeBytes {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func bad(msg string) error { return errs.E(errs.Invalid, errs.CodeInvalid, msg) }
