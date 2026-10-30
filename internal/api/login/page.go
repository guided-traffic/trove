package login

import (
	"html/template"
	"net/http"

	"github.com/guided-traffic/trove/internal/auth/oidc"
)

// errorPageHTML is the page a failed login ends on (R-AUTH-006). It needs no
// script and no style sheet, and html/template escapes the provider's
// error_description, which the domain has truncated to 200 characters.
const errorPageHTML = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Sign-in failed · trove</title><meta name="robots" content="noindex"></head>
<body>
<h1>Sign-in failed</h1>
<p>The sign-in could not be completed. Reason: <code id="reason">{{.Reason}}</code></p>
{{if .Detail}}<p>The identity provider said: <q id="detail">{{.Detail}}</q></p>{{end}}
<p><a href="/ui/login">Start the sign-in again</a></p>
</body>
</html>
`

// statusOf is the status of the error page of a failed login: 503 when a
// dependency of trove did not answer, 502 when the provider is at fault, 500
// when the provisioning failed, 400 for everything the browser or the user
// caused.
func statusOf(f *oidc.LoginFailure) int {
	switch {
	case f.Unavailable:
		return http.StatusServiceUnavailable
	case f.Provider:
		return http.StatusBadGateway
	case f.Reason == oidc.ReasonProvisioningFailed:
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
}

// writeErrorPage renders the page with status 400, 500, 502 or 503. The headers
// keep it out of frames and caches (R-AUTH-012 clickjacking defense).
func writeErrorPage(w http.ResponseWriter, f *oidc.LoginFailure) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	h.Set("X-Frame-Options", "DENY")
	w.WriteHeader(statusOf(f))
	// A failed login is rare; the template is parsed where it is used, so the
	// package holds no state (R-ARCH-032).
	page := template.Must(template.New("login-error").Parse(errorPageHTML))
	_ = page.Execute(w, struct{ Reason, Detail string }{f.Reason, f.Detail})
}
