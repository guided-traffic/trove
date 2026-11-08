package session

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// LoggedOut is what logout found: the provider and the stored ID token of the
// ended session, for RP-initiated logout (R-AUTH-013). Ended is false when the
// cookie named no live session; logout is idempotent.
type LoggedOut struct {
	Ended      bool
	ID         string
	ProviderID string
	IDToken    string
}

// Logout ends the session of the request's cookie with reason logout and
// records the end in the area auth in the same transaction (R-AUTH-013). The
// request must pass both CSRF checks of R-AUTH-012 when the cookie names a live
// session; a cookie of an ended or unknown session ends nothing and passes.
func (s *Store) Logout(r *http.Request) (LoggedOut, error) {
	ctx := r.Context()
	cookie := CookieValue(r, CookieSession)
	if !WellFormed(cookie) {
		return LoggedOut{}, nil
	}
	var rows []q.SessionForLogoutRow
	if err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		rows, err = q.New(db).SessionForLogout(ctx, Hash(cookie))
		return err
	}); err != nil {
		return LoggedOut{}, err
	}
	if len(rows) == 0 || !rows[0].Live {
		return LoggedOut{}, nil
	}
	row := rows[0]
	if err := s.checkCSRF(r, row.ID, row.CsrfSha256); err != nil {
		return LoggedOut{}, err
	}
	out := LoggedOut{ID: row.ID, ProviderID: row.ProviderID, IDToken: row.IDToken}
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		ended, err := qs.EndSessionsByID(ctx, q.EndSessionsByIDParams{Ids: []string{row.ID}, Reason: EndLogout, Withdrawn: true})
		if err != nil {
			return err
		}
		out.Ended = len(ended) > 0
		var es []endedSession
		for _, e := range ended {
			es = append(es, endedSession{e.ID, e.SecretSha256Hex})
		}
		return notifyEnded(s.o.Changes, tx, es)
	})
	return out, err
}

// EndForPrincipal ends every session of a principal on the caller's
// transaction, with the change in the area auth, and returns the IDs of the
// sessions it ended and of those it withdrew after they had ended (Q-AUTHZ-262).
// It takes the principal's row lock first, as a sign-in and a re-authentication
// do, so no session of the principal starts or is replaced while it runs. The
// identity area calls it when it disables or deletes a user (R-AUTH-016); the
// caller records its own event. reason is one of the End constants.
func (s *Store) EndForPrincipal(ctx context.Context, tx meta.Tx, principalID, reason string) (ended, withdrawn []string, err error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, nil, err
	}
	if _, err := qs.LockPrincipal(ctx, principalID); err != nil {
		return nil, nil, err
	}
	rows, err := qs.EndSessionsOfPrincipal(ctx, q.EndSessionsOfPrincipalParams{PrincipalID: principalID, Reason: reason})
	if err != nil {
		return nil, nil, err
	}
	ended, es := endedOf(rows)
	if withdrawn, err = qs.WithdrawEndedSessionsOfPrincipal(ctx, principalID); err != nil {
		return nil, nil, err
	}
	return ended, withdrawn, notifyEnded(s.o.Changes, tx, es)
}

// EndPrincipalSessions is EndForPrincipal in a transaction of its own, with the
// event auth.session.ended (trove admin sessions end --user, R-AUTH-015; the
// session API of wave 2). It returns the IDs of the sessions it ended and of
// those it withdrew after they had ended; the event names both, apart, and is
// written when there is either.
func (s *Store) EndPrincipalSessions(ctx context.Context, principalID string, actor audit.Actor) ([]string, error) {
	var ids []string
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ended, withdrawn, err := s.EndForPrincipal(ctx, tx, principalID, EndRevoked)
		if ids = append(ended, withdrawn...); err != nil || len(ids) == 0 {
			return err
		}
		return s.o.Audit.RecordTx(ctx, tx, endedEvent(actor, EndRevoked, principalID, ended, withdrawn))
	})
	return ids, err
}

// EndOne ends one session of a principal (DELETE /users/me/sessions/{id},
// R-AUTH-010) and reports whether there was one: a live session ends, a session
// that had ended already is withdrawn (Q-AUTHZ-262), and either is found; a
// session of another principal, and one withdrawn already, matches nothing, so a
// repeated end answers found once and then not. reason is one of the End
// constants; the event auth.session.ended names the actor.
func (s *Store) EndOne(ctx context.Context, principalID, sessionID, reason string, actor audit.Actor) (bool, error) {
	found := false
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		found = false
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		if _, err := qs.LockPrincipal(ctx, principalID); err != nil {
			return err
		}
		rows, err := qs.EndSessionOfPrincipal(ctx, q.EndSessionOfPrincipalParams{ID: sessionID, PrincipalID: principalID, Reason: reason})
		if err != nil {
			return err
		}
		ended, es := endedOf(rows)
		withdrawn, err := qs.WithdrawEndedSessionOfPrincipal(ctx, q.WithdrawEndedSessionOfPrincipalParams{ID: sessionID, PrincipalID: principalID})
		if err != nil {
			return err
		}
		if len(ended)+len(withdrawn) == 0 {
			return nil
		}
		found = true
		if err := notifyEnded(s.o.Changes, tx, es); err != nil {
			return err
		}
		return s.o.Audit.RecordTx(ctx, tx, endedEvent(actor, reason, principalID, ended, withdrawn))
	})
	return found, err
}

// EndExcept ends every session of a principal but keepID (DELETE
// /users/me/sessions?keepCurrent=true), withdraws every one that had ended
// already, the sessions keepID replaced included (Q-AUTHZ-262), and returns the
// IDs of both, under the principal's row lock.
func (s *Store) EndExcept(ctx context.Context, principalID, keepID, reason string, actor audit.Actor) ([]string, error) {
	var ids []string
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		ids = nil
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		if _, err := qs.LockPrincipal(ctx, principalID); err != nil {
			return err
		}
		rows, err := qs.EndSessionsOfPrincipalExcept(ctx, q.EndSessionsOfPrincipalExceptParams{PrincipalID: principalID, KeepID: keepID, Reason: reason})
		if err != nil {
			return err
		}
		ended, es := endedOf(rows)
		withdrawn, err := qs.WithdrawEndedSessionsOfPrincipalExcept(ctx, q.WithdrawEndedSessionsOfPrincipalExceptParams{PrincipalID: principalID, KeepID: keepID})
		if err != nil {
			return err
		}
		if ids = append(ended, withdrawn...); len(ids) == 0 {
			return nil
		}
		if err := notifyEnded(s.o.Changes, tx, es); err != nil {
			return err
		}
		return s.o.Audit.RecordTx(ctx, tx, endedEvent(actor, reason, principalID, ended, withdrawn))
	})
	return ids, err
}

// EndBreakGlass ends every open break-glass session (R-AUTH-014): a rotated
// password (password_changed), a disabled account or trove admin break-glass
// end-sessions (revoked).
func (s *Store) EndBreakGlass(ctx context.Context, reason string, actor audit.Actor) error {
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		_, err := s.EndBreakGlassTx(ctx, tx, reason, actor)
		return err
	})
}

// EndBreakGlassTx is EndBreakGlass on the caller's transaction: the rotation
// hold ends the sessions in the transaction that opens it (Q-AUTH-14). It takes
// the break-glass principal's row lock first, as a break-glass sign-in does. The
// break-glass sessions that had ended already are withdrawn as well
// (Q-AUTHZ-262); the event names both, apart.
func (s *Store) EndBreakGlassTx(ctx context.Context, tx meta.Tx, reason string, actor audit.Actor) ([]string, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, err
	}
	if _, err := qs.LockBreakGlassPrincipal(ctx); err != nil {
		return nil, err
	}
	rows, err := qs.EndBreakGlassSessions(ctx, reason)
	if err != nil {
		return nil, err
	}
	ended, es := endedOf(rows)
	withdrawn, err := qs.WithdrawEndedBreakGlassSessions(ctx)
	if err != nil {
		return nil, err
	}
	ids := append(ended, withdrawn...)
	if len(ids) == 0 {
		return nil, nil
	}
	if err := notifyEnded(s.o.Changes, tx, es); err != nil {
		return nil, err
	}
	return ids, s.o.Audit.RecordTx(ctx, tx, endedEvent(actor, reason, "", ended, withdrawn))
}

// endStatementRow is what every end statement returns of a session it ended: its ID and the hash of its secret.
type endStatementRow = struct {
	ID              string
	SecretSha256Hex string
}

// endedOf lists the IDs of the sessions an end statement returned and what the change of the area auth names of them.
func endedOf[R ~endStatementRow](rows []R) ([]string, []endedSession) {
	ids := make([]string, 0, len(rows))
	es := make([]endedSession, 0, len(rows))
	for _, r := range rows {
		row := endStatementRow(r)
		ids = append(ids, row.ID)
		es = append(es, endedSession{row.ID, row.SecretSha256Hex})
	}
	return ids, es
}

// EndAll ends every session, in batches of at most 1000 per transaction
// (trove admin sessions end --all, R-AUTH-015). It returns the number ended.
func (s *Store) EndAll(ctx context.Context, actor audit.Actor) (int, error) {
	total := 0
	for {
		n := 0
		err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			rows, err := qs.EndAllSessions(ctx)
			if err != nil || len(rows) == 0 {
				n = 0
				return err
			}
			n = len(rows)
			ids := make([]string, 0, len(rows))
			es := make([]endedSession, 0, len(rows))
			for _, r := range rows {
				ids = append(ids, r.ID)
				es = append(es, endedSession{r.ID, r.SecretSha256Hex})
			}
			if err := notifyEnded(s.o.Changes, tx, es); err != nil {
				return err
			}
			return s.o.Audit.RecordTx(ctx, tx, endedEvent(actor, EndRevoked, "", ids, nil))
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < 1000 {
			return total, s.withdrawEnded(ctx)
		}
	}
}

// withdrawEnded withdraws the sessions that had ended before an end of every session, in batches of at most 1000 per
// transaction, so that a delete job one of them queued stops too (Q-AUTHZ-262). Nothing is cached of an ended session and no
// event names it again.
func (s *Store) withdrawEnded(ctx context.Context) error {
	for {
		var n int64
		err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
			qs, err := queries(tx)
			if err != nil {
				return err
			}
			n, err = qs.WithdrawEndedSessions(ctx)
			return err
		})
		if err != nil || n < 1000 {
			return err
		}
	}
}

// LiveSession is a row of a session list (R-AUTH-010).
type LiveSession = q.ListLiveSessionsOfPrincipalRow

// List returns the live sessions of a principal, newest first.
func (s *Store) List(ctx context.Context, principalID string) ([]LiveSession, error) {
	var rows []LiveSession
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		rows, err = q.New(db).ListLiveSessionsOfPrincipal(ctx, principalID)
		return err
	})
	return rows, err
}

// LiveBreakGlassSessions counts the open break-glass sessions.
func (s *Store) LiveBreakGlassSessions(ctx context.Context) (int64, error) {
	var n []int64
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		var err error
		n, err = q.New(db).CountLiveBreakGlassSessions(ctx)
		return err
	})
	if err != nil || len(n) == 0 {
		return 0, err
	}
	return n[0], nil
}

// endedEvent is auth.session.ended for sessions ended by someone other than
// their user (R-AUTH-041): the sessions it ended, and apart from them the
// sessions it withdrew after they had ended (Q-AUTHZ-262). The details name at
// most 1000 session IDs of each.
func endedEvent(actor audit.Actor, reason, principalID string, ended, withdrawn []string) audit.Event {
	firstK := func(ids []string) []string {
		if len(ids) > 1000 {
			return ids[:1000]
		}
		return ids
	}
	d := map[string]any{"reason": reason, "session_ids": firstK(ended), "count": len(ended)}
	if len(withdrawn) > 0 {
		d["withdrawn_ids"], d["withdrawn_count"] = firstK(withdrawn), len(withdrawn)
	}
	details, _ := json.Marshal(d)
	ev := audit.Event{Type: audit.EventAuthSessionEnded, Actor: actor, Outcome: audit.Success, Details: details}
	if principalID != "" {
		ev.Subject = audit.Subject{Kind: "user", ID: principalID}
	}
	return ev
}
