//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

const (
	bastionPrincipal = "bb000000-0000-0000-0000-0000000000a1"
	bastionOther     = "bb000000-0000-0000-0000-0000000000a2"
	bastionToken     = "bb000000-0000-0000-0000-0000000000b1"
	bastionEvent     = "bb000000-0000-0000-0000-0000000000c1"
)

// TestBastionMigration checks 00032_bastion to 00034_bastion_validate and the statements of the Bastion query
// files against the migrated schema (R-BAS-004, R-BAS-005, R-BAS-010, R-BAS-015, R-HA-018, R-BAK-004).
func TestBastionMigration(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	_, _, admin := migrated(t, st)
	ctx := context.Background()

	t.Run("R-BAK-004 the Bastion tables carry their export classes", func(t *testing.T) {
		want := map[string]tables.Class{"bastion": tables.Data, "bastion_contact": tables.Transient}
		for name, class := range want {
			if got, ok := tables.Lookup(name); !ok || got.Class != class {
				t.Errorf("%s registered as %+v (%v), want %s", name, got, ok, class)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+name) {
				t.Errorf("table %s is not created by the migrations", name)
			}
		}
	})

	t.Run("R-HA-018 the Bastion steps: an expand step, the contract step that only loosens, the validation", func(t *testing.T) {
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		phases := map[int]string{}
		for _, s := range steps {
			if s.Version >= 32 && s.Version <= 34 {
				phases[s.Version] = s.Phase
				if s.Version == 33 && s.RequiresApp != "0.1.0" {
					t.Errorf("%s: requires-app %q, want the lowest release (it only loosens)", s.Name, s.RequiresApp)
				}
				if s.Version != 33 && s.RequiresApp != "" {
					t.Errorf("%s: an expand step with requires-app %q", s.Name, s.RequiresApp)
				}
			}
		}
		if phases[32] != "expand" || phases[33] != "contract" || phases[34] != "expand" {
			t.Errorf("phases of 00032 to 00034 = %v", phases)
		}
		for _, c := range []string{"principal_kind_check", "principal_bastion_name", "api_token_type_check"} {
			if !scalar[bool](t, admin, "SELECT convalidated FROM pg_constraint WHERE conname = $1 AND connamespace = $2::regnamespace", c, schemaName) {
				t.Errorf("%s is not validated", c)
			}
		}
	})

	t.Run("a registration is a principal of the kind bastion with a name of at most 20 characters", func(t *testing.T) { // R-BAS-004
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ($1, 'bastion', 'partners')", bastionPrincipal)
		mustExec(t, admin, "INSERT INTO trove.bastion (principal_id, description) VALUES ($1, 'partner network')", bastionPrincipal)
		wantState(t, admin, "23514", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'bastion', 'Partners')")
		wantState(t, admin, "23514", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'bastion', '1partners')")
		wantState(t, admin, "23514", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'bastion', 'abcdefghijklmnopqrstu')")
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ('bb000000-0000-0000-0000-0000000000a4', 'bastion', 'abcdefghijklmnopqrst')")
		mustExec(t, admin, "INSERT INTO trove.bastion (principal_id) VALUES ('bb000000-0000-0000-0000-0000000000a4')")
		wantState(t, admin, "23505", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'bastion', 'partners')")
		// A subtype row of a principal of another kind is refused (AI001).
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ($1, 'service_account', 'not-a-bastion')", bastionOther)
		wantState(t, admin, "AI001", "INSERT INTO trove.bastion (principal_id) VALUES ($1)", bastionOther)
		// The kinds of the other users of the principal table are unchanged.
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'user', 'bastion-test-user')")
		wantState(t, admin, "23514", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'robot', 'x')")
	})

	t.Run("a registration holds no role binding and no membership; it owns bst tokens", func(t *testing.T) { // R-BAS-004, R-BAS-005
		wantState(t, admin, "AI001", "INSERT INTO trove.authz_role_binding (role, principal_id) VALUES ('admin', $1)", bastionPrincipal)
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ('bb000000-0000-0000-0000-0000000000a3', 'group', 'bastion-test-group')")
		mustExec(t, admin, "INSERT INTO trove.user_group (principal_id, origin) VALUES ('bb000000-0000-0000-0000-0000000000a3', 'admin')")
		wantState(t, admin, "AI001", "INSERT INTO trove.group_membership (group_id, member_id, source) "+
			"VALUES ('bb000000-0000-0000-0000-0000000000a3', $1, 'manual')", bastionPrincipal)
		token := "INSERT INTO trove.api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at) " +
			"VALUES ($1, $2, decode(repeat('cd', 32), 'hex'), 'trv_bst_…abcd', $3, 'Bastion token', '{}', 'api', %s)"
		mustExec(t, admin, strings.Replace(token, "%s", "now() + interval '1 year'", 1), bastionToken, "bst", bastionPrincipal)
		// A Bastion token expires; a bst token of a user principal is refused by the owner guard.
		wantState(t, admin, "23514", strings.Replace(token, "%s", "NULL", 1), "bb000000-0000-0000-0000-0000000000b2", "bst", bastionPrincipal)
		wantState(t, admin, "23514", strings.Replace(token, "%s", "now() + interval '1 day'", 1), "bb000000-0000-0000-0000-0000000000b3", "xyz", bastionPrincipal)
	})

	t.Run("the description is a member of the registration: a change moves principal.revision", func(t *testing.T) { // R-API-012
		before := scalar[int64](t, admin, "SELECT revision FROM trove.principal WHERE id = $1", bastionPrincipal)
		mustExec(t, admin, "UPDATE trove.bastion SET description = 'partner network, EU' WHERE principal_id = $1", bastionPrincipal)
		if after := scalar[int64](t, admin, "SELECT revision FROM trove.principal WHERE id = $1", bastionPrincipal); after != before+1 {
			t.Errorf("revision %d -> %d, want +1", before, after)
		}
	})

	t.Run("the statements of the registration file run against the schema", func(t *testing.T) { // R-BAS-004
		mustExec(t, admin, "SET search_path = trove, public")
		qs := q.New(admin)

		got, err := qs.BastionGet(ctx, "partners")
		if err != nil || len(got) != 1 || got[0].ID != bastionPrincipal || !got[0].Enabled || got[0].Description != "partner network, EU" {
			t.Fatalf("BastionGet: %+v %v", got, err)
		}
		if rows, err := qs.BastionGet(ctx, "unknown"); err != nil || len(rows) != 0 {
			t.Fatalf("BastionGet of an unknown name: %v %v", rows, err)
		}
		if n, err := qs.BastionCount(ctx); err != nil || n != 2 {
			t.Fatalf("BastionCount: %d %v", n, err)
		}
		list, err := qs.BastionList(ctx, q.BastionListParams{MaxRows: 10})
		if err != nil || len(list) != 2 || list[0].Name != "abcdefghijklmnopqrst" || list[1].Name != "partners" {
			t.Fatalf("BastionList: %+v %v", list, err)
		}
		if rows, err := qs.BastionLock(ctx, "partners"); err != nil || len(rows) != 1 || rows[0].Revision < 1 {
			t.Fatalf("BastionLock: %+v %v", rows, err)
		}
		if err := qs.BastionUpdateDescription(ctx, q.BastionUpdateDescriptionParams{ID: bastionPrincipal, Description: "partners"}); err != nil {
			t.Fatalf("BastionUpdateDescription: %v", err)
		}
		if n, err := qs.BastionCountActiveTokens(ctx, bastionPrincipal); err != nil || n != 1 {
			t.Fatalf("BastionCountActiveTokens: %d %v", n, err)
		}
		toks, err := qs.BastionListTokens(ctx, q.BastionListTokensParams{ID: bastionPrincipal})
		if err != nil || len(toks) != 1 || toks[0].ID != bastionToken || toks[0].Expired {
			t.Fatalf("BastionListTokens: %+v %v", toks, err)
		}
		if all, err := qs.BastionListTokensOfAll(ctx); err != nil || len(all) != 1 || all[0].BastionID != bastionPrincipal {
			t.Fatalf("BastionListTokensOfAll: %+v %v", all, err)
		}
		if own, err := qs.BastionTokenOwner(ctx, q.BastionTokenOwnerParams{ID: bastionToken, Name: "partners"}); err != nil || len(own) != 1 {
			t.Fatalf("BastionTokenOwner: %+v %v", own, err)
		}
		if own, err := qs.BastionTokenOwner(ctx, q.BastionTokenOwnerParams{ID: bastionToken, Name: "other"}); err != nil || len(own) != 0 {
			t.Fatalf("BastionTokenOwner of another registration: %+v %v", own, err)
		}
		secret := make([]byte, 32)
		for i := range secret {
			secret[i] = 0xcd
		}
		by, err := qs.BastionTokenBySecret(ctx, secret)
		if err != nil || len(by) != 1 || by[0].TokenID != bastionToken || by[0].Name != "partners" || !by[0].Enabled || by[0].Revoked || by[0].Expired {
			t.Fatalf("BastionTokenBySecret: %+v %v", by, err)
		}
		if off, err := qs.BastionSetEnabled(ctx, q.BastionSetEnabledParams{ID: bastionPrincipal, Enabled: false}); err != nil || len(off) != 1 {
			t.Fatalf("BastionSetEnabled(false): %v %v", off, err)
		}
		if again, err := qs.BastionSetEnabled(ctx, q.BastionSetEnabledParams{ID: bastionPrincipal, Enabled: false}); err != nil || len(again) != 0 {
			t.Fatalf("BastionSetEnabled(false) twice: %v %v", again, err)
		}
		if by, err := qs.BastionTokenBySecret(ctx, secret); err != nil || len(by) != 1 || by[0].Enabled {
			t.Fatalf("BastionTokenBySecret of a disabled registration: %+v %v", by, err)
		}
		if on, err := qs.BastionSetEnabled(ctx, q.BastionSetEnabledParams{ID: bastionPrincipal, Enabled: true}); err != nil || len(on) != 1 {
			t.Fatalf("BastionSetEnabled(true): %v %v", on, err)
		}
	})

	t.Run("a Bastion token is not a token of the token API", func(t *testing.T) { // R-BAS-004, R-ARCH-025
		qs := q.New(admin)
		rows, err := qs.IdentityListTokens(ctx, q.IdentityListTokensParams{OwnerID: bastionPrincipal, MaxRows: 50})
		if err != nil || len(rows) != 0 {
			t.Fatalf("IdentityListTokens of the registration: %+v %v", rows, err)
		}
		all, err := qs.IdentityListTokens(ctx, q.IdentityListTokensParams{MaxRows: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range all {
			if r.Type == "bst" {
				t.Errorf("the list holds the Bastion token %s", r.ID)
			}
		}
		if row, err := qs.IdentityTokenRow(ctx, bastionToken); err != nil || len(row) != 0 {
			t.Fatalf("IdentityTokenRow of a Bastion token: %+v %v", row, err)
		}
	})

	t.Run("the contact of a replica is written once a minute and reports a change", func(t *testing.T) { // R-BAS-010
		qs := q.New(admin)
		p := q.BastionContactUpsertParams{
			BastionID: bastionPrincipal, Replica: "trove-bastion-7d9c-x2k4q", ClientAddress: "198.51.100.20", Release: "0.15.0",
			InterfaceVersion: 1, BaseUrl: "https://packages.example.com", TokenID: bastionToken,
		}
		first, err := qs.BastionContactUpsert(ctx, p)
		if err != nil || first.Written != 1 || !first.Changed {
			t.Fatalf("first contact: %+v %v", first, err)
		}
		same, err := qs.BastionContactUpsert(ctx, p)
		if err != nil || same.Written != 0 || same.Changed {
			t.Fatalf("a second contact within the minute: %+v %v", same, err)
		}
		p.Release = "0.15.1"
		upgraded, err := qs.BastionContactUpsert(ctx, p)
		if err != nil || upgraded.Written != 1 || !upgraded.Changed {
			t.Fatalf("a changed release: %+v %v", upgraded, err)
		}
		mustExec(t, admin, "UPDATE trove.bastion_contact SET contact_at = now() - interval '2 minutes'")
		due, err := qs.BastionContactUpsert(ctx, p)
		if err != nil || due.Written != 1 || due.Changed {
			t.Fatalf("a contact a minute after the last write: %+v %v", due, err)
		}
		cs, err := qs.BastionContactList(ctx, bastionPrincipal)
		if err != nil || len(cs) != 1 || cs[0].Release != "0.15.1" || cs[0].ClientAddress != "198.51.100.20" || cs[0].InterfaceVersion != 1 {
			t.Fatalf("BastionContactList: %+v %v", cs, err)
		}
		if all, err := qs.BastionContactListAll(ctx); err != nil || len(all) != 1 {
			t.Fatalf("BastionContactListAll: %+v %v", all, err)
		}
		if urls, err := qs.BastionLastBaseURL(ctx, "partners"); err != nil || len(urls) != 1 || urls[0] != "https://packages.example.com" {
			t.Fatalf("BastionLastBaseURL: %v %v", urls, err)
		}
		mustExec(t, admin, "UPDATE trove.bastion_contact SET contact_at = now() - interval '40 days'")
		if n, err := qs.BastionContactPrune(ctx, 30*24*3600); err != nil || n != 1 {
			t.Fatalf("BastionContactPrune: %d %v", n, err)
		}
	})

	t.Run("the feed statements read the journaled events behind and ahead of a cursor", func(t *testing.T) { // R-BAS-015
		qs := q.New(admin)
		mustExec(t, admin, "SELECT trove.audit_maintain_partitions()")
		ins := "INSERT INTO trove.audit_event (id, occurred_at, action, category, outcome, actor_type, resource_type, resource_id, repository, details) " +
			"VALUES ($1, now() - $2::interval, $3, 'auth', 'success', 'user', 'token', 'x', $4, '{\"tokens\":[\"t1\"]}')"
		mustExec(t, admin, ins, "bb000000-0000-0000-0000-0000000000d1", "10 minutes", "token.revoked", "")
		mustExec(t, admin, ins, "bb000000-0000-0000-0000-0000000000d2", "3 minutes", "token.revoked", "")
		mustExec(t, admin, ins, bastionEvent, "2 minutes", "npm.unpublish", "npm-local")
		mustExec(t, admin, ins, "bb000000-0000-0000-0000-0000000000d3", "1 minute", "package.published", "npm-local")
		actions := []string{"token.revoked", "npm.unpublish"}
		at := scalar[string](t, admin, "SELECT to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') FROM trove.audit_event WHERE id = $1", "bb000000-0000-0000-0000-0000000000d2")

		ahead, err := qs.BastionFeedAhead(ctx, q.BastionFeedAheadParams{CursorAt: at, CursorID: "bb000000-0000-0000-0000-0000000000d2", Actions: actions, MaxRows: 10})
		if err != nil || len(ahead) != 1 || ahead[0].ID != bastionEvent || ahead[0].Repository != "npm-local" || !strings.Contains(string(ahead[0].Details), "t1") {
			t.Fatalf("BastionFeedAhead: %+v %v", ahead, err)
		}
		// The lookback of five minutes reaches the 3-minute event but not the 10-minute one.
		behind, err := qs.BastionFeedBehind(ctx, q.BastionFeedBehindParams{CursorAt: at, CursorID: "bb000000-0000-0000-0000-0000000000d2", LookbackSecs: 300, Actions: actions, MaxRows: 100})
		if err != nil || len(behind) != 1 || behind[0].ID != "bb000000-0000-0000-0000-0000000000d2" {
			t.Fatalf("BastionFeedBehind: %+v %v", behind, err)
		}
		// The synthetic cursor reads from a time with the nil UUID.
		oldest, err := qs.BastionFeedAhead(ctx, q.BastionFeedAheadParams{
			CursorAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), CursorID: "00000000-0000-0000-0000-000000000000", Actions: actions, MaxRows: 2,
		})
		if err != nil || len(oldest) != 2 {
			t.Fatalf("BastionFeedAhead from a synthetic cursor: %+v %v", oldest, err)
		}
		state, err := qs.BastionFeedCursorState(ctx, q.BastionFeedCursorStateParams{CursorAt: at, CursorID: "bb000000-0000-0000-0000-0000000000d2"})
		if err != nil || !state.CursorExists || state.RetentionDays != 365 || state.DbNowUs < time.Now().Add(-time.Minute).UnixMicro() {
			t.Fatalf("BastionFeedCursorState: %+v %v", state, err)
		}
		gone, err := qs.BastionFeedCursorState(ctx, q.BastionFeedCursorStateParams{CursorAt: at, CursorID: "bb000000-0000-0000-0000-0000000000ff"})
		if err != nil || gone.CursorExists {
			t.Fatalf("BastionFeedCursorState of a vanished entry: %+v %v", gone, err)
		}
		newest, err := qs.BastionFeedNow(ctx, q.BastionFeedNowParams{WindowSecs: 3600, Actions: actions})
		if err != nil || newest.ID != bastionEvent {
			t.Fatalf("BastionFeedNow: %+v %v", newest, err)
		}
	})

	t.Run("a content entry names the repository and every virtual repository that includes it, recursively", func(t *testing.T) { // R-BAS-015
		qs := q.New(admin)
		mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class, project_id) VALUES ('npm-local', 'npm', 'local', (SELECT id FROM trove.project WHERE key = 'it')), ('npm-all', 'npm', 'virtual', (SELECT id FROM trove.project WHERE key = 'it')), ('npm-top', 'npm', 'virtual', (SELECT id FROM trove.project WHERE key = 'it'))")
		mustExec(t, admin, "INSERT INTO trove.virtual_members (virtual_id, position, member_id) "+
			"SELECT v.id, 0, m.id FROM trove.repositories v, trove.repositories m WHERE v.key = 'npm-all' AND m.key = 'npm-local'")
		mustExec(t, admin, "INSERT INTO trove.virtual_members (virtual_id, position, member_id) "+
			"SELECT v.id, 0, m.id FROM trove.repositories v, trove.repositories m WHERE v.key = 'npm-top' AND m.key = 'npm-all'")
		keys, err := qs.BastionVirtualClosure(ctx, []string{"it/npm-local"})
		if err != nil || strings.Join(keys, ",") != "it/npm-all,it/npm-local,it/npm-top" {
			t.Fatalf("BastionVirtualClosure: %v %v", keys, err)
		}
		if keys, err := qs.BastionVirtualClosure(ctx, []string{"it/unknown"}); err != nil || len(keys) != 0 {
			t.Fatalf("BastionVirtualClosure of an unknown key: %v %v", keys, err)
		}
		kinds, err := qs.BastionPrincipalKinds(ctx, []string{bastionPrincipal, bastionOther, "bb000000-0000-0000-0000-0000000000ee"})
		if err != nil || len(kinds) != 2 {
			t.Fatalf("BastionPrincipalKinds: %+v %v", kinds, err)
		}
	})

	t.Run("deleting a registration removes its subtype row, its contacts and its tokens", func(t *testing.T) { // R-BAS-004
		qs := q.New(admin)
		if _, err := qs.BastionContactUpsert(ctx, q.BastionContactUpsertParams{BastionID: bastionPrincipal, Replica: "r1", InterfaceVersion: 1}); err != nil {
			t.Fatal(err)
		}
		mustExec(t, admin, "DELETE FROM trove.principal WHERE id = $1", bastionPrincipal)
		for _, table := range []string{"bastion", "bastion_contact"} {
			col := "principal_id"
			if table == "bastion_contact" {
				col = "bastion_id"
			}
			if n := scalar[int](t, admin, "SELECT count(*) FROM trove."+table+" WHERE "+col+" = $1", bastionPrincipal); n != 0 {
				t.Errorf("%s still holds %d rows of the deleted registration", table, n)
			}
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.api_token WHERE principal_id = $1", bastionPrincipal); n != 0 {
			t.Errorf("%d tokens of the deleted registration remain", n)
		}
	})
}
