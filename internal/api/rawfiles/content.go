package rawfiles

import (
	"fmt"
	"github.com/guided-traffic/trove/internal/qname"
	"io"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The content routes (R-API-025, Q-UI-106) are the transfer of /files/ for the UI: the session cookie reaches only the
// role api after the cut into task areas (R-ARCH-001, R-EP-010), and /files/ is served by packages, so the UI's
// downloads and uploads go through
//
//	GET, HEAD, PUT /api/v1/projects/{projectKey}/repositories/{repoKey}/content/{path...}
//
// on the API's own origin. The routes share everything with /files/ through [Handler.serve] (path cleaning, the decision
// of every request on one index, Range and conditional requests, Repr-Digest, the content headers, the blocking of
// R-VULN-015, the download statistics), so a caller without read gets the same 404 here as there. They differ in three
// things only: a request that the session cookie did not authenticate answers the 404 of an unknown path, whatever its
// method; a PUT above the limit of the UI's uploads is refused with 413 before the body is read; and DELETE is not
// offered (the UI deletes through the items operation), so the session's DELETE answers 405 (Q-ARCH-307).

const (
	contentBase   = "/api/v1/projects/"
	contentMiddle = "/content/"
)

// ContentPattern is the ServeMux pattern of the content routes for a method, the {path...} form of the multi-segment
// wildcard. The document names the same template with {path} (api/openapi.yaml).
func ContentPattern(method string) string {
	return method + " " + contentBase + "{projectKey}/repositories/{repoKey}" + contentMiddle + "{path...}"
}

// Content is the handler of the content routes. Create it with [Handler.Content].
type Content struct {
	h         *Handler
	maxUpload int64
}

// Content returns the handler of the content routes over the same service and decision point. maxUpload is
// ui.deploy.max_upload_bytes; 0 is unlimited.
func (h *Handler) Content(maxUpload int64) *Content {
	return &Content{h: h, maxUpload: max(0, maxUpload)}
}

// Register mounts GET, HEAD and PUT of the content routes on the router. Another method is answered by the router's
// fallback: the 405 with Allow: GET, HEAD, PUT for a request the session cookie authenticated, and for every other
// request the 404 of an unknown path, as for the three methods it mounts (Q-ARCH-307).
func (c *Content) Register(r *httpserver.Router) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
		r.Handle(ContentPattern(m), c)
	}
	r.HideMethods(contentBase, func(req *http.Request) bool {
		_, _, ok := splitContentPath(req.URL.EscapedPath())
		return ok && !bySession(req)
	})
}

// bySession reports whether the session cookie authenticated the request.
func bySession(r *http.Request) bool {
	return httpserver.PrincipalFrom(r.Context()).Credential.Method == httpserver.MethodSession
}

// ServeHTTP answers a content request.
func (c *Content) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Token clients keep their bulk traffic on packages: only a request the session cookie authenticated is served, and
	// every other one is the answer of a path the role does not have (Q-UI-106). The stage has run: a request without a
	// credential never reaches here unless an anonymous caller is admitted, who is refused alike.
	if !bySession(r) {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path."))
		return
	}
	key, rest, ok := splitContentPath(r.URL.EscapedPath())
	if !ok {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path."))
		return
	}
	c.h.serve(w, r, key, rest, c.maxUpload)
}

// splitContentPath cuts "/api/v1/projects/<project>/repositories/<key>/content/<rest>" and decodes the name; rest stays
// escaped. A project or key with an escape is no name (as in qname.ParseAddress).
func splitContentPath(escaped string) (name qname.Repo, rest string, ok bool) {
	tail, found := strings.CutPrefix(escaped, contentBase)
	if !found {
		return qname.Repo{}, "", false
	}
	project, tail, found := strings.Cut(tail, "/repositories/")
	if !found || strings.Contains(project, "/") {
		return qname.Repo{}, "", false
	}
	key, rest, found := strings.Cut(tail, contentMiddle)
	if !found || strings.Contains(key, "/") {
		return qname.Repo{}, "", false
	}
	name = qname.Repo{Project: project, Key: key}
	if !name.Valid() {
		return qname.Repo{}, "", false
	}
	return name, rest, true
}

// limitBody refuses a body above the limit of the UI's uploads. A declared length above it fails the first read, so
// nothing is spooled; a body of unknown length fails when it crosses the limit. The deploy reads the body only after it
// decided the caller, so a caller who may not deploy gets that refusal and not the 413.
type limitBody struct {
	r     io.Reader
	limit int64
	read  int64
	// declared is the Content-Length of the request, or -1.
	declared int64
}

func tooLarge(limit int64) error {
	return errs.E(errs.TooLarge, errs.CodeTooLarge, fmt.Sprintf("The upload is larger than ui.deploy.max_upload_bytes (%d bytes); use a package-manager client or /files/ with a token for larger files.", limit))
}

func (b *limitBody) Read(p []byte) (int, error) {
	if b.declared > b.limit {
		return 0, tooLarge(b.limit)
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return n, tooLarge(b.limit)
	}
	return n, err
}
