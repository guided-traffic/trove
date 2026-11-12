//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_AUTH_044_TheExternalFlagReachesTheStageOfEveryReplica: an administrator flags a user external; the stage of every replica
// hands the flag to authorization with the user's status and groups, within auth.revocation_bound, with or without Valkey.
func TestR_AUTH_044_TheExternalFlagReachesTheStageOfEveryReplica(t *testing.T) {
	for _, mode := range []vkMode{vkLive, vkDead} {
		t.Run(mode.String(), func(t *testing.T) {
			e := newEnv(t)
			a, b := e.newReplica("a", replicaOpts{valkey: mode}), e.newReplica("b", replicaOpts{valkey: mode})
			ctx := context.Background()
			alice := a.user("alice")
			pat := a.pat(alice)
			external := func(r *replica) bool {
				res, err := r.bearer(pat.Secret, "/api/v1/auth/whoami")
				if err != nil {
					t.Fatal(err)
				}
				return res.Principal.External
			}
			if external(a) || external(b) {
				t.Fatal("a new user is internal by default")
			}
			before, err := a.manager.GetUser(ctx, "alice")
			if err != nil {
				t.Fatal(err)
			}
			u, err := a.manager.UpdateUser(ctx, admin, "alice", principal.UserPatch{Expected: before.Revision, External: ptr(true)})
			if err != nil {
				t.Fatal(err)
			}
			if !u.External || u.Revision <= before.Revision {
				t.Fatalf("the update answers external=%v revision %d after %d: the flag moves the ETag of the user", u.External, u.Revision, before.Revision)
			}
			waitFor(t, "external on replica B", revocationBound, func() bool { return external(b) })
			if !external(a) {
				t.Error("the replica that wrote does not see the flag")
			}
			evs := a.rec.named(audit.EventUserExternalChanged)
			if len(evs) != 1 || len(evs[0].Journal) == 0 {
				t.Fatalf("%d user.external_changed events, want 1 with a journal entry", len(evs))
			}
			var entry struct {
				PrincipalID string `json:"principalId"`
				Before      bool   `json:"before"`
				After       bool   `json:"after"`
			}
			if err := json.Unmarshal(evs[0].Journal, &entry); err != nil || entry.PrincipalID != alice.ID || entry.Before || !entry.After {
				t.Fatalf("journal entry %s: %v", evs[0].Journal, err)
			}

			t.Run("the value it has changes nothing and records nothing", func(t *testing.T) {
				if _, err := a.manager.UpdateUser(ctx, admin, "alice", principal.UserPatch{Expected: errs.AnyRevision, External: ptr(true)}); err != nil {
					t.Fatal(err)
				}
				if n := len(a.rec.named(audit.EventUserExternalChanged)); n != 1 {
					t.Errorf("%d events after a repeat", n)
				}
			})
			t.Run("clearing the flag widens and reaches the replicas as well", func(t *testing.T) {
				if _, err := a.manager.UpdateUser(ctx, admin, "alice", principal.UserPatch{Expected: errs.AnyRevision, External: ptr(false)}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "internal again on replica B", revocationBound, func() bool { return !external(b) })
			})
			t.Run("the gate of a recent authentication refuses a change and is not asked for a repeat", func(t *testing.T) {
				asked := 0
				refuse := func(context.Context) error {
					asked++
					return errs.E(errs.Unauthenticated, errs.CodeReauthenticationRequired, "Sign in again.")
				}
				_, err := a.manager.UpdateUser(ctx, admin, "alice", principal.UserPatch{Expected: errs.AnyRevision, External: ptr(true), ExternalGate: refuse})
				wantCode(t, err, 401, errs.CodeReauthenticationRequired)
				if _, err := a.manager.UpdateUser(ctx, admin, "alice", principal.UserPatch{Expected: errs.AnyRevision, External: ptr(false), ExternalGate: refuse}); err != nil {
					t.Fatalf("a repeat was asked for a recent authentication: %v", err)
				}
				if asked != 1 || external(a) {
					t.Errorf("asked %d times, external %v", asked, external(a))
				}
			})
			t.Run("the list keeps the users of the flag", func(t *testing.T) {
				if _, err := a.manager.UpdateUser(ctx, admin, "alice", principal.UserPatch{Expected: errs.AnyRevision, External: ptr(true)}); err != nil {
					t.Fatal(err)
				}
				a.user("bob")
				names := func(filter string) []string {
					rows, err := a.manager.ListUsers(ctx, principal.UserFilter{External: filter, Limit: 10})
					if err != nil {
						t.Fatal(err)
					}
					var out []string
					for _, u := range rows {
						out = append(out, u.Name)
					}
					return out
				}
				if got := names("true"); len(got) != 1 || got[0] != "alice" {
					t.Errorf("external=true: %v", got)
				}
				if got := names("false"); len(got) != 1 || got[0] != "bob" {
					t.Errorf("external=false: %v", got)
				}
				if got := names(""); len(got) != 2 {
					t.Errorf("no filter: %v", got)
				}
			})
		})
	}
}

// TestR_AUTH_044_TheBreakGlassAccountIsNeverExternal: the flag of the break-glass account is 409 break_glass_immutable.
func TestR_AUTH_044_TheBreakGlassAccountIsNeverExternal(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	e.insertBreakGlass("breakglass")
	_, err := a.manager.UpdateUser(context.Background(), admin, "breakglass", principal.UserPatch{Expected: errs.AnyRevision, External: ptr(true)})
	wantCode(t, err, 409, errs.CodeBreakGlassImmutable)
	if n := len(a.rec.named(audit.EventUserExternalChanged)); n != 0 {
		t.Errorf("%d events for a refused change", n)
	}
}

// TestR_AUTH_044_ANewUserTakesTheInstallationsDefault: a user created without the member takes newUsersExternal as it stands in the
// creating transaction, whatever created it; the member wins; the setting changes no existing user (Q-AUTH-231).
func TestR_AUTH_044_ANewUserTakesTheInstallationsDefault(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	flag := func(name string) bool {
		u, err := a.manager.GetUser(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return u.External
	}
	before := a.user("before")
	if _, err := a.settings.Update(ctx, admin, token.SettingsPatch{Expected: errs.AnyRevision, NewUsersExternal: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	a.user("later")
	if _, err := a.manager.CreateUser(ctx, admin, principal.NewUser{Name: "named-internal", External: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if flag(before.Name) || !flag("later") || flag("named-internal") {
		t.Errorf("before=%v later=%v named-internal=%v, want false true false", flag(before.Name), flag("later"), flag("named-internal"))
	}
	if _, err := a.settings.Update(ctx, admin, token.SettingsPatch{Expected: errs.AnyRevision, NewUsersExternal: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.manager.CreateUser(ctx, admin, principal.NewUser{Name: "named-external", External: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	a.user("last")
	if !flag("later") || !flag("named-external") || flag("last") {
		t.Errorf("later=%v named-external=%v last=%v, want true true false", flag("later"), flag("named-external"), flag("last"))
	}
	if len(a.rec.named(audit.EventUserExternalChanged)) != 0 {
		t.Error("creating a user with the flag recorded a change of the flag")
	}
}

// TestR_AUTH_044_TheFirstLoginTakesTheInstallationsDefault: a user created by its first OIDC login takes newUsersExternal as it
// stands in that transaction; a later login changes nothing (Q-AUTH-231).
func TestR_AUTH_044_TheFirstLoginTakesTheInstallationsDefault(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	external := func(name string) bool {
		u, err := a.manager.GetUser(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return u.External
	}
	if _, err := a.provisioner.SignIn(ctx, claims("dana", "sub-dana")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.settings.Update(ctx, admin, token.SettingsPatch{Expected: errs.AnyRevision, NewUsersExternal: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.provisioner.SignIn(ctx, claims("erin", "sub-erin")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.provisioner.SignIn(ctx, claims("dana", "sub-dana")); err != nil {
		t.Fatal(err)
	}
	if external("dana") || !external("erin") {
		t.Errorf("dana %v erin %v, want false true: the setting is read when the user is created and changes no existing user", external("dana"), external("erin"))
	}
}
