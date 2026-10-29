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
		ended, err := qs.EndSessionsByID(ctx, q.EndSessionsByIDParams{Ids: []string{row.ID}, Reason: EndLogout})
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
// transaction, with the change in the area auth, and returns their IDs. The
// identity area calls it when it disables or deletes a user (R-AUTH-016); the
// caller records its own event. reason is one of the End constants.
func (s *Store) EndForPrincipal(ctx context.Context, tx meta.Tx, principalID, reason string) ([]string, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, err
	}
	rows, err := qs.EndSessionsOfPrincipal(ctx, q.EndSessionsOfPrincipalParams{PrincipalID: principalID, Reason: reason})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	es := make([]endedSession, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
		es = append(es, endedSession{r.ID, r.SecretSha256Hex})
	}
	return ids, notifyEnded(s.o.Changes, tx, es)
}

// EndPrincipalSessions is EndForPrincipal in a transaction of its own, with the
// event auth.session.ended (trove admin sessions end --user, R-AUTH-015; the
// session API of wave 2).
func (s *Store) EndPrincipalSessions(ctx context.Context, principalID string, actor audit.Actor) ([]string, error) {
	var ids []string
	err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		var err error
		if ids, err = s.EndForPrincipal(ctx, tx, principalID, EndRevoked); err != nil || len(ids) == 0 {
			return err
		}
		return s.o.Audit.RecordTx(ctx, tx, endedEvent(actor, EndRevoked, principalID, ids))
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
// hold ends the sessions in the transaction that opens it (Q-AUTH-14).
func (s *Store) EndBreakGlassTx(ctx context.Context, tx meta.Tx, reason string, actor audit.Actor) ([]string, error) {
	qs, err := queries(tx)
	if err != nil {
		return nil, err
	}
	rows, err := qs.EndBreakGlassSessions(ctx, reason)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	es := make([]endedSession, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
		es = append(es, endedSession{r.ID, r.SecretSha256Hex})
	}
	if err := notifyEnded(s.o.Changes, tx, es); err != nil {
		return nil, err
	}
	return ids, s.o.Audit.RecordTx(ctx, tx, endedEvent(actor, reason, "", ids))
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
			return s.o.Audit.RecordTx(ctx, tx, endedEvent(actor, EndRevoked, "", ids))
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < 1000 {
			return total, nil
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
// their user (R-AUTH-041). The details name at most 1000 session IDs.
func endedEvent(actor audit.Actor, reason, principalID string, ids []string) audit.Event {
	shown := ids
	if len(shown) > 1000 {
		shown = shown[:1000]
	}
	details, _ := json.Marshal(map[string]any{"reason": reason, "session_ids": shown, "count": len(ids)})
	ev := audit.Event{Type: audit.EventAuthSessionEnded, Actor: actor, Outcome: audit.Success, Details: details}
	if principalID != "" {
		ev.Subject = audit.Subject{Kind: "user", ID: principalID}
	}
	return ev
}
