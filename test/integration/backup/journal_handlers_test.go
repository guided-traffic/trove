//go:build integration

package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// ev is one hand-written journal object: what a producer's entry looks like (the entries are the documented shapes of the
// owning pages) or what somebody with the runtime credential could write.
type ev struct{ event, entry string }

// replayEvents writes the events as journal objects, a millisecond apart, replays them over an in-memory store with the
// handlers the packages registered, and returns the results by position. The restore point lies an hour back, so every object
// is newer than it.
func (e *jenv) replayEvents(events ...ev) (*journal.Summary, []journal.Outcome) {
	e.t.Helper()
	store := newMemStore()
	base := time.Now().Add(-time.Minute)
	ids := make([]string, len(events))
	for i, x := range events {
		ids[i] = e.put(store, base.Add(time.Duration(i)*time.Millisecond), x.event, x.entry)
	}
	sum, _ := e.replayAt(e.replayer(store, e.rec), time.Now().Add(-time.Hour))
	out := make([]journal.Outcome, len(events))
	for i, id := range ids {
		out[i] = outcomeOf(e.t, sum, id)
	}
	return sum, out
}

func (e *jenv) want(got []journal.Outcome, want ...journal.Outcome) {
	e.t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		e.t.Fatalf("outcomes = %v, want %v", got, want)
	}
}

func (e *jenv) count(sql string, args ...any) string { return e.stack.Text(sql, args...) }

// policyEntry reads the actions of one principal in a policy, "" for no entry.
func (e *jenv) policyActions(policy, principal string) string {
	e.t.Helper()
	return e.count(`SELECT COALESCE((SELECT array_to_string(actions, ',') FROM authz_policy_entry WHERE policy_id = $1::uuid AND principal_id = $2::uuid), '')`, policy, principal)
}

func (e *jenv) includes(policy string) string {
	e.t.Helper()
	return e.count(`SELECT COALESCE(string_agg(pattern, ',' ORDER BY pattern), '') FROM authz_policy_pattern WHERE policy_id = $1::uuid AND mode = 'include'`, policy)
}

func (e *jenv) policyID(name string) string {
	return e.count("SELECT id::text FROM authz_policy WHERE name = $1", name)
}

func (e *jenv) repoID(key string) string {
	return e.count("SELECT id::text FROM repositories WHERE key = $1", key)
}

func (e *jenv) principalID(kind, name string) string {
	return e.count("SELECT id::text FROM principal WHERE kind = $1 AND name = $2", kind, name)
}

// state is a policy's access-bearing state as the producer journals it.
func state(repoID string, include []string, entries map[string][]string) string {
	type ent struct {
		PrincipalID string   `json:"principalId"`
		Actions     []string `json:"actions"`
	}
	var es []ent
	for p, a := range entries {
		es = append(es, ent{p, a})
	}
	if include == nil {
		include = []string{}
	}
	b, _ := json.Marshal(map[string]any{
		"resourceType": "repository",
		"selectors":    []map[string]any{{"kind": "key", "repositoryId": atoi(repoID)}},
		"packages":     map[string]any{"include": include, "exclude": []string{}},
		"paths":        map[string]any{"include": []string{}, "exclude": []string{}},
		"entries":      es,
	})
	return string(b)
}

func atoi(s string) int64 {
	var n int64
	_, _ = fmt.Sscan(s, &n)
	return n
}

func policyUpdated(id, before, after string) ev {
	return ev{"authz.policy.updated", fmt.Sprintf(`{"policyId":%q,"name":"p","before":%s,"after":%s}`, id, before, after)}
}

// teamWeb seeds the worked example of R-AUTHZ-031: team-web-publish gives group:team-web deploy, annotate and read on
// npm-internal for @web/*, and user:lead-web the same.
func (e *jenv) teamWeb() (policy, repo, team, lead string) {
	e.t.Helper()
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	e.stack.User("w", "team-web")
	team = e.principalID("group", "team-web")
	leadP := e.stack.User("lead-web")
	e.stack.Policy("team-web-publish", []string{"annotate", "deploy", "read"}, []string{"npm-internal"}, testenv.Person{ID: team, Name: "team-web", Kind: "group"}, leadP)
	policy = e.policyID("team-web-publish")
	e.stack.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) VALUES ($1::uuid, 'package', 'include', '@web/*')`, policy)
	return policy, e.repoID("npm-internal"), team, leadP.ID
}

// TestR_AUTHZ_031_PolicyUpdateRule: the update rule of R-HA-054 with the policy comparator, on the criteria of R-AUTHZ-031 and
// the forged-entry criterion of R-HA-054.
func TestR_AUTHZ_031_PolicyUpdateRule(t *testing.T) {
	e := newJEnv(t)
	policy, repo, team, lead := e.teamWeb()
	full := []string{"annotate", "deploy", "read"}

	t.Run("a narrowing is applied and the lead entry is left alone", func(t *testing.T) {
		before := state(repo, []string{"@web/*"}, map[string][]string{team: full, lead: full})
		after := state(repo, []string{"@web/*"}, map[string][]string{team: {"read"}, lead: full})
		_, out := e.replayEvents(policyUpdated(policy, before, after))
		e.want(out, journal.Applied)
		if got := e.policyActions(policy, team); got != "read" {
			t.Errorf("team-web holds %q, want read", got)
		}
		if got := e.policyActions(policy, lead); got != "annotate,deploy,read" {
			t.Errorf("lead-web holds %q, want its three actions", got)
		}
		_, out = e.replayEvents(policyUpdated(policy, before, after))
		e.want(out, journal.AlreadyInEffect)
	})

	t.Run("an emptied include list is skipped and the policy keeps its include", func(t *testing.T) {
		e.stack.Exec(`UPDATE authz_policy_entry SET actions = $2 WHERE policy_id = $1::uuid AND principal_id = $3::uuid`, policy, full, team)
		before := state(repo, []string{"@web/*"}, map[string][]string{team: full, lead: full})
		after := state(repo, nil, map[string][]string{team: full, lead: full})
		_, out := e.replayEvents(policyUpdated(policy, before, after))
		e.want(out, journal.Skipped)
		if got := e.includes(policy); got != "@web/*" {
			t.Errorf("include = %q, want @web/*", got)
		}
	})

	t.Run("a pattern that merely looks narrower suspends the policy", func(t *testing.T) {
		before := state(repo, []string{"@web/*"}, map[string][]string{team: full, lead: full})
		after := state(repo, []string{"@web/ui-*"}, map[string][]string{team: full, lead: full})
		sum, out := e.replayEvents(policyUpdated(policy, before, after))
		e.want(out, journal.Suspended)
		if n := e.count("SELECT count(*)::text FROM authz_policy_entry WHERE policy_id = $1::uuid", policy); n != "0" {
			t.Errorf("a suspended policy keeps %s entries, want none", n)
		}
		if !strings.Contains(string(sum.Results[0].Detail), "@web/ui-*") {
			t.Errorf("the output must print the after-state: %s", sum.Results[0].Detail)
		}
	})

	t.Run("an unknown policy is subject unknown", func(t *testing.T) {
		after := state(repo, nil, nil)
		_, out := e.replayEvents(policyUpdated("0192f0d2-0000-7000-8000-000000000001", after, after))
		e.want(out, journal.SubjectUnknown)
	})
}

// TestR_HA_054_ForgedEntriesCannotEscalate: three hand-written objects (an unknown event type, an update that adds an action to
// a policy, and one whose before-state is forged so that the addition looks like a narrowing) are reported skipped or
// suspended, and no principal gains access.
func TestR_HA_054_ForgedEntriesCannotEscalate(t *testing.T) {
	e := newJEnv(t)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	carol := e.stack.User("carol")
	bob := e.stack.User("bob")
	e.stack.Policy("readers", []string{"read"}, []string{"npm-internal"}, carol)
	policy := e.policyID("readers")
	repo := e.repoID("npm-internal")

	honest := state(repo, nil, map[string][]string{carol.ID: {"read"}})
	adds := state(repo, nil, map[string][]string{carol.ID: {"deploy", "read"}, bob.ID: {"read"}})
	forgedBefore := state(repo, nil, map[string][]string{carol.ID: {"delete", "deploy", "read"}, bob.ID: {"read"}})

	sum, out := e.replayEvents(
		ev{"made.up.event", `{"x":1}`},
		policyUpdated(policy, honest, adds),
		policyUpdated(policy, forgedBefore, adds),
	)
	e.want(out, journal.Skipped, journal.Skipped, journal.Suspended)
	if got := e.policyActions(policy, carol.ID); got != "" {
		t.Errorf("carol holds %q, want no entry: the forged suspension removed it", got)
	}
	if got := e.policyActions(policy, bob.ID); got != "" {
		t.Errorf("bob gained access: %q", got)
	}
	if sum.Counts[journal.Applied] != 0 {
		t.Errorf("a forged entry was applied: %v", sum.Counts)
	}
}

// TestR_HA_054_UnreadableObjectsAreReported: an object that does not decode, one whose key differs from its ID, a newer version
// and a key that is no journal key never stop the replay and are listed.
func TestR_HA_054_UnreadableObjectsAreReported(t *testing.T) {
	e := newJEnv(t)
	store := newMemStore()
	now := time.Now().Add(-time.Minute)
	day := now.UTC().Format("2006/01/02")
	base := journal.ListPrefix(e.prefix) + day + "/"
	_ = store.Put(context.Background(), base+"0192f0d2-0000-7000-8000-00000000000a.json", []byte("not json"))
	_ = store.Put(context.Background(), base+"0192f0d2-0000-7000-8000-00000000000b.json", []byte(fmt.Sprintf(
		`{"v":1,"id":"0192f0d2-0000-7000-8000-00000000000c","at":%q,"event":"token.revoked","entry":{}}`, now.UTC().Format(time.RFC3339Nano))))
	_ = store.Put(context.Background(), base+"0192f0d2-0000-7000-8000-00000000000d.json", []byte(fmt.Sprintf(
		`{"v":99,"id":"0192f0d2-0000-7000-8000-00000000000d","at":%q,"event":"token.revoked","entry":{}}`, now.UTC().Format(time.RFC3339Nano))))
	_ = store.Put(context.Background(), base+"junk.txt", []byte("x"))
	id := e.put(store, now, "made.up.event", `{}`)
	sum, _ := e.replayAt(e.replayer(store, nil), time.Now().Add(-time.Hour))
	if len(sum.Unreadable) != 4 {
		t.Fatalf("unreadable = %v, want 4 objects", sum.Unreadable)
	}
	if outcomeOf(t, sum, id) != journal.Skipped {
		t.Errorf("the readable entry must still be replayed")
	}
}

// TestR_AUTHZ_031_RemovalsAndOwnership: role bindings, policy deletion, ownership and the settings comparator.
func TestR_AUTHZ_031_RemovalsAndOwnership(t *testing.T) {
	e := newJEnv(t)
	e.stack.Repository("npm-internal", "local", "npm", `{"local":{"packageOwnership":"enforce"}}`)
	e.stack.Repository("npmjs", "remote", "npm", `{"remote":{"cacheFillAction":"read"}}`)
	admins := e.stack.User("pa", "platform-admins")
	_ = admins
	gid := e.principalID("group", "platform-admins")
	e.stack.Exec("INSERT INTO authz_role_binding (role, principal_id) VALUES ('admin', $1::uuid)", gid)
	e.stack.Policy("doomed", []string{"read"}, []string{"npm-internal"}, testenv.Person{ID: gid, Name: "platform-admins", Kind: "group"})
	doomed := e.policyID("doomed")
	owner := e.stack.User("owner1")
	e.stack.Exec(`INSERT INTO package_ownership (domain, format, package_name, state) VALUES ($1, 'npm', '@web/new-lib', 'claimed')`, "repo:"+e.repoID("npm-internal"))
	e.stack.Exec(`INSERT INTO package_owner (id, domain, format, package_name, owner_kind, principal_id, display, reason)
		VALUES (gen_random_uuid(), $1, 'npm', '@web/new-lib', 'principal', $2::uuid, 'user:owner1', 'first-publish')`, "repo:"+e.repoID("npm-internal"), owner.ID)
	npmjs, internal := e.repoID("npmjs"), e.repoID("npm-internal")

	sum, out := e.replayEvents(
		ev{"authz.role.unbound", fmt.Sprintf(`{"role":"admin","principalId":%q,"principal":"group:platform-admins"}`, gid)},
		ev{"authz.role.unbound", fmt.Sprintf(`{"role":"admin","principalId":%q}`, gid)},
		ev{"authz.role.unbound", `{"role":"admin","principalId":"0192f0d2-0000-7000-8000-0000000000aa"}`},
		ev{"authz.role.unbound", `{"role":"god","principalId":"0192f0d2-0000-7000-8000-0000000000aa"}`},
		ev{"authz.policy.deleted", fmt.Sprintf(`{"policyId":%q,"name":"doomed"}`, doomed)},
		ev{"authz.ownership.released", fmt.Sprintf(`{"domain":%q,"format":"npm","package":"@web/new-lib","principalId":%q}`, "repo:"+internal, owner.ID)},
		ev{"authz.settings.changed", fmt.Sprintf(`{"repositoryId":%q,"before":{"cacheFillAction":"read"},"after":{"cacheFillAction":"deploy"}}`, npmjs)},
		ev{"authz.settings.changed", fmt.Sprintf(`{"repositoryId":%q,"before":{"packageOwnership":"enforce"},"after":{"packageOwnership":"off"}}`, internal)},
	)
	e.want(out, journal.Applied, journal.AlreadyInEffect, journal.SubjectUnknown, journal.Skipped,
		journal.Applied, journal.Applied, journal.Applied, journal.Skipped)
	if !sum.NoAdminBinding {
		t.Error("the replay left no admin binding and must say so")
	}
	if n := e.count("SELECT count(*)::text FROM authz_policy WHERE id = $1::uuid", doomed); n != "0" {
		t.Error("the policy must be deleted")
	}
	if st := e.count("SELECT state FROM package_ownership WHERE package_name = '@web/new-lib'"); st != "claimed" {
		t.Errorf("a name left without owners stays claimed, is %q", st)
	}
	if n := e.count("SELECT count(*)::text FROM package_owner WHERE package_name = '@web/new-lib'"); n != "0" {
		t.Error("the owner must be removed")
	}
	if got := e.count("SELECT config->'remote'->>'cacheFillAction' FROM repositories WHERE id = $1::bigint", npmjs); got != "deploy" {
		t.Errorf("cacheFillAction = %q, want deploy", got)
	}
	if got := e.count("SELECT config->'local'->>'packageOwnership' FROM repositories WHERE id = $1::bigint", internal); got != "enforce" {
		t.Errorf("packageOwnership = %q, want enforce kept", got)
	}
}

// TestR_AUTHZ_031_ProjectEntries: membership removal, the role comparator, the project delete rule and the repository move.
func TestR_AUTHZ_031_ProjectEntries(t *testing.T) {
	e := newJEnv(t)
	web := e.stack.User("webdev")
	dave := e.stack.User("dave")
	eve := e.stack.User("eve")
	e.stack.Project("web")
	e.stack.Project("empty")
	e.stack.Member("web", web, "developer")
	e.stack.Member("web", dave, "reader")
	e.stack.Member("web", eve, "developer")
	e.stack.Member("empty", dave, "owner")
	e.stack.RepositoryIn("web", "npm-src", "local", "npm", "{}")
	e.stack.Project("target")
	webID := e.count("SELECT id::text FROM project WHERE key = 'web'")
	emptyID := e.count("SELECT id::text FROM project WHERE key = 'empty'")
	targetID := e.count("SELECT id::text FROM project WHERE key = 'target'")
	repo := e.repoID("npm-src")
	member := func(event, pid string, p testenv.Person, extra string) ev {
		return ev{event, fmt.Sprintf(`{"projectId":%q,"project":"x","principalId":%q,"principal":"user:x"%s}`, pid, p.ID, extra)}
	}

	sum, out := e.replayEvents(
		member("authz.project.member_removed", webID, web, ""),
		member("authz.project.member_removed", webID, web, ""),
		member("authz.project.member_changed", webID, dave, `,"before":"reader","after":"developer"`),
		member("authz.project.member_changed", webID, eve, `,"before":"maintainer","after":"reader"`),
		ev{"authz.project.deleted", fmt.Sprintf(`{"projectId":%q,"project":"empty"}`, emptyID)},
		ev{"authz.project.deleted", fmt.Sprintf(`{"projectId":%q,"project":"web"}`, webID)},
		ev{"repository.moved", fmt.Sprintf(`{"repositoryId":%q,"from":{"id":%q,"key":"web"},"to":{"id":%q,"key":"target"}}`, repo, webID, targetID)},
		ev{"repository.moved", fmt.Sprintf(`{"repositoryId":%q,"from":{"id":%q,"key":"web"},"to":{"id":%q,"key":"target"}}`, repo, webID, targetID)},
	)
	e.want(out, journal.Applied, journal.AlreadyInEffect, journal.Skipped, journal.Applied,
		journal.Applied, journal.Applied, journal.Suspended, journal.Suspended)
	if r := e.count("SELECT COALESCE((SELECT role FROM project_member WHERE project_id = $1::uuid AND principal_id = $2::uuid), 'none')", webID, web.ID); r != "none" {
		t.Errorf("webdev is still a member (%s)", r)
	}
	if n := e.count("SELECT count(*)::text FROM project WHERE key = 'empty'"); n != "0" {
		t.Error("an empty project must be deleted")
	}
	if n := e.count("SELECT count(*)::text FROM project WHERE key = 'web'"); n != "1" {
		t.Error("a project that holds a repository must be kept")
	}
	if n := e.count("SELECT count(*)::text FROM project_member WHERE project_id = $1::uuid", webID); n != "0" {
		t.Errorf("the kept project must lose its members, has %s", n)
	}
	if !strings.Contains(string(sum.Results[5].Detail), `"kept":true`) {
		t.Errorf("the kept project must be reported: %s", sum.Results[5].Detail)
	}
	if d := e.count("SELECT (config->>'disabled') FROM repositories WHERE id = $1::bigint", repo); d != "true" {
		t.Errorf("the moved repository must be disabled, disabled = %q", d)
	}
	if p := e.count("SELECT project_id::text FROM repositories WHERE id = $1::bigint", repo); p != webID {
		t.Error("a replay must not move the repository")
	}
}

// TestR_AUTH_041_IdentityEntries: users, service accounts, groups, memberships, rotation and the settings.
func TestR_AUTH_041_IdentityEntries(t *testing.T) {
	e := newJEnv(t)
	alice := e.stack.User("alice", "devs")
	bob := e.stack.User("bob")
	gone := e.stack.User("gone")
	sa := e.stack.ServiceAccount("ci-bot")
	bg := e.stack.User("breakglass")
	e.stack.Exec("UPDATE app_user SET is_break_glass = true WHERE principal_id = $1::uuid", bg.ID)
	devs := e.principalID("group", "devs")
	oldTok := e.tokenID(e.stack.Token(bob, scope.Default()))
	rotated := e.tokenID(e.stack.Token(alice, scope.Default()))
	later := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)

	_, out := e.replayEvents(
		ev{"user.disabled", fmt.Sprintf(`{"principalId":%q,"username":"alice","disabledReason":"admin"}`, bob.ID)},
		ev{"user.disabled", fmt.Sprintf(`{"principalId":%q,"username":"alice","disabledReason":"admin"}`, bob.ID)},
		ev{"user.deleted", fmt.Sprintf(`{"principalId":%q,"username":"gone"}`, gone.ID)},
		ev{"user.disabled", fmt.Sprintf(`{"principalId":%q,"username":"breakglass","disabledReason":"admin"}`, bg.ID)},
		ev{"serviceaccount.deleted", fmt.Sprintf(`{"principalId":%q,"name":"ci-bot"}`, sa.ID)},
		ev{"group.members.changed", fmt.Sprintf(`{"group":"devs","groupId":%q,"removed":[{"member":"alice","memberId":%q,"source":"manual"}]}`, devs, alice.ID)},
		ev{"group.deleted", fmt.Sprintf(`{"principalId":%q,"name":"devs"}`, devs)},
		ev{"token.rotated", fmt.Sprintf(`{"predecessor":%q,"expiresAt":%q,"overlapSeconds":600}`, rotated, later)},
		ev{"token.rotated", fmt.Sprintf(`{"predecessor":%q,"expiresAt":null,"overlapSeconds":0}`, oldTok)},
		ev{"settings.auth.changed", `{"tokens":{"pat":{"maxTTLSeconds":{"before":null,"after":null}},"sat":{"maxTTLSeconds":{"before":null,"after":null}}}}`},
		ev{"user.disabled", `{"principalId":"not-an-id"}`},
	)
	e.want(out, journal.Applied, journal.AlreadyInEffect, journal.Applied, journal.Skipped, journal.Applied, journal.Applied,
		journal.Applied, journal.Applied, journal.Applied, journal.AlreadyInEffect, journal.Skipped)
	status := func(id string) string { return e.count("SELECT status FROM principal WHERE id = $1::uuid", id) }
	for name, id := range map[string]string{"bob": bob.ID, "gone": gone.ID, "ci-bot": sa.ID} {
		if status(id) != "disabled" {
			t.Errorf("%s must be disabled, is %s", name, status(id))
		}
	}
	if status(bg.ID) != "active" {
		t.Error("the break-glass account must not be touched")
	}
	if n := e.count("SELECT count(*)::text FROM principal WHERE id = $1::uuid", devs); n != "0" {
		t.Error("the group must be deleted")
	}
	if !e.revokedAt(oldTok) {
		t.Error("a rotation with no overlap revokes the predecessor")
	}
	if e.count("SELECT (expires_at <= now() + interval '11 minutes')::text FROM api_token WHERE id = $1::uuid", rotated) != "true" {
		t.Error("the predecessor's expiry must come forward to the journaled one")
	}
}

// TestR_AUTHZ_036_ProjectVisibilityReplay: authz.project.visibility_changed lowers a project to the journaled level and
// cascades to its repositories (R-AUTHZ-033 rule 3); a level that is the current one is in effect, a later one is skipped, an
// unknown project is reported unknown.
func TestR_AUTHZ_036_ProjectVisibilityReplay(t *testing.T) {
	e := newJEnv(t)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	project := e.count("SELECT id::text FROM project WHERE key = 'it'")
	e.stack.Exec("UPDATE project SET visibility = 'public'")
	e.stack.Exec("UPDATE repositories SET visibility_artifacts = 'public', visibility_wiki = 'internal'")
	version := e.count("SELECT config_version::text FROM repositories WHERE key = 'npm-internal'")
	entry := func(id, before, after string) ev {
		return ev{"authz.project.visibility_changed", fmt.Sprintf(`{"project":"it","projectId":%q,"before":%q,"after":%q}`, id, before, after)}
	}
	_, out := e.replayEvents(
		entry(project, "public", "internal"),
		entry(project, "public", "internal"),
		entry(project, "internal", "public"),
		entry("0192f1c4-0000-7000-8000-00000000dead", "public", "private"),
		entry(project, "public", "open"),
	)
	e.want(out, journal.Applied, journal.AlreadyInEffect, journal.Skipped, journal.SubjectUnknown, journal.Skipped)
	if got := e.count("SELECT visibility FROM project WHERE key = 'it'"); got != "internal" {
		t.Errorf("the project is %s, want internal", got)
	}
	if got := e.count("SELECT visibility_artifacts || '/' || visibility_wiki FROM repositories WHERE key = 'npm-internal'"); got != "internal/internal" {
		t.Errorf("the repository is %s: the cascade lowers the artifacts and leaves the wiki that was not above the level", got)
	}
	if e.count("SELECT config_version::text FROM repositories WHERE key = 'npm-internal'") == version {
		t.Error("the cascade does not raise the configuration version of the repository")
	}
}

// TestR_AUTH_044_ExternalFlagReplay: user.external_changed sets the flag when the entry set it and never clears it; the
// break-glass account is not touched.
func TestR_AUTH_044_ExternalFlagReplay(t *testing.T) {
	e := newJEnv(t)
	alice := e.stack.User("alice")
	bob := e.stack.User("bob")
	bg := e.stack.User("breakglass")
	e.stack.Exec("UPDATE app_user SET is_break_glass = true WHERE principal_id = $1::uuid", bg.ID)
	e.stack.Exec("UPDATE app_user SET external = true WHERE principal_id = $1::uuid", bob.ID)
	entry := func(id string, before, after bool) ev {
		return ev{"user.external_changed", fmt.Sprintf(`{"principalId":%q,"before":%t,"after":%t}`, id, before, after)}
	}
	_, out := e.replayEvents(
		entry(alice.ID, false, true),
		entry(alice.ID, false, true),
		entry(bob.ID, true, false),
		entry(bg.ID, false, true),
		entry("0192f1c4-0000-7000-8000-00000000dead", false, true),
		ev{"user.external_changed", fmt.Sprintf(`{"principalId":%q}`, alice.ID)},
	)
	e.want(out, journal.Applied, journal.AlreadyInEffect, journal.Skipped, journal.Skipped, journal.SubjectUnknown, journal.Skipped)
	external := func(id string) string {
		return e.count("SELECT external::text FROM app_user WHERE principal_id = $1::uuid", id)
	}
	if external(alice.ID) != "true" || external(bob.ID) != "true" || external(bg.ID) != "false" {
		t.Errorf("alice %s, bob %s, break-glass %s: the flag is set, never cleared, and the break-glass account is untouched", external(alice.ID), external(bob.ID), external(bg.ID))
	}
}

// TestR_AUTHZ_036_RepositoryLevelsReplay: authz.settings.changed names the two levels of a repository as visibility.artifacts and
// visibility.wiki (R-AUTHZ-036). A lowering is applied, one that is in effect is reported so, one that opens content is skipped,
// and the wiki never stays above the artifacts.
func TestR_AUTHZ_036_RepositoryLevelsReplay(t *testing.T) {
	e := newJEnv(t)
	e.stack.Repository("npm-internal", "local", "npm", "{}")
	id := e.repoID("npm-internal")
	e.stack.Exec("UPDATE project SET visibility = 'public'")
	e.stack.Exec("UPDATE repositories SET visibility_artifacts = 'public', visibility_wiki = 'public'")
	entry := func(before, after string) ev {
		return ev{"authz.settings.changed", fmt.Sprintf(`{"repositoryId":%q,"before":{%s},"after":{%s}}`, id, before, after)}
	}
	levels := func() string {
		return e.count("SELECT visibility_artifacts || '/' || visibility_wiki FROM repositories WHERE id = $1::bigint", id)
	}
	version := e.count("SELECT config_version::text FROM repositories WHERE id = $1::bigint", id)
	_, out := e.replayEvents(
		entry(`"visibility.artifacts":"public","visibility.wiki":"public"`, `"visibility.artifacts":"internal","visibility.wiki":"internal"`),
		entry(`"visibility.artifacts":"public","visibility.wiki":"public"`, `"visibility.artifacts":"internal","visibility.wiki":"internal"`),
		entry(`"visibility.artifacts":"internal"`, `"visibility.artifacts":"public"`),
		entry(`"visibility.wiki":"internal"`, `"visibility.wiki":"private"`),
		entry(`"visibility.artifacts":"internal"`, `"visibility.artifacts":"private"`),
	)
	e.want(out, journal.Applied, journal.AlreadyInEffect, journal.Skipped, journal.Applied, journal.Applied)
	if got := levels(); got != "private/private" {
		t.Errorf("the repository is %s, want private/private: the last entry lowers the artifacts, and the wiki goes down with them", got)
	}
	if e.count("SELECT config_version::text FROM repositories WHERE id = $1::bigint", id) == version {
		t.Error("the replay does not raise the configuration version of the repository")
	}
}
