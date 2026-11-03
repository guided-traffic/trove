package nuget

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

func ecosystemRange(events ...advisory.Event) advisory.Range {
	return advisory.Range{Type: advisory.RangeEcosystem, Events: events}
}

func TestVulnerabilityAdapterIsFound(t *testing.T) {
	c := formats.CapabilitiesOf(New())
	if c.Vulnerability == nil || c.Vulnerability.Ecosystem() != "NuGet" {
		t.Fatalf("CapabilitiesOf(nuget).Vulnerability = %v", c.Vulnerability)
	}
}

func TestVulnerabilityAdapterNormalization(t *testing.T) {
	f := New()
	for in, want := range map[string]string{"Newtonsoft.Json": "newtonsoft.json", "Contoso.Util": "contoso.util", "a_b-c": "a_b-c"} {
		if got, ok := f.AdvisoryName(in); !ok || got != want {
			t.Errorf("AdvisoryName(%q) = %q, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "a..b", "-a", "a b", "a/b"} {
		if _, ok := f.AdvisoryName(bad); ok {
			t.Errorf("AdvisoryName(%q) is ok, want refused", bad)
		}
	}
	for in, want := range map[string]string{"13.0.1": "13.0.1", "1.0": "1.0.0", "1.0.0.0": "1.0.0", "1.2.0-BETA": "1.2.0-beta", "1.0.0+meta": "1.0.0", "1.0.0.1": "1.0.0.1"} {
		if got, ok := f.AdvisoryVersion(in); !ok || got != want {
			t.Errorf("AdvisoryVersion(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "not-a-version", "1.2.3.4.5", "1..0"} {
		if _, ok := f.AdvisoryVersion(bad); ok {
			t.Errorf("AdvisoryVersion(%q) is ok, want refused", bad)
		}
	}
	if c, err := f.CompareVersions("1.0.0", "1.0.0.1"); err != nil || c != -1 {
		t.Errorf("the fourth part orders: %d, %v", c, err)
	}
	if _, err := f.CompareVersions("1.0.0", "x"); err == nil {
		t.Error("CompareVersions of an unparsable version did not fail")
	}
}

// R-VULN-009: the table test of NuGet, 30 versions with pre-releases and four-part versions, against the evaluation
// algorithm of the OSV schema. The expected sets are written from the schema's rules and NuGet's precedence
// (1.2.0-beta < 1.2.0-rc.1 < 1.2.0 < 1.2.0.1).
func TestR_VULN_009_NuGetTable(t *testing.T) {
	versions := []string{
		"0.0.1", "0.1.0", "0.9.0", "0.9.9", "1.0.0-alpha", "1.0.0-rc.1", "1.0.0", "1.0.0.1", "1.0.1", "1.1.0",
		"1.2.0-beta", "1.2.0-rc.1", "1.2.0", "1.2.0.1", "1.2.1", "1.2.10", "1.9.9", "1.10.0", "2.0.0-beta.1", "2.0.0",
		"2.0.1", "2.5.0", "2.9.9", "3.0.0-alpha", "3.0.0", "3.0.1", "3.1.0", "4.0.0", "5.0.0-beta.2", "10.0.0",
	}
	if len(versions) != 30 {
		t.Fatalf("the table has %d versions, want 30", len(versions))
	}
	introducedFixed := ecosystemRange(evt("introduced", "1.0.0"), evt("fixed", "1.2.0"))
	a := []string{"1.0.0", "1.0.0.1", "1.0.1", "1.1.0", "1.2.0-beta", "1.2.0-rc.1"}
	cases := []struct {
		name string
		e    advisory.Affected
		want []string
		fix  bool
	}{
		{"introduced 1.0.0, fixed 1.2.0", advisory.Affected{Ranges: []advisory.Range{introducedFixed}}, a, true},
		{
			"introduced 0, last_affected 2.0.0",
			advisory.Affected{Ranges: []advisory.Range{ecosystemRange(evt("introduced", "0"), evt("last_affected", "2.0.0"))}},
			[]string{
				"0.0.1", "0.1.0", "0.9.0", "0.9.9", "1.0.0-alpha", "1.0.0-rc.1", "1.0.0", "1.0.0.1", "1.0.1", "1.1.0", "1.2.0-beta", "1.2.0-rc.1", "1.2.0",
				"1.2.0.1", "1.2.1", "1.2.10", "1.9.9", "1.10.0", "2.0.0-beta.1", "2.0.0",
			},
			false,
		},
		{
			"introduced 1.0.0 with limit 3.0.0",
			advisory.Affected{Ranges: []advisory.Range{ecosystemRange(evt("introduced", "1.0.0"), evt("limit", "3.0.0"))}},
			[]string{
				"1.0.0", "1.0.0.1", "1.0.1", "1.1.0", "1.2.0-beta", "1.2.0-rc.1", "1.2.0", "1.2.0.1", "1.2.1", "1.2.10", "1.9.9", "1.10.0", "2.0.0-beta.1",
				"2.0.0", "2.0.1", "2.5.0", "2.9.9", "3.0.0-alpha",
			},
			false,
		},
		{
			"two ranges in one entry",
			advisory.Affected{Ranges: []advisory.Range{introducedFixed, ecosystemRange(evt("introduced", "3.0.0"), evt("fixed", "4.0.0"))}},
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
func TestR_VULN_009_NuGetUnevaluable(t *testing.T) {
	f := New()
	e := advisory.Affected{
		Ranges:   []advisory.Range{ecosystemRange(evt("introduced", "0"), evt("fixed", "not-a-version"))},
		Versions: []string{"1.0.0"},
	}
	if r := match.Affects(f, e, "1.0.0"); !r.Affected || r.Unevaluable != 1 {
		t.Errorf("1.0.0: %+v, want affected through versions[] with one unevaluable range", r)
	}
	if r := match.Affects(f, e, "1.0.1"); r.Affected || r.Unevaluable != 1 {
		t.Errorf("1.0.1: %+v, want not affected, one unevaluable range", r)
	}
}
