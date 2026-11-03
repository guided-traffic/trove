package match

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// dotted is a test adapter: versions are dotted numbers ("1.2.3"), compared field by field.
type dotted struct{}

func (dotted) Ecosystem() string                    { return "test" }
func (dotted) AdvisoryName(n string) (string, bool) { return strings.ToLower(n), n != "" }
func (dotted) AdvisoryVersion(v string) (string, bool) {
	if _, err := parse(v); err != nil {
		return "", false
	}
	return v, true
}

func parse(v string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(v, ".") {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("not a version: %q", v)
		}
		out = append(out, n)
	}
	return out, nil
}

func (dotted) CompareVersions(a, b string) (int, error) {
	x, err := parse(a)
	if err != nil {
		return 0, err
	}
	y, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := range max(len(x), len(y)) {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return map[bool]int{true: -1, false: 1}[p < q], nil
		}
	}
	return 0, nil
}

var _ Adapter = dotted{}

func ev(kind advisory.EventKind, v string) advisory.Event {
	return advisory.Event{Kind: kind, Version: v}
}

func rng(events ...advisory.Event) advisory.Range {
	return advisory.Range{Type: advisory.RangeEcosystem, Events: events}
}

// R-VULN-009: the OSV schema's evaluation over the format's order.
func TestR_VULN_009_Affects(t *testing.T) {
	introducedFixed := rng(ev("introduced", "1.0.0"), ev("fixed", "1.2.0"))
	cases := []struct {
		name    string
		e       advisory.Affected
		version string
		want    Result
	}{
		{"before introduced", advisory.Affected{Ranges: []advisory.Range{introducedFixed}}, "0.9.9", Result{}},
		{"at introduced", advisory.Affected{Ranges: []advisory.Range{introducedFixed}}, "1.0.0", Result{Affected: true, FixAvailable: true}},
		{"inside", advisory.Affected{Ranges: []advisory.Range{introducedFixed}}, "1.1.5", Result{Affected: true, FixAvailable: true}},
		{"at fixed", advisory.Affected{Ranges: []advisory.Range{introducedFixed}}, "1.2.0", Result{}},
		{"after fixed", advisory.Affected{Ranges: []advisory.Range{introducedFixed}}, "2.0.0", Result{}},
		{"zero introduced, last_affected: at the bound", advisory.Affected{Ranges: []advisory.Range{rng(ev("introduced", "0"), ev("last_affected", "2.0.0"))}}, "2.0.0", Result{Affected: true}},
		{"zero introduced, last_affected: above", advisory.Affected{Ranges: []advisory.Range{rng(ev("introduced", "0"), ev("last_affected", "2.0.0"))}}, "2.0.1", Result{}},
		{"introduced alone: open end", advisory.Affected{Ranges: []advisory.Range{rng(ev("introduced", "1.0.0"))}}, "9.9.9", Result{Affected: true}},
		{"limit bounds the range", advisory.Affected{Ranges: []advisory.Range{rng(ev("introduced", "1.0.0"), ev("limit", "3.0.0"))}}, "3.0.0", Result{}},
		{"limit below it", advisory.Affected{Ranges: []advisory.Range{rng(ev("introduced", "1.0.0"), ev("limit", "3.0.0"))}}, "2.9.0", Result{Affected: true}},
		{"two intervals in one range: the second", advisory.Affected{Ranges: []advisory.Range{
			rng(ev("introduced", "1.0.0"), ev("fixed", "1.1.0"), ev("introduced", "2.0.0"), ev("fixed", "2.1.0")),
		}}, "2.0.5", Result{Affected: true, FixAvailable: true}},
		{"two intervals in one range: the gap", advisory.Affected{Ranges: []advisory.Range{
			rng(ev("introduced", "1.0.0"), ev("fixed", "1.1.0"), ev("introduced", "2.0.0"), ev("fixed", "2.1.0")),
		}}, "1.5.0", Result{}},
		{"events out of order are sorted", advisory.Affected{Ranges: []advisory.Range{rng(ev("fixed", "1.2.0"), ev("introduced", "1.0.0"))}}, "1.1.0", Result{Affected: true, FixAvailable: true}},
		{"introduced and fixed at one version: empty", advisory.Affected{Ranges: []advisory.Range{rng(ev("introduced", "1.0.0"), ev("fixed", "1.0.0"))}}, "1.0.0", Result{}},
		{"two ranges in one entry", advisory.Affected{Ranges: []advisory.Range{introducedFixed, rng(ev("introduced", "3.0.0"))}}, "3.1.0", Result{Affected: true}},
		{"versions[] lists it", advisory.Affected{Versions: []string{"5.0.0", "5.0.1"}}, "5.0.1", Result{Affected: true}},
		{"versions[] does not list it", advisory.Affected{Versions: []string{"5.0.0"}}, "5.0.1", Result{}},
		{"every version matches without parsing", advisory.Affected{EveryVersion: true}, "not-a-version", Result{Affected: true}},
		{"a GIT range is ignored", advisory.Affected{Ranges: []advisory.Range{{Type: "GIT", Events: []advisory.Event{ev("introduced", "0")}}}}, "1.0.0", Result{}},
		{"a bad event version: unevaluable, matches nothing", advisory.Affected{Ranges: []advisory.Range{rng(ev("introduced", "0"), ev("fixed", "not-a-version"))}}, "1.0.0", Result{Unevaluable: 1}},
		{"a bad range does not hide versions[]", advisory.Affected{
			Ranges: []advisory.Range{rng(ev("introduced", "0"), ev("fixed", "not-a-version"))}, Versions: []string{"1.0.0"},
		}, "1.0.0", Result{Affected: true, Unevaluable: 1}},
		{"a bad range does not hide a good one", advisory.Affected{
			Ranges: []advisory.Range{rng(ev("introduced", "0"), ev("fixed", "x")), introducedFixed},
		}, "1.1.0", Result{Affected: true, FixAvailable: true, Unevaluable: 1}},
		{"a stored version that does not parse matches ranges never", advisory.Affected{Ranges: []advisory.Range{rng(ev("introduced", "0"))}}, "latest", Result{}},
		{"an unevaluable range is counted for a stored version that does not parse", advisory.Affected{Ranges: []advisory.Range{rng(ev("introduced", "0"), ev("fixed", "x"))}}, "latest", Result{Unevaluable: 1}},
		{"last_affected then a new interval", advisory.Affected{Ranges: []advisory.Range{
			rng(ev("introduced", "1.0.0"), ev("last_affected", "1.5.0"), ev("introduced", "3.0.0"), ev("fixed", "3.2.0")),
		}}, "3.1.0", Result{Affected: true, FixAvailable: true}},
		{"last_affected ends an interval without a fix", advisory.Affected{Ranges: []advisory.Range{
			rng(ev("introduced", "1.0.0"), ev("last_affected", "1.5.0")),
		}}, "1.2.0", Result{Affected: true}},
		{"a fixed range and an open one: the fix counts for the range that affects", advisory.Affected{Ranges: []advisory.Range{
			introducedFixed, rng(ev("introduced", "0")),
		}}, "1.1.0", Result{Affected: true, FixAvailable: true}},
		{"every version with a version that parses", advisory.Affected{EveryVersion: true}, "1.0.0", Result{Affected: true}},
		{"a stored version that does not parse still matches versions[]", advisory.Affected{Versions: []string{"latest"}}, "latest", Result{Affected: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Affects(dotted{}, c.e, c.version); got != c.want {
				t.Errorf("Affects(%s) = %+v, want %+v", c.version, got, c.want)
			}
		})
	}
}

func TestAffectsDoesNotChangeItsInput(t *testing.T) {
	e := advisory.Affected{Ranges: []advisory.Range{rng(ev("fixed", "1.2.0"), ev("introduced", "1.0.0"))}}
	_ = Affects(dotted{}, e, "1.1.0")
	if e.Ranges[0].Events[0].Kind != advisory.EventFixed {
		t.Error("Affects sorted the caller's events")
	}
}

func TestMatcherVersionIsPositive(t *testing.T) {
	if MatcherVersion < 1 {
		t.Fatalf("MatcherVersion = %d", MatcherVersion)
	}
}
