package session

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

type unreadable struct{ t *testing.T }

func (u unreadable) Read(context.Context, func(q.DBTX) error) error {
	u.t.Fatal("the database was asked")
	return nil
}

func TestStanding(t *testing.T) {
	// R-AUTH-010: the end reasons of a session row.
	t.Run("R-STOR-018 a session ended by time, by the session limit or by its re-authentication still stands for a job; every other end withdraws it", func(t *testing.T) {
		for reason, stands := range map[string]bool{
			EndIdle: true, EndAbsolute: true, EndEvicted: true, EndReauthRotated: true,
			EndLogout: false, EndRevoked: false, EndPrincipalDisable: false, EndPasswordChanged: false, "a-reason-of-a-later-build": false,
		} {
			if got := stillStands(reason); got != stands {
				t.Errorf("%s: stands = %v, want %v", reason, got, stands)
			}
		}
	})

	t.Run("R-STOR-018 an ID that names no session row is withdrawn without a query", func(t *testing.T) {
		if _, err := Standing(context.Background(), unreadable{t}, "test-session"); !errors.Is(err, ErrWithdrawn) {
			t.Fatalf("err = %v", err)
		}
	})

	// R-AUTH-011, R-AUTH-010: a re-authentication and a sign-in in the same browser hand the session on; a planted session of
	// another principal does not.
	t.Run("R-STOR-018 a chain of sessions stands with its last link, a gap is purged, a foreign link or an overlong chain is withdrawn", func(t *testing.T) {
		const me, other = "p-me", "p-other"
		live := q.SessionStandingChainRow{Found: true, PrincipalID: me}
		replaced := func(reason string) q.SessionStandingChainRow {
			return q.SessionStandingChainRow{Found: true, PrincipalID: me, Ended: true, EndReason: reason, SuccessorID: "next"}
		}
		long := make([]q.SessionStandingChainRow, MaxSuccessors+1)
		for i := range long {
			long[i] = replaced(EndReauthRotated)
		}
		for _, tc := range []struct {
			name  string
			chain []q.SessionStandingChainRow
			want  error
		}{
			{"live", []q.SessionStandingChainRow{live}, nil},
			{"idle", []q.SessionStandingChainRow{{Found: true, PrincipalID: me, Ended: true, EndReason: EndIdle}}, nil},
			{"withdrawn after it idled", []q.SessionStandingChainRow{{Found: true, PrincipalID: me, Ended: true, EndReason: EndIdle, Withdrawn: true}}, ErrWithdrawn},
			{"re-authenticated, successor live", []q.SessionStandingChainRow{replaced(EndReauthRotated), live}, nil},
			{"signed in again in the same browser, successor live", []q.SessionStandingChainRow{replaced(EndRevoked), live}, nil},
			{"re-authenticated, successor signed out", []q.SessionStandingChainRow{replaced(EndReauthRotated), {Found: true, PrincipalID: me, Ended: true, EndReason: EndLogout, Withdrawn: true}}, ErrWithdrawn},
			{"a successor of another principal", []q.SessionStandingChainRow{replaced(EndRevoked), {Found: true, PrincipalID: other}}, ErrWithdrawn},
			{"a successor that is gone", []q.SessionStandingChainRow{replaced(EndReauthRotated), {}}, ErrPurged},
			{"a session that is gone", []q.SessionStandingChainRow{{}}, ErrPurged},
			{"longer than the bound", long, ErrWithdrawn},
		} {
			got, err := standingOf(tc.chain)
			if !errors.Is(err, tc.want) || (tc.want == nil && got != me) {
				t.Errorf("%s: %q, %v; want %v", tc.name, got, err, tc.want)
			}
		}
	})
}
