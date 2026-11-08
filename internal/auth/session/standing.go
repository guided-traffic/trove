package session

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The answers of [Standing] that end a job's use of a session.
var (
	// ErrWithdrawn is a session that someone withdrew: ended by a sign-out, an administrator, a disable or a changed break-glass
	// password, or reached by such a withdrawal after it had ended, or ended for a reason this build does not know.
	ErrWithdrawn = errors.New("session: withdrawn")
	// ErrPurged is a session whose row, or the row of a session that replaced it, is gone: the session cleanup removes a row 30 days
	// after the session ended (R-AUTH-043), and a job cannot tell a withdrawal from an end by time any more (Q-STOR-194).
	ErrPurged = errors.New("session: purged")
)

// MaxSuccessors bounds the chain of sessions that replaced one another (a re-authentication, a sign-in in the same browser) that
// Standing follows past the first session. A link exists only while the session it replaces is live, so a chain grows by at most one
// link per sign-in or re-authentication of the browser while its session lives, and rows go 30 days after their end; a chain longer
// than this is treated as withdrawn (Q-AUTHZ-262). One statement reads the whole chain.
const MaxSuccessors = 1000

// Reader runs a read on a pooled connection. *postgres.DB implements it.
type Reader interface {
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// Standing reads whether the session id still stands for a job that continues a request the session authenticated (Q-AUTHZ-261,
// Q-AUTHZ-262), and returns its principal. It reads the rows, never the session cache, so an end or a withdrawal that committed before
// the call is seen at once. A session that ended by time - its idle or absolute deadline, whether or not a request recorded the end -
// stands, and so does one that the session limit evicted, unless a later withdrawal reached it (the end of the user's other sessions,
// an administrator's end, a disable, a changed break-glass password mark the ended sessions they reach). A session that a
// re-authentication, or a sign-in of the same principal in the same browser, replaced stands as long as its successor does: the
// withdrawal of the successor reaches it. Every other end withdraws it ([ErrWithdrawn]); a row that is gone is [ErrPurged].
func Standing(ctx context.Context, db Reader, id string) (principalID string, err error) {
	if !principal.IsID(id) {
		return "", ErrWithdrawn
	}
	var chain []q.SessionStandingChainRow
	if err := db.Read(ctx, func(db q.DBTX) error {
		var err error
		chain, err = q.New(db).SessionStandingChain(ctx, q.SessionStandingChainParams{ID: id, MaxLinks: MaxSuccessors})
		return err
	}); err != nil {
		return "", principal.DBErr("session.standing", err)
	}
	return standingOf(chain)
}

// standingOf decides the chain of a session, first link first.
func standingOf(chain []q.SessionStandingChainRow) (string, error) {
	owner := ""
	for i, r := range chain {
		if !r.Found {
			return "", ErrPurged
		}
		if i == 0 {
			owner = r.PrincipalID
		}
		switch {
		case r.Withdrawn, r.PrincipalID != owner:
			return "", ErrWithdrawn
		case !r.Ended:
			return owner, nil
		case r.SuccessorID != "":
			if i == len(chain)-1 {
				return "", ErrWithdrawn // longer than MaxSuccessors
			}
		case stillStands(r.EndReason):
			return owner, nil
		default:
			return "", ErrWithdrawn
		}
	}
	return "", ErrPurged
}

// stillStands reports whether a session that ended with reason still stands for a job: no one chose to end it - a deadline, the
// session limit, or its replacement by a re-authentication of the same browser.
func stillStands(reason string) bool {
	switch reason {
	case EndIdle, EndAbsolute, EndEvicted, EndReauthRotated:
		return true
	}
	return false
}
