// Package errs is the error model every component uses (R-ARCH-023).
//
// A component returns an *Error with a Kind, a stable Code and a Detail that is
// safe to show a caller. Only an *Error is rendered to a client; any other
// error becomes Internal with the generic code "internal". Cause and Op are
// for logs and never leave the process.
package errs

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kind classifies an error and fixes its HTTP status.
type Kind uint8

// The kinds of R-ARCH-023. Internal is the zero value on purpose: an error
// nobody classified is an internal error.
const (
	Internal             Kind = iota
	Invalid                   // 400; 422 when the body parsed but failed validation
	Unauthenticated           // 401 + WWW-Authenticate
	Forbidden                 // 403
	NotFound                  // 404
	MethodNotAllowed          // 405 + Allow
	Conflict                  // 409
	PreconditionFailed        // 412
	TooLarge                  // 413
	Unsupported               // 415
	RateLimited               // 429 + Retry-After
	InsufficientStorage       // 507
	Unavailable               // 503 + Retry-After
	UpstreamFailed            // 502
	UpstreamTimeout           // 504
	Misdirected               // 421: the request's host is not one this endpoint answers for (R-EP-003)
	Unprocessable             // 422 without field errors: a state or name rule refused a well-formed request (R-API-014, R-API-004)
	PreconditionRequired      // 428: the operation needs If-Match (R-API-012)
)

// StatusClientClosed is the status logged for a request whose client went away.
// It is never sent: a cancelled client context is not an error (R-ARCH-023).
const StatusClientClosed = 499

// String returns the lower-case kind name used in logs.
func (k Kind) String() string {
	switch k {
	case Internal:
		return "internal"
	case Invalid:
		return "invalid"
	case Unauthenticated:
		return "unauthenticated"
	case Forbidden:
		return "forbidden"
	case NotFound:
		return "not_found"
	case MethodNotAllowed:
		return "method_not_allowed"
	case Conflict:
		return "conflict"
	case PreconditionFailed:
		return "precondition_failed"
	case TooLarge:
		return "too_large"
	case Unsupported:
		return "unsupported"
	case RateLimited:
		return "rate_limited"
	case InsufficientStorage:
		return "insufficient_storage"
	case Unavailable:
		return "unavailable"
	case UpstreamFailed:
		return "upstream_failed"
	case UpstreamTimeout:
		return "upstream_timeout"
	case Misdirected:
		return "misdirected"
	case Unprocessable:
		return "unprocessable"
	case PreconditionRequired:
		return "precondition_required"
	default:
		return fmt.Sprintf("kind(%d)", uint8(k))
	}
}

// FieldError names one invalid input by JSON pointer.
type FieldError struct {
	Pointer string `json:"pointer"`
	Code    string `json:"code"`
	Detail  string `json:"detail"`
}

// Error is the one error type that is rendered to clients.
type Error struct {
	Kind       Kind
	Code       string // stable snake_case code, part of the API contract
	Detail     string // safe for the caller
	Fields     []FieldError
	RetryAfter time.Duration
	Challenge  string   // WWW-Authenticate value of an Unauthenticated error
	Allow      []string // Allow methods of a MethodNotAllowed error
	// Extensions are further members of the problem body (R-API-009): currentEtag
	// of a 412, tokenId of a replayed token creation, job of a 409 on a task.
	// Values are plain JSON values; a key never replaces a standard member.
	Extensions map[string]any
	// status, when set, replaces the status of the kind. It exists for the one
	// case the kind cannot express: a 400 that lists the offending member of an
	// unparsable body (R-API-003).
	status int
	Op     string // where it happened; logged, never rendered
	Err    error  // cause; logged, never rendered
}

// Option adjusts an Error under construction.
type Option func(*Error)

// E builds an *Error.
func E(kind Kind, code, detail string, opts ...Option) *Error {
	e := &Error{Kind: kind, Code: code, Detail: detail}
	for _, o := range opts {
		o(e)
	}
	return e
}

// WithCause records the underlying error for logs.
func WithCause(err error) Option { return func(e *Error) { e.Err = err } }

// WithOp records where the error happened, for logs.
func WithOp(op string) Option { return func(e *Error) { e.Op = op } }

// WithFields attaches per-field validation errors.
func WithFields(f ...FieldError) Option { return func(e *Error) { e.Fields = append(e.Fields, f...) } }

// WithRetryAfter sets the Retry-After hint of RateLimited and Unavailable errors.
func WithRetryAfter(d time.Duration) Option { return func(e *Error) { e.RetryAfter = d } }

// WithChallenge sets the WWW-Authenticate value of an Unauthenticated error.
func WithChallenge(c string) Option { return func(e *Error) { e.Challenge = c } }

// WithBadRequest keeps an Invalid error at 400 although it lists fields: the
// body could not be read as the resource at all (unknown or duplicate member,
// wrong JSON type), as opposed to a well-formed body whose values are invalid
// (422, R-API-003, R-API-009).
func WithBadRequest() Option { return func(e *Error) { e.status = 400 } }

// WithExtension adds one extension member to the problem body (R-API-009). The
// names type, title, status, detail, instance, code, requestId and errors are
// the standard members and are ignored.
func WithExtension(name string, value any) Option {
	return func(e *Error) {
		if e.Extensions == nil {
			e.Extensions = map[string]any{}
		}
		e.Extensions[name] = value
	}
}

// WithAllow sets the Allow methods of a MethodNotAllowed error.
func WithAllow(methods ...string) Option { return func(e *Error) { e.Allow = methods } }

// Error renders the error for logs: code, detail, operation and cause.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Code)
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	if e.Op != "" {
		b.WriteString(" (")
		b.WriteString(e.Op)
		b.WriteString(")")
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

// Status returns the HTTP status of the error.
func (e *Error) Status() int {
	if e.status != 0 {
		return e.status
	}
	switch e.Kind {
	case Invalid:
		if len(e.Fields) > 0 {
			return 422
		}
		return 400
	case Unauthenticated:
		return 401
	case Forbidden:
		return 403
	case NotFound:
		return 404
	case MethodNotAllowed:
		return 405
	case Conflict:
		return 409
	case PreconditionFailed:
		return 412
	case TooLarge:
		return 413
	case Unsupported:
		return 415
	case RateLimited:
		return 429
	case InsufficientStorage:
		return 507
	case Unavailable:
		return 503
	case UpstreamFailed:
		return 502
	case UpstreamTimeout:
		return 504
	case Misdirected:
		return 421
	case Unprocessable:
		return 422
	case PreconditionRequired:
		return 428
	default:
		return 500
	}
}

// As finds the first *Error in the chain of err.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// KindOf returns the kind of the first *Error in the chain, Internal otherwise.
func KindOf(err error) Kind {
	if e, ok := As(err); ok {
		return e.Kind
	}
	return Internal
}

// Wrap returns err as an *Error. An *Error in the chain is returned unchanged
// except that an empty Op is filled; any other error becomes Internal and keeps
// err as its cause. Wrap(nil) is nil.
func Wrap(err error, op string) *Error {
	if err == nil {
		return nil
	}
	if e, ok := As(err); ok {
		if e.Op == "" {
			e.Op = op
		}
		return e
	}
	return E(Internal, CodeInternal, "", WithOp(op), WithCause(err))
}
