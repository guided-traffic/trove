//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// The writers of the identity narrowings of F-72 (R-HA-054): every entry is written in the transaction of the change, with
// the parts the replay needs. The replay itself is tested against a restored copy in test/integration/backup.

// outboxEntries returns the entries of the revocation_outbox rows of an event, oldest first.
func (e *env) outboxEntries(event string) []map[string]any {
	e.t.Helper()
	rows, err := e.admin.Query(context.Background(), `SELECT entry FROM revocation_outbox WHERE event = $1 ORDER BY at, id`, event)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			e.t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			e.t.Fatalf("entry %s: %v", raw, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func names(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

// TestR_AUTH_016_ARemovedPendingLinkIsJournaledInItsTransaction: an update that drops a pending link writes one user.updated
// entry with the provider; a profile-only update writes an entry with an empty list; an update a stale revision refuses
// writes none and removes nothing (Q-AUTH-205).
func TestR_AUTH_016_ARemovedPendingLinkIsJournaledInItsTransaction(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead, auditDB: true})
	ctx := context.Background()
	u, err := a.manager.CreateUser(ctx, admin, principal.NewUser{
		Name: "alice", Email: "alice@example.com",
		Identities: []principal.PendingIdentity{{Provider: "corp"}, {Provider: "partners"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(e.outboxEntries("user.updated")); n != 0 {
		t.Fatalf("%d user.updated entries after the create, want none", n)
	}

	t.Run("a stale revision writes no entry and removes no link", func(t *testing.T) {
		keep := []principal.PendingIdentity{{Provider: "corp"}}
		if _, err := a.manager.UpdateUser(ctx, admin, "alice", principal.UserPatch{Identities: &keep, Expected: 987654}); err == nil {
			t.Fatal("a stale revision was accepted")
		}
		if n := len(e.outboxEntries("user.updated")); n != 0 {
			t.Errorf("%d entries, want none", n)
		}
		if n := e.scalar(`SELECT count(*) FROM user_identity WHERE user_id = $1::uuid`, u.ID); n != 2 {
			t.Errorf("%d links after the refused update, want 2", n)
		}
	})
	t.Run("a removed link is part of the entry", func(t *testing.T) {
		keep := []principal.PendingIdentity{{Provider: "corp"}}
		if _, err := a.manager.UpdateUser(ctx, admin, "alice", principal.UserPatch{Identities: &keep, Expected: errs.AnyRevision}); err != nil {
			t.Fatal(err)
		}
		got := e.outboxEntries("user.updated")
		if len(got) != 1 || got[0]["principalId"] != u.ID || got[0]["name"] != "alice" || !slices.Equal(names(got[0]["pendingLinksRemoved"]), []string{"partners"}) {
			t.Fatalf("entries %v, want one naming partners", got)
		}
	})
	t.Run("a profile-only update writes an empty list", func(t *testing.T) {
		dn := "Alice A."
		if _, err := a.manager.UpdateUser(ctx, admin, "alice", principal.UserPatch{DisplayName: &dn, Expected: errs.AnyRevision}); err != nil {
			t.Fatal(err)
		}
		got := e.outboxEntries("user.updated")
		if len(got) != 2 {
			t.Fatalf("%d entries, want 2", len(got))
		}
		if l, ok := got[1]["pendingLinksRemoved"].([]any); !ok || len(l) != 0 {
			t.Errorf("pendingLinksRemoved = %v, want []", got[1]["pendingLinksRemoved"])
		}
	})
	t.Run("a link that is already linked is not removed and not named", func(t *testing.T) {
		if _, err := a.provisioner.SignIn(ctx, claims("alice", "sub-alice")); err != nil {
			t.Fatal(err)
		}
		none := []principal.PendingIdentity{}
		if _, err := a.manager.UpdateUser(ctx, admin, "alice", principal.UserPatch{Identities: &none, Expected: errs.AnyRevision}); err != nil {
			t.Fatal(err)
		}
		got := e.outboxEntries("user.updated")
		if l := got[len(got)-1]["pendingLinksRemoved"].([]any); len(l) != 0 {
			t.Errorf("a linked identity was named: %v", l)
		}
		if n := e.scalar(`SELECT count(*) FROM user_identity WHERE user_id = $1::uuid AND subject IS NOT NULL`, u.ID); n != 1 {
			t.Errorf("%d linked identities, want 1", n)
		}
	})
}

// TestR_AUTH_018_ARenameCarriesTheFormerNameInItsEntry: the entry of an update that renamed the group names the name before;
// one that did not rename leaves the key out (Q-AUTH-206).
func TestR_AUTH_018_ARenameCarriesTheFormerNameInItsEntry(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead, auditDB: true})
	ctx := context.Background()
	if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "sre"}); err != nil {
		t.Fatal(err)
	}
	desc, name := "site reliability", "ops"
	if _, err := a.manager.UpdateGroup(ctx, admin, "sre", principal.GroupPatch{Description: &desc, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.manager.UpdateGroup(ctx, admin, "sre", principal.GroupPatch{Name: &name, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	got := e.outboxEntries("group.updated")
	if len(got) != 2 {
		t.Fatalf("%d entries, want 2", len(got))
	}
	if _, ok := got[0]["renamedFrom"]; ok || got[0]["name"] != "sre" {
		t.Errorf("the description update carries %v", got[0])
	}
	if got[1]["renamedFrom"] != "sre" || got[1]["name"] != "ops" {
		t.Errorf("the rename carries %v, want renamedFrom sre and name ops", got[1])
	}
}

// TestR_AUTH_040_ASettingsChangeCarriesTheMaximaInItsEntry: every settings change writes both runtime maxima before and after,
// null for the YAML value, whether the change touched them or not (Q-AUTH-207).
func TestR_AUTH_040_ASettingsChangeCarriesTheMaximaInItsEntry(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead, auditDB: true})
	ctx := context.Background()
	pat, notice := 2*time.Hour, 48*time.Hour
	if _, err := a.settings.Update(ctx, admin, token.SettingsPatch{PATMaxTTL: &pat, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.settings.Update(ctx, admin, token.SettingsPatch{ExpiryNotice: &notice, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	got := e.outboxEntries("settings.auth.changed")
	if len(got) != 2 {
		t.Fatalf("%d entries, want 2", len(got))
	}
	maxOf := func(entry map[string]any, kind string) map[string]any {
		return entry["tokens"].(map[string]any)[kind].(map[string]any)["maxTTLSeconds"].(map[string]any)
	}
	if m := maxOf(got[0], "pat"); m["before"] != nil || m["after"] != float64(7200) {
		t.Errorf("first pat maximum %v, want null to 7200", m)
	}
	if m := maxOf(got[0], "sat"); m["before"] != nil || m["after"] != nil {
		t.Errorf("first sat maximum %v, want null to null", m)
	}
	if m := maxOf(got[1], "pat"); m["before"] != float64(7200) || m["after"] != float64(7200) {
		t.Errorf("a change of the expiry notice must still carry the pat maximum: %v", m)
	}
	if _, ok := got[1]["anonymousAccess"]; ok {
		t.Error("the entry still carries the anonymous switch")
	}
}

// TestR_AUTH_008_ADetachedGroupIsNeitherFilledNorCreated: a login whose group claim maps to a detached group's name adds no
// membership, creates no second group and does not fail, with create_groups on and off; an oidc membership left in the group
// is removed by the sync and journaled as user.groups.synced (Q-AUTH-206).
func TestR_AUTH_008_ADetachedGroupIsNeitherFilledNorCreated(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead, auditDB: true})
	ctx := context.Background()
	with := func(create bool, g ...string) func(*principal.LoginClaims) {
		return func(c *principal.LoginClaims) { c.Groups = g; c.CreateGroups = create }
	}
	alice, err := a.provisioner.SignIn(ctx, claims("alice", "sub-alice", with(true, "team-x", "other")))
	if err != nil {
		t.Fatal(err)
	}
	if got := groupNames(t, a, alice.ID); !slices.Equal(got, []string{"other", "team-x"}) {
		t.Fatalf("groups before the detach: %v", got)
	}
	e.exec(`UPDATE user_group SET sync_detached = true WHERE principal_id = (SELECT id FROM principal WHERE kind = 'group' AND name = 'team-x')`)

	for _, create := range []bool{true, false} {
		bob, err := a.provisioner.SignIn(ctx, claims("bob", "sub-bob", with(create, "team-x")))
		if err != nil {
			t.Fatalf("create_groups %v: the login failed: %v", create, err)
		}
		if got := groupNames(t, a, bob.ID); len(got) != 0 {
			t.Errorf("create_groups %v: bob was added to %v", create, got)
		}
	}
	if n := e.scalar(`SELECT count(*) FROM principal WHERE kind = 'group' AND name = 'team-x'`); n != 1 {
		t.Errorf("%d groups named team-x, want the one", n)
	}
	// Alice still holds the oidc membership the group was filled with: her next login removes it, journaled.
	if _, err := a.provisioner.SignIn(ctx, claims("alice", "sub-alice", with(true, "team-x", "other"))); err != nil {
		t.Fatal(err)
	}
	if got := groupNames(t, a, alice.ID); !slices.Equal(got, []string{"other"}) {
		t.Errorf("alice's groups after the sync: %v, want other only", got)
	}
	synced := e.outboxEntries("user.groups.synced")
	last := synced[len(synced)-1]
	rem := last["removed"].([]any)
	if len(rem) != 1 || rem[0].(map[string]any)["group"] != "team-x" || rem[0].(map[string]any)["source"] != "oidc:corp" {
		t.Errorf("the removal is journaled as %v", last)
	}
}

// TestR_AUTH_018_SyncDetachedIsClearedOnlyToFalse: the API shows syncDetached, refuses true with 422 and a pointer, clears it
// with false so that the next login fills the group again, and a rename leaves it as it is (Q-AUTH-206).
func TestR_AUTH_018_SyncDetachedIsClearedOnlyToFalse(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkLive, auditDB: true})
	fx := e.newFixture(a)
	s := a.serve(e, apitest.New(t, v1.EmbeddedYAML()))
	ctx := context.Background()
	if _, err := a.provisioner.SignIn(ctx, claims("alice", "sub-alice", func(c *principal.LoginClaims) { c.Groups = []string{"team-x"}; c.CreateGroups = true })); err != nil {
		t.Fatal(err)
	}
	api := func(method, path string, body any) reply {
		return s.call(method, path, fx.adminToken, body, "If-Match", "*")
	}

	rp := api(get, "/api/v1/groups/team-x", nil)
	want(t, rp, 200, "")
	if rp.json(t)["syncDetached"] != false {
		t.Fatalf("a group that was never detached shows %v", rp.json(t)["syncDetached"])
	}
	rp = api(patch, "/api/v1/groups/team-x", map[string]any{"syncDetached": true})
	want(t, rp, 422, "validation_failed")
	if f := rp.json(t)["errors"]; f == nil {
		t.Errorf("the refusal names no field: %s", rp.Body)
	} else if p := f.([]any)[0].(map[string]any); p["pointer"] != "/syncDetached" || p["code"] != "invalid_value" {
		t.Errorf("field error %v, want /syncDetached invalid_value", p)
	}

	e.exec(`UPDATE user_group SET sync_detached = true WHERE principal_id = (SELECT id FROM principal WHERE kind = 'group' AND name = 'team-x')`)
	rp = api(get, "/api/v1/groups/team-x", nil)
	if rp.json(t)["syncDetached"] != true {
		t.Fatalf("a detached group shows %v", rp.json(t)["syncDetached"])
	}
	if items := list(t, api(get, "/api/v1/groups", nil)); !slices.ContainsFunc(items, func(g map[string]any) bool { return g["name"] == "team-x" && g["syncDetached"] == true }) {
		t.Errorf("the list does not show the flag: %v", items)
	}
	rp = api(patch, "/api/v1/groups/team-x", map[string]any{"name": "team-y"})
	want(t, rp, 200, "")
	if rp.json(t)["syncDetached"] != true {
		t.Errorf("a rename cleared the flag: %v", rp.json(t))
	}
	want(t, api(patch, "/api/v1/groups/team-y", map[string]any{"name": "team-x"}), 200, "")

	rp = api(patch, "/api/v1/groups/team-x", map[string]any{"syncDetached": false})
	want(t, rp, 200, "")
	if rp.json(t)["syncDetached"] != false {
		t.Fatalf("the clear answered %v", rp.json(t))
	}
	bob, err := a.provisioner.SignIn(ctx, claims("bob", "sub-bob", func(c *principal.LoginClaims) { c.Groups = []string{"team-x"}; c.CreateGroups = true }))
	if err != nil {
		t.Fatal(err)
	}
	if got := groupNames(t, a, bob.ID); !slices.Equal(got, []string{"team-x"}) {
		t.Errorf("after the clear the login fills the group: %v", got)
	}
	// Clearing a flag that is not set changes nothing and writes no group.updated beyond the rename's.
	before := len(e.outboxEntries("group.updated"))
	want(t, api(patch, "/api/v1/groups/team-x", map[string]any{"syncDetached": false}), 200, "")
	if n := len(e.outboxEntries("group.updated")); n != before {
		t.Errorf("a clear of an unset flag wrote %d entries", n-before)
	}
}

// TestR_AUTH_040_ARuntimeMaximumBeyondOneHundredYearsIsRefused: the settings API never stores a maximum the replay of
// settings.auth.changed would reject, even where the ceiling in the configuration is higher (Q-AUTH-207).
func TestR_AUTH_040_ARuntimeMaximumBeyondOneHundredYearsIsRefused(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead, auditDB: true, tokens: func(c *config.AuthTokens) {
		c.PAT.MaxTTL, c.SAT.MaxTTL = 2*config.MaxTokenLifetime, 2*config.MaxTokenLifetime
	}})
	ctx := context.Background()
	over := config.MaxTokenLifetime + time.Hour
	for name, patch := range map[string]token.SettingsPatch{
		"pat": {PATMaxTTL: &over, Expected: errs.AnyRevision},
		"sat": {SATMaxTTL: &over, Expected: errs.AnyRevision},
	} {
		_, err := a.settings.Update(ctx, admin, patch)
		if ee, ok := errs.As(err); !ok || ee.Code != errs.CodeExceedsCeiling {
			t.Errorf("%s: %v, want exceeds_ceiling", name, err)
		}
	}
	if n := len(e.outboxEntries("settings.auth.changed")); n != 0 {
		t.Errorf("%d entries after refused changes", n)
	}
	longest := config.MaxTokenLifetime
	if _, err := a.settings.Update(ctx, admin, token.SettingsPatch{PATMaxTTL: &longest, Expected: errs.AnyRevision}); err != nil {
		t.Fatalf("the longest value must be accepted: %v", err)
	}
	if got := e.outboxEntries("settings.auth.changed"); len(got) != 1 {
		t.Errorf("%d entries, want 1", len(got))
	}
}
