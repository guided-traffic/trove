package project

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.Invalid || e.Code != errs.CodeValidation {
		t.Fatalf("not a validation error: %v", err)
	}
	out := map[string]string{}
	for _, f := range e.Fields {
		out[f.Pointer] = f.Code
	}
	return out
}

func TestR_AUTHZ_026_KeyRule(t *testing.T) {
	for _, key := range []string{"web", "ab", "a-b", "web-app-2", "a" + strings.Repeat("b", 31)} {
		if !KeyRE.MatchString(key) {
			t.Errorf("%q should be a key", key)
		}
	}
	for _, key := range []string{"", "a", "1web", "-web", "Web", "web_app", "web.app", "a" + strings.Repeat("b", 32), "web app"} {
		if KeyRE.MatchString(key) {
			t.Errorf("%q should not be a key", key)
		}
	}
}

func TestR_AUTHZ_026_ValidationOfTheFields(t *testing.T) {
	ok := Input{Key: "web", DisplayName: "Web platform", Rights: DefaultRights()}
	if err := problems(ok, true); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	t.Run("R-AUTHZ-026 the key is checked on create only", func(t *testing.T) {
		bad := ok
		bad.Key = "Web"
		if got := fieldsOf(t, problems(bad, true)); got["/key"] != "invalid_key" {
			t.Errorf("create: %v", got)
		}
		if err := problems(bad, false); err != nil {
			t.Errorf("replace ignores the key: %v", err)
		}
	})
	t.Run("R-AUTHZ-026 the display name has 1 to 128 characters and is not blank", func(t *testing.T) {
		for _, name := range []string{"", "   ", strings.Repeat("x", 129)} {
			in := ok
			in.DisplayName = name
			if got := fieldsOf(t, problems(in, true)); got["/displayName"] != "invalid_display_name" {
				t.Errorf("%d characters: %v", len(name), got)
			}
		}
		in := ok
		in.DisplayName = strings.Repeat("ä", 128) // 128 characters, 256 bytes
		if err := problems(in, true); err != nil {
			t.Errorf("128 characters: %v", err)
		}
	})
	t.Run("R-AUTHZ-026 the description has at most 1024 characters", func(t *testing.T) {
		in := ok
		in.Description = strings.Repeat("x", 1025)
		if got := fieldsOf(t, problems(in, true)); got["/description"] != "invalid_description" {
			t.Errorf("%v", got)
		}
		in.Description = strings.Repeat("x", 1024)
		if err := problems(in, true); err != nil {
			t.Errorf("1024: %v", err)
		}
	})
	t.Run("R-AUTHZ-026 every bad field is reported", func(t *testing.T) {
		got := fieldsOf(t, problems(Input{Key: "X"}, true))
		if len(got) != 2 {
			t.Errorf("%v", got)
		}
	})
}

func TestR_AUTHZ_026_AdministratorReferences(t *testing.T) {
	kind, name, ok, err := parseAdminRef("group:web-leads")
	if err != nil || !ok || kind != KindGroup || name != "web-leads" {
		t.Errorf("group: %v %v %v %v", kind, name, ok, err)
	}
	kind, name, ok, err = parseAdminRef("user:alice")
	if err != nil || !ok || kind != KindUser || name != "alice" {
		t.Errorf("user: %v %v %v %v", kind, name, ok, err)
	}
	for _, ref := range []string{"sa:ci-bot", "ci:gitlab/main", "anonymous", "authenticated"} {
		if _, _, ok, err := parseAdminRef(ref); err != nil || ok {
			t.Errorf("%s may not administer: ok=%v err=%v", ref, ok, err)
		}
	}
	for _, ref := range []string{"", "alice", "user:", "robot:x"} {
		if _, _, _, err := parseAdminRef(ref); err == nil {
			t.Errorf("%q is malformed", ref)
		} else if got := fieldsOf(t, err); got["/principalRef"] != "invalid_principal" {
			t.Errorf("%q: %v", ref, got)
		}
	}
	if got := fieldsOf(t, notAdministrator("sa:ci-bot")); got["/principalRef"] != "principal_kind_not_allowed" {
		t.Errorf("%v", got)
	}
}

// A lookup that violates the bounds is refused before it touches the database: the
// service has no DB here, so reaching it would panic.
func TestR_AUTHZ_026_LookupBounds(t *testing.T) {
	s := &service{}
	ctx := context.Background()
	for _, prefix := range []string{"", "a", "ä"} {
		if _, err := s.LookupPrincipals(ctx, prefix, "", 0); err == nil {
			t.Errorf("a prefix of %q was accepted", prefix)
		} else if got := fieldsOf(t, err); got["/q"] != "prefix_too_short" {
			t.Errorf("%q: %v", prefix, got)
		}
	}
	if _, err := s.LookupPrincipals(ctx, "al", "service_account", 0); err == nil {
		t.Error("a kind that cannot administer was accepted")
	}
	if _, err := s.LookupPrincipals(ctx, "al", "", -1); err == nil {
		t.Error("a negative limit was accepted")
	}
	if MaxLookup != 20 || MinLookupPrefix != 2 {
		t.Errorf("the bounds of Q-AUTHZ-182 are 2 and 20, not %d and %d", MinLookupPrefix, MaxLookup)
	}
}

func rawOf(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestR_AUTHZ_031_ProjectEventsCarryTheirJournalEntries(t *testing.T) {
	actor := authz.Subject{Kind: principal.KindUser, ID: "u1", Name: "root"}
	p := Project{ID: "p1", Key: "web", DisplayName: "Web", Rights: Rights{ManagePolicies: true}, Version: 4}
	before := stateOf(Project{DisplayName: "Web", Rights: Rights{ManagePolicies: true, ManageRepositories: true}})
	after := stateOf(p)

	t.Run("R-AUTHZ-031 an update journals the two switches before and after", func(t *testing.T) {
		ev := projectEvent(audit.EventProjectUpdated, actor, p, before, after, 9, nil)
		j := rawOf(t, ev.Journal)
		if j["projectId"] != "p1" || j["project"] != "web" {
			t.Errorf("identity: %v", j)
		}
		b, a := j["before"].(map[string]any), j["after"].(map[string]any)
		if b["manageRepositories"] != true || a["manageRepositories"] != false || a["managePolicies"] != true {
			t.Errorf("switches: %v", j)
		}
		if _, leaked := b["displayName"]; leaked {
			t.Errorf("the journal holds what is not access-bearing: %v", b)
		}
		if d := rawOf(t, ev.Details); d["epoch"] != float64(9) || d["version"] != float64(4) {
			t.Errorf("details: %v", d)
		}
	})
	t.Run("R-AUTHZ-031 a delete journals the ID and the key, and lists the policies it deleted", func(t *testing.T) {
		ev := projectEvent(audit.EventProjectDeleted, actor, p, before, nil, 10, []string{"web-a", "web-b"})
		if j := rawOf(t, ev.Journal); j["projectId"] != "p1" || j["project"] != "web" {
			t.Errorf("%v", j)
		}
		if d := rawOf(t, ev.Details); len(d["policiesDeleted"].([]any)) != 2 {
			t.Errorf("%v", d)
		}
	})
	t.Run("R-AUTHZ-031 a removed administrator and an unassigned repository journal IDs", func(t *testing.T) {
		ev := adminEvent(audit.EventProjectAdminRemoved, actor, p, PrincipalRef{Kind: KindGroup, Name: "web-leads", ID: "g1"}, 11)
		if j := rawOf(t, ev.Journal); j["principalId"] != "g1" || j["principal"] != "group:web-leads" || j["projectId"] != "p1" {
			t.Errorf("admin: %v", j)
		}
		ev = assignmentEvent(audit.EventProjectRepositoryUnassigned, actor, p, repoMove{ID: "7", Key: "web-npm", Moved: true, Other: "docs"}, 12)
		if j := rawOf(t, ev.Journal); j["repositoryId"] != "7" || j["repository"] != "web-npm" || j["projectId"] != "p1" {
			t.Errorf("unassign: %v", j)
		}
		if d := rawOf(t, ev.Details); d["otherProject"] != "docs" || d["moved"] != true {
			t.Errorf("details: %v", d)
		}
	})
	t.Run("R-AUTHZ-031 the events that grant carry no journal entry", func(t *testing.T) {
		for _, ev := range []audit.Event{
			projectEvent(audit.EventProjectCreated, actor, p, nil, after, 1, nil),
			adminEvent(audit.EventProjectAdminAdded, actor, p, PrincipalRef{Kind: KindUser, Name: "alice", ID: "u2"}, 2),
			assignmentEvent(audit.EventProjectRepositoryAssigned, actor, p, repoMove{ID: "7", Key: "web-npm"}, 3),
		} {
			if ev.Journal != nil {
				t.Errorf("%s has a journal entry", ev.Type)
			}
		}
	})
	t.Run("R-AUTHZ-031 the flagged types of the registry are the ones that journal", func(t *testing.T) {
		for name, want := range map[string]bool{
			audit.EventProjectCreated: false, audit.EventProjectUpdated: true, audit.EventProjectDeleted: true,
			audit.EventProjectAdminAdded: false, audit.EventProjectAdminRemoved: true,
			audit.EventProjectRepositoryAssigned: false, audit.EventProjectRepositoryUnassigned: true,
		} {
			et, ok := audit.Lookup(name)
			if !ok {
				t.Fatalf("%s is not registered", name)
			}
			if got := et.Journal == audit.JournalRevocation; got != want {
				t.Errorf("%s: journal=%v, want %v", name, got, want)
			}
		}
	})
}

func TestR_AUTHZ_026_ReferencesOfTheCreator(t *testing.T) {
	for _, tc := range []struct{ kind, name, want string }{
		{"", "", ""},
		{"user", "root", "user:root"},
		{"group", "ops", "group:ops"},
		{"service_account", "bot", "sa:bot"},
		{"ci_mapping", "gl/main", "ci:gl/main"},
	} {
		if got := refOfRow(tc.kind, tc.name); got != tc.want {
			t.Errorf("%s/%s: %q", tc.kind, tc.name, got)
		}
	}
}

func TestR_AUTHZ_026_EmptyProject(t *testing.T) {
	if !(Project{}).Empty() || (Project{Repositories: 1}).Empty() || (Project{ServiceAccounts: 1}).Empty() {
		t.Error("a project is empty when it has no repository and no service account")
	}
	if err := checkDeletable(Project{Key: "web", Repositories: 1, Version: 2}, errs.AnyRevision); err == nil {
		t.Error("a project with a repository was deletable")
	} else if e, _ := errs.As(err); e.Code != errs.CodeProjectNotEmpty {
		t.Errorf("%v", err)
	}
	if rev, ok := errs.CurrentRevision(checkDeletable(Project{Key: "web", Version: 2}, 1)); !ok || rev != 2 {
		t.Errorf("a stale tag answers 412 with the current version: %d %v", rev, ok)
	}
	if err := checkDeletable(Project{Key: "web", Version: 2}, 2); err != nil {
		t.Errorf("%v", err)
	}
}
