package v1

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The caller's UI preferences (R-API-021). A user who never saved any has the
// defaults at revision 1 and no row; the first write inserts revision 2. The
// revision column user_preferences.revision is the ETag (R-API-012).

type prefs struct {
	locale, theme, projectFilter string
	keyboardShortcuts            bool
	revision                     int64
}

func (p prefs) representation() gen.Preferences {
	return gen.Preferences{
		Locale: optString(p.locale), Theme: optString(p.theme), KeyboardShortcuts: p.keyboardShortcuts,
		ProjectFilter: optString(p.projectFilter), Revision: int(p.revision),
	}
}

// loadPreferences reads the row of the user, or the defaults at revision 1.
func loadPreferences(ctx context.Context, db q.DBTX, userID string) (prefs, error) {
	rows, err := q.New(db).PreferencesGet(ctx, userID)
	if err != nil {
		return prefs{}, err
	}
	if len(rows) == 0 {
		return prefs{revision: 1}, nil
	}
	r := rows[0]
	return prefs{locale: r.Locale, theme: r.Theme, projectFilter: r.ProjectFilter, keyboardShortcuts: r.KeyboardShortcuts, revision: r.Revision}, nil
}

// preferencesUser answers 403 for a caller that is not a user: a service
// account or a CI token has no preferences (R-API-021).
func preferencesUser(r *http.Request) (string, error) {
	p := httpserver.PrincipalFrom(r.Context())
	if p.Kind != httpserver.KindUser && p.Kind != httpserver.KindBreakGlass {
		return "", errs.E(errs.Forbidden, errs.CodeForbidden, "Service accounts and CI tokens have no UI preferences.")
	}
	return p.ID, nil
}

// GetPreferences implements GET /api/v1/users/me/preferences.
func (s *Server) GetPreferences(w http.ResponseWriter, r *http.Request, _ gen.GetPreferencesParams) {
	if s.o.API.Store == nil {
		httpserver.Problem(w, r, unavailable("The preferences"))
		return
	}
	uid, err := preferencesUser(r)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	var cur prefs
	err = s.o.API.Store.Read(r.Context(), func(db q.DBTX) (e error) { cur, e = loadPreferences(r.Context(), db, uid); return })
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.preferences"))
		return
	}
	if NotModified(w, r, ETag(cur.revision, "")) {
		return
	}
	SetETag(w, cur.revision, "")
	writeJSON(w, r, http.StatusOK, cur.representation())
}

// prefsPatchBody is a merge patch of the preferences; revision is read only.
type prefsPatchBody struct {
	Locale            json.RawMessage `json:"locale"`
	Theme             json.RawMessage `json:"theme"`
	KeyboardShortcuts json.RawMessage `json:"keyboardShortcuts"`
	ProjectFilter     json.RawMessage `json:"projectFilter"`
	Revision          json.RawMessage `json:"revision"`
}

func invalidValue(pointer, detail string) error {
	return errs.E(errs.Invalid, errs.CodeValidation, "A value is invalid.", errs.WithFields(errs.FieldError{Pointer: pointer, Code: "invalid_value", Detail: detail}))
}

// apply merges the patch into cur and validates the result.
func (in prefsPatchBody) apply(cur prefs) (prefs, error) {
	next := cur
	set := func(raw json.RawMessage, pointer string, dst *string, allowed ...string) error {
		v, err := patchString(raw, pointer)
		if err != nil || v == nil {
			return err
		}
		if len(allowed) > 0 && *v != "" {
			ok := false
			for _, a := range allowed {
				ok = ok || *v == a
			}
			if !ok {
				return invalidValue(pointer, "one of "+allowedList(allowed)+" or null")
			}
		}
		if len(*v) > 255 {
			return invalidValue(pointer, "at most 255 characters")
		}
		*dst = *v
		return nil
	}
	err := errorsFirst(
		func() error { return set(in.Locale, "/locale", &next.locale, "en", "de") },
		func() error { return set(in.Theme, "/theme", &next.theme, "light", "dark") },
		func() error { return set(in.ProjectFilter, "/projectFilter", &next.projectFilter) },
		func() error {
			b, err := patchBool(in.KeyboardShortcuts, "/keyboardShortcuts")
			if b != nil {
				next.keyboardShortcuts = *b
			}
			return err
		},
	)
	return next, err
}

func allowedList(a []string) string {
	out := ""
	for i, v := range a {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// UpdatePreferences implements PATCH /api/v1/users/me/preferences.
func (s *Server) UpdatePreferences(w http.ResponseWriter, r *http.Request, _ gen.UpdatePreferencesParams) {
	if s.o.API.Store == nil {
		httpserver.Problem(w, r, unavailable("The preferences"))
		return
	}
	pre, err := ParseIfMatch(r)
	if err == nil {
		err = pre.Require()
	}
	var uid string
	if err == nil {
		uid, err = preferencesUser(r)
	}
	var raw []byte
	if err == nil {
		raw, err = ReadMergePatch(w, r, s.bodyLimit())
	}
	var in prefsPatchBody
	if err == nil {
		err = StrictUnmarshal(raw, &in)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	rev, byRevision := pre.Revision("")
	var out prefs
	err = s.o.API.Store.InTx(r.Context(), func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errs.E(errs.Internal, errs.CodeInternal, "The request failed.")
		}
		cur, err := loadPreferences(r.Context(), ptx, uid)
		if err != nil {
			return err
		}
		if byRevision && rev != cur.revision {
			return Failed(cur.revision, "")
		}
		next, err := in.apply(cur)
		if err != nil {
			return err
		}
		if next == cur {
			out = cur
			return nil
		}
		rows, err := q.New(ptx).PreferencesUpsert(r.Context(), q.PreferencesUpsertParams{
			UserID: uid, Locale: next.locale, Theme: next.theme, KeyboardShortcuts: next.keyboardShortcuts, ProjectFilter: next.projectFilter,
			IfRevision: rev,
		})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			latest, err := loadPreferences(r.Context(), ptx, uid)
			if err != nil {
				return err
			}
			return Failed(latest.revision, "")
		}
		r0 := rows[0]
		out = prefs{locale: r0.Locale, theme: r0.Theme, projectFilter: r0.ProjectFilter, keyboardShortcuts: r0.KeyboardShortcuts, revision: r0.Revision}
		return nil
	})
	if err != nil {
		if _, ok := errs.As(err); !ok {
			err = errs.Wrap(err, "api.preferences")
		}
		httpserver.Problem(w, r, err)
		return
	}
	SetETag(w, out.revision, "")
	writeJSON(w, r, http.StatusOK, out.representation())
}
