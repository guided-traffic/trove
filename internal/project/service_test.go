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
	"github.com/guided-traffic/trove/internal/qname"
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
	ok := Input{Key: "web", DisplayName: "Web platform"}
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

// TestR_AUTHZ_026_MemberReferences: a member is a user or a group (Q-AUTHZ-207); every other kind of principal
// answers 422 principal_not_allowed (Q-AUTHZ-215), a reference that does not parse 422 validation_failed.
func TestR_AUTHZ_026_MemberReferences(t *testing.T) {
	kind, name, err := parseMemberRef("group:web-leads")
	if err != nil || kind != KindGroup || name != "web-leads" {
		t.Errorf("group: %v %v %v", kind, name, err)
	}
	kind, name, err = parseMemberRef("user:alice")
	if err != nil || kind != KindUser || name != "alice" {
		t.Errorf("user: %v %v %v", kind, name, err)
	}
	for _, ref := range []string{"sa:web/partner", "sa:ci-bot", "ci:gitlab/main", "ci:gitlab/web/release", "anonymous", "authenticated", "bastion:partners"} {
		_, _, err := parseMemberRef(ref)
		if got := fieldsOf(t, err); got["/principalRef"] != errs.CodePrincipalNotAllowed {
			t.Errorf("%s is no member kind: %v", ref, err)
		}
	}
	for _, ref := range []string{"", "alice", "user:", "robot:x"} {
		if _, _, err := parseMemberRef(ref); err == nil {
			t.Errorf("%q is malformed", ref)
		} else if got := fieldsOf(t, err); got["/principalRef"] != "invalid_principal" {
			t.Errorf("%q: %v", ref, got)
		}
	}
}

// TestR_AUTHZ_026_RolesOfAMember: a role is one of the four; the service refuses every other value before it
// touches the database.
func TestR_AUTHZ_026_RolesOfAMember(t *testing.T) {
	for _, r := range []Role{RoleReader, RoleDeveloper, RoleMaintainer, RoleOwner} {
		if err := validRole(r); err != nil {
			t.Errorf("%s: %v", r, err)
		}
	}
	if got := fieldsOf(t, validRole(authz.ProjectRoleNone)); got["/role"] != "invalid_role" {
		t.Errorf("no role: %v", got)
	}
	s := &service{}
	if _, _, _, err := s.AddMember(context.Background(), authz.Subject{}, "web", "user:dave", authz.ProjectRoleNone, false); err == nil {
		t.Error("a member without a role was accepted")
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
	p := Project{ID: "p1", Key: "web", DisplayName: "Web", Version: 4}
	before := stateOf(Project{DisplayName: "Web"})
	after := stateOf(p)

	t.Run("R-AUTHZ-031 an update changes nothing access-bearing and journals nothing", func(t *testing.T) {
		ev := projectEvent(audit.EventProjectUpdated, actor, p, before, after, 9, nil)
		if ev.Journal != nil || ev.Project != "web" {
			t.Errorf("journal %s, project %q", ev.Journal, ev.Project)
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
	t.Run("R-AUTHZ-031 the events that grant carry no journal entry", func(t *testing.T) {
		for _, ev := range []audit.Event{
			projectEvent(audit.EventProjectCreated, actor, p, nil, after, 1, nil),
		} {
			if ev.Journal != nil {
				t.Errorf("%s has a journal entry", ev.Type)
			}
		}
	})
	t.Run("R-AUTHZ-031 the flagged types of the registry are the ones that journal", func(t *testing.T) {
		for name, want := range map[string]bool{
			audit.EventProjectCreated: false, audit.EventProjectUpdated: false, audit.EventProjectDeleted: true,
			audit.EventProjectMemberAdded: false, audit.EventProjectMemberChanged: true, audit.EventProjectMemberRemoved: true,
			audit.EventRepositoryMoved: true,
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

// TestR_AUTHZ_031_MemberEventsCarryTheirJournalEntries: a removed member and a changed role narrow rights and
// carry the journal entry of R-AUTHZ-031 (the project and the principal by ID and reference, the roles of a
// change); an added member grants and carries none. Every event names the project (Q-AUTHZ-209).
func TestR_AUTHZ_031_MemberEventsCarryTheirJournalEntries(t *testing.T) {
	actor := authz.Subject{Kind: principal.KindUser, ID: "u1", Name: "root"}
	dave := PrincipalRef{Kind: KindUser, Name: "dave", ID: "p-dave"}
	added := memberEvent(audit.EventProjectMemberAdded, actor, "web", "pid", dave, 7, authz.ProjectRoleNone, RoleMaintainer)
	if added.Journal != nil || added.Project != "web" {
		t.Errorf("added: journal %s, project %q", added.Journal, added.Project)
	}
	if d := rawOf(t, added.Details); d["role"] != "maintainer" || d["principal"] != "user:dave" || d["epoch"] != float64(7) {
		t.Errorf("added: %v", d)
	}
	changed := memberEvent(audit.EventProjectMemberChanged, actor, "web", "pid", dave, 8, RoleMaintainer, RoleReader)
	if j := rawOf(t, changed.Journal); j["projectId"] != "pid" || j["principalId"] != "p-dave" || j["before"] != "maintainer" || j["after"] != "reader" {
		t.Errorf("changed: %v", j)
	}
	removed := memberEvent(audit.EventProjectMemberRemoved, actor, "web", "pid", dave, 9, RoleReader, authz.ProjectRoleNone)
	if j := rawOf(t, removed.Journal); j["project"] != "web" || j["principal"] != "user:dave" {
		t.Errorf("removed: %v", j)
	}
	if d := rawOf(t, removed.Details); d["role"] != "reader" {
		t.Errorf("removed: %v", d)
	}
	for _, ev := range []audit.Event{added, changed, removed} {
		withinSchema(t, ev)
	}
}

// withinSchema checks the details of an event against the data schema its type registers: every member is a
// property of the schema and every required one is there.
func withinSchema(t *testing.T, ev audit.Event) {
	t.Helper()
	et, ok := audit.Lookup(ev.Type)
	if !ok {
		t.Fatalf("%s is not registered", ev.Type)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal([]byte(et.Schema), &schema); err != nil {
		t.Fatal(err)
	}
	d := rawOf(t, ev.Details)
	for k := range d {
		if _, ok := schema.Properties[k]; !ok {
			t.Errorf("%s: %q is no property of the schema", ev.Type, k)
		}
	}
	for _, k := range schema.Required {
		if _, ok := d[k]; !ok {
			t.Errorf("%s: the required %q is missing", ev.Type, k)
		}
	}
}

// TestR_REPO_060_TheMovedEvent: repository.moved names the repository by ID and its former address, both
// projects by ID and key, and journals them (R-AUTHZ-031); repository.moved_in records the same move in the
// target project under the new address, without a journal entry (Q-REPO-233).
func TestR_REPO_060_TheMovedEvent(t *testing.T) {
	actor := authz.Subject{Kind: principal.KindUser, ID: "u1", Name: "root"}
	from, to := qname.Repo{Project: "web", Key: "npm-libs"}, qname.Repo{Project: "mobile", Key: "npm-libs"}
	ev, in := movedEvents(actor, from, to, "42", "pw", "pm", 5, 11)
	if ev.Type != audit.EventRepositoryMoved || ev.Repository != "web/npm-libs" || ev.Project != "web" || ev.Subject.ID != "42" {
		t.Errorf("%+v", ev)
	}
	d := rawOf(t, ev.Details)
	if d["from"] != "web" || d["to"] != "mobile" || d["repository"] != "npm-libs" || d["configVersion"] != float64(5) {
		t.Errorf("details %v", d)
	}
	j := rawOf(t, ev.Journal)
	if j["repositoryId"] != "42" || j["from"].(map[string]any)["id"] != "pw" || j["to"].(map[string]any)["key"] != "mobile" {
		t.Errorf("journal %v", j)
	}
	withinSchema(t, ev)
	t.Run("Q-REPO-233 the arrival in the target project", func(t *testing.T) {
		if in.Type != audit.EventRepositoryMovedIn || in.Repository != "mobile/npm-libs" || in.Project != "mobile" ||
			in.Subject.ID != "42" || in.Subject.Name != "mobile/npm-libs" || in.Journal != nil {
			t.Errorf("%+v", in)
		}
		if string(in.Details) != string(ev.Details) {
			t.Errorf("details %s, the leaving's %s", in.Details, ev.Details)
		}
		withinSchema(t, in)
	})
}

// TestR_REPO_060_ACIMappingNamesTheRepositoryByKey: an inline grant of a CI mapping of the source project's
// tier names the repository by a bare key in the project context and refuses the move; a pattern, a class, a
// template and a qualified name (which a project context does not take) name it not.
func TestR_REPO_060_ACIMappingNamesTheRepositoryByKey(t *testing.T) {
	in := qname.Context{Project: "web"}
	for _, tc := range []struct {
		doc  string
		want bool
	}{
		{`{"inline":[{"repositories":["npm-libs"],"actions":["read"]}]}`, true},
		{`{"inline":[{"repositories":["npm-other","npm-libs"],"actions":["read"]}]}`, true},
		{`{"inline":[{"repositories":["npm-*"],"actions":["read"]}]}`, false},
		{`{"inline":[{"repositories":["@any-local"],"actions":["read"]}]}`, false},
		{`{"inline":[{"repositories":["npm-{{ project }}"],"actions":["read"]}]}`, false},
		{`{"inline":[{"repositories":["web/npm-libs"],"actions":["read"]}]}`, false},
		{`{"policies":["npm-read"]}`, false},
		{`not json`, false},
	} {
		if got := specNames(tc.doc, in, "npm-libs"); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.doc, got, tc.want)
		}
	}
}

// TestR_REPO_060_RefusalsOfAMove: 409 repository_in_use lists the references by kind and name in the
// member `references`; 409 already_exists names the target.
func TestR_REPO_060_RefusalsOfAMove(t *testing.T) {
	err := inUse(qname.Repo{Project: "web", Key: "npm-libs"}, []Reference{{Kind: RefVirtualMember, Name: "npm-all"}, {Kind: RefPolicy, Name: "npm-read"}})
	e, ok := errs.As(err)
	if !ok || e.Status() != 409 || e.Code != errs.CodeRepositoryInUse {
		t.Fatalf("%v", err)
	}
	refs, _ := e.Extensions["references"].([]Reference)
	if len(refs) != 2 || refs[0].Name != "npm-all" || !strings.Contains(e.Detail, "virtual_member npm-all") {
		t.Errorf("%+v", e)
	}
	if e, _ := errs.As(keyTaken(qname.Repo{Project: "mobile", Key: "npm-libs"})); e.Status() != 409 || e.Code != errs.CodeAlreadyExists {
		t.Errorf("%+v", e)
	}
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

// TestR_BAS_004_ABastionIsNoProjectMember: bastion:<name> as a member answers 422 principal_not_allowed, as no
// Bastion registration holds a role, a grant or a membership.
func TestR_BAS_004_ABastionIsNoProjectMember(t *testing.T) {
	_, _, err := parseMemberRef("bastion:partners")
	if got := fieldsOf(t, err); got["/principalRef"] != errs.CodePrincipalNotAllowed {
		t.Fatalf("parseMemberRef = %v, want 422 principal_not_allowed", err)
	}
}
