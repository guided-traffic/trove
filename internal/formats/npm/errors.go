package npm

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/observability"
)

// The request headers an npm client sends to say what it is doing (R-NPM-005):
// they are logged in the access record and never used for a decision.
const (
	headerCommand = "npm-command"
	headerSession = "npm-session"
)

// recordClientFacts adds the access-log fields of R-NPM-005 to the request record.
// The values come from the client; the record clips and scrubs them.
func recordClientFacts(r *http.Request) {
	info := observability.RequestInfoFrom(r.Context())
	if info == nil {
		return
	}
	for _, h := range []string{headerCommand, headerSession} {
		if v := cleanField(r.Header.Get(h)); v != "" {
			info.SetField(h, v)
		}
	}
}

// cleanField drops the control characters of a header value.
func cleanField(v string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
}

// errorBody is the native error body of npm (R-NPM-005): npm prints "error" first,
// other clients read "message", the status is authoritative.
type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

// defaultChallenge is the challenge of a 401 that names none: a token as the Basic
// password with any user name, or as a Bearer token (R-NPM-004).
const defaultChallenge = `Basic realm="trove"`

// WriteError renders an error in the native shape of npm (R-NPM-005): status,
// Content-Type application/json and the three members error, message and code.
// The chain's refusals on this format's paths are rendered here too
// (Q-ARCH-214), which is why it must not call back into the chain's problem
// writer.
//
// A 401 never carries the elements otp or ipaddress, which make npm and pnpm
// ask for a one-time password or report an IP restriction (R-NPM-004).
func (f *Format) WriteError(w http.ResponseWriter, r *http.Request, err error) {
	recordClientFacts(r)
	p, e := errs.ProblemOf(err, "", "")
	text := p.Detail
	if text == "" {
		text = p.Title
	}
	if e.Kind == errs.MethodNotAllowed && e.Extensions["repositoryClass"] == "remote" && r.Method != http.MethodGet && r.Method != http.MethodHead {
		// A write to a remote repository (R-NPM-001, R-NPM-005): the code and the text say why.
		p.Code, text = errs.CodeNPMClassNotWritable, "This is a remote repository: it caches an upstream registry and takes no writes. Publish to a local repository, or to a virtual repository that has one as its deployment target."
	}
	h := w.Header()
	for k, v := range e.Headers {
		h.Set(k, v)
	}
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	switch e.Kind {
	case errs.Unauthenticated:
		h.Set("WWW-Authenticate", challengeOf(e.Challenge))
	case errs.MethodNotAllowed:
		if len(e.Allow) > 0 {
			h.Set("Allow", strings.Join(e.Allow, ", "))
		}
	case errs.RateLimited, errs.Unavailable:
		h.Set("Retry-After", retryAfterSeconds(e.RetryAfter))
	default:
		if e.RetryAfter > 0 {
			h.Set("Retry-After", retryAfterSeconds(e.RetryAfter))
		}
	}
	body, mErr := json.Marshal(errorBody{Error: text, Message: text, Code: p.Code})
	if mErr != nil { // cannot happen: strings only
		body = []byte(`{"error":"internal error","message":"internal error","code":"internal"}`)
	}
	w.WriteHeader(p.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	// A refusal of an operation the format does not implement is the client's, however it is numbered.
	if p.Status >= 500 && e.Kind != errs.NotImplemented {
		attrs := []any{"status", p.Status, "code", p.Code, "kind", e.Kind.String()}
		if e.Op != "" {
			attrs = append(attrs, "op", e.Op)
		}
		if e.Err != nil {
			attrs = append(attrs, "cause", e.Err.Error())
		}
		slog.Error("npm request failed", attrs...)
	}
}

// challengeOf returns the WWW-Authenticate value of a 401: the one the
// authentication stage set, unless it is empty or names otp or ipaddress.
func challengeOf(c string) string {
	l := strings.ToLower(c)
	if c == "" || strings.Contains(l, "otp") || strings.Contains(l, "ipaddress") {
		return defaultChallenge
	}
	return c
}

func retryAfterSeconds(d time.Duration) string {
	s := int64((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.FormatInt(s, 10)
}

// notFound is the answer for a resource that does not exist and for one the caller
// may not see: the same bytes either way (R-AUTHZ-016).
func notFound() *errs.Error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
}

// unauthenticated is the 401 of an endpoint that needs an authenticated caller; the
// challenge is the default of R-NPM-004.
func unauthenticated() *errs.Error {
	return errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "This endpoint requires an authenticated caller.")
}

// notImplemented is the answer to an npm operation trove does not implement: 404
// for a GET, PUT or DELETE and 501 for a POST (R-NPM-001, R-NPM-043), so npm's
// fetch layer, which retries a GET and a PUT on any 5xx but never a POST, fails
// at once.
func notImplemented(method string) error {
	if method == http.MethodPost {
		return errs.E(errs.NotImplemented, errs.CodeNPMNotImplemented, "This npm operation is not implemented by trove.")
	}
	return errs.E(errs.NotFound, errs.CodeNotFound, "This npm operation is not implemented by trove.")
}
