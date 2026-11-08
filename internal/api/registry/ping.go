package registry

import (
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/api/registry/route"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// versionBody is the answer of the version check: an empty JSON object, as the distribution specification writes it.
const versionBody = "{}"

// VersionOptions configure [NewVersion].
type VersionOptions struct {
	// WriteError renders a refusal in the format's shape (R-OCI-003).
	WriteError func(http.ResponseWriter, *http.Request, error)
	// Realm names the realm and the service of the challenge; nil reads the request's base URL (registrytoken.Realm).
	Realm func(*http.Request) (realm, service string)
}

// version is the version check, GET and HEAD of /v2/ (R-OCI-003).
type version struct{ o VersionOptions }

// NewVersion creates the handler of the version check. It answers 200 with {} for a request with a valid credential of any
// kind, the anonymous registry token included, and 401 with the challenge of R-OCI-004 for a request without a credential
// (Q-OCI-20): the authentication stage lets a request without a credential through as anonymous where anonymous requests are
// allowed, so this handler builds that challenge itself. An invalid credential never arrives here: the stage refused it
// (401 UNAUTHORIZED) before.
func NewVersion(o VersionOptions) http.Handler {
	if o.Realm == nil {
		o.Realm = realmOfRequest
	}
	return &version{o: o}
}

func (h *version) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(route.APIVersionHeader, route.APIVersion)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.o.WriteError(w, r, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "The version check answers GET and HEAD.",
			errs.WithAllow(http.MethodGet, http.MethodHead)))
		return
	}
	// A request is "without a credential" when it is anonymous and carries no registry token: the anonymous registry token is a
	// credential (it comes from the token service), and every other credential makes the caller a principal.
	if p := httpserver.PrincipalFrom(r.Context()); p.Anonymous && p.Credential.Registry == nil {
		h.o.WriteError(w, r, Challenge(h.o.Realm, r))
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/json")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Length", strconv.Itoa(len(versionBody)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(versionBody))
	}
}

// realmOfRequest is the realm and the service of the request's response base URL (R-EP-003).
func realmOfRequest(r *http.Request) (realm, service string) {
	return registrytoken.Realm(httpserver.InfoFrom(r.Context()).BaseURL)
}
