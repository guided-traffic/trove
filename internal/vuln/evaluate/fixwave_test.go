package evaluate

import (
	"testing"

	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// R-VULN-012, Q-VULN-65: the kind of a match is that of the records whose entry affects the version: a malicious record of the
// group that does not affect it makes nothing malicious, one that does makes the match malicious.
func TestR_VULN_012_MatchKindIsThatOfTheRecordsThatAffectTheVersion(t *testing.T) {
	to := func(v string) advisory.Affected {
		return advisory.Affected{Name: "p", Ranges: []advisory.Range{{Type: advisory.RangeEcosystem, Events: []advisory.Event{{Kind: "introduced", Version: "0"}, {Kind: "fixed", Version: v}}}}}
	}
	rows := []store.AffectedRow{
		{Name: "p", RecordID: "GHSA-1", CanonicalID: "MAL-1", Kind: advisory.KindVulnerability, Severity: advisory.SeverityHigh, Entry: to("2.0.0")},
		{Name: "p", RecordID: "MAL-1", CanonicalID: "MAL-1", Kind: advisory.KindMalicious, Severity: advisory.SeverityCritical, Entry: advisory.Affected{Name: "p", Versions: []string{"9.0.0"}}},
	}
	g := groupAffected(rows)["p"]
	if g.live["MAL-1"] != advisory.KindMalicious {
		t.Errorf("the group's kind: %q, want malicious", g.live["MAL-1"])
	}
	var st stats
	if got := matchVersion(dottedAdapter{}, g, "1.0.0", &st)["MAL-1"]; got == nil || got.Kind != advisory.KindVulnerability {
		t.Errorf("1.0.0 (the ordinary record affects it): %+v", got)
	}
	if got := matchVersion(dottedAdapter{}, g, "9.0.0", &st)["MAL-1"]; got == nil || got.Kind != advisory.KindMalicious {
		t.Errorf("9.0.0 (the malicious record names it): %+v", got)
	}
	if got := matchVersion(dottedAdapter{}, g, "5.0.0", &st); len(got) != 0 {
		t.Errorf("5.0.0 is affected by nothing: %v", got)
	}
}

// R-VULN-012, Q-VULN-65: the order of a merge of two findings (a block first, a dismissal last) and the status a resolved finding
// stands for in it (the status it had before).
func TestR_VULN_012_MergeStrength(t *testing.T) {
	order := []vuln.Status{vuln.StatusDismissed, vuln.StatusNeedsTriage, vuln.StatusConfirmed, vuln.StatusBlocked}
	for i := 1; i < len(order); i++ {
		if strength(order[i-1]) >= strength(order[i]) {
			t.Errorf("%s is not weaker than %s", order[i-1], order[i])
		}
	}
	cases := []struct {
		f    store.Finding
		want vuln.Status
	}{
		{store.Finding{Status: vuln.StatusConfirmed}, vuln.StatusConfirmed},
		{store.Finding{Status: vuln.StatusResolved, BeforeResolution: vuln.StatusBlocked}, vuln.StatusBlocked},
		{store.Finding{Status: vuln.StatusResolved}, vuln.StatusNeedsTriage},
		{store.Finding{Status: vuln.StatusResolved, BeforeResolution: vuln.StatusResolved}, vuln.StatusNeedsTriage},
	}
	for _, c := range cases {
		if got := effective(&c.f); got != c.want {
			t.Errorf("effective(%s before %q) = %s, want %s", c.f.Status, c.f.BeforeResolution, got, c.want)
		}
	}
}
