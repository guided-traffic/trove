package oidc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// HandleLogout is POST /auth/logout (R-AUTH-013): with the session cookie and
// the CSRF checks of R-AUTH-012 it ends the session, clears both cookies and
// answers 200 with {"redirectTo": …}. It is idempotent: an ended or unknown
// session still answers 200 with /ui/logged-out. GET answers 405 through the
// router, so image tags and links cannot log anyone out.
func (s *Service) HandleLogout(w http.ResponseWriter, r *http.Request) {
	out, err := s.o.Sessions.Logout(r)
	if err != nil {
		s.o.Problem(w, r, session.Problem(err))
		return
	}
	session.ClearCookies(w)
	redirect, rp := LoggedOutPath, false
	if out.Ended {
		if u, ok := s.endSessionURL(out); ok {
			redirect, rp = u, true
		}
		details, _ := json.Marshal(map[string]any{"session_id": out.ID, "rp_initiated": rp})
		s.o.Audit.Record(r.Context(), audit.Event{
			Type: audit.EventAuthLogout, Actor: audit.Actor{Kind: "user"}, Outcome: audit.Success, Source: s.source(r), Details: details,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"redirectTo": redirect})
}

// endSessionURL is the RP-initiated logout of OpenID Connect RP-Initiated
// Logout 1.0 §2: the provider's end_session_endpoint with id_token_hint,
// client_id and the post-logout redirect URI, when the provider advertises it
// and rp_initiated_logout is on.
func (s *Service) endSessionURL(out session.LoggedOut) (string, bool) {
	p, ok := s.byID[out.ProviderID]
	if !ok || !*p.cfg.RPInitiatedLogout {
		return "", false
	}
	doc := p.document()
	if doc == nil || doc.EndSessionEndpoint == "" {
		return "", false
	}
	u, err := url.Parse(doc.EndSessionEndpoint)
	if err != nil {
		return "", false
	}
	v := u.Query()
	if out.IDToken != "" {
		v.Set("id_token_hint", out.IDToken)
	}
	v.Set("client_id", p.cfg.ClientID)
	v.Set("post_logout_redirect_uri", strings.TrimSuffix(s.o.BaseURL, "/")+LoggedOutPath)
	u.RawQuery = v.Encode()
	return u.String(), true
}

// countPending reads the unexpired browser and command-line flows (R-AUTH-038).
func countPending(ctx context.Context, db *postgres.DB) (int64, error) {
	var n []int64
	err := db.Read(ctx, func(d q.DBTX) error {
		var err error
		n, err = q.New(d).CountPendingFlows(ctx)
		return err
	})
	if err != nil || len(n) == 0 {
		return 0, err
	}
	return n[0], nil
}
