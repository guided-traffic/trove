// Package login is the edge of the browser login: the routes under /auth/ that
// parse the request (provider ID, state, code, the cookies, the form of the local
// login), call the login domain (internal/auth/oidc) with plain inputs, and write
// what comes back as cookies, redirects, problems and the error page (R-AUTH-005,
// R-AUTH-006, R-AUTH-013, R-AUTH-014). The CSRF and origin checks of R-AUTH-012
// that need the request are made here. It imports httpserver, session and oidc and
// not the management API (internal/api/v1); the architecture test keeps it that way.
package login

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The browser routes of the login (R-AUTH-005, R-AUTH-006, R-AUTH-013,
// R-AUTH-014). They lie outside /api/v1/ in the ui family of R-EP-005; the
// composition root registers them through [Handler.Register].
const (
	RouteStart      = "GET /auth/oidc/{providerId}/start"
	RouteCallback   = "GET /auth/oidc/{providerId}/callback"
	RouteLogout     = "POST /auth/logout"
	RouteLocalLogin = "POST /auth/local/login"
)

// maxLoginBody bounds the local-login request.
const maxLoginBody = 8 << 10

// Options configure a [Handler]. Every field is optional: the defaults read what
// the endpoint's chain stored in the request context, which is what the
// composition root wants; a test without the chain passes its own.
type Options struct {
	// ClientAddr is the client address after the trusted-proxy rule (R-ARCH-006),
	// "" when there is none.
	ClientAddr func(*http.Request) string
	// Source is the source of the audit events of the request (R-AUTH-041).
	Source session.SourceFunc
	// Origin is the origin of the base URL the request's endpoint resolved, the
	// one the local login's origin check compares with (R-AUTH-012).
	Origin func(*http.Request) string
}

// Handler serves the browser routes of the login.
type Handler struct {
	svc      *oidc.Service
	sessions *session.Store
	o        Options
}

// New builds the handler over the login domain and the session store that
// ends a session on logout.
func New(svc *oidc.Service, sessions *session.Store, o Options) *Handler {
	if o.ClientAddr == nil {
		o.ClientAddr = func(r *http.Request) string {
			if c := httpserver.InfoFrom(r.Context()).Client; c.IsValid() {
				return c.String()
			}
			return ""
		}
	}
	if o.Source == nil {
		o.Source = func(r *http.Request) audit.Source { return httpserver.SourceFrom(r.Context()) }
	}
	if o.Origin == nil {
		o.Origin = func(r *http.Request) string {
			return session.NormalizeOrigin(httpserver.InfoFrom(r.Context()).BaseURL)
		}
	}
	return &Handler{svc: svc, sessions: sessions, o: o}
}

// Register puts the four routes on an endpoint's router. They belong to the ui
// family, so an endpoint whose surface lacks ui drops them (R-EP-005).
func (h *Handler) Register(router *httpserver.Router) {
	router.HandleFunc(RouteStart, h.HandleStart)
	router.HandleFunc(RouteCallback, h.HandleCallback)
	router.HandleFunc(RouteLogout, h.HandleLogout)
	router.HandleFunc(RouteLocalLogin, h.HandleLocalLogin)
}

// HandleStart is GET /auth/oidc/{providerId}/start (R-AUTH-005): it redirects to
// the provider's authorization endpoint and binds the flow to the browser with a
// cookie. No credential is needed; purpose=reauth needs a live session
// (R-AUTH-011).
func (h *Handler) HandleStart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := h.svc.StartFlow(r.Context(), oidc.StartInput{
		ProviderID:    r.PathValue("providerId"),
		Reauth:        q.Get("purpose") == "reauth",
		SessionCookie: session.CookieValue(r, session.CookieSession),
		ReturnTo:      q.Get("return_to"),
		Binding:       session.CookieValue(r, session.CookieBinding),
		ClientAddr:    h.o.ClientAddr(r),
	})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	session.SetBindingCookie(w, out.Binding, out.BindingTTL)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, out.AuthorizationURL, http.StatusFound)
}

// HandleCallback is GET /auth/oidc/{providerId}/callback (R-AUTH-006). Any
// failure ends the login with an error page (400, or 502 when the provider is at
// fault) and auth.login.failed with the reason; no session is created.
func (h *Handler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.Callback(r.Context(), oidc.CallbackInput{
		ProviderID:    r.PathValue("providerId"),
		Query:         r.URL.Query(),
		Binding:       session.CookieValue(r, session.CookieBinding),
		SessionCookie: session.CookieValue(r, session.CookieSession),
		ClientAddr:    h.o.ClientAddr(r),
		UserAgent:     r.UserAgent(),
		Source:        h.o.Source(r),
	})
	if err != nil {
		var f *oidc.LoginFailure
		if errors.As(err, &f) {
			writeErrorPage(w, f)
			return
		}
		httpserver.Problem(w, r, err)
		return
	}
	session.SetCookies(w, out.Session.Secret, out.Session.CSRF)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, out.ReturnTo, http.StatusFound)
}

// HandleLogout is POST /auth/logout (R-AUTH-013): with the session cookie and
// the CSRF checks of R-AUTH-012 it ends the session, clears both cookies and
// answers 200 with {"redirectTo": …}. It is idempotent: an ended or unknown
// session still answers 200 with /ui/logged-out. GET answers 405 through the
// router, so image tags and links cannot log anyone out.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	ended, err := h.sessions.Logout(r)
	if err != nil {
		httpserver.Problem(w, r, session.Problem(err))
		return
	}
	session.ClearCookies(w)
	out := h.svc.Logout(r.Context(), oidc.LogoutInput{Session: ended, Source: h.o.Source(r)})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"redirectTo": out.RedirectTo})
}

// loginRequest is the body of POST /auth/local/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// HandleLocalLogin is POST /auth/local/login (R-AUTH-014). It answers 204 with
// the session cookies, 401 invalid_credentials, 403 local_login_disabled, 429
// for the lockout or the flow-start limit, and 503 during a rotation hold or
// while the verification slots are taken.
func (h *Handler) HandleLocalLogin(w http.ResponseWriter, r *http.Request) {
	if !session.SameOrigin(r, h.o.Origin(r)) {
		httpserver.Problem(w, r, errs.E(errs.Forbidden, errs.CodeCSRFFailed, "The local login must come from the trove UI (Origin or Sec-Fetch-Site)."))
		return
	}
	addr := h.o.ClientAddr(r)
	if err := h.svc.AdmitLocalLogin(r.Context(), addr); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	var req loginRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxLoginBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpserver.Problem(w, r, errs.E(errs.Invalid, errs.CodeInvalid, "The body must be {\"username\": …, \"password\": …}."))
		return
	}
	created, err := h.svc.LocalLogin(r.Context(), oidc.LocalLoginInput{
		Username: req.Username, Password: req.Password,
		SessionCookie: session.CookieValue(r, session.CookieSession),
		ClientAddr:    addr, UserAgent: r.UserAgent(), Source: h.o.Source(r),
	})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	session.SetCookies(w, created.Secret, created.CSRF)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
