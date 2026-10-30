package npm

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// whoami and ping (R-NPM-019, flow F7). Both need an authenticated subject: a request
// that carries no credential, or one that no longer authenticates (a revoked token
// is anonymous to the chain), answers 401 with the challenge of R-NPM-004. Nothing is
// cached here: the authentication stage reads its token and principal caches within
// the bound of R-ARCH-018, so a revocation is in force on every replica at once.

// authenticated reports whether the request carries an identity.
func authenticated(rq *formats.Request) bool {
	return !rq.Subject.Anonymous && rq.Subject.Name != ""
}

// handlePing is GET -/ping: 200 {}. The query write=true npm adds is ignored.
func (f *Format) handlePing(_ context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if !authenticated(rq) {
		return unauthenticated()
	}
	w.Header().Set("Cache-Control", "no-store")
	return writeJSON(w, http.StatusOK, struct{}{})
}

// handleWhoami is GET -/whoami: 200 {"username": <name>} with the name as R-AUTH-033
// renders it (principalName).
func (f *Format) handleWhoami(_ context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if !authenticated(rq) {
		return unauthenticated()
	}
	w.Header().Set("Cache-Control", "no-store")
	return writeJSON(w, http.StatusOK, map[string]string{"username": principalName(rq)})
}
