package npm

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/match"
)

func evt(kind advisory.EventKind, v string) advisory.Event {
	return advisory.Event{Kind: kind, Version: v}
}

func semverRange(events ...advisory.Event) advisory.Range {
	return advisory.Range{Type: advisory.RangeSemver, Events: events}
}

func TestVulnerabilityAdapterIsFound(t *testing.T) {
	c := formats.CapabilitiesOf(New())
	if c.Vulnerability == nil || c.Vulnerability.Ecosystem() != "npm" {
		t.Fatalf("CapabilitiesOf(npm).Vulnerability = %v", c.Vulnerability)
	}
}

func TestVulnerabilityAdapterNormalization(t *testing.T) {
	f := New()
	for in, want := range map[string]string{"left-pad": "left-pad", "@acme/ui": "@acme/ui", "LegacyName": "LegacyName"} {
		if got, ok := f.AdvisoryName(in); !ok || got != want {
			t.Errorf("AdvisoryName(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "a/b/c", "@scope", "..", "a%b"} {
		if _, ok := f.AdvisoryName(bad); ok {
			t.Errorf("AdvisoryName(%q) is ok, want refused", bad)
		}
	}
	for in, want := range map[string]string{"1.2.3": "1.2.3", "1.2.3-beta.1": "1.2.3-beta.1", "1.2.3+build.5": "1.2.3", "13.0": "13.0.0", "7": "7.0.0"} {
		if got, ok := f.AdvisoryVersion(in); !ok || got != want {
			t.Errorf("AdvisoryVersion(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "not-a-version", "v1.2.3", "1.2.x", "1.2.3.4"} {
		if _, ok := f.AdvisoryVersion(bad); ok {
			t.Errorf("AdvisoryVersion(%q) is ok, want refused", bad)
		}
	}
	if c, err := f.CompareVersions("1.0.0+a", "1.0.0+b"); err != nil || c != 0 {
		t.Errorf("build metadata is ignored: %d, %v", c, err)
	}
	if _, err := f.CompareVersions("1.0.0", "x"); err == nil {
		t.Error("CompareVersions of an unparsable version did not fail")
	}
}

// R-VULN-009: the table test of npm, 30 versions with pre-releases, against the evaluation algorithm of the OSV schema.
// The expected sets are written from the schema's rules and SemVer precedence (1.2.0-beta.1 < 1.2.0-rc.1 < 1.2.0).
func TestR_VULN_009_NPMTable(t *testing.T) {
	versions := []string{
		"0.0.1", "0.1.0", "0.9.0", "0.9.9", "1.0.0-alpha", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "1.2.0-beta.1",
		"1.2.0-rc.1", "1.2.0", "1.2.1", "1.2.10", "1.9.9", "1.10.0", "2.0.0-beta.1", "2.0.0", "2.0.1", "2.5.0",
		"2.9.9", "3.0.0-alpha", "3.0.0-rc.1", "3.0.0", "3.0.1", "3.1.0", "4.0.0", "5.0.0-beta.2", "5.0.0", "10.0.0",
	}
	if len(versions) != 30 {
		t.Fatalf("the table has %d versions, want 30", len(versions))
	}
	introducedFixed := semverRange(evt("introduced", "1.0.0"), evt("fixed", "1.2.0"))
	a := []string{"1.0.0", "1.0.1", "1.1.0", "1.2.0-beta.1", "1.2.0-rc.1"}
	cases := []struct {
		name string
		e    advisory.Affected
		want []string
		fix  bool
	}{
		{"introduced 1.0.0, fixed 1.2.0", advisory.Affected{Ranges: []advisory.Range{introducedFixed}}, a, true},
		{
			"introduced 0, last_affected 2.0.0",
			advisory.Affected{Ranges: []advisory.Range{semverRange(evt("introduced", "0"), evt("last_affected", "2.0.0"))}},
			[]string{
				"0.0.1", "0.1.0", "0.9.0", "0.9.9", "1.0.0-alpha", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "1.2.0-beta.1", "1.2.0-rc.1", "1.2.0",
				"1.2.1", "1.2.10", "1.9.9", "1.10.0", "2.0.0-beta.1", "2.0.0",
			},
			false,
		},
		{
			"introduced 1.0.0 with limit 3.0.0",
			advisory.Affected{Ranges: []advisory.Range{semverRange(evt("introduced", "1.0.0"), evt("limit", "3.0.0"))}},
			[]string{
				"1.0.0", "1.0.1", "1.1.0", "1.2.0-beta.1", "1.2.0-rc.1", "1.2.0", "1.2.1", "1.2.10", "1.9.9", "1.10.0", "2.0.0-beta.1", "2.0.0", "2.0.1",
				"2.5.0", "2.9.9", "3.0.0-alpha", "3.0.0-rc.1",
			},
			false,
		},
		{
			"two ranges in one entry",
			advisory.Affected{Ranges: []advisory.Range{introducedFixed, semverRange(evt("introduced", "3.0.0"), evt("fixed", "4.0.0"))}},
			append(slices.Clone(a), "3.0.0", "3.0.1", "3.1.0"), true,
		},
		{"a versions list", advisory.Affected{Versions: []string{"1.0.1", "2.0.0", "5.0.0-beta.2"}}, []string{"1.0.1", "2.0.0", "5.0.0-beta.2"}, false},
	}
	f := New()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, v := range versions {
				r := match.Affects(f, c.e, v)
				if r.Unevaluable != 0 {
					t.Fatalf("%s: unevaluable %d", v, r.Unevaluable)
				}
				if r.Affected {
					got = append(got, v)
					if r.FixAvailable != c.fix {
						t.Errorf("%s: FixAvailable = %v, want %v", v, r.FixAvailable, c.fix)
					}
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("affected = %v\nwant       %v", got, c.want)
			}
		})
	}
}

// R-VULN-009: a range with the event fixed "not-a-version" is counted as unevaluable and matches nothing; the versions
// list of the same entry still matches.
func TestR_VULN_009_NPMUnevaluable(t *testing.T) {
	f := New()
	e := advisory.Affected{
		Ranges:   []advisory.Range{semverRange(evt("introduced", "0"), evt("fixed", "not-a-version"))},
		Versions: []string{"1.0.0"},
	}
	if r := match.Affects(f, e, "1.0.0"); !r.Affected || r.Unevaluable != 1 {
		t.Errorf("1.0.0: %+v, want affected through versions[] with one unevaluable range", r)
	}
	if r := match.Affects(f, e, "1.0.1"); r.Affected || r.Unevaluable != 1 {
		t.Errorf("1.0.1: %+v, want not affected, one unevaluable range", r)
	}
}
