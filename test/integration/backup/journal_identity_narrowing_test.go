//go:build integration

package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/journal/replay"
)

// The identity narrowings of F-72 (R-HA-054, R-BAK-024): a removed pending link (Q-AUTH-205), a group renamed after the
// restore point (Q-AUTH-206) and a lowered token lifetime maximum (Q-AUTH-207). Each test changes a source database after t
// through the services the API uses, ships its outbox, replays the journal into the copy made at t, and shows that the
// narrowing holds. Hand-written entries show the fail-closed cases.

const idpIssuer = "https://idp.example.com"

func loginClaims(username, sub string, mut ...func(*principal.LoginClaims)) principal.LoginClaims {
	c := principal.LoginClaims{
		Provider: "corp", Issuer: idpIssuer, Subject: sub, Username: username,
		Email: username + "@example.com", EmailVerified: true, DisplayName: "User " + username,
		GroupSync: true, JIT: true, SyncUsername: true,
	}
	for _, m := range mut {
		m(&c)
	}
	return c
}

func provisioner(t *testing.T, s *twinSide) *principal.PGProvisioner {
	t.Helper()
	metrics, err := principal.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return principal.NewProvisioner(principal.ProvisionerOptions{DB: s.db, Notifier: s.changes, Recorder: s.rec, BreakGlassName: "breakglass", Metrics: metrics})
}

// settingsStore builds the runtime settings of one side as a replica does, with the YAML ceilings of newIdentity.
func settingsStore(t *testing.T, s *twinSide) *token.SettingsStore {
	t.Helper()
	reg := prometheus.NewRegistry()
	caches, err := cache.NewRegistry(s.changes, reg)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := principal.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.AuthTokens{
		CacheTTL: 15 * time.Second, RetentionAfterEnd: 2160 * time.Hour, ExpiryNotice: 168 * time.Hour,
		PAT: config.AuthTokenPAT{DefaultTTL: 24 * time.Hour, MaxTTL: 48 * time.Hour}, SAT: config.AuthTokenSAT{DefaultTTL: 24 * time.Hour, MaxTTL: 48 * time.Hour},
	}
	return token.NewSettingsStore(token.SettingsOptions{
		DB: s.db, Notifier: s.changes, Recorder: s.rec, Cache: caches, Gate: principal.Gate{Bound: 2 * time.Second, Metrics: metrics},
		Tokens: cfg, Anonymous: config.AuthAnonymous{Allowed: true},
	})
}

func entryJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestR_AUTH_016_ARemovedPendingLinkStaysRemovedAfterARestore: the pending link an update removed after t is removed again
// by the replay, so the provider's account of that username cannot take the user over; a link the restored user holds linked
// is never removed, an unknown user is subject unknown, and a second pass applies nothing.
func TestR_AUTH_016_ARemovedPendingLinkStaysRemovedAfterARestore(t *testing.T) {
	tw := newTwin(t)
	src := tw.open(tw.src)
	ids := newIdentity(t, src)
	ctx := context.Background()
	pending := []principal.PendingIdentity{{Provider: "corp", Issuer: idpIssuer}, {Provider: "partners", Issuer: "https://partners.example.com"}}
	_, err := ids.manager.CreateUser(ctx, producerAdmin, principal.NewUser{Name: "alice", Email: "alice@example.com", Identities: pending})
	if err != nil {
		t.Fatal(err)
	}
	erin, err := ids.manager.CreateUser(ctx, producerAdmin, principal.NewUser{Name: "erin", Email: "erin@example.com", Identities: pending[:1]})
	if err != nil {
		t.Fatal(err)
	}
	src.exec(`UPDATE user_identity SET subject = 'sub-erin', linked_at = now() WHERE user_id = $1::uuid`, erin.ID)
	src.close()
	tw.snapshot() // t

	src = tw.open(tw.src)
	ids = newIdentity(t, src)
	keep := pending[1:]
	if _, err := ids.manager.UpdateUser(ctx, producerAdmin, "alice", principal.UserPatch{Identities: &keep, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	dn := "Alice A."
	if _, err := ids.manager.UpdateUser(ctx, producerAdmin, "alice", principal.UserPatch{DisplayName: &dn, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	if n := src.ship(store); n != 2 {
		t.Fatalf("%d journal objects shipped, want 2: every user update carries its entry", n)
	}
	src.close()

	rst := tw.open(tw.restored)
	linkCount := func(user, provider string) int64 {
		return rst.number(`SELECT count(*) FROM user_identity i JOIN principal p ON p.id = i.user_id WHERE p.name = $1 AND i.provider_id = $2`, user, provider)
	}
	if linkCount("alice", "corp") != 1 {
		t.Fatal("the copy at t must hold alice's pending link of corp")
	}
	before := rst.epoch("auth")
	sum, _ := rst.replay(store, tw.at, rst.changes)
	got := outcomes(sum)
	if o := got["user.updated alice"]; !slices.Equal(o, []journal.Outcome{journal.Applied, journal.AlreadyInEffect}) {
		t.Errorf("user.updated alice: %v, want applied for the removal, already in effect for the profile change", o)
	}
	if linkCount("alice", "corp") != 0 || linkCount("alice", "partners") != 1 {
		t.Errorf("alice holds corp %d, partners %d: the removed link must be gone and the kept one stay", linkCount("alice", "corp"), linkCount("alice", "partners"))
	}
	if rst.epoch("auth") != before {
		t.Error("a pending link is read from PostgreSQL at each login; no change area is told")
	}
	prov := provisioner(t, rst)
	if _, err := prov.SignIn(ctx, loginClaims("alice", "sub-attacker", func(c *principal.LoginClaims) { c.JIT = false })); err == nil {
		t.Error("the provider's account of the name took the user over through the removed link")
	}
	if _, err := prov.SignIn(ctx, loginClaims("alice", "sub-partner", func(c *principal.LoginClaims) {
		c.JIT, c.Provider, c.Issuer = false, "partners", "https://partners.example.com"
	})); err != nil {
		t.Errorf("the kept pending link must still claim: %v", err)
	}

	t.Run("a hand-written entry naming a linked provider changes nothing; an unknown user and a malformed entry are not applied", func(t *testing.T) {
		forged := newMemStore()
		handWritten(t, forged, "user.updated", entryJSON(t, userEntry(erin.ID, "erin", "corp")))
		handWritten(t, forged, "user.updated", entryJSON(t, userEntry("0192f1c4-0000-7000-8000-00000000dead", "ghost", "corp")))
		handWritten(t, forged, "user.updated", []byte(`{"principalId":"`+erin.ID+`"}`))
		// A provider with a NUL would fail the statement that carries it and stop the replay: it is skipped before any statement.
		handWritten(t, forged, "user.updated", entryJSON(t, userEntry(erin.ID, "erin", "co\x00rp")))
		fs, _ := rst.replay(forged, tw.at, rst.changes)
		res := outcomes(fs)
		if o := res["user.updated erin"]; !slices.Equal(o, []journal.Outcome{journal.AlreadyInEffect}) {
			t.Errorf("user.updated erin: %v, want already in effect", o)
		}
		if linkCount("erin", "corp") != 1 || rst.number(`SELECT count(*) FROM user_identity WHERE subject = 'sub-erin'`) != 1 {
			t.Error("a linked identity was removed")
		}
		if fs.Counts[journal.SubjectUnknown] != 1 || fs.Counts[journal.Skipped] != 2 || fs.Counts[journal.Applied] != 0 {
			t.Errorf("counts %v, want one subject unknown, two skipped, none applied", fs.Counts)
		}
	})
	t.Run("R-BAK-024 a second replay applies nothing again", func(t *testing.T) {
		again, _ := rst.replay(store, tw.at, rst.changes)
		if again.Counts[journal.AlreadyInEffect] != 2 || again.Counts[journal.Applied] != 0 {
			t.Errorf("second replay: %v, want both already in effect", again.Counts)
		}
	})
}

// replayChecked replays like twinSide.replay and also runs the admin check of the restore commands, so the summary says whether
// anybody still holds the admin role.
func (s *twinSide) replayChecked(store journal.Store, restoredTo time.Time) (*journal.Summary, string) {
	s.t.Helper()
	r := replay.New(replay.Options{
		DB: s.db, Reader: replay.NewReader(store, twinPrefix), Notifier: s.changes, AdminBinding: authz.HasAdminBinding, Log: quietLog,
	})
	var out strings.Builder
	sum, err := r.Replay(context.Background(), journal.ReplayOptions{RestoredTo: restoredTo, Margin: 5 * time.Minute, Out: &out})
	if err != nil {
		s.t.Fatalf("replay: %v", err)
	}
	return sum, out.String()
}

func userEntry(id, name string, providers ...string) map[string]any {
	return map[string]any{"principalId": id, "name": name, "pendingLinksRemoved": providers}
}

// TestR_AUTH_018_AGroupRenamedAfterTheRestorePointIsDetached: a group renamed after t keeps the restored name, is detached from
// group synchronization and loses its oidc memberships; its manual memberships and its role binding stay, and a login whose
// claim names the group fills nothing and creates no group. A restored group that already carries the journaled name is in
// effect; a forged entry for another group only detaches that group.
func TestR_AUTH_018_AGroupRenamedAfterTheRestorePointIsDetached(t *testing.T) {
	tw := newTwin(t)
	src := tw.open(tw.src)
	ids := newIdentity(t, src)
	prov := provisioner(t, src)
	ctx := context.Background()
	idp := func(g ...string) func(*principal.LoginClaims) {
		return func(c *principal.LoginClaims) { c.Groups, c.CreateGroups = g, true }
	}
	if _, err := prov.SignIn(ctx, loginClaims("alice", "sub-alice", idp("sre", "admins"))); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.manager.CreateUser(ctx, producerAdmin, principal.NewUser{Name: "bob", Email: "bob@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.manager.CreateGroup(ctx, producerAdmin, principal.NewGroup{Name: "plat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ids.manager.PatchMembers(ctx, producerAdmin, "sre", "", []string{"bob"}, nil); err != nil {
		t.Fatal(err)
	}
	groupID := func(s *twinSide, name string) string {
		return s.text(`SELECT id::text FROM principal WHERE kind = 'group' AND name = $1`, name)
	}
	src.exec(`INSERT INTO authz_role_binding (role, principal_id) VALUES ('repository-admin', $1::uuid), ('admin', $2::uuid)`, groupID(src, "sre"), groupID(src, "admins"))
	src.ship(newMemStore()) // what the setup journaled is shipped before t
	src.close()
	tw.snapshot() // t

	src = tw.open(tw.src)
	ids = newIdentity(t, src)
	ops := "ops"
	if _, err := ids.manager.UpdateGroup(ctx, producerAdmin, "sre", principal.GroupPatch{Name: &ops, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	if n := src.ship(store); n != 1 {
		t.Fatalf("%d journal objects shipped, want the rename's", n)
	}
	src.close()

	rst := tw.open(tw.restored)
	sreID := groupID(rst, "sre")
	state := func(name string) (detached bool, oidc, manual, bindings int64) {
		id := groupID(rst, name)
		return rst.text(`SELECT sync_detached::text FROM user_group WHERE principal_id = $1::uuid`, id) == "true",
			rst.number(`SELECT count(*) FROM group_membership WHERE group_id = $1::uuid AND source LIKE 'oidc:%'`, id),
			rst.number(`SELECT count(*) FROM group_membership WHERE group_id = $1::uuid AND source = 'manual'`, id),
			rst.number(`SELECT count(*) FROM authz_role_binding WHERE principal_id = $1::uuid`, id)
	}
	if d, o, m, b := state("sre"); d || o != 1 || m != 1 || b != 1 {
		t.Fatalf("the copy at t: detached %v, oidc %d, manual %d, bindings %d", d, o, m, b)
	}
	sum, _ := rst.replay(store, tw.at, rst.changes)
	const sreSubject = "group.updated sre (detached from group synchronization; holds the role binding repository-admin)"
	if o := outcomes(sum)[sreSubject]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
		t.Errorf("%s: %v, want applied; the line names the group and the role it holds", sreSubject, outcomes(sum))
	}
	if d, o, m, b := state("sre"); !d || o != 0 || m != 1 || b != 1 {
		t.Errorf("after the replay: detached %v, oidc %d, manual %d, bindings %d; want detached, no oidc member, the manual member and the binding kept", d, o, m, b)
	}
	if groupID(rst, "sre") != sreID || rst.number(`SELECT count(*) FROM principal WHERE kind = 'group' AND name = 'ops'`) != 0 {
		t.Error("the replay renamed the group")
	}
	// A login that carries the old group name fills nothing and creates no group.
	if _, err := provisioner(t, rst).SignIn(ctx, loginClaims("carol", "sub-carol", idp("sre"))); err != nil {
		t.Fatal(err)
	}
	if n := rst.number(`SELECT count(*) FROM group_membership m JOIN principal u ON u.id = m.member_id WHERE u.name = 'carol'`); n != 0 {
		t.Errorf("carol joined %d groups through the claim of a detached group", n)
	}

	t.Run("a restored group that already carries the journaled name is in effect; a forged entry detaches only the group it names", func(t *testing.T) {
		forged := newMemStore()
		entry := func(id, name, from string) []byte {
			return entryJSON(t, map[string]any{"principalId": id, "name": name, "renamedFrom": from, "autoJoin": map[string]bool{"before": false, "after": false}})
		}
		platID, adminsID := groupID(rst, "plat"), groupID(rst, "admins")
		handWritten(t, forged, "group.updated", entry(platID, "plat", "plat-old"))
		handWritten(t, forged, "group.updated", entry(adminsID, "x-admins", "admins"))
		handWritten(t, forged, "group.updated", entry("0192f1c4-0000-7000-8000-00000000dead", "ghost", "spirit"))
		handWritten(t, forged, "group.deleted", entryJSON(t, map[string]string{"principalId": "0192f1c4-0000-7000-8000-00000000dead", "name": "a\x00b"}))
		handWritten(t, forged, "group.updated", entryJSON(t, map[string]any{"principalId": platID, "name": "Bad Name", "renamedFrom": "plat", "autoJoin": map[string]bool{"before": false, "after": false}}))
		fs, printed := rst.replayChecked(forged, tw.at)
		res := outcomes(fs)
		if !strings.Contains(printed, "holds the role binding admin") {
			t.Errorf("the replay printed no line for the detached group that holds the admin role:\n%s", printed)
		}
		if !fs.NoAdminBinding || !strings.Contains(printed, "no enabled administrator is left") {
			t.Errorf("NoAdminBinding = %v: the only admin binding is a group whose members the detach removed, and the replay says so:\n%s", fs.NoAdminBinding, printed)
		}
		if o := res["group.updated plat"]; !slices.Equal(o, []journal.Outcome{journal.AlreadyInEffect}) {
			t.Errorf("group.updated plat: %v, want already in effect", o)
		}
		if o := res["group.updated admins (detached from group synchronization; holds the role binding admin)"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
			t.Errorf("group.updated admins: %v, want applied with the role named in the line", res)
		}
		if d, _, _, _ := state("plat"); d {
			t.Error("a group that carries the journaled name was detached")
		}
		if d, o, _, b := state("admins"); !d || o != 0 || b != 1 {
			t.Errorf("admins: detached %v, oidc %d, bindings %d; want detached, no oidc member, the binding kept", d, o, b)
		}
		if fs.Counts[journal.SubjectUnknown] != 1 || fs.Counts[journal.Skipped] != 2 {
			t.Errorf("counts %v, want one subject unknown and two skipped (a malformed name, a NUL in the name of a deleted group)", fs.Counts)
		}
		// The recovery of the lockout: the break-glass sign-in attaches the group again.
		if _, err := newIdentity(t, rst).manager.UpdateGroup(ctx, producerAdmin, "admins", principal.GroupPatch{SyncDetached: new(bool), Expected: errs.AnyRevision}); err != nil {
			t.Fatal(err)
		}
		if d, _, _, _ := state("admins"); d {
			t.Error("the clear did not attach the group")
		}
	})
	t.Run("R-BAK-024 a second replay applies nothing again", func(t *testing.T) {
		again, _ := rst.replay(store, tw.at, rst.changes)
		if again.Counts[journal.AlreadyInEffect] != 1 || again.Counts[journal.Applied] != 0 {
			t.Errorf("second replay: %v, want already in effect", again.Counts)
		}
	})
}

// ttl reads a runtime lifetime of the side's settings in seconds; -1 is NULL, the YAML value.
func (s *twinSide) ttl(column string) int64 {
	return s.number(fmt.Sprintf(`SELECT coalesce(extract(epoch FROM %s), -1)::bigint FROM auth_settings`, column))
}

// TestR_AUTH_026_ALoweredMaximumStaysLoweredAfterARestore: a maximum lowered after t is lowered again by the replay and the
// default above it follows; a raise after t, a journaled null and a value longer than the restored one change nothing; a
// value below a minute or above 100 years is dropped alone; the defaults are not journaled (Q-AUTH-207).
func TestR_AUTH_026_ALoweredMaximumStaysLoweredAfterARestore(t *testing.T) {
	tw := newTwin(t)
	src := tw.open(tw.src)
	ctx := context.Background()
	hours := func(h int) *time.Duration { d := time.Duration(h) * time.Hour; return &d }
	if _, err := settingsStore(t, src).Update(ctx, producerAdmin, token.SettingsPatch{PATDefaultTTL: hours(12), PATMaxTTL: hours(24), Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	src.ship(newMemStore()) // what the setup journaled is shipped before t
	src.close()
	tw.snapshot() // t

	src = tw.open(tw.src)
	settings := settingsStore(t, src)
	if _, err := settings.Update(ctx, producerAdmin, token.SettingsPatch{PATDefaultTTL: hours(1), PATMaxTTL: hours(2), Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.Update(ctx, producerAdmin, token.SettingsPatch{PATMaxTTL: hours(3), Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	if n := src.ship(store); n != 2 {
		t.Fatalf("%d journal objects shipped, want 2", n)
	}
	src.close()

	rst := tw.open(tw.restored)
	if rst.ttl("pat_max_ttl") != 86400 || rst.ttl("pat_default_ttl") != 43200 || rst.ttl("sat_max_ttl") != -1 {
		t.Fatalf("the copy at t holds pat max %d, default %d, sat max %d", rst.ttl("pat_max_ttl"), rst.ttl("pat_default_ttl"), rst.ttl("sat_max_ttl"))
	}
	revision := rst.number(`SELECT revision FROM auth_settings`)
	epoch := rst.epoch("auth")
	sum, _ := rst.replay(store, tw.at, rst.changes)
	got := outcomes(sum)["settings.auth.changed settings"]
	if !slices.Equal(got, []journal.Outcome{journal.Applied, journal.Skipped}) {
		t.Errorf("settings.auth.changed: %v, want applied for the lowering, skipped for the raise after it", got)
	}
	if rst.ttl("pat_max_ttl") != 7200 {
		t.Errorf("pat maximum %d s after the replay, want 7200: the raise to 3 h is not restored", rst.ttl("pat_max_ttl"))
	}
	if rst.ttl("pat_default_ttl") != 7200 {
		t.Errorf("pat default %d s, want 7200: the default above the new maximum is lowered to it", rst.ttl("pat_default_ttl"))
	}
	if rst.number(`SELECT revision FROM auth_settings`) <= revision || rst.epoch("auth") == epoch {
		t.Error("the lowering raises the revision of the settings and tells the replicas")
	}

	t.Run("R-BAK-024 a second replay applies nothing again", func(t *testing.T) {
		again, _ := rst.replay(store, tw.at, rst.changes)
		if again.Counts[journal.Applied] != 0 {
			t.Errorf("second replay: %v, want nothing applied", again.Counts)
		}
		if again.Counts[journal.AlreadyInEffect] != 1 || again.Counts[journal.Skipped] != 1 {
			t.Errorf("second replay: %v, want the lowering in effect and the raise skipped again", again.Counts)
		}
	})
	t.Run("a journaled null, a longer value and a value below a minute change nothing; a shorter value for the unset maximum applies", func(t *testing.T) {
		forged := newMemStore()
		max := func(before, after any) map[string]any {
			return map[string]any{"maxTTLSeconds": map[string]any{"before": before, "after": after}}
		}
		settingsEntry := func(pat, sat map[string]any) []byte {
			return entryJSON(t, map[string]any{
				"anonymousAccess": map[string]bool{"before": false, "after": false},
				"tokens":          map[string]any{"pat": pat, "sat": sat},
			})
		}
		handWritten(t, forged, "settings.auth.changed", settingsEntry(max(7200, nil), max(nil, nil)))
		handWritten(t, forged, "settings.auth.changed", settingsEntry(max(3600, 14400), max(nil, nil)))
		handWritten(t, forged, "settings.auth.changed", settingsEntry(max(nil, nil), max(nil, 59)))
		handWritten(t, forged, "settings.auth.changed", entryJSON(t, map[string]any{"anonymousAccess": map[string]bool{"before": false, "after": false}}))
		fs, _ := rst.replay(forged, tw.at, rst.changes)
		if fs.Counts[journal.Applied] != 0 || fs.Counts[journal.Skipped] != 3 || fs.Counts[journal.AlreadyInEffect] != 1 {
			t.Errorf("counts %v, want three skipped (a null, a longer value and the null beside the unreadable sat maximum, which is dropped alone) and one in effect (the anonymous part alone)", fs.Counts)
		}
		if rst.ttl("pat_max_ttl") != 7200 || rst.ttl("sat_max_ttl") != -1 {
			t.Errorf("pat max %d, sat max %d: a forged entry widened a maximum", rst.ttl("pat_max_ttl"), rst.ttl("sat_max_ttl"))
		}
		// An out-of-range maximum narrows nothing and does not take the anonymous switch-off with it.
		rst.exec(`UPDATE auth_settings SET anonymous_access = true`)
		mixed := newMemStore()
		handWritten(t, mixed, "settings.auth.changed", entryJSON(t, map[string]any{
			"anonymousAccess": map[string]bool{"before": true, "after": false},
			"tokens":          map[string]any{"pat": max(7200, 99999999999), "sat": max(nil, "x")},
		}))
		ms, _ := rst.replay(mixed, tw.at, rst.changes)
		if ms.Counts[journal.Applied] != 1 || rst.text(`SELECT anonymous_access::text FROM auth_settings`) != "false" || rst.ttl("pat_max_ttl") != 7200 {
			t.Errorf("counts %v, anonymous %s, pat max %d: the switch must go off and the maximum stay", ms.Counts, rst.text(`SELECT anonymous_access::text FROM auth_settings`), rst.ttl("pat_max_ttl"))
		}
		shorter := newMemStore()
		handWritten(t, shorter, "settings.auth.changed", settingsEntry(max(nil, nil), max(nil, 3600)))
		ss, _ := rst.replay(shorter, tw.at, rst.changes)
		if ss.Counts[journal.Applied] != 1 || rst.ttl("sat_max_ttl") != 3600 || rst.ttl("sat_default_ttl") != -1 {
			t.Errorf("counts %v, sat max %d, sat default %d: a number below the YAML value applies and a NULL default stays NULL", ss.Counts, rst.ttl("sat_max_ttl"), rst.ttl("sat_default_ttl"))
		}
	})
}
