package ansible

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// challenge is the WWW-Authenticate value of every 401 on a Galaxy endpoint (R-ANS-006): a client that retries on a challenge sends
// Basic, which takes the token as the password.
const challenge = `Basic realm="trove"`

// maxRetryAfter bounds the Retry-After of a Galaxy endpoint whatever value the owning page computes: a client counts the wait against
// its own timeout.
const maxRetryAfter = 30 * time.Second

// noRoute is the detail of the dispatcher's 404 for a path no route accepts.
const noRoute = "No route matches the request path."

const (
	detailNoRoles       = "This repository does not serve roles; roles are served by remote repositories of a Galaxy and by virtual repositories over one. Wrap the role into a collection to publish it here."
	detailRemotePublish = "This is a remote repository: it caches an upstream Galaxy and takes no publishes. Publish to a local repository, or to a virtual repository that has one as its deployment target."
)

// WriteError renders an error in the shape the Galaxy client reads (R-ANS-007, R-ARCH-023, Q-ANS-27), chosen by the route:
//
//   - at the root and below v3/: {"errors":[{"status":"404","code":"not_found","title":"Not found.","detail":"…"}]}; the client prints
//     detail first, then title;
//   - below v1/: {"default": "<sentence>"}.
//
// code is the registry code of the error without the format's prefix (ansible_version_exists is version_exists), and the codes of the
// kinds a Galaxy names its own way: not_authenticated, permission_denied, invalid_request and upstream_unavailable. The status is the
// error's: 401, 403 and 404 by R-AUTHZ-016, 405 with Allow, 409 for version_exists and version_tombstoned, 413, 422, 502 and 503. The
// chain's refusals on this format's paths are rendered here too (Q-ARCH-214), which is why it must not call back into the chain's
// problem writer.
func (f *Format) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	p, e := errs.ProblemOf(err, "", "")
	v1 := belowV1(r)
	status, code, detail := p.Status, galaxyCode(p.Code), p.Detail
	if detail == "" {
		detail = p.Title
	}
	switch {
	case v1 && e.Kind == errs.NotFound && detail == noRoute,
		v1 && e.Kind == errs.MethodNotAllowed && e.Extensions["repositoryClass"] == "local":
		// A local repository has no v1 API (R-ANS-014): the answer is the 404 a missing API is, with the sentence that says why.
		status, code, detail = http.StatusNotFound, "not_found", detailNoRoles
	case e.Kind == errs.MethodNotAllowed && e.Extensions["repositoryClass"] == "remote" && r.Method == http.MethodPost:
		detail = detailRemotePublish
	}
	h := w.Header()
	for k, v := range e.Headers {
		h.Set(k, v)
	}
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	switch e.Kind {
	case errs.Unauthenticated:
		h.Set("WWW-Authenticate", challenge)
	case errs.MethodNotAllowed:
		if len(e.Allow) > 0 && status == http.StatusMethodNotAllowed {
			h.Set("Allow", strings.Join(e.Allow, ", "))
		}
	case errs.RateLimited, errs.Unavailable:
		h.Set("Retry-After", retryAfterSeconds(e.RetryAfter))
	default:
		if e.RetryAfter > 0 {
			h.Set("Retry-After", retryAfterSeconds(e.RetryAfter))
		}
	}
	body := errorBody(v1, status, code, p.Title, detail)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	// A refusal of an operation the format does not serve is the client's, however it is numbered.
	if status >= 500 && e.Kind != errs.NotImplemented {
		attrs := []any{"status", status, "code", p.Code, "kind", e.Kind.String()}
		if e.Op != "" {
			attrs = append(attrs, "op", e.Op)
		}
		if e.Err != nil {
			attrs = append(attrs, "cause", e.Err.Error())
		}
		slog.Error("ansible request failed", attrs...)
	}
}

// errorBody renders the body of an error in the v3 or the v1 shape.
func errorBody(v1 bool, status int, code, title, detail string) []byte {
	if v1 {
		b, _ := json.Marshal(struct {
			Default string `json:"default"`
		}{detail})
		return b
	}
	b, _ := json.Marshal(struct {
		Errors []galaxyError `json:"errors"`
	}{[]galaxyError{{Status: strconv.Itoa(status), Code: code, Title: sentenceCase(title), Detail: detail}}})
	return b
}

type galaxyError struct {
	Status string `json:"status"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// sentenceCase ends the title with a full stop, as the Galaxy writes its titles ("Not found.").
func sentenceCase(s string) string {
	if s == "" || strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

// galaxyCode is the code of the Galaxy shape for a registry code.
func galaxyCode(code string) string {
	switch code {
	case errs.CodeUnauthenticated:
		return "not_authenticated"
	case errs.CodeForbidden:
		return "permission_denied"
	case errs.CodeInvalid:
		return "invalid_request"
	case errs.CodeUpstreamFailed, errs.CodeUpstreamTimeout, errs.CodeUnavailable:
		return "upstream_unavailable"
	}
	return strings.TrimPrefix(code, "ansible_")
}

// belowV1 reports whether the request addresses a path below the repository's v1/ (the role API), whose errors have the shape
// {"default": …}. The path is /api/ansible/<project>/<repo>/<rest>; a path of another form is a v3 path.
func belowV1(r *http.Request) bool {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/api/ansible/")
	if !ok {
		return false
	}
	parts := strings.SplitN(rest, "/", 3)
	return len(parts) == 3 && (parts[2] == segV1 || strings.HasPrefix(parts[2], segV1+"/"))
}

func retryAfterSeconds(d time.Duration) string {
	d = min(d, maxRetryAfter)
	s := int64((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.FormatInt(s, 10)
}
