//go:build integration

package login

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/session"
)

// TestR_STOR_018_TheDeleteJobReadsTheSessionThatAskedFromItsRow: the delete job of a large subtree asks again, for every batch,
// whether the session that queued it still stands (Q-AUTHZ-261, Q-AUTHZ-262). The read goes to the session rows, never to a cache: a
// withdrawal stops the job at once, also of a session that had already ended by time, by the session limit or by a
// re-authentication; an end by time alone does not; a row that is gone is purged.
func TestR_STOR_018_TheDeleteJobReadsTheSessionThatAskedFromItsRow(t *testing.T) {
	e := newEnv(t)
	e.start(1, e.defaultAuth())
	ctx := context.Background()
	rep := e.replicas[0]
	actor := audit.Actor{Kind: "user", Name: "test"}
	principalNamed := func(name string, breakGlass bool) string {
		t.Helper()
		var id string
		if err := e.admin.QueryRow(ctx, `INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'user', $1) RETURNING id::text`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := e.admin.Exec(ctx, `INSERT INTO app_user (principal_id, origin, is_break_glass) VALUES ($1::uuid, 'admin', $2)`, id, breakGlass); err != nil {
			t.Fatal(err)
		}
		return id
	}
	uid := principalNamed("deleter", false)
	create := func(n session.NewSession) session.Created {
		t.Helper()
		c, err := rep.sessions.Create(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	open := func() string { return create(session.NewSession{PrincipalID: uid}).ID }
	set := func(id, assignment string) {
		t.Helper()
		if _, err := e.admin.Exec(ctx, `UPDATE ui_session SET `+assignment+` WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
	}
	stands := func(t *testing.T, id, owner string) {
		t.Helper()
		got, err := session.Standing(ctx, rep.db, id)
		if err != nil || got != owner {
			t.Fatalf("the session must stand for its principal %s: %q, %v", owner, got, err)
		}
	}
	gone := func(t *testing.T, id string, want error) {
		t.Helper()
		if _, err := session.Standing(ctx, rep.db, id); !errors.Is(err, want) {
			t.Fatalf("the session must be %v: %v", want, err)
		}
	}
	logout := func(t *testing.T, c session.Created) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, e.baseURL+"/auth/logout", nil)
		r.AddCookie(&http.Cookie{Name: session.CookieSession, Value: c.Secret})
		r.Header.Set(session.HeaderCSRF, c.CSRF)
		r.Header.Set("Origin", e.baseURL)
		out, err := rep.sessions.Logout(r)
		if err != nil || !out.Ended {
			t.Fatalf("logout: %+v, %v", out, err)
		}
	}

	t.Run("R-STOR-018 a live session stands for its principal", func(t *testing.T) { stands(t, open(), uid) })

	// R-AUTH-010: the idle and the absolute deadline.
	t.Run("R-STOR-018 a session ended by time stands, whether or not a request recorded the end", func(t *testing.T) {
		idle := open()
		set(idle, `ended_at = now(), end_reason = 'idle'`)
		stands(t, idle, uid)
		ran := open()
		set(ran, `absolute_expires_at = now() - interval '1 second'`)
		stands(t, ran, uid)
	})

	// R-AUTH-013, R-AUTH-015, R-AUTH-016: a sign-out, an administrator's end, a disable.
	t.Run("R-STOR-018 a sign-out, an administrator's end and a disable withdraw the session at once", func(t *testing.T) {
		for _, reason := range []string{session.EndLogout, session.EndRevoked, session.EndPrincipalDisable} {
			id := open()
			set(id, `ended_at = now(), end_reason = '`+reason+`'`)
			gone(t, id, session.ErrWithdrawn)
		}
	})

	// R-AUTH-011, R-AUTH-013: the re-authenticated session carries the job of the one it replaced.
	t.Run("R-STOR-018 a re-authentication hands the session's standing to its successor, and a sign-out of the successor withdraws both", func(t *testing.T) {
		first := create(session.NewSession{PrincipalID: uid})
		second := create(session.NewSession{PrincipalID: uid, ReplaceID: first.ID})
		stands(t, first.ID, uid)
		third := create(session.NewSession{PrincipalID: uid, ReplaceID: second.ID})
		stands(t, first.ID, uid)
		logout(t, third)
		gone(t, first.ID, session.ErrWithdrawn)
		gone(t, second.ID, session.ErrWithdrawn)
	})

	// R-AUTH-010: the fixation rule ends the session the browser presented at a sign-in.
	t.Run("R-STOR-018 a sign-in of the same principal in the same browser hands the session on, and a planted session of another principal is withdrawn by it", func(t *testing.T) {
		before := create(session.NewSession{PrincipalID: uid})
		after := create(session.NewSession{PrincipalID: uid, ReplaceCookie: before.Secret})
		stands(t, before.ID, uid)
		logout(t, after)
		gone(t, before.ID, session.ErrWithdrawn)
		planter := principalNamed("planter", false)
		planted := create(session.NewSession{PrincipalID: planter})
		create(session.NewSession{PrincipalID: uid, ReplaceCookie: planted.Secret})
		gone(t, planted.ID, session.ErrWithdrawn)
	})

	// R-AUTH-010, R-AUTH-041: the end of one own session that had ended already.
	t.Run("R-STOR-018 ending one own session that had already ended withdraws it, answers that it was found and records the end", func(t *testing.T) {
		idle := open()
		set(idle, `ended_at = now(), end_reason = 'idle'`)
		events := len(e.audit.events(audit.EventAuthSessionEnded))
		found, err := rep.sessions.EndOne(ctx, uid, idle, session.EndLogout, actor)
		if err != nil || !found {
			t.Fatalf("EndOne: found %v, %v", found, err)
		}
		gone(t, idle, session.ErrWithdrawn)
		evs := e.audit.events(audit.EventAuthSessionEnded)
		if len(evs) != events+1 {
			t.Fatalf("%d auth.session.ended events, want one more than %d", len(evs), events)
		}
		var d struct {
			SessionIDs   []string `json:"session_ids"`
			WithdrawnIDs []string `json:"withdrawn_ids"`
		}
		if err := json.Unmarshal(evs[len(evs)-1].Details, &d); err != nil || len(d.SessionIDs) != 0 || len(d.WithdrawnIDs) != 1 || d.WithdrawnIDs[0] != idle {
			t.Errorf("the event must name the withdrawn session apart from the ended ones: %s, %v", evs[len(evs)-1].Details, err)
		}
		if found, err := rep.sessions.EndOne(ctx, uid, idle, session.EndLogout, actor); err != nil || found {
			t.Errorf("a second end of the withdrawn session: found %v, %v", found, err)
		}
	})

	// R-AUTH-010: the session limit evicts, the user ends the other sessions.
	t.Run("R-STOR-018 an evicted session is withdrawn by the end of the user's other sessions, and so is the session the kept one replaced by re-authentication", func(t *testing.T) {
		evicted := open()
		set(evicted, `ended_at = now(), end_reason = 'evicted'`)
		stands(t, evicted, uid)
		before := create(session.NewSession{PrincipalID: uid})
		kept := create(session.NewSession{PrincipalID: uid, ReplaceID: before.ID})
		stands(t, before.ID, uid)
		if _, err := rep.sessions.EndExcept(ctx, uid, kept.ID, session.EndLogout, actor); err != nil {
			t.Fatal(err)
		}
		gone(t, evicted, session.ErrWithdrawn)
		gone(t, before.ID, session.ErrWithdrawn)
		stands(t, kept.ID, uid)
	})

	// R-AUTH-010: a thief holds a copy of the browser's session, the user signs in again in that browser and ends the other sessions.
	t.Run("R-STOR-018 a stolen copy of the browser's earlier session loses its delete job when the user, signed in again, ends the other sessions", func(t *testing.T) {
		copied := create(session.NewSession{PrincipalID: uid})
		again := create(session.NewSession{PrincipalID: uid, ReplaceCookie: copied.Secret})
		stands(t, copied.ID, uid) // the thief's job follows the new session of the browser
		if _, err := rep.sessions.EndExcept(ctx, uid, again.ID, session.EndLogout, actor); err != nil {
			t.Fatal(err)
		}
		gone(t, copied.ID, session.ErrWithdrawn)
		stands(t, again.ID, uid)
	})

	// R-AUTH-015: an administrator ends the sessions of a user.
	t.Run("R-STOR-018 an administrator's end of a user's sessions withdraws the ones that had ended already", func(t *testing.T) {
		idle := open()
		set(idle, `ended_at = now(), end_reason = 'idle'`)
		if _, err := rep.sessions.EndPrincipalSessions(ctx, uid, actor); err != nil {
			t.Fatal(err)
		}
		gone(t, idle, session.ErrWithdrawn)
	})

	// R-AUTH-014: a changed break-glass password ends every break-glass session.
	t.Run("R-STOR-018 a break-glass session that idled out is withdrawn by a change of the break-glass password", func(t *testing.T) {
		bg := principalNamed("break-glass", true)
		idle := create(session.NewSession{PrincipalID: bg, BreakGlass: true}).ID
		set(idle, `ended_at = now(), end_reason = 'idle'`)
		stands(t, idle, bg)
		if err := rep.sessions.EndBreakGlass(ctx, session.EndPasswordChanged, actor); err != nil {
			t.Fatal(err)
		}
		gone(t, idle, session.ErrWithdrawn)
	})

	// R-AUTH-043: the session cleanup deletes a row 30 days after its end.
	t.Run("R-STOR-018 a session whose row, or whose successor's row, is gone is purged", func(t *testing.T) {
		id := open()
		if _, err := e.admin.Exec(ctx, `DELETE FROM ui_session WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
		gone(t, id, session.ErrPurged)
		first := create(session.NewSession{PrincipalID: uid})
		second := create(session.NewSession{PrincipalID: uid, ReplaceID: first.ID})
		if _, err := e.admin.Exec(ctx, `DELETE FROM ui_session WHERE id = $1::uuid`, second.ID); err != nil {
			t.Fatal(err)
		}
		gone(t, first.ID, session.ErrPurged)
	})
}
