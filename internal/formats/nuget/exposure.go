package nuget

import (
	"fmt"
	"sync"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// NuGet on an endpoint with exposure rules (R-NUGET-034, R-EP-018). The adapter is the
// version syntax the decision point compiles rules with (catalog.Versioning: ValidateRange,
// Prerelease, InRange) and the dimensions the rules may narrow: packages and versions.
// NuGet has no tags. formats.Register puts both into the catalog, and authz reads them there.
//
// The filter that removes what an endpoint does not expose from a rendered document is the
// work of the packages that render the documents (R-NUGET-034); until they exist no route
// returns a document that lists versions.

// maxRanges bounds the parsed ranges InRange keeps. A range is typed by an administrator and
// there are at most 1,000 rules per endpoint; the bound only keeps a replica's memory
// independent of how often rules were edited.
const maxRanges = 4096

// exposure is the NuGet ExposureAdapter. It keeps the ranges it parsed: the decision point
// asks InRange for every version and rule, and a range is parsed once instead of on every
// call. The memo is a field of the Format, never a package variable (R-ARCH-032).
type exposure struct {
	mu     sync.RWMutex
	ranges map[string]*version.Range
}

var _ formats.ExposureAdapter = (*exposure)(nil)

func newExposure() *exposure { return &exposure{ranges: map[string]*version.Range{}} }

// ValidateRange checks the range of a rule with NuGet's range grammar, without floating
// versions (R-NUGET-003, Q-NUGET-20): "[1.0,2.0)", "(,1.0]", a bare version as a minimum.
func (*exposure) ValidateRange(expr string) error {
	if _, err := version.ParseRange(expr); err != nil {
		return fmt.Errorf("%q is not a NuGet version range: an interval like [1.0,2.0) or a minimum version, without floating versions, at most %d bytes", expr, version.MaxRangeLength)
	}
	return nil
}

// Prerelease reports whether a version has a pre-release label. ok is false for a text that
// is no version, which a rule without prereleases therefore never exposes.
func (*exposure) Prerelease(v string) (pre, ok bool) {
	p, err := version.Parse(v)
	if err != nil {
		return false, false
	}
	return p.IsPrerelease(), true
}

// InRange reports whether a version lies in a validated range. A range that does not parse
// admits nothing, and so does a text that is no version.
func (x *exposure) InRange(v, expr string) bool {
	r := x.rangeOf(expr)
	if r == nil {
		return false
	}
	p, err := version.Parse(v)
	return err == nil && r.Satisfies(p)
}

// Dimensions: NuGet rules narrow packages and versions.
func (*exposure) Dimensions() formats.ExposureDimensions {
	return formats.ExposureDimensions{Packages: true, Versions: true}
}

// rangeOf returns the parsed range of a rule's text, nil when it does not parse.
func (x *exposure) rangeOf(expr string) *version.Range {
	x.mu.RLock()
	r, ok := x.ranges[expr]
	x.mu.RUnlock()
	if ok {
		return r
	}
	parsed, err := version.ParseRange(expr)
	if err == nil {
		r = &parsed
	}
	x.mu.Lock()
	if len(x.ranges) >= maxRanges {
		clear(x.ranges)
	}
	x.ranges[expr] = r
	x.mu.Unlock()
	return r
}
