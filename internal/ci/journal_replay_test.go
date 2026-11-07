package ci

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/journal"
)

// The comparators of R-CI-020, the reading of journaled records and the fall-through of Q-CI-11. The handlers' writes
// run against PostgreSQL in test/integration/backup/journal_ci_replay_test.go.

func strp(s string) *string { return &s }

func i64p(v int64) *int64 { return &v }

// replayIssuerFixture is a GitLab issuer as the API stores it.
func replayIssuerFixture() Issuer {
	return Issuer{
		ID: "0192f0c4-0000-7000-8000-000000000001", Name: "gitlab-com", Kind: KindGitLab, Issuer: "https://gitlab.com", Enabled: true,
		AllowedAudiences: []string{"https://trove.example.com", "npm:trove.example.com"}, ExclusiveAudience: true,
		AllowedAlgorithms: []string{"RS256", "ES256"}, AllowedTyp: []string{"JWT"}, ClockSkewSeconds: 60,
		RequiredClaims: []string{"project_id"}, PinClaims: []string{"project_id", "namespace_id"}, MaxTTLSeconds: i64p(3600),
		Source: SourceAPI, Version: 4,
	}
}

func journaledIssuer(is Issuer) issuerJournalState {
	return issuerJournalState{Valid: true, Record: *recordOf(is)}
}

// replayMappingFixture is acme-release-publish of R-CI-009's example: read and deploy of the namespace's scope.
func replayMappingFixture() Mapping {
	return Mapping{
		ID: "0192f0c4-0000-7000-8000-0000000000a1", IssuerID: "0192f0c4-0000-7000-8000-000000000001", Issuer: "gitlab-com",
		PrincipalID: "0192f0c4-0000-7000-8000-0000000000b1", Name: "acme-release-publish", Tier: TierSystem, Priority: 100,
		Enabled: true, SubjectKinds: []string{SubjectIDToken}, Version: 3,
		Match: mustNode(`{"all": [{"claim": "namespace_id", "equals": "4711"}, {"claim": "ref_protected", "equals": true}]}`),
		Grants: claims.Spec{
			Policies: []string{"ci-read-internal"},
			Inline: []claims.InlineGrant{{
				Repositories: []string{"web/npm-internal"}, Packages: &claims.PatternSet{Include: []string{"@{{ namespace_path | slug }}/*"}},
				Actions: []claims.Action{claims.Read, claims.Deploy}, Ownership: claims.OwnershipBindProject,
			}},
		},
		MaxTTLSeconds: i64p(3600), Options: claims.Options{CrossProjectSource: claims.CrossProjectDeny},
	}
}

func journaledMapping(m Mapping) mappingJournalState {
	return mappingJournalState{Valid: true, Record: *recordOfMapping(&m)}
}

func ciOutcome[T any](t *testing.T, cmp journal.Narrower[T], current, before, after T) journal.Outcome {
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

func TestR_CI_020_TheIssuerComparator(t *testing.T) {
	cmp := issuerNarrower{}
	base := replayIssuerFixture()
	with := func(f func(*Issuer)) issuerJournalState {
		is := replayIssuerFixture()
		f(&is)
		return journaledIssuer(is)
	}
	t.Run("R-CI-020 each field may move in the direction of its row", func(t *testing.T) {
		for name, after := range map[string]issuerJournalState{
			"audiences a non-empty subset":   with(func(is *Issuer) { is.AllowedAudiences = []string{"https://trove.example.com"} }),
			"algorithms a non-empty subset":  with(func(is *Issuer) { is.AllowedAlgorithms = []string{"RS256"} }),
			"required claims a superset":     with(func(is *Issuer) { is.RequiredClaims = []string{"project_id", "ref_protected"} }),
			"single use on":                  with(func(is *Issuer) { is.SingleUseSubjectTokens = true }),
			"a smaller clock skew":           with(func(is *Issuer) { is.ClockSkewSeconds = 30 }),
			"a smaller max_ttl":              with(func(is *Issuer) { is.MaxTTLSeconds = i64p(600) }),
			"the description and version":    with(func(is *Issuer) { is.Description, is.Version = "changed", 9 }),
			"the same sets in another order": with(func(is *Issuer) { is.AllowedAudiences = []string{"npm:trove.example.com", "https://trove.example.com"} }),
		} {
			if !cmp.Narrows(journaledIssuer(base), after) {
				t.Errorf("%s must narrow", name)
			}
		}
	})
	t.Run("R-CI-020 any other difference does not narrow", func(t *testing.T) {
		lax := replayIssuerFixture()
		lax.ExclusiveAudience, lax.AllowMissingTyp, lax.MaxTTLSeconds = false, true, nil
		for name, c := range map[string][2]issuerJournalState{
			"an added audience":         {journaledIssuer(base), with(func(is *Issuer) { is.AllowedAudiences = append(is.AllowedAudiences, "x") })},
			"an emptied audience list":  {journaledIssuer(base), with(func(is *Issuer) { is.AllowedAudiences = nil })},
			"a removed required claim":  {journaledIssuer(base), with(func(is *Issuer) { is.RequiredClaims = nil })},
			"exclusive audience off":    {journaledIssuer(base), with(func(is *Issuer) { is.ExclusiveAudience = false })},
			"missing typ allowed":       {journaledIssuer(base), with(func(is *Issuer) { is.AllowMissingTyp = true })},
			"the job-token fallback on": {journaledIssuer(base), with(func(is *Issuer) { is.JobTokenFallback.Enabled = true })},
			"a larger clock skew":       {journaledIssuer(base), with(func(is *Issuer) { is.ClockSkewSeconds = 120 })},
			"an unbounded max_ttl":      {journaledIssuer(base), with(func(is *Issuer) { is.MaxTTLSeconds = nil })},
			"enabled":                   {with(func(is *Issuer) { is.Enabled = false }), journaledIssuer(base)},
			"a key source":              {journaledIssuer(base), with(func(is *Issuer) { is.JWKSURI = strp("https://gitlab.com/oauth/discovery/keys") })},
			"a CA bundle":               {journaledIssuer(base), with(func(is *Issuer) { is.CABundleRef = strp("secret:ca") })},
			"public issuer":             {journaledIssuer(base), with(func(is *Issuer) { is.PublicIssuer = true })},
			"a pin claim":               {journaledIssuer(base), with(func(is *Issuer) { is.PinClaims = []string{"project_id"} })},
			"the outbound proxy":        {journaledIssuer(base), with(func(is *Issuer) { is.UseOutboundProxy = true })},
			"another name":              {journaledIssuer(base), with(func(is *Issuer) { is.Name = "gitlab-other" })},
			"another issuer URL":        {journaledIssuer(base), with(func(is *Issuer) { is.Issuer = "https://gitlab.example.com" })},
			"another ID":                {journaledIssuer(base), with(func(is *Issuer) { is.ID = "0192f0c4-0000-7000-8000-000000000002" })},
			"an unreadable record":      {journaledIssuer(base), {}},
			"a lax issuer":              {journaledIssuer(base), journaledIssuer(lax)},
		} {
			if cmp.Narrows(c[0], c[1]) {
				t.Errorf("%s must not narrow", name)
			}
		}
	})
	t.Run("R-CI-020 an added audience is skipped, and a forged before-state turns it into a suspension, never an acceptance", func(t *testing.T) {
		cur := journaledIssuer(base)
		after := with(func(is *Issuer) { is.AllowedAudiences = append(is.AllowedAudiences, "https://new.example.com") })
		if got := ciOutcome[issuerJournalState](t, cmp, cur, cur, after); got != journal.Skipped {
			t.Errorf("an added audience: %s, want skipped", got)
		}
		forged := with(func(is *Issuer) {
			is.AllowedAudiences = append(is.AllowedAudiences, "https://new.example.com", "https://forged.example.com")
		})
		if got := ciOutcome[issuerJournalState](t, cmp, cur, forged, after); got != journal.Suspended {
			t.Errorf("a forged before-state: %s, want suspended", got)
		}
	})
}

func TestR_CI_020_TheMappingComparator(t *testing.T) {
	cmp := mappingNarrower{}
	base := replayMappingFixture()
	with := func(f func(*Mapping)) mappingJournalState {
		m := replayMappingFixture()
		f(&m)
		return journaledMapping(m)
	}
	t.Run("R-CI-020 a pure removal of grants and the restricting options narrow", func(t *testing.T) {
		for name, after := range map[string]mappingJournalState{
			"deploy removed":        with(func(m *Mapping) { m.Grants.Inline[0].Actions = []claims.Action{claims.Read} }),
			"a policy removed":      with(func(m *Mapping) { m.Grants.Policies = nil }),
			"an include narrowed":   with(func(m *Mapping) { m.Grants.Inline[0].Repositories = []string{"web/npm-internal"} }),
			"a shorter lifetime":    with(func(m *Mapping) { m.MaxTTLSeconds = i64p(600) }),
			"a description changed": with(func(m *Mapping) { m.Description, m.Version = "new", 8 }),
			"an inline grant twice": with(func(m *Mapping) { m.Grants.Inline = append(m.Grants.Inline, m.Grants.Inline[0]) }),
		} {
			if !cmp.Narrows(journaledMapping(base), after) {
				t.Errorf("%s must narrow", name)
			}
		}
		ro := replayMappingFixture()
		ro.Options = claims.Options{CrossProjectSource: claims.CrossProjectReadOnly, AllowUnprotectedRefs: true}
		if !cmp.Narrows(journaledMapping(ro), journaledMapping(base)) {
			t.Error("cross_project_source deny and allow_unprotected_refs false narrow")
		}
		two := replayMappingFixture()
		two.Grants.Inline[0].Packages.Include = []string{"@acme/*", "@acme-labs/*"}
		one := replayMappingFixture()
		one.Grants.Inline[0].Packages.Include = []string{"@acme/*"}
		if !cmp.Narrows(journaledMapping(two), journaledMapping(one)) {
			t.Error("an include list that loses a pattern and stays non-empty narrows")
		}
	})
	t.Run("R-CI-020 anything that adds, or changes which mapping is a tier's first match, does not narrow", func(t *testing.T) {
		leafless := mustNode(`{"all": [{"claim": "namespace_id", "equals": "4711"}]}`)
		for name, after := range map[string]mappingJournalState{
			"a leaf removed from all": with(func(m *Mapping) { m.Match = leafless }),
			"enabled":                 with(func(m *Mapping) { m.Enabled = false }),
			"priority":                with(func(m *Mapping) { m.Priority = 50 }),
			"subject kinds":           with(func(m *Mapping) { m.SubjectKinds = []string{SubjectIDToken, SubjectJobToken} }),
			"an added action":         with(func(m *Mapping) { m.Grants.Inline[0].Actions = append(m.Grants.Inline[0].Actions, claims.Overwrite) }),
			"an added policy":         with(func(m *Mapping) { m.Grants.Policies = append(m.Grants.Policies, "admin-all") }),
			"an added group":          with(func(m *Mapping) { m.Grants.Groups = []string{"release-managers"} }),
			"an added inline grant": with(func(m *Mapping) {
				m.Grants.Inline = append(m.Grants.Inline, claims.InlineGrant{
					Repositories: []string{"web/npm-public"}, Packages: &claims.PatternSet{Include: []string{"*"}},
					Actions: []claims.Action{claims.Read}, Ownership: claims.OwnershipNone,
				})
			}),
			"an emptied include": with(func(m *Mapping) { m.Grants.Inline[0].Packages.Include = nil }),
			"an exclude added":   with(func(m *Mapping) { m.Grants.Inline[0].Packages.Exclude = []string{"@acme/internal"} }),
			"a template changed": with(func(m *Mapping) { m.Grants.Inline[0].Packages.Include = []string{"@{{ project_path | slug }}/*"} }),
			"ownership":          with(func(m *Mapping) { m.Grants.Inline[0].Ownership = claims.OwnershipNone }),
			"packages to paths": with(func(m *Mapping) {
				m.Grants.Inline[0].Paths, m.Grants.Inline[0].Packages = m.Grants.Inline[0].Packages, nil
			}),
			"no grant left":            with(func(m *Mapping) { m.Grants = claims.Spec{} }),
			"an unbounded lifetime":    with(func(m *Mapping) { m.MaxTTLSeconds = nil }),
			"cross-project read_only":  with(func(m *Mapping) { m.Options.CrossProjectSource = claims.CrossProjectReadOnly }),
			"unprotected refs allowed": with(func(m *Mapping) { m.Options.AllowUnprotectedRefs = true }),
			"another tier":             with(func(m *Mapping) { m.Tier, m.Project = ProjectTier("web"), "web" }),
			"another name":             with(func(m *Mapping) { m.Name = "other" }),
			"an unreadable record":     {},
		} {
			if cmp.Narrows(journaledMapping(base), after) {
				t.Errorf("%s must not narrow", name)
			}
		}
	})
	t.Run("R-CI-020 deploy removed from an inline grant is applied; a leaf removed from all is suspended", func(t *testing.T) {
		cur := journaledMapping(base)
		noDeploy := with(func(m *Mapping) { m.Grants.Inline[0].Actions = []claims.Action{claims.Read} })
		if got := ciOutcome[mappingJournalState](t, cmp, cur, cur, noDeploy); got != journal.Applied {
			t.Errorf("deploy removed: %s, want applied", got)
		}
		leaf := noDeploy
		leaf.Record.Match = mustNode(`{"all": [{"claim": "namespace_id", "equals": "4711"}]}`)
		if got := ciOutcome[mappingJournalState](t, cmp, noDeploy, noDeploy, leaf); got != journal.Suspended {
			t.Errorf("a leaf removed: %s, want suspended", got)
		}
	})
}

func TestR_CI_020_JournaledRecordsAreReadStrictly(t *testing.T) {
	t.Run("R-CI-020 the producers' entries carry records the handlers read", func(t *testing.T) {
		is := replayIssuerFixture()
		narrowed := is
		narrowed.AllowedAudiences = []string{"https://trove.example.com"}
		ev := issuerEvent(audit.EventCIIssuerUpdated, audit.Actor{Kind: "user"}, &is, &narrowed, false, 0)
		var ie issuerJournalEntry
		if err := json.Unmarshal(ev.Journal, &ie); err != nil || ie.IssuerID != is.ID {
			t.Fatalf("issuer entry %s: %v", ev.Journal, err)
		}
		if !readIssuerState(ie.Before).Valid || !readIssuerState(ie.After).Valid {
			t.Fatalf("issuer records do not read: %s", ev.Journal)
		}
		if !(issuerNarrower{}).Narrows(journaledIssuer(is), readIssuerState(ie.After)) {
			t.Fatal("the journaled after-state must compare with the stored issuer")
		}

		m := replayMappingFixture()
		after := m
		after.Grants.Inline = []claims.InlineGrant{{
			Repositories: m.Grants.Inline[0].Repositories, Packages: m.Grants.Inline[0].Packages,
			Actions: []claims.Action{claims.Read}, Ownership: m.Grants.Inline[0].Ownership,
		}}
		mev := mappingEvent(audit.EventCIMappingUpdated, audit.Actor{Kind: "user"}, &m, &after, false)
		var me mappingJournalEntry
		if err := json.Unmarshal(mev.Journal, &me); err != nil || me.MappingID != m.ID || me.IssuerID != m.IssuerID || me.Tier != TierSystem || me.Priority != 100 {
			t.Fatalf("mapping entry %s: %v", mev.Journal, err)
		}
		if st := readMappingState(me.After); !st.Valid || !(mappingNarrower{}).Narrows(journaledMapping(m), st) {
			t.Fatalf("the journaled after-state must read and narrow: %s", me.After)
		}
		for _, name := range []string{audit.EventCIMappingDisabled, audit.EventCIMappingDeleted} {
			dev := mappingEvent(name, audit.Actor{Kind: "user"}, &m, nil, false)
			if err := json.Unmarshal(dev.Journal, &me); err != nil || me.MappingID != m.ID || !readMappingState(me.Before).Valid {
				t.Fatalf("%s entry %s: %v", name, dev.Journal, err)
			}
		}
	})
	t.Run("R-CI-020 a field the comparator does not know, a missing field or another type is not readable", func(t *testing.T) {
		raw := mustJSON(recordOfMapping(new(replayMappingFixture())))
		var good map[string]any
		_ = json.Unmarshal(raw, &good)
		if spaced, _ := json.MarshalIndent(good, "", "  "); !readMappingState(spaced).Valid {
			t.Fatal("reformatted JSON must read")
		}
		for name, f := range map[string]func(map[string]any){
			"unknown":               func(m map[string]any) { m["triggeringUser"] = true },
			"missing":               func(m map[string]any) { delete(m, "priority") },
			"type":                  func(m map[string]any) { m["enabled"] = "true" },
			"unknown grant member":  func(m map[string]any) { m["grants"].(map[string]any)["groupsFromClaim"] = "groups" },
			"unknown condition key": func(m map[string]any) { m["match"] = map[string]any{"claim": "x", "equals": "y", "or": 1} },
		} {
			m := map[string]any{}
			b, _ := json.Marshal(good)
			_ = json.Unmarshal(b, &m)
			f(m)
			bad, _ := json.Marshal(m)
			if readMappingState(bad).Valid {
				t.Errorf("%s: %s read as valid", name, bad)
			}
		}
		for _, bad := range []string{"", "null", "[]", "{", `{"id": 1}`} {
			if readIssuerState(json.RawMessage(bad)).Valid || readMappingState(json.RawMessage(bad)).Valid {
				t.Errorf("%q read as valid", bad)
			}
		}
	})
}

func TestR_CI_020_ADisabledMappingSuspendsTheLaterMappingsOfItsTier(t *testing.T) {
	m := func(id, issuer, tier string, priority int, enabled bool) Mapping {
		return Mapping{ID: id, IssuerID: issuer, Tier: tier, Priority: priority, Enabled: enabled}
	}
	disabled := m("mr-readonly", "i1", TierSystem, 10, true)
	all := []Mapping{
		m("read", "i1", TierSystem, 900, true),
		disabled,
		m("release-publish", "i1", TierSystem, 100, true),
		m("off", "i1", TierSystem, 500, false),
		m("before", "i1", TierSystem, 5, true),
		m("project", "i1", ProjectTier("web"), 200, true),
		m("other-issuer", "i2", TierSystem, 300, true),
	}
	got := fallThrough(all, disabled)
	if len(got) != 2 || got[0].ID != "release-publish" || got[1].ID != "read" {
		t.Fatalf("fallThrough = %+v, want release-publish then read: the enabled later mappings of the tier, no other tier or issuer", got)
	}
	if len(fallThrough(all, m("read", "i1", TierSystem, 900, true))) != 0 {
		t.Fatal("the last mapping of a tier lets nothing fall through")
	}
}

func TestR_CI_020_TheHandlersAreRegistered(t *testing.T) {
	for _, ev := range []string{
		audit.EventCIIssuerUpdated, audit.EventCIIssuerDisabled, audit.EventCIIssuerDeleted,
		audit.EventCIMappingUpdated, audit.EventCIMappingDisabled, audit.EventCIMappingDeleted,
	} {
		if h, ok := journal.HandlerFor(ev); !ok || h.Domain != "ci" {
			t.Errorf("HandlerFor(%s) = %q, %v", ev, h.Domain, ok)
		}
	}
	for _, ev := range []string{audit.EventCIIssuerCreated, audit.EventCIIssuerEnabled, audit.EventCIMappingCreated, audit.EventCIMappingEnabled} {
		if _, ok := journal.HandlerFor(ev); ok {
			t.Errorf("%s grants and must never be replayed", ev)
		}
	}
}
