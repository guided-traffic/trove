package registry

import (
	"io"
	"net/http"

	"github.com/guided-traffic/trove/internal/auth/registrytoken"
)

// challengeWriter replaces the bare challenge of an authz.Unauthenticated error ("Bearer", R-AUTHZ-016: the generic one that
// protocol endpoints replace) by the registry's (R-OCI-004 step 1): the realm and the service of the request and the scope the
// route needs. A caller without a credential, where anonymous access is on, gets it for a private repository: it learns where to ask
// for a token, and the answer is the one for a repository that does not exist.
type challengeWriter struct {
	http.ResponseWriter
	e     *Enforcer
	r     *http.Request
	wrote bool
}

func (c *challengeWriter) WriteHeader(code int) {
	// An informational status (103 Early Hints) is not the response: it is forwarded, and the final status is still decided here.
	if c.wrote || code < http.StatusOK {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	c.wrote = true
	if h := c.Header(); code == http.StatusUnauthorized && h.Get("WWW-Authenticate") == "Bearer" {
		realm, service := c.e.o.Realm(c.r)
		h.Set("WWW-Authenticate", registrytoken.Challenge(realm, service, c.e.Scope(c.r), ""))
	}
	c.ResponseWriter.WriteHeader(code)
}

// ReadFrom keeps the underlying writer's io.ReaderFrom fast path (sendfile, splice) for a blob download copied with io.Copy: the
// wrapper embeds the interface and would otherwise hide it. The header decision is made first, as a Write would make it.
func (c *challengeWriter) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := c.ResponseWriter.(io.ReaderFrom); ok {
		if !c.wrote {
			c.WriteHeader(http.StatusOK)
		}
		return rf.ReadFrom(src)
	}
	return io.Copy(struct{ io.Writer }{c}, src)
}

func (c *challengeWriter) Write(p []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the underlying writer (Flush, deadlines).
func (c *challengeWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
