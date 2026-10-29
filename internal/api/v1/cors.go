package v1

import (
	"net/http"
	"slices"
	"strings"
)

// Cross-origin access (R-API-018). The UI is same-origin, so CORS is off until
// api.cors.allowed_origins lists exact origins. It then applies to requests that
// carry an Authorization header. Access-Control-Allow-Credentials is never sent,
// so a session cookie is never usable cross-origin.

func corsAllowedHeaders() []string {
	return []string{"Authorization", "Content-Type", "If-Match", "If-None-Match", "Idempotency-Key", "Trove-Authz-Min-Epoch"}
}

func corsExposedHeaders() []string {
	return []string{
		"ETag", "Location", "Link", "Retry-After", "RateLimit", "RateLimit-Policy", "X-Request-Id",
		"Deprecation", "Sunset", "Trove-Authz-Epoch",
	}
}

func corsMethods() []string { return []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"} }

// Preflight returns the middleware that answers the CORS preflight of an allowed
// origin and passes every other request on untouched. It sits before the
// authentication stage of the chain, because a browser sends a preflight without
// credentials and a preflight that authentication refused would block every
// cross-origin call (R-API-018); it opens nothing else: the actual request that
// follows is authenticated as any other. With no origin configured it is the
// identity. [CORS] answers the same preflights for a chain without that stage.
func Preflight(origins []string) func(http.Handler) http.Handler {
	if len(origins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	allowed := slices.Clone(origins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); isPreflight(r, origin, allowed) {
				answerPreflight(w, origin)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isPreflight reports whether r is the preflight of an allowed origin for a path
// of the management API.
func isPreflight(r *http.Request, origin string, allowed []string) bool {
	return origin != "" && isAPIPath(r.URL.Path) && slices.Contains(allowed, origin) &&
		r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

// answerPreflight writes the 204 of a preflight, with the headers every answer
// under /api/v1/ carries (R-API-017).
func answerPreflight(w http.ResponseWriter, origin string) {
	h := w.Header()
	setAPIHeaders(h)
	h.Add("Vary", "Origin")
	h.Add("Vary", "Access-Control-Request-Method")
	h.Add("Vary", "Access-Control-Request-Headers")
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Methods", strings.Join(corsMethods(), ", "))
	h.Set("Access-Control-Allow-Headers", strings.Join(corsAllowedHeaders(), ", "))
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

// CORS returns the middleware for the exact origins; with none it passes every
// request through untouched and the server emits no Access-Control-* header.
func CORS(origins []string) func(http.Handler) http.Handler {
	if len(origins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	allowed := slices.Clone(origins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || !isAPIPath(r.URL.Path) || !slices.Contains(allowed, origin) {
				next.ServeHTTP(w, r)
				return
			}
			if isPreflight(r, origin, allowed) {
				answerPreflight(w, origin)
				return
			}
			h := w.Header()
			if r.Header.Get("Authorization") != "" {
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Expose-Headers", strings.Join(corsExposedHeaders(), ", "))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isAPIPath(p string) bool { return strings.HasPrefix(p, "/api/v1/") }
