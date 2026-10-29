package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"time"
)

// The three trove cookies and the CSRF header (R-AUTH-005, R-AUTH-010,
// R-AUTH-012). The __Host- prefix makes the browser refuse any cookie of these
// names that lacks Secure or Path=/ or carries Domain (Q-AUTH-2).
const (
	CookieSession = "__Host-trv_session"
	CookieCSRF    = "__Host-trv_csrf"
	CookieBinding = "__Host-trv_oidc_binding"
	HeaderCSRF    = "X-CSRF-Token"
)

// secretLen is the length of a cookie secret: 32 random bytes, base64url
// without padding (R-AUTH-005, R-AUTH-010, R-AUTH-012).
const secretLen = 43

// NewSecret returns 32 random bytes from crypto/rand in base64url, 43
// characters: the form of session secrets, CSRF tokens, browser bindings, OIDC
// state, nonce and PKCE verifier.
func NewSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil { // crypto/rand does not fail on supported platforms
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// WellFormed reports whether s has the form of [NewSecret]. A cookie of any
// other form is refused without a database access.
func WellFormed(s string) bool {
	if len(s) != secretLen {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil
}

// Hash is the SHA-256 that PostgreSQL stores instead of a secret.
func Hash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// SetCookies writes the session and CSRF cookies of a new session (R-AUTH-010,
// R-AUTH-012): no Domain, no Max-Age, no Expires, so both are host-only and live
// for the browser session. SameSite=Lax on the session cookie lets the first
// navigation after the IdP's cross-site redirect carry it; the CSRF cookie is
// readable by the UI and Strict.
func SetCookies(w http.ResponseWriter, secret, csrf string) {
	http.SetCookie(w, &http.Cookie{Name: CookieSession, Value: secret, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: CookieCSRF, Value: csrf, Path: "/", Secure: true, SameSite: http.SameSiteStrictMode}) //nolint:gosec // G124: the UI reads the CSRF cookie, so it is not HttpOnly (R-AUTH-012)
}

// ClearCookies removes both cookies (R-AUTH-013). The browser ignores a
// __Host- Set-Cookie without Secure and Path=/, so a bare Max-Age=0 would leave
// the cookie in place.
func ClearCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieSession, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: CookieCSRF, Value: "", Path: "/", Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1}) //nolint:gosec // G124: the CSRF cookie is not HttpOnly (R-AUTH-012)
}

// SetBindingCookie writes the browser binding of a login flow (R-AUTH-005):
// Secure, HttpOnly, SameSite=Lax, Path=/, Max-Age 600, no Domain.
func SetBindingCookie(w http.ResponseWriter, value string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieBinding, Value: value, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(maxAge / time.Second),
	})
}

// CookieValue returns the value of the named cookie, "" when absent. A request
// that carries the name twice is answered from the first one, as browsers send
// the most specific first; a __Host- cookie cannot be shadowed by a Domain one.
func CookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}
