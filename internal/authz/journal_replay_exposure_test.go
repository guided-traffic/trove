package authz

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/journal"
)

// The comparator of R-EP-020 and the reading of journaled exposure states. The handler's writes run against
// PostgreSQL in test/integration/backup/journal_exposure_replay_test.go.

// acmeRule is the rule acme-sdk of R-EP-012's example as the comparator sees it.
func acmeRule() ExposureRuleInput {
	r := ">=2.0.0"
	return normInput(ExposureRuleInput{
		Name: "acme-sdk", Repository: "web/npm-customers", Enabled: true,
		Packages: PatternLists{Include: []string{"@acme/sdk", "@acme/sdk-*"}, Exclude: []string{"@acme/sdk-internal"}},
		Versions: VersionLists{Exclude: []string{"2.0.1"}, Range: &r},
		Tags:     PatternLists{Include: []string{"latest", "lts"}},
	})
}

func expState(in ExposureRuleInput) exposureJournalState {
	return exposureJournalState{Valid: true, Endpoint: "customers", RepositoryID: 7, State: normInput(in)}
}

// outcome runs the update rule of R-HA-054 with the comparator and reports what it chose.
func outcome(t *testing.T, cmp exposureNarrower, current, before, after exposureJournalState) journal.Outcome {
	t.Helper()
	out, _, err := journal.ApplyUpdate(cmp, current, before, after, journal.UpdateActions{
		Apply:   func() error { return nil },
		Suspend: func() (json.RawMessage, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestR_EP_020_TheComparator(t *testing.T) {
	cmp := exposureNarrower{Names: exactNames{}}
	with := func(f func(*ExposureRuleInput)) exposureJournalState {
		in := acmeRule()
		f(&in)
		return expState(in)
	}
	base := expState(acmeRule())

	t.Run("R-EP-020 the same state narrows both ways", func(t *testing.T) {
		if !cmp.Narrows(base, base) {
			t.Fatal("a state must narrow itself")
		}
	})
	t.Run("R-EP-020 a journaled update that widens packages.include by @acme/* is skipped", func(t *testing.T) {
		before := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"@acme/sdk"}}}))
		after := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"@acme/sdk", "@acme/*"}}}))
		if got := outcome(t, cmp, before, before, after); got != journal.Skipped {
			t.Fatalf("outcome = %s, want skipped", got)
		}
	})
	t.Run("R-EP-020 one that replaces @acme/sdk by @acme/sdk-* is suspended", func(t *testing.T) {
		before := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"@acme/sdk"}}}))
		after := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"@acme/sdk-*"}}}))
		if got := outcome(t, cmp, before, before, after); got != journal.Suspended {
			t.Fatalf("outcome = %s, want suspended", got)
		}
	})
	t.Run("R-EP-020 a replacement by a wider glob is no containment the comparator proves, and is suspended", func(t *testing.T) {
		before := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"@acme/sdk"}}}))
		after := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"@acme/*"}}}))
		if got := outcome(t, cmp, before, before, after); got != journal.Suspended {
			t.Fatalf("outcome = %s, want suspended (patterns are compared without glob containment)", got)
		}
	})
	t.Run("R-EP-020 an emptied include list is unrestricted and never narrows", func(t *testing.T) {
		after := with(func(in *ExposureRuleInput) { in.Packages.Include = nil })
		if cmp.Narrows(base, after) {
			t.Fatal("removing the last include pattern widens the rule")
		}
		if got := outcome(t, cmp, base, base, after); got != journal.Skipped {
			t.Fatalf("outcome = %s, want skipped", got)
		}
	})
	t.Run("R-EP-020 a removed include pattern and an added exclude pattern narrow", func(t *testing.T) {
		for name, after := range map[string]exposureJournalState{
			"include":           with(func(in *ExposureRuleInput) { in.Packages.Include = []string{"@acme/sdk"} }),
			"exclude":           with(func(in *ExposureRuleInput) { in.Versions.Exclude = append(in.Versions.Exclude, "2.0.0") }),
			"tag":               with(func(in *ExposureRuleInput) { in.Tags.Include = []string{"latest"} }),
			"path":              with(func(in *ExposureRuleInput) { in.Paths.Exclude = []string{"**/internal/**"} }),
			"version include":   with(func(in *ExposureRuleInput) { in.Versions.Include = []string{"2.*"} }),
			"disabled":          with(func(in *ExposureRuleInput) { in.Enabled = false }),
			"same in any order": with(func(in *ExposureRuleInput) { in.Packages.Include = []string{"@acme/sdk-*", "@acme/sdk"} }),
		} {
			if !cmp.Narrows(base, after) {
				t.Errorf("%s: the after-state narrows the rule", name)
			}
			if got := outcome(t, cmp, base, base, after); got != journal.Applied {
				t.Errorf("%s: outcome = %s, want applied", name, got)
			}
		}
	})
	t.Run("R-EP-020 a removed exclude pattern, an enabled rule or pre-releases do not narrow", func(t *testing.T) {
		off := with(func(in *ExposureRuleInput) { in.Enabled = false })
		for name, c := range map[string][2]exposureJournalState{
			"exclude removed":  {base, with(func(in *ExposureRuleInput) { in.Packages.Exclude = nil })},
			"enabled":          {off, base},
			"pre-releases":     {base, with(func(in *ExposureRuleInput) { in.Prereleases = true })},
			"tag include more": {base, with(func(in *ExposureRuleInput) { in.Tags.Include = append(in.Tags.Include, "next") })},
		} {
			if cmp.Narrows(c[0], c[1]) {
				t.Errorf("%s: the after-state grants more and must not narrow", name)
			}
		}
	})
	t.Run("R-EP-020 a range narrows only when current has none or it is equal", func(t *testing.T) {
		none := with(func(in *ExposureRuleInput) { in.Versions.Range = nil })
		if !cmp.Narrows(none, base) {
			t.Error("a range on a rule without one narrows it")
		}
		if cmp.Narrows(base, none) {
			t.Error("dropping the range widens the rule")
		}
		later := ">=2.1.0"
		moved := with(func(in *ExposureRuleInput) { in.Versions.Range = &later })
		if cmp.Narrows(base, moved) {
			t.Error("a different range is no narrowing the comparator can prove")
		}
		if got := outcome(t, cmp, base, base, moved); got != journal.Suspended {
			t.Errorf("range >=2.0.0 to >=2.1.0: outcome = %s, want suspended", got)
		}
	})
	t.Run("R-EP-020 another endpoint or repository never narrows", func(t *testing.T) {
		other := base
		other.Endpoint = "partners"
		moved := base
		moved.RepositoryID = 8
		if cmp.Narrows(base, other) || cmp.Narrows(base, moved) {
			t.Fatal("the endpoint and the repository ID must be equal")
		}
	})
	t.Run("R-EP-020 an unreadable state proves nothing and suspends", func(t *testing.T) {
		bad := exposureJournalState{}
		if cmp.Narrows(base, bad) || cmp.Narrows(bad, base) {
			t.Fatal("an invalid state must not narrow")
		}
		if got := outcome(t, cmp, base, base, bad); got != journal.Suspended {
			t.Fatalf("outcome = %s, want suspended", got)
		}
	})
	t.Run("R-EP-020 package patterns compare as the matcher folds them, paths as written", func(t *testing.T) {
		nuget := exposureNarrower{Names: lowerNames{}}
		cur := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"Acme.Sdk", "Acme.Sdk.*"}}}))
		aft := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"acme.sdk"}}}))
		if !nuget.Narrows(cur, aft) {
			t.Error("NuGet names fold: acme.sdk is Acme.Sdk")
		}
		if cmp.Narrows(cur, aft) {
			t.Error("npm names compare as written")
		}
		p1 := expState(normInput(ExposureRuleInput{Enabled: true, Paths: PatternLists{Include: []string{"Releases/**"}}}))
		p2 := expState(normInput(ExposureRuleInput{Enabled: true, Paths: PatternLists{Include: []string{"releases/**"}}}))
		if nuget.Narrows(p1, p2) {
			t.Error("paths compare as written")
		}
	})
	t.Run("R-HA-054 a forged before-state only chooses between skipped and suspended", func(t *testing.T) {
		// The after-state adds @acme/billing; the forged before-state holds it already, so the addition looks like a
		// narrowing of the before-state. The restored rule decides: it is suspended, never widened.
		cur := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"@acme/sdk"}}}))
		after := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"@acme/sdk", "@acme/billing"}}}))
		forged := expState(normInput(ExposureRuleInput{Enabled: true, Packages: PatternLists{Include: []string{"@acme/sdk", "@acme/billing", "@acme/x"}}}))
		for _, before := range []exposureJournalState{cur, forged} {
			if got := outcome(t, cmp, cur, before, after); got == journal.Applied {
				t.Fatalf("an addition was applied with before-state %v", before.State.Packages)
			}
		}
	})
}

func TestR_EP_020_JournaledStatesAreReadStrictly(t *testing.T) {
	r := exposureReplay{reg: testRegistry()}
	rule := ExposureRule{ExposureRuleInput: acmeRule(), ID: "r-1", Endpoint: "customers", RepositoryID: 7, Format: "npm"}
	producer := func() json.RawMessage { return mustJSON(stateOfRule(rule.ExposureRuleInput)) }

	t.Run("R-EP-020 the producer's state reads back, also as PostgreSQL reformats it", func(t *testing.T) {
		st := r.journalState(producer(), rule)
		cmp := exposureNarrower{Names: exactNames{}}
		if !st.Valid || !cmp.Narrows(st, expState(rule.ExposureRuleInput)) {
			t.Fatalf("state = %+v", st)
		}
		var v map[string]any
		_ = json.Unmarshal(producer(), &v)
		spaced, _ := json.MarshalIndent(v, "", "   ")
		if !r.journalState(spaced, rule).Valid {
			t.Fatal("reformatted JSON must read")
		}
	})
	t.Run("R-EP-020 the update entry of the rule service carries states the handler reads", func(t *testing.T) {
		before := stateOfRule(rule.ExposureRuleInput)
		narrowed := rule.ExposureRuleInput
		narrowed.Packages.Include = []string{"@acme/sdk"}
		ev := exposureEvent(EventExposureUpdated, Subject{}, ExposureRule{ExposureRuleInput: narrowed, ID: "r-1", Endpoint: "customers", RepositoryID: 7},
			before, stateOfRule(narrowed), []string{"packages"}, 3, "")
		var e exposureJournalEntry
		if err := json.Unmarshal(ev.Journal, &e); err != nil || e.RuleID != "r-1" || e.RepositoryID != 7 || e.Endpoint != "customers" {
			t.Fatalf("entry = %+v, %v", e, err)
		}
		if !r.journalState(e.Before, rule).Valid || !r.journalState(e.After, rule).Valid {
			t.Fatal("the producer's states must read")
		}
	})
	t.Run("R-EP-020 an unknown member, a missing member, another type or trailing data make a state unreadable", func(t *testing.T) {
		var good map[string]any
		_ = json.Unmarshal(producer(), &good)
		variants := map[string]func(m map[string]any){
			"unknown": func(m map[string]any) { m["grants"] = []string{"x"} },
			"missing": func(m map[string]any) { delete(m, "prereleases") },
			"type":    func(m map[string]any) { m["enabled"] = "yes" },
			"nested": func(m map[string]any) {
				m["packages"] = map[string]any{"include": []string{"x"}, "exclude": []string{}, "all": true}
			},
		}
		for name, f := range variants {
			m := map[string]any{}
			for k, v := range good {
				m[k] = v
			}
			f(m)
			raw, _ := json.Marshal(m)
			if r.journalState(raw, rule).Valid {
				t.Errorf("%s: %s read as valid", name, raw)
			}
		}
		for _, raw := range []string{"", "null", "[]", string(producer()) + "{}", "{"} {
			if r.journalState(json.RawMessage(raw), rule).Valid {
				t.Errorf("%q read as valid", raw)
			}
		}
	})
	t.Run("R-EP-020 an after-state that is no rule the API accepts is not valid", func(t *testing.T) {
		bad := func(f func(*ExposureRuleInput)) json.RawMessage {
			in := rule.ExposureRuleInput
			f(&in)
			return mustJSON(stateOfRule(in))
		}
		broken := ">=2.0.0 <"
		for name, raw := range map[string]json.RawMessage{
			"pattern": bad(func(in *ExposureRuleInput) { in.Packages.Exclude = []string{""} }),
			"range":   bad(func(in *ExposureRuleInput) { in.Versions.Range = &broken }),
			"count":   bad(func(in *ExposureRuleInput) { in.Paths.Exclude = strings.Split(strings.Repeat("x,", 257), ",") }),
		} {
			if r.journalState(raw, rule).Valid {
				t.Errorf("%s: an invalid after-state read as valid", name)
			}
		}
		generic := rule
		generic.Format = "generic"
		if r.journalState(producer(), generic).Valid {
			t.Error("versions and tags on a generic repository are no rule the API accepts")
		}
	})
}

func TestR_EP_020_TheHandlerIsRegistered(t *testing.T) {
	for _, ev := range []string{EventExposureUpdated, EventExposureDeleted} {
		h, ok := journal.HandlerFor(ev)
		if !ok || h.Domain != "exposure" {
			t.Errorf("HandlerFor(%s) = %+v, %v", ev, h.Domain, ok)
		}
	}
	if _, ok := journal.HandlerFor(EventExposureCreated); ok {
		t.Error("a creation grants and is never replayed")
	}
}
