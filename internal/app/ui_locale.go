package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// uiPreferenceTimeout bounds what the choice of a locale may cost a request to
// /ui/: a slow database turns into the browser's language, never into a slow redirect.
const uiPreferenceTimeout = 2 * time.Second

// localeSessions resolves a session cookie (*session.Store).
type localeSessions interface {
	Validate(ctx context.Context, cookie string) (session.Info, error)
}

// uiLocalePreference is webui.Options.Preference (R-UI-001, R-API-021): the
// locale stored for the signed-in user, or "" for a request without a valid
// session, a user without a stored locale, and any failure, so that
// Accept-Language decides. The authentication stage ignores the session cookie on
// /ui/ (R-AUTH-001 item 3), so this reads it itself and takes the same
// validation as the stage: an ended, expired or forged cookie names nobody.
// Nothing is written, and the answer of /ui/ carries Vary and no-store already.
func uiLocalePreference(sessions localeSessions, locale func(ctx context.Context, userID string) (string, error), log *slog.Logger) func(*http.Request) string {
	if sessions == nil || locale == nil {
		return nil
	}
	return func(r *http.Request) string {
		c, err := r.Cookie(auth.SessionCookie)
		if err != nil || c.Value == "" {
			return ""
		}
		ctx, cancel := context.WithTimeout(r.Context(), uiPreferenceTimeout)
		defer cancel()
		info, err := sessions.Validate(ctx, c.Value)
		if err != nil {
			return ""
		}
		l, err := locale(ctx, info.Principal.ID)
		if err != nil {
			log.Debug("the stored locale of the user could not be read; the browser's language applies", "error", err)
			return ""
		}
		return l
	}
}

// uiPreference builds the preference function of the UI from the session store
// and the preferences table; nil on a replica without them (the shell profile),
// where nobody is signed in.
func (a *App) uiPreference() func(*http.Request) string {
	store, db := part[sessionsArea](a.parts).store, a.database()
	if store == nil || db == nil {
		return nil
	}
	read := func(ctx context.Context, userID string) (string, error) {
		var locale string
		err := db.Read(ctx, func(tx q.DBTX) error {
			rows, err := q.New(tx).PreferencesGet(ctx, userID)
			if err == nil && len(rows) == 1 {
				locale = rows[0].Locale
			}
			return err
		})
		return locale, err
	}
	return uiLocalePreference(store, read, a.log.With("component", "ui"))
}
