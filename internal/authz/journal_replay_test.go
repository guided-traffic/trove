package authz

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/journal"
)

func pstate(include, exclude []string, entries ...journalEntry) *journalState {
	return &journalState{
		ResourceType: "repository", Selectors: []journalSelector{{Kind: "key", RepositoryID: 7}},
		Packages: PatternLists{Include: include, Exclude: exclude}, Paths: PatternLists{Include: []string{}, Exclude: []string{}},
		Entries: entries,
	}
}

func pent(p string, actions ...string) journalEntry {
	return journalEntry{PrincipalID: p, Actions: actions}
}

// TestR_AUTHZ_031_PolicyComparator: Narrows answers true only when it can prove that the after-state grants nothing the current
// state does not, and false whenever it cannot tell.
func TestR_AUTHZ_031_PolicyComparator(t *testing.T) {
	cur := pstate([]string{"@web/*"}, nil, pent("a", "deploy", "read"), pent("b", "read"))
	cases := []struct {
		name  string
		after *journalState
		want  bool
	}{
		{"identical", pstate([]string{"@web/*"}, nil, pent("a", "deploy", "read"), pent("b", "read")), true},
		{"an action fewer", pstate([]string{"@web/*"}, nil, pent("a", "read"), pent("b", "read")), true},
		{"an entry fewer", pstate([]string{"@web/*"}, nil, pent("a", "deploy", "read")), true},
		{"an exclude more", pstate([]string{"@web/*"}, []string{"@web/secret"}, pent("a", "read")), true},
		{"an action more", pstate([]string{"@web/*"}, nil, pent("a", "delete", "deploy", "read")), false},
		{"a principal more", pstate([]string{"@web/*"}, nil, pent("a", "read"), pent("c", "read")), false},
		{"an emptied include list is unrestricted", pstate(nil, nil, pent("a", "read")), false},
		{"a different pattern is not glob-contained", pstate([]string{"@web/ui-*"}, nil, pent("a", "read")), false},
		{"an include more", pstate([]string{"@web/*", "@other/*"}, nil, pent("a", "read")), false},
		{"another resource type", &journalState{ResourceType: "build", Selectors: cur.Selectors, Packages: cur.Packages, Paths: cur.Paths}, false},
		{"another project", &journalState{ResourceType: "repository", Project: "web", Selectors: cur.Selectors, Packages: cur.Packages, Paths: cur.Paths}, false},
		{"a selector more", &journalState{ResourceType: "repository", Selectors: []journalSelector{{Kind: "key", RepositoryID: 7}, {Kind: "class", Class: "any"}}, Packages: cur.Packages, Paths: cur.Paths}, false},
		{"another repository", &journalState{ResourceType: "repository", Selectors: []journalSelector{{Kind: "key", RepositoryID: 8}}, Packages: cur.Packages, Paths: cur.Paths}, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (policyOrder{}).Narrows(cur, c.after); got != c.want {
				t.Errorf("Narrows = %v, want %v", got, c.want)
			}
		})
	}
	if (policyOrder{}).Narrows(nil, cur) {
		t.Error("a nil current state never narrows")
	}
	// An exclude of the current state that the after-state drops widens it.
	withExclude := pstate([]string{"@web/*"}, []string{"@web/secret"}, pent("a", "read"))
	if (policyOrder{}).Narrows(withExclude, pstate([]string{"@web/*"}, nil, pent("a", "read"))) {
		t.Error("dropping an exclude must not count as narrowing")
	}
}

// TestR_AUTHZ_031_UpdateRuleOnPolicies: the three outcomes of the update rule with the policy comparator, in the order of the
// acceptance criteria.
func TestR_AUTHZ_031_UpdateRuleOnPolicies(t *testing.T) {
	cur := pstate([]string{"@web/*"}, nil, pent("a", "deploy", "read"))
	run := func(before, after *journalState) journal.Outcome {
		out, _, err := journal.ApplyUpdate[*journalState](policyOrder{}, cur, before, after, journal.UpdateActions{
			Apply: func() error { return nil }, Suspend: func() (json.RawMessage, error) { return nil, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := run(cur, pstate([]string{"@web/*"}, nil, pent("a", "read"))); got != journal.Applied {
		t.Errorf("narrowing = %s, want applied", got)
	}
	if got := run(cur, pstate(nil, nil, pent("a", "deploy", "read"))); got != journal.Skipped {
		t.Errorf("emptied include = %s, want skipped", got)
	}
	if got := run(cur, pstate([]string{"@web/ui-*"}, nil, pent("a", "deploy", "read"))); got != journal.Suspended {
		t.Errorf("replaced pattern = %s, want suspended", got)
	}
	// A forged before-state can only choose between skipping and suspending: it never produces an application.
	forged := pstate([]string{"@web/*"}, nil, pent("a", "delete", "deploy", "read"), pent("x", "manage"))
	if got := run(forged, pstate([]string{"@web/*"}, nil, pent("a", "delete", "deploy", "read"), pent("x", "manage"))); got == journal.Applied {
		t.Errorf("a broadening was applied")
	}
}

// TestR_AUTHZ_031_SettingsComparator: the order of each access setting, most restrictive first, and the conservative handling
// of what the replay cannot read.
func TestR_AUTHZ_031_SettingsComparator(t *testing.T) {
	cases := []struct {
		name         string
		current, aft repoSettings
		want         bool
	}{
		{"cache fill to deploy", repoSettings{"cacheFillAction": "read"}, repoSettings{"cacheFillAction": "deploy"}, true},
		{"cache fill to read", repoSettings{"cacheFillAction": "deploy"}, repoSettings{"cacheFillAction": "read"}, false},
		{"ownership to off", repoSettings{"packageOwnership": "enforce"}, repoSettings{"packageOwnership": "off"}, false},
		{"authentication required", repoSettings{"requireAuthentication": "false"}, repoSettings{"requireAuthentication": "true"}, true},
		{"authentication lifted", repoSettings{"requireAuthentication": "true"}, repoSettings{"requireAuthentication": "false"}, false},
		{"hide existence from inherit to true", repoSettings{"hideExistence": "inherit"}, repoSettings{"hideExistence": "true"}, true},
		{"hide existence from inherit to false", repoSettings{"hideExistence": "inherit"}, repoSettings{"hideExistence": "false"}, false},
		{"hide existence to inherit", repoSettings{"hideExistence": "false"}, repoSettings{"hideExistence": "inherit"}, true},
		{"hide existence to inherit from true", repoSettings{"hideExistence": "true"}, repoSettings{"hideExistence": "inherit"}, false},
		{"one narrowing and one broadening", repoSettings{"cacheFillAction": "read", "requireAuthentication": "true"}, repoSettings{"cacheFillAction": "deploy", "requireAuthentication": "false"}, false},
		{"a setting the current state does not set", repoSettings{}, repoSettings{"packageOwnership": "enforce"}, false},
		{"a value outside the order", repoSettings{"cacheFillAction": "read"}, repoSettings{"cacheFillAction": "write"}, false},
		{"an unknown setting", repoSettings{"x": "a"}, repoSettings{"x": "a"}, false},
		{"nothing named", repoSettings{"cacheFillAction": "read"}, repoSettings{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (settingsOrder{}).Narrows(c.current, c.aft); got != c.want {
				t.Errorf("Narrows = %v, want %v", got, c.want)
			}
		})
	}
}

func TestR_AUTHZ_031_WriteSettingOnlyWhereItApplies(t *testing.T) {
	doc := map[string]json.RawMessage{"description": json.RawMessage(`"x"`), "remote": json.RawMessage(`{"url":"https://u","n":12345678901234567890}`)}
	if writeSetting("local", doc, "cacheFillAction", "deploy") {
		t.Error("a local repository has no cacheFillAction")
	}
	if !writeSetting("remote", doc, "cacheFillAction", "deploy") {
		t.Fatal("a remote repository has one")
	}
	raw, _ := json.Marshal(doc)
	if string(raw) != `{"description":"x","remote":{"cacheFillAction":"deploy","n":12345678901234567890,"url":"https://u"}}` {
		t.Errorf("the other members must survive untouched: %s", raw)
	}
}
