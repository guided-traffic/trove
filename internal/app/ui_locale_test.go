package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
)

type fakeSessions struct {
	valid string
	err   error
}

func (f fakeSessions) Peek(_ context.Context, cookie string) (session.Info, error) {
	if f.err != nil || cookie != f.valid {
		return session.Info{}, errors.Join(session.ErrInvalid, f.err)
	}
	return session.Info{Principal: principal.Principal{ID: "u1"}}, nil
}

func TestR_UI_001_StoredLocaleOfTheSignedInUser(t *testing.T) {
	stored := map[string]string{"u1": "de"}
	read := func(_ context.Context, id string) (string, error) { return stored[id], nil }
	pref := uiLocalePreference(fakeSessions{valid: "good"}, read, slog.Default())
	req := func(cookie string) *http.Request {
		r := httptest.NewRequestWithContext(context.Background(), "GET", "/ui/", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: cookie})
		}
		return r
	}
	if got := pref(req("good")); got != "de" {
		t.Errorf("valid session: %q, want de", got)
	}
	if got := pref(req("")); got != "" {
		t.Errorf("no cookie: %q, want the browser's language", got)
	}
	if got := pref(req("forged")); got != "" {
		t.Errorf("a cookie that is not a valid session: %q, want the browser's language", got)
	}
	stored["u1"] = ""
	if got := pref(req("good")); got != "" {
		t.Errorf("no stored locale: %q, want the browser's language", got)
	}
	failing := uiLocalePreference(fakeSessions{valid: "good"}, func(context.Context, string) (string, error) { return "de", errors.New("down") }, slog.Default())
	if got := failing(req("good")); got != "" {
		t.Errorf("an unreadable preference: %q, want the browser's language", got)
	}
	if uiLocalePreference(nil, read, slog.Default()) != nil {
		t.Error("a replica without sessions needs no preference function")
	}
}
