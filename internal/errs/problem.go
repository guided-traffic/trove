package errs

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ProblemContentType is the media type of RFC 9457 problem details.
const ProblemContentType = "application/problem+json"

// ProblemTypePrefix prefixes the code in the "type" member. The reference is
// relative to the origin, so it holds for every base URL.
const ProblemTypePrefix = "/api/v1/problems/"

// Problem is the wire form: the RFC 9457 members plus the trove extensions
// code, requestId and errors.
type Problem struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Detail    string       `json:"detail,omitempty"`
	Instance  string       `json:"instance,omitempty"`
	Code      string       `json:"code"`
	RequestID string       `json:"requestId,omitempty"`
	Errors    []FieldError `json:"errors,omitempty"`
	// Extensions are the further members of the body (Error.Extensions).
	Extensions map[string]any `json:"-"`
}

// MarshalJSON writes the standard members and then the extensions, which never
// replace a standard member.
func (p Problem) MarshalJSON() ([]byte, error) {
	type plain Problem
	base, err := json.Marshal(plain(p))
	if err != nil || len(p.Extensions) == 0 {
		return base, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	for k, v := range p.Extensions {
		if _, standard := m[k]; standard || reservedMember(k) {
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		m[k] = raw
	}
	return json.Marshal(m)
}

func reservedMember(name string) bool {
	switch name {
	case "type", "title", "status", "detail", "instance", "code", "requestId", "errors":
		return true
	}
	return false
}

// Problem converts err into its wire form. An error that is not an *Error
// becomes Internal with the generic detail; its cause is not part of the result.
func ProblemOf(err error, requestID, instance string) (Problem, *Error) {
	e, ok := As(err)
	if !ok {
		e = E(Internal, CodeInternal, "", WithCause(err))
	}
	title := http.StatusText(e.Status())
	if info, found := Lookup(e.Code); found {
		title = info.Title
	}
	detail := e.Detail
	if e.Kind == Internal {
		detail = "The server failed to process the request."
	}
	return Problem{
		Type:       ProblemTypePrefix + e.Code,
		Title:      title,
		Status:     e.Status(),
		Detail:     detail,
		Instance:   instance,
		Code:       e.Code,
		RequestID:  requestID,
		Errors:     e.Fields,
		Extensions: e.Extensions,
	}, e
}

// retryAfterSeconds rounds a duration up to whole seconds, at least one.
func retryAfterSeconds(d time.Duration) string {
	s := int64((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.FormatInt(s, 10)
}

// Write renders err as problem+json. It sets WWW-Authenticate, Allow and
// Retry-After where the kind requires them, and logs a 5xx at error level with
// the cause and a 4xx at debug level (R-ARCH-023). instance is the request path.
func Write(w http.ResponseWriter, r *http.Request, err error, requestID string, log *slog.Logger) {
	instance := ""
	if r != nil && r.URL != nil {
		instance = r.URL.Path
	}
	p, e := ProblemOf(err, requestID, instance)
	h := w.Header()
	h.Set("Content-Type", ProblemContentType)
	// A problem body echoes request data (the path in instance, field names);
	// nosniff keeps a browser from reading it as anything but JSON. The chain
	// sets it for every answer; a problem written outside the chain has it too.
	h.Set("X-Content-Type-Options", "nosniff")
	switch e.Kind {
	case Unauthenticated:
		if e.Challenge != "" {
			h.Set("WWW-Authenticate", e.Challenge)
		}
	case MethodNotAllowed:
		if len(e.Allow) > 0 {
			h.Set("Allow", strings.Join(e.Allow, ", "))
		}
	case RateLimited, Unavailable:
		h.Set("Retry-After", retryAfterSeconds(e.RetryAfter))
	default:
		// A conflict that tells the client when to try again, such as an
		// idempotent request still in progress (R-API-014).
		if e.RetryAfter > 0 {
			h.Set("Retry-After", retryAfterSeconds(e.RetryAfter))
		}
	}
	body, mErr := json.Marshal(p)
	if mErr != nil { // cannot happen: Problem holds only strings and ints
		body = []byte(`{"type":"` + ProblemTypePrefix + CodeInternal + `","title":"Internal error","status":500,"code":"internal"}`)
		p.Status = http.StatusInternalServerError
	}
	w.WriteHeader(p.Status)
	_, _ = w.Write(body)

	if log == nil {
		return
	}
	attrs := []any{"request_id", requestID, "status", p.Status, "code", p.Code, "kind", e.Kind.String()}
	if e.Op != "" {
		attrs = append(attrs, "op", e.Op)
	}
	if e.Err != nil {
		attrs = append(attrs, "cause", e.Err.Error())
	}
	if p.Status >= 500 {
		log.Error("request failed", attrs...)
	} else {
		log.Debug("request rejected", attrs...)
	}
}
