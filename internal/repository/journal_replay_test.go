package repository

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/vuln"
)

func ref(id int64, key string) relRef { return relRef{id: id, key: key} }

func doc(mod func(d *Document)) *Document {
	d := &Document{Key: "npm-all", Class: Virtual, Format: "npm"}
	d.normalize()
	if mod != nil {
		mod(d)
	}
	return d
}

func vs(s vuln.Settings) *vuln.Settings { return &s }

// TestR_REPO_061_TheUpdateFindsEveryNarrowingPart: the comparison of an update with the stored document and rows names the
// parts of the table of R-REPO-061, and only the parts that narrow.
func TestR_REPO_061_TheUpdateFindsEveryNarrowingPart(t *testing.T) {
	s := &Service{cat: vulnCatalog()}
	virtual := Repository{ID: "7", Name: qname.Repo{Project: "web", Key: "npm-all"}, Class: Virtual, Format: "npm"}
	local := Repository{ID: "8", Name: qname.Repo{Project: "web", Key: "npm-local"}, Class: Local, Format: "npm"}
	remote := Repository{ID: "9", Name: qname.Repo{Project: "web", Key: "npmjs"}, Class: Remote, Format: "npm"}
	a, b, c := ref(1, "a"), ref(2, "b"), ref(3, "c")
	off := vuln.DefaultSettings()
	off.Enabled = false
	strict := vuln.DefaultSettings()
	strict.OnMissingData = vuln.MissingBlock
	lax := vuln.DefaultSettings()
	lax.BlockMalicious = false
	mixed := vuln.DefaultSettings()
	mixed.BlockMalicious = false
	mixed.MatchPublicAdvisories = vuln.MatchAlways
	falsy := false

	cases := []struct {
		name     string
		r        Repository
		cur, nd  *Document
		old, nrw *virtualRel
		want     []string
	}{
		{"nothing changed", remote, doc(nil), doc(nil), nil, nil, []string{}},
		{
			"an include list from empty", remote, doc(nil), doc(func(d *Document) { d.IncludePatterns = []string{"@acme/*"} }),
			nil, nil,
			[]string{"include"},
		},
		{
			"an include list that lost a pattern", remote,
			doc(func(d *Document) { d.IncludePatterns = []string{"@a/*", "@b/*"} }), doc(func(d *Document) { d.IncludePatterns = []string{"@a/*"} }),
			nil, nil,
			[]string{"include"},
		},
		{
			"an include list that lost one pattern and gained another", remote,
			doc(func(d *Document) { d.IncludePatterns = []string{"@a/*", "@b/*"} }), doc(func(d *Document) { d.IncludePatterns = []string{"@a/*", "@c/*"} }),
			nil, nil,
			[]string{"include"},
		},
		{
			"an include list that only gained a pattern widens", remote,
			doc(func(d *Document) { d.IncludePatterns = []string{"@a/*"} }), doc(func(d *Document) { d.IncludePatterns = []string{"@a/*", "@b/*"} }),
			nil, nil,
			[]string{},
		},
		{
			"an include list widened to a wildcard that takes in the old pattern", remote,
			doc(func(d *Document) { d.IncludePatterns = []string{"@acme/ui-*"} }), doc(func(d *Document) { d.IncludePatterns = []string{"@acme/*"} }),
			nil, nil,
			[]string{},
		},
		{
			"an include pattern changed in case only", remote,
			doc(func(d *Document) { d.IncludePatterns = []string{"@Acme/*"} }), doc(func(d *Document) { d.IncludePatterns = []string{"@acme/*"} }),
			nil, nil,
			[]string{},
		},
		{
			"an include pattern narrowed to a deeper literal", remote,
			doc(func(d *Document) { d.IncludePatterns = []string{"@acme/*"} }), doc(func(d *Document) { d.IncludePatterns = []string{"@acme/ui-*"} }),
			nil, nil,
			[]string{"include"},
		},
		{
			"an emptied include list admits everything", remote,
			doc(func(d *Document) { d.IncludePatterns = []string{"@a/*"} }), doc(nil), nil, nil,
			[]string{},
		},
		{
			"an exclude pattern added", remote, doc(nil), doc(func(d *Document) { d.ExcludePatterns = []string{"@evil/*"} }),
			nil, nil,
			[]string{"excludeAdded"},
		},
		{
			"an exclude pattern removed widens", remote,
			doc(func(d *Document) { d.ExcludePatterns = []string{"@evil/*"} }), doc(nil), nil, nil,
			[]string{},
		},
		{
			"autoClaim from the explicit false to the default", local,
			doc(func(d *Document) { d.Class = Local; d.Local = &LocalSettings{AutoClaim: &falsy} }), doc(func(d *Document) { d.Class = Local }),
			nil, nil,
			[]string{"autoClaim"},
		},
		{
			"autoClaim from the default to the explicit false widens", local,
			doc(func(d *Document) { d.Class = Local }), doc(func(d *Document) { d.Class = Local; d.Local = &LocalSettings{AutoClaim: &falsy} }),
			nil, nil,
			[]string{},
		},
		{
			"a member out", virtual, doc(nil), doc(nil),
			&virtualRel{members: []relRef{a, b}}, &virtualRel{members: []relRef{a}},
			[]string{"membersRemoved"},
		},
		{
			"a member swapped for another", virtual, doc(nil), doc(nil),
			&virtualRel{members: []relRef{a, b}}, &virtualRel{members: []relRef{a, c}},
			[]string{"membersRemoved"},
		},
		{
			"a member added or moved widens", virtual, doc(nil), doc(nil),
			&virtualRel{members: []relRef{a, b}}, &virtualRel{members: []relRef{b, a, c}},
			[]string{},
		},
		{
			"a pin rule added", virtual, doc(nil), doc(nil),
			&virtualRel{members: []relRef{a, b}}, &virtualRel{members: []relRef{a, b}, pins: []relPin{{patterns: []string{"@acme/*"}, members: []relRef{a}}}},
			[]string{"pinsAdded"},
		},
		{
			"a pin member removed", virtual, doc(nil), doc(nil),
			&virtualRel{members: []relRef{a, b}, pins: []relPin{{patterns: []string{"@acme/*"}, members: []relRef{a, b}}}},
			&virtualRel{members: []relRef{a, b}, pins: []relPin{{patterns: []string{"@acme/*"}, members: []relRef{a}}}},
			[]string{"pinMembersRemoved"},
		},
		{
			"a pin member added widens", virtual, doc(nil), doc(nil),
			&virtualRel{members: []relRef{a, b}, pins: []relPin{{patterns: []string{"@acme/*"}, members: []relRef{a}}}},
			&virtualRel{members: []relRef{a, b}, pins: []relPin{{patterns: []string{"@acme/*"}, members: []relRef{a, b}}}},
			[]string{},
		},
		{
			"a pin rule removed widens", virtual, doc(nil), doc(nil),
			&virtualRel{members: []relRef{a}, pins: []relPin{{patterns: []string{"@acme/*"}, members: []relRef{a}}}},
			&virtualRel{members: []relRef{a}},
			[]string{},
		},
		{
			"a pattern added to a rule that exists is a pattern no rule held", virtual, doc(nil), doc(nil),
			&virtualRel{members: []relRef{a}, pins: []relPin{{patterns: []string{"@acme/*"}, members: []relRef{a}}}},
			&virtualRel{members: []relRef{a}, pins: []relPin{{patterns: []string{"@acme/*", "@other/*"}, members: []relRef{a}}}},
			[]string{"pinsAdded"},
		},
		{
			"vulnerabilities stronger", remote,
			doc(func(d *Document) { d.Vulnerabilities = vs(off) }), doc(func(d *Document) { d.Vulnerabilities = vs(vuln.DefaultSettings()) }),
			nil, nil,
			[]string{"vulnerabilities"},
		},
		{
			"vulnerabilities from the absent member to a stronger one", remote,
			doc(nil), doc(func(d *Document) { d.Vulnerabilities = vs(strict) }), nil, nil,
			[]string{"vulnerabilities"},
		},
		{
			"vulnerabilities weaker", remote,
			doc(nil), doc(func(d *Document) { d.Vulnerabilities = vs(lax) }), nil, nil,
			[]string{},
		},
		{
			"vulnerabilities stronger in one field and weaker in another", remote,
			doc(nil), doc(func(d *Document) { d.Vulnerabilities = vs(mixed) }), nil, nil,
			[]string{"vulnerabilities"},
		},
		{
			"vulnerabilities set to the defaults they were", remote,
			doc(func(d *Document) { d.Vulnerabilities = vs(vuln.DefaultSettings()) }), doc(nil), nil, nil,
			[]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := s.narrowing(tc.r, tc.cur, tc.nd, tc.old, tc.nrw)
			if got := e.parts(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parts = %v, want %v (entry %+v)", got, tc.want, e)
			}
		})
	}

	t.Run("the entry carries only the narrowing half of a mixed update", func(t *testing.T) {
		cur := doc(func(d *Document) { d.IncludePatterns = []string{"@a/*", "@b/*"} })
		nd := doc(func(d *Document) { d.IncludePatterns = []string{"@a/*", "@c/*"} })
		e := s.narrowing(remote, cur, nd, nil, nil)
		if !slices.Equal(e.Include, []string{"@a/*", "@c/*"}) || e.RepositoryID != "9" || e.Key != "web/npmjs" {
			t.Fatalf("entry = %+v", e)
		}
	})
	t.Run("the pin and member parts name the members by ID and qualified key", func(t *testing.T) {
		e := s.narrowing(virtual, doc(nil), doc(nil),
			&virtualRel{members: []relRef{a, b}, pins: []relPin{{patterns: []string{"@acme/*"}, members: []relRef{a, b}}}},
			&virtualRel{members: []relRef{a}, pins: []relPin{{patterns: []string{"@acme/*"}, members: []relRef{a}}, {patterns: []string{"@new/*"}, members: nil}}})
		wantRemoved := []repoRef{{RepositoryID: "2", Key: "web/b"}}
		if !reflect.DeepEqual(e.MembersRemoved, wantRemoved) {
			t.Errorf("membersRemoved = %+v", e.MembersRemoved)
		}
		if !reflect.DeepEqual(e.PinMembersRemoved, []pinPart{{Pattern: "@acme/*", Members: wantRemoved}}) {
			t.Errorf("pinMembersRemoved = %+v", e.PinMembersRemoved)
		}
		if !reflect.DeepEqual(e.PinsAdded, []pinPart{{Pattern: "@new/*", Members: []repoRef{}}}) {
			t.Errorf("pinsAdded = %+v", e.PinsAdded)
		}
	})
	t.Run("the vulnerabilities part carries both complete members", func(t *testing.T) {
		e := s.narrowing(remote, doc(nil), doc(func(d *Document) { d.Vulnerabilities = vs(strict) }), nil, nil)
		if e.Vulnerabilities == nil || e.Vulnerabilities.Before != vuln.DefaultSettings() || e.Vulnerabilities.After != strict {
			t.Fatalf("vulnerabilities = %+v", e.Vulnerabilities)
		}
	})
}

func narrowedObject(raw string) journal.Object {
	return journal.Object{V: 1, ID: "0192f1c4-0000-7000-8000-0000000000a1", Event: EventNarrowed, Entry: json.RawMessage(raw)}
}

// TestR_REPO_061_NarrowedEntriesAreReadStrictly: an entry that is malformed, names a thing that is no ID, exceeds the lists a
// valid update writes, or carries nothing is refused by the reader before it acts on anything (R-HA-054, forged entries).
func TestR_REPO_061_NarrowedEntriesAreReadStrictly(t *testing.T) {
	many := func(n int, f string) string {
		items := make([]string, n)
		for i := range items {
			items[i] = f
		}
		return strings.Join(items, ",")
	}
	cases := map[string]string{
		"not an object":                                  `[]`,
		"no repository":                                  `{"key":"web/x","include":["a"]}`,
		"a repository that is no ID":                     `{"repositoryId":"web/x","include":["a"]}`,
		"a repository ID of zero":                        `{"repositoryId":"0","include":["a"]}`,
		"an include list that is a text":                 `{"repositoryId":"5","include":"a"}`,
		"too many include patterns":                      `{"repositoryId":"5","include":[` + many(MaxPatterns+1, `"a"`) + `]}`,
		"too many exclude patterns":                      `{"repositoryId":"5","excludeAdded":[` + many(MaxPatterns+1, `"a"`) + `]}`,
		"a member that is no ID":                         `{"repositoryId":"5","membersRemoved":[{"repositoryId":"x","key":"w/a"}]}`,
		"a pin member that is no ID":                     `{"repositoryId":"5","pinsAdded":[{"pattern":"a","members":[{"repositoryId":"-1","key":"w/a"}]}]}`,
		"too many members":                               `{"repositoryId":"5","membersRemoved":[` + many(maxEntryRefs+1, `{"repositoryId":"1","key":"w/a"}`) + `]}`,
		"a vulnerabilities member with an unknown field": `{"repositoryId":"5","vulnerabilities":{"before":{"enabled":true},"after":{"enabled":true,"extra":1}}}`,
		"a vulnerabilities value out of the set":         `{"repositoryId":"5","vulnerabilities":{"before":{"enabled":true},"after":{"matchPublicAdvisories":"sometimes"}}}`,
		"no part at all":                                 `{"repositoryId":"5","key":"web/x"}`,
		"autoClaim false is no part":                     `{"repositoryId":"5","autoClaim":false}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, why := readNarrowed(narrowedObject(raw)); why == "" {
				t.Fatalf("the entry %s was accepted", raw)
			}
		})
	}
	t.Run("a complete entry is read", func(t *testing.T) {
		raw := `{"repositoryId":"5","key":"web/npm-all","include":["@a/*"],"excludeAdded":["@evil/*"],"autoClaim":true,` +
			`"membersRemoved":[{"repositoryId":"2","key":"web/b"}],"pinsAdded":[{"pattern":"@new/*","members":[{"repositoryId":"1","key":"web/a"}]}],` +
			`"pinMembersRemoved":[{"pattern":"@acme/*","members":[{"repositoryId":"2","key":"web/b"}]}],` +
			`"vulnerabilities":{"before":{"enabled":false},"after":{"enabled":true,"matchPublicAdvisories":"always"}}}`
		e, id, why := readNarrowed(narrowedObject(raw))
		if why != "" || id != 5 {
			t.Fatalf("why = %q id = %d", why, id)
		}
		if got := e.parts(); len(got) != 7 {
			t.Fatalf("parts = %v", got)
		}
	})
}

// TestR_REPO_061_TheIncludeReplayKeepsTheIntersection: an empty restored list takes the journaled one, otherwise the patterns
// both lists hold stay in the restored order, and none left disables the repository without touching the list (Q-REPO-247).
func TestR_REPO_061_TheIncludeReplayKeepsTheIntersection(t *testing.T) {
	cases := []struct {
		name            string
		have, journaled []string
		want            []string
		suspended       bool
		changed         bool
	}{
		{"an empty restored list takes the journaled one", nil, []string{"@a/*", "@b/*"}, []string{"@a/*", "@b/*"}, false, true},
		{"a restored list that holds a pattern more keeps the shared ones", []string{"@a/*", "@x/*", "@b/*"}, []string{"@b/*", "@a/*"}, []string{"@a/*", "@b/*"}, false, true},
		{"a restored list that is the journaled one is in effect", []string{"@a/*"}, []string{"@a/*"}, []string{"@a/*"}, false, false},
		{"a restored list inside the journaled one is in effect", []string{"@a/*"}, []string{"@a/*", "@b/*"}, []string{"@a/*"}, false, false},
		{"no shared pattern disables and leaves the list", []string{"@x/*"}, []string{"@a/*"}, []string{"@x/*"}, true, false},
		{"a consolidating edit keeps the restored pattern the journaled one covers", []string{"@acme/ui-*", "@x/*"}, []string{"@acme/*"}, []string{"@acme/ui-*"}, false, true},
		{"a pattern spelled in another case is the same pattern", []string{"@A/*", "@x/*"}, []string{"@a/*"}, []string{"@A/*"}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &replayer{doc: map[string]json.RawMessage{}, set: Settings{IncludePatterns: tc.have}, cat: vulnCatalog(), format: "npm"}
			r.info, r.known = r.cat.Lookup("npm")
			r.include(tc.journaled)
			if !slices.Equal(r.set.IncludePatterns, tc.want) {
				t.Errorf("list = %v, want %v", r.set.IncludePatterns, tc.want)
			}
			if got := len(r.suspended) > 0; got != tc.suspended || r.set.Disabled != tc.suspended {
				t.Errorf("suspended = %v (disabled %v), want %v", got, r.set.Disabled, tc.suspended)
			}
			if got := len(r.changed) > 0; got != tc.changed {
				t.Errorf("changed = %v, want %v", got, tc.changed)
			}
			// A second pass on the result finds it in effect.
			second := &replayer{doc: map[string]json.RawMessage{}, set: Settings{IncludePatterns: r.set.IncludePatterns, Disabled: r.set.Disabled}, disabled: r.disabled, cat: r.cat, format: "npm", info: r.info, known: r.known}
			second.include(tc.journaled)
			if len(second.changed) > 0 || len(second.suspended) > 0 || second.touched {
				t.Errorf("a second pass changed %v / suspended %v", second.changed, second.suspended)
			}
		})
	}
}

// TestR_REPO_061_AnExcludeListIsExtendedAndNeverBeyondTheLimit: the journaled patterns the restored list lacks are added; a list
// that would exceed 100 patterns disables the repository instead.
func TestR_REPO_061_AnExcludeListIsExtendedAndNeverBeyondTheLimit(t *testing.T) {
	r := &replayer{doc: map[string]json.RawMessage{}, set: Settings{ExcludePatterns: []string{"a"}}}
	r.excludes([]string{"a", "b"})
	if !slices.Equal(r.set.ExcludePatterns, []string{"a", "b"}) || len(r.suspended) != 0 {
		t.Fatalf("list = %v suspended %v", r.set.ExcludePatterns, r.suspended)
	}
	full := make([]string, MaxPatterns)
	for i := range full {
		full[i] = "p" + string(rune('A'+i%26)) + strings.Repeat("x", i/26)
	}
	r = &replayer{doc: map[string]json.RawMessage{}, set: Settings{ExcludePatterns: full}}
	r.excludes([]string{"one-more"})
	if len(r.set.ExcludePatterns) != MaxPatterns || len(r.suspended) != 1 || !r.set.Disabled {
		t.Fatalf("list %d suspended %v disabled %v", len(r.set.ExcludePatterns), r.suspended, r.set.Disabled)
	}
}

// TestR_REPO_061_AStrongerMemberIsTheMoreBlockingOfBoth: each field takes the more blocking of the restored and the journaled
// value; a journaled weaker value changes nothing.
func TestR_REPO_061_AStrongerMemberIsTheMoreBlockingOfBoth(t *testing.T) {
	d := vuln.DefaultSettings()
	weak := vuln.Settings{Enabled: false, MatchPublicAdvisories: vuln.MatchNever, BlockMalicious: false, OnMissingData: vuln.MissingAllow}
	strong := vuln.Settings{Enabled: true, MatchPublicAdvisories: vuln.MatchAlways, BlockMalicious: true, OnMissingData: vuln.MissingBlock}
	if got := moreBlocking(weak, strong); got != strong {
		t.Errorf("weak + strong = %+v", got)
	}
	if got := moreBlocking(strong, weak); got != strong {
		t.Errorf("strong + weak = %+v", got)
	}
	if got := moreBlocking(d, d); got != d {
		t.Errorf("defaults + defaults = %+v", got)
	}
	mixed := vuln.Settings{Enabled: true, MatchPublicAdvisories: vuln.MatchNever, BlockMalicious: true, OnMissingData: vuln.MissingBlock}
	if got := moreBlocking(d, mixed); got.MatchPublicAdvisories != vuln.MatchAuto || got.OnMissingData != vuln.MissingBlock {
		t.Errorf("defaults + mixed = %+v", got)
	}
}

// TestR_REPO_061_ClaimsEntriesKeepOnlyCreatedAndWidenedClaims: a claim that is new or goes from members to global is part of
// the entry; a claim narrowed to members, kept as it was or removed is not (R-REPO-061).
func TestR_REPO_061_ClaimsEntriesKeepOnlyCreatedAndWidenedClaims(t *testing.T) {
	want := wantClaims{
		scope: map[string]string{"new-members": ScopeMembers, "new-global": ScopeGlobal, "widened": ScopeGlobal, "narrowed": ScopeMembers, "same": ScopeMembers, "same-global": ScopeGlobal},
		order: []string{"new-members", "new-global", "widened", "narrowed", "same", "same-global"},
	}
	manual := map[string]string{"widened": ScopeMembers, "narrowed": ScopeGlobal, "same": ScopeMembers, "same-global": ScopeGlobal, "dropped": ScopeMembers}
	added, journaled, removed, touchesGlobal := diffClaims(want, manual)
	if len(added) != 4 {
		t.Errorf("added (the event's details) = %+v", added)
	}
	wantJournal := []claimChange{{"new-members", ScopeMembers}, {"new-global", ScopeGlobal}, {"widened", ScopeGlobal}}
	if !reflect.DeepEqual(journaled, wantJournal) {
		t.Errorf("journaled = %+v, want %+v", journaled, wantJournal)
	}
	if !slices.Equal(removed, []string{"dropped"}) || !touchesGlobal {
		t.Errorf("removed = %v touchesGlobal = %v", removed, touchesGlobal)
	}
	_, journaled, _, _ = diffClaims(wantClaims{scope: map[string]string{}}, map[string]string{"gone": ScopeGlobal})
	if len(journaled) != 0 {
		t.Errorf("a removal journals %+v", journaled)
	}
}

// TestR_REPO_061_ClaimsEntriesAreReadStrictly: the reader refuses an entry whose source, scope or ID is not one.
func TestR_REPO_061_ClaimsEntriesAreReadStrictly(t *testing.T) {
	obj := func(raw string) journal.Object {
		return journal.Object{V: 1, ID: "0192f1c4-0000-7000-8000-0000000000a1", Event: EventClaimsChanged, Entry: json.RawMessage(raw)}
	}
	for name, raw := range map[string]string{
		"not an ID":       `{"repositoryId":"x","source":"auto","added":[]}`,
		"another source":  `{"repositoryId":"5","source":"import","added":[]}`,
		"another scope":   `{"repositoryId":"5","source":"manual","added":[{"pattern":"a","scope":"all"}]}`,
		"added as a text": `{"repositoryId":"5","source":"manual","added":"a"}`,
		"added missing":   `{"repositoryId":"5","key":"web/x","source":"manual"}`,
		"added null":      `{"repositoryId":"5","source":"auto","added":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, why := readClaims(obj(raw)); why == "" {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
	if e, id, why := readClaims(obj(`{"repositoryId":"5","key":"web/x","source":"manual","added":[]}`)); why != "" || id != 5 || e.Source != SourceManual {
		t.Errorf("a removal-only entry: %+v %d %q", e, id, why)
	}
}

// TestR_REPO_061_APinRuleOverlapsAClaimOrAnotherRule: the test of step 6 that decides between adding a rule and disabling.
func TestR_REPO_061_APinRuleOverlapsAClaimOrAnotherRule(t *testing.T) {
	cat := vulnCatalog()
	pat := func(p string) ClaimPattern {
		c, err := parseFolded(cat, "npm", p)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	members := []relRef{ref(1, "a"), ref(2, "b")}
	claims := []q.JournalFormatClaimsRow{
		{RepoID: 1, Pattern: "@own/*", Scope: ScopeMembers},
		{RepoID: 99, Pattern: "@elsewhere/*", Scope: ScopeMembers},
		{RepoID: 98, Pattern: "@everyone/*", Scope: ScopeGlobal},
	}
	rules := []relPin{{patterns: []string{"@pinned/*"}, members: members[:1]}}
	for _, tc := range []struct {
		pattern string
		want    bool
	}{
		{"@free/*", false},
		{"@own/lib", true},        // a claim of a member
		{"@elsewhere/lib", false}, // a members claim of a repository outside: not in effect here
		{"@everyone/lib", true},   // a global claim
		{"@pinned/lib", true},     // another rule
		{"@pinned/*", true},
	} {
		if got := pinOverlaps(cat, "npm", pat(tc.pattern), rules, members, claims); got != tc.want {
			t.Errorf("%s: overlaps = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

// TestR_HA_054_TheRepositoryHandlerIsRegistered: both events have the one handler, and the audit registry flags them with a
// required entry.
func TestR_HA_054_TheRepositoryHandlerIsRegistered(t *testing.T) {
	for _, name := range []string{EventNarrowed, EventClaimsChanged} {
		h, ok := journal.HandlerFor(name)
		if !ok || h.Domain != "repository" {
			t.Errorf("%s: handler %+v registered %v", name, h, ok)
		}
		typ, ok := audit.Lookup(name)
		if !ok || typ.Journal != audit.JournalRevocation || typ.Entry != audit.EntryRequired {
			t.Errorf("%s: type %+v", name, typ)
		}
	}
}

// TestR_REPO_061_APatternCoversOnlyWhatItProvablyTakesIn: the containment the writer uses to tell a widened include pattern from a
// narrowed one proves two cases and refuses the rest.
func TestR_REPO_061_APatternCoversOnlyWhatItProvablyTakesIn(t *testing.T) {
	for _, tc := range []struct {
		q, p string
		want bool
	}{
		{"@acme/*", "@acme/ui-*", true},
		{"@acme/*", "@acme/ui", true},
		{"@acme/*", "@acme/a/b", false}, // a star stops at a slash
		{"@acme/**", "@acme/a/b", true},
		{"@acme/*", "@acme/**", false},
		{"@acme/*", "@acme/ui-[ab]", false},
		{"a?*", "ab*", false}, // a wildcard before the tail is not proved
		{"@acme/ui-*", "@acme/*", false},
		{"left-pad", "left-pad", true},
		{"left-pad", "left-pa*", false},
	} {
		if got := patternCovers(tc.q, tc.p); got != tc.want {
			t.Errorf("patternCovers(%q, %q) = %v, want %v", tc.q, tc.p, got, tc.want)
		}
	}
}
