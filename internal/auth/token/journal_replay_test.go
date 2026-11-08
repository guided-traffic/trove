package token

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
)

// TestR_AUTH_037_AnonymousSwitchOrder: off grants nothing that on does not, so only a switch that ends off or stays as it is
// narrows.
func TestR_AUTH_037_AnonymousSwitchOrder(t *testing.T) {
	for _, c := range []struct{ current, after, want bool }{
		{true, false, true}, {false, false, true}, {true, true, true}, {false, true, false},
	} {
		if got := (switchOrder{}).Narrows(c.current, c.after); got != c.want {
			t.Errorf("Narrows(%v, %v) = %v, want %v", c.current, c.after, got, c.want)
		}
	}
}

func TestR_AUTH_029_ReplayReasonsAreTheColumnsValues(t *testing.T) {
	for _, ok := range []string{ReasonOwner, ReasonAdmin, ReasonRotated, ReasonNPMLogout, ReasonLeaked, ReasonPrincipalDisabled, ReasonParentRevoked, "scim", "mapping_deleted"} {
		if !validReplayReason(ok) {
			t.Errorf("%q must be accepted", ok)
		}
	}
	if validReplayReason("forged") || validReplayReason("") {
		t.Error("a reason outside the column's list must not be stored")
	}
}

// TestR_AUTH_026_TheMaximumOrderTreatsNullAsTheLongest: a journaled maximum narrows when it is a number at most the current
// one, or any number while the current one is the YAML value; null, the YAML value, is the longest and never narrows
// (Q-AUTH-207).
func TestR_AUTH_026_TheMaximumOrderTreatsNullAsTheLongest(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	for _, c := range []struct {
		name           string
		current, after *int64
		want           bool
	}{
		{"shorter", n(3600), n(1800), true},
		{"equal", n(3600), n(3600), true},
		{"longer", n(1800), n(3600), false},
		{"number below the YAML value", nil, n(1800), true},
		{"null after a number", n(1800), nil, false},
		{"null after null", nil, nil, false},
	} {
		if got := (maxOrder{}).Narrows(c.current, c.after); got != c.want {
			t.Errorf("%s: Narrows = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestR_AUTH_041_SettingsEntriesAreReadPartByPart: the entry the writer produces reads back; a part the entry lacks or holds
// out of range (not a whole number of seconds, under a minute, over 100 years, no after-value) is dropped alone and never
// takes another part with it; an entry with no readable part is skipped as malformed before any statement runs (Q-AUTH-207).
func TestR_AUTH_041_SettingsEntriesAreReadPartByPart(t *testing.T) {
	const anon = `"anonymousAccess":{"before":true,"after":false}`
	const max = `{"maxTTLSeconds":{"before":null,"after":1800}}`
	good := `{` + anon + `,"tokens":{"pat":` + max + `,"sat":{"maxTTLSeconds":{"before":3600,"after":null}}}}`
	e, ok := readSettingsEntry(journal.Object{Entry: json.RawMessage(good)})
	if !ok || e.Anonymous == nil || e.Anonymous.After || e.PAT == nil || e.PAT.Seconds == nil || *e.PAT.Seconds != 1800 || e.SAT == nil || e.SAT.Seconds != nil {
		t.Fatalf("%s reads as %+v, %v", good, e, ok)
	}
	maxOf := func(after string) string { return `{"maxTTLSeconds":{"before":null,"after":` + after + `}}` }
	for name, c := range map[string]struct {
		entry               string
		anonymous, pat, sat bool // which parts must be readable
	}{
		"no tokens":          {`{` + anon + `}`, true, false, false},
		"no anonymous":       {`{"tokens":{"pat":` + max + `,"sat":` + max + `}}`, false, true, true},
		"no sat":             {`{` + anon + `,"tokens":{"pat":` + max + `}}`, true, true, false},
		"no maximum":         {`{` + anon + `,"tokens":{"pat":{},"sat":` + max + `}}`, true, false, true},
		"no after":           {`{` + anon + `,"tokens":{"pat":{"maxTTLSeconds":{"before":null}},"sat":` + max + `}}`, true, false, true},
		"a string":           {`{` + anon + `,"tokens":{"pat":` + maxOf(`"1800"`) + `,"sat":` + max + `}}`, true, false, true},
		"a fraction":         {`{` + anon + `,"tokens":{"pat":` + maxOf(`1800.5`) + `,"sat":` + max + `}}`, true, false, true},
		"under a minute":     {`{` + anon + `,"tokens":{"pat":` + maxOf(`59`) + `,"sat":` + max + `}}`, true, false, true},
		"zero":               {`{` + anon + `,"tokens":{"pat":` + maxOf(`0`) + `,"sat":` + max + `}}`, true, false, true},
		"one second over":    {`{` + anon + `,"tokens":{"pat":` + maxOf(`3153600001`) + `,"sat":` + max + `}}`, true, false, true},
		"the longest":        {`{` + anon + `,"tokens":{"pat":` + maxOf(`3153600000`) + `,"sat":` + max + `}}`, true, true, true},
		"a null maximum":     {`{` + anon + `,"tokens":{"pat":{"maxTTLSeconds":null},"sat":` + max + `}}`, true, false, true},
		"a null kind":        {`{` + anon + `,"tokens":{"pat":null,"sat":` + max + `}}`, true, false, true},
		"an empty anonymous": {`{"anonymousAccess":{},"tokens":{"pat":` + max + `,"sat":` + max + `}}`, false, true, true},
		"a mistyped switch":  {`{"anonymousAccess":{"before":"no","after":false},"tokens":{"pat":` + max + `,"sat":` + max + `}}`, false, true, true},
	} {
		got, ok := readSettingsEntry(journal.Object{Entry: json.RawMessage(c.entry)})
		if !ok || (got.Anonymous != nil) != c.anonymous || (got.PAT != nil) != c.pat || (got.SAT != nil) != c.sat {
			t.Errorf("%s: parts anonymous %v pat %v sat %v (ok %v), want %v %v %v", name, got.Anonymous != nil, got.PAT != nil, got.SAT != nil, ok, c.anonymous, c.pat, c.sat)
		}
	}
	for name, bad := range map[string]string{
		"not an object": `[]`,
		"nothing":       `{}`,
		"all dropped":   `{"anonymousAccess":{},"tokens":{"pat":` + maxOf(`5`) + `,"sat":null}}`,
	} {
		o := journal.Object{Event: audit.EventSettingsAuthChanged, Entry: json.RawMessage(bad)}
		if _, ok := readSettingsEntry(o); ok {
			t.Errorf("%s: %s was read", name, bad)
		}
		if r, err := replaySettings(context.Background(), nil, journal.Env{}, o); err != nil || r.Outcome != journal.Skipped {
			t.Errorf("%s: %+v, %v; want skipped as malformed", name, r, err)
		}
	}
}
