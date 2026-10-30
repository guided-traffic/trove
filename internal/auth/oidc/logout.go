package oidc

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// LogoutInput is a session the edge has ended (session.Store.Logout, which checks
// the CSRF rules of R-AUTH-012) and the source of the request.
type LogoutInput struct {
	Session session.LoggedOut
	Source  audit.Source
}

// LogoutOutput says where the browser goes after a logout.
type LogoutOutput struct {
	// RedirectTo is the provider's end-session URL (RP-initiated logout) or
	// [LoggedOutPath].
	RedirectTo string
}

// Logout finishes POST /auth/logout (R-AUTH-013): it works out where the browser
// goes and records auth.logout when a session was ended. It is idempotent: an
// ended or unknown session still leads to [LoggedOutPath].
func (s *Service) Logout(ctx context.Context, in LogoutInput) LogoutOutput {
	redirect, rp := LoggedOutPath, false
	if in.Session.Ended {
		if u, ok := s.endSessionURL(in.Session); ok {
			redirect, rp = u, true
		}
		details, _ := json.Marshal(map[string]any{"session_id": in.Session.ID, "rp_initiated": rp})
		s.o.Audit.Record(ctx, audit.Event{
			Type: audit.EventAuthLogout, Actor: audit.Actor{Kind: "user"}, Outcome: audit.Success, Source: in.Source, Details: details,
		})
	}
	return LogoutOutput{RedirectTo: redirect}
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
