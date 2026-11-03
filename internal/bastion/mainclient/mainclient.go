// Package mainclient is the Bastion's client of the main installation (R-BAS-006, R-BAS-008, R-BAS-021,
// R-BAS-025, R-BAS-027): the four operations of the interface and the forwarding of a caller's request. It is
// the only package of the Bastion that talks to the main installation, so the budget, the interface headers,
// the forwarded-for headers, the token and the error classes are applied in one place.
//
// [HTTPClient] (client.go) implements [Client] over net/http with TLS only (the system roots plus
// bastion.main.ca_file, read again without a restart, R-BAS-044); [SlotBudget] (budget.go) is the budget of
// R-BAS-025; [Reloader] (reload.go) re-reads the token file (R-BAS-006).
package mainclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/tlsfiles"
)

// Class says on whose behalf a request to the main installation is made; the budget gives anonymous callers a
// share of their own (R-BAS-025, Q-BAS-35).
type Class string

// The classes of the budget.
const (
	Authenticated Class = "authenticated"
	Anonymous     Class = "anonymous"
)

// Kind is the kind of a request to the main installation (R-BAS-040 trove_bastion_main_requests_total).
type Kind string

// The kinds. The handshake and the feed take no slot of the budget (R-BAS-025).
const (
	KindForward     Kind = "forward"
	KindConditional Kind = "conditional"
	KindCheck       Kind = "check"
	KindProbe       Kind = "probe"
	KindFeed        Kind = "feed"
	KindHandshake   Kind = "handshake"
	KindDownloads   Kind = "downloads"
)

// Budget is the hook through which every request but the handshake and the feed takes a slot of the replica's
// concurrency budget (R-BAS-025). Acquire waits at most bastion.main.queue_timeout and returns [ErrBusy] when
// no slot is free; the returned function releases the slot and must be called exactly once, after the response
// body was read to its end or closed. [SlotBudget] implements it; the [Client] calls it, the engine does not.
type Budget interface {
	Acquire(ctx context.Context, class Class) (release func(), err error)
}

// Token is the Bastion token in use and what is known about it (R-BAS-006). The server's token reloader owns
// the value and replaces it atomically; requests in flight finish with the old one.
type Token struct {
	// Value is the trv_bst_ string. It is a secret: never logged, never in an error.
	Value string
	// Hint is the public hint (trv_bst_ and the first four characters) for logs.
	Hint string
}

// TokenSource returns the token to use for the next request.
type TokenSource interface {
	Token() Token
}

// The errors of the client. The engine maps them to reachability (R-BAS-027), the alerts (R-BAS-041) and the
// answers of R-BAS-029; a request that got a response of the main installation is not an error of this list
// unless the response says that the Bastion itself is refused.
var (
	// ErrBusy: no slot of the budget within the queue timeout; 503 bastion_busy.
	ErrBusy = errors.New("mainclient: no slot of the concurrency budget")
	// ErrTokenRefused: 401 bastion_token_invalid. The engine treats the main installation as unreachable and
	// sets trove_bastion_main_token_refused (R-BAS-006).
	ErrTokenRefused = errors.New("mainclient: the main installation refused the Bastion token")
	// ErrInterfaceMismatch: 400 bastion_interface_mismatch, or an answer whose Trove-Bastion-Interface differs
	// (R-BAS-009, R-BAS-028). The wrapping error is *MismatchError.
	ErrInterfaceMismatch = errors.New("mainclient: the interface versions differ")
	// ErrUnreachable: a connect or TLS error, a timeout or an unmarked 5xx; 502 main_failed or 504 main_timeout
	// for the caller, and no answer is kept (R-BAS-021). The wrapping error is *UnreachableError.
	ErrUnreachable = errors.New("mainclient: the main installation did not answer")
	// ErrBadAnswer: the main installation answered, but not with what the interface says: an unexpected
	// status on an interface route, a body that does not decode strictly, a malformed marking. The wrapping
	// error is *AnswerError. For the caller it is 502 main_failed.
	ErrBadAnswer = errors.New("mainclient: the main installation's answer is not what the interface defines")
)

// MismatchError carries the version the main installation spoke, when it said (the problem member
// interfaceVersion, or the answer header); 0 when it did not.
type MismatchError struct{ Main int }

// Error implements error.
func (e *MismatchError) Error() string { return ErrInterfaceMismatch.Error() }

// Is matches [ErrInterfaceMismatch].
func (e *MismatchError) Is(target error) bool { return target == ErrInterfaceMismatch }

// UnreachableError is the error of a request that got no usable answer. Timeout is true for a deadline, a
// connect or TLS-handshake timeout and a response-header timeout (504 main_timeout); Status is the status of an
// unmarked 5xx, 0 for a transport failure.
type UnreachableError struct {
	Err     error
	Status  int
	Timeout bool
}

// Error implements error. The text names no URL query and no header.
func (e *UnreachableError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: status %d", ErrUnreachable.Error(), e.Status)
	}
	return fmt.Sprintf("%s: %v", ErrUnreachable.Error(), e.Err)
}

// Unwrap returns the transport error.
func (e *UnreachableError) Unwrap() error { return e.Err }

// Is matches [ErrUnreachable].
func (e *UnreachableError) Is(target error) bool { return target == ErrUnreachable }

// AnswerError carries the status and, when the body was a problem, its code.
type AnswerError struct {
	Status int
	Code   string
	Err    error
}

// Error implements error.
func (e *AnswerError) Error() string {
	switch {
	case e.Err != nil:
		return fmt.Sprintf("%s: status %d: %v", ErrBadAnswer.Error(), e.Status, e.Err)
	case e.Code != "":
		return fmt.Sprintf("%s: status %d, code %s", ErrBadAnswer.Error(), e.Status, e.Code)
	}
	return fmt.Sprintf("%s: status %d", ErrBadAnswer.Error(), e.Status)
}

// Unwrap returns the decoding error, if any.
func (e *AnswerError) Unwrap() error { return e.Err }

// Is matches [ErrBadAnswer].
func (e *AnswerError) Is(target error) bool { return target == ErrBadAnswer }

// Forward is a caller's request on its way to the main installation (R-BAS-021, R-BAS-017).
type Forward struct {
	// Request is the caller's request: method, path and query as received, the caller's Authorization, the
	// end-to-end headers. The client removes Cookie, Forwarded, X-Forwarded-*, Trove-*, Range, If-* and the
	// hop-by-hop headers, adds the interface headers and the forwarded-for headers of ClientAddress, and the
	// Bastion token in iface.HeaderAuthorization. Conditional headers are the Bastion's own: Conditional.
	Request *http.Request
	// Class selects the budget class.
	Class Class
	// Kind is KindForward, KindConditional or KindProbe.
	Kind Kind
	// ClientAddress is the caller's address as the Bastion determined it (R-BAS-019).
	ClientAddress string
	// IfNoneMatch is the validator of a conditional request (R-BAS-022, R-BAS-023): the Bastion's own
	// If-None-Match, never the caller's.
	IfNoneMatch string
	// Probe makes the request the bodiless probe of R-BAS-014: Content-Length 0, no credential,
	// iface.HeaderProbe.
	Probe bool
	// Body is the body of a forwarded read route that takes one, at most bastion.main.max_forwarded_body.
	Body io.Reader
}

// Response is the main installation's answer to a [Forward]. The body streams; the caller closes it, which
// also releases the budget slot.
type Response struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
	// Marking is the parsed marking of the response; Marked is false for an answer without iface.HeaderClass
	// (a proxy in front of the main installation). An unmarked 5xx never arrives here: it is an
	// [ErrUnreachable]. The Trove-Bastion-* headers are removed from Header, so that nothing replays them.
	Marking iface.Marking
	Marked  bool
	// Route is the value of Trove-Bastion-Route, read before the marking headers are removed: the answer of a
	// probe to a read route that takes a body is a 204 with iface.RouteReadWithBody (R-BAS-014).
	Route string
}

// Client is the Bastion's view of the main installation. Every method takes the budget slot its kind needs
// (except Handshake and Feed), applies the headers of R-BAS-008, decodes with iface.DecodeStrict and counts
// trove_bastion_main_requests_total and the duration. The errors are the ones above.
type Client interface {
	// Handshake calls GET /api/v1/bastion/handshake (R-BAS-010). The client also reads the main installation's
	// interface version from the header and returns *MismatchError on a difference.
	Handshake(ctx context.Context) (iface.Handshake, error)
	// Check calls POST /api/v1/bastion/check (R-BAS-011).
	Check(ctx context.Context, class Class, req iface.CheckRequest) (iface.CheckAnswer, error)
	// Feed calls GET /api/v1/bastion/feed (R-BAS-015, R-BAS-026).
	Feed(ctx context.Context, after iface.Cursor, limit int) (iface.FeedAnswer, error)
	// Downloads calls POST /api/v1/bastion/downloads (R-BAS-016); a 2xx is nil.
	Downloads(ctx context.Context, req iface.DownloadsRequest) error
	// Do forwards a caller's request (R-BAS-021) or sends a probe (R-BAS-014).
	Do(ctx context.Context, f Forward) (*Response, error)
}

// Options are what [New] needs; the server builds them from the configuration.
type Options struct {
	// URL is bastion.main.url; https only (R-BAS-044).
	URL string
	// BaseURL is server.base_url: the scheme, host and port the client sends as the forwarded host
	// (R-BAS-017).
	BaseURL string
	// Replica is the pod name for iface.HeaderReplica; Release is the release for the User-Agent.
	Replica string
	Release string
	Tokens  TokenSource
	// Budget is the slot budget; nil means no bound (tests of other packages).
	Budget Budget
	// ConnectTimeout and ResponseHeaderTimeout are bastion.main.connect_timeout and response_header_timeout.
	ConnectTimeout        time.Duration
	ResponseHeaderTimeout time.Duration
	// CAFile is bastion.main.ca_file: a bundle added to the system roots and read again without a restart.
	CAFile string
	// Metrics counts trove_bastion_main_requests_total, the duration and trove_bastion_main_token_refused.
	Metrics *metrics.Metrics
	// Log, Tracer, Obs and TLSMetrics wire the shared outbound factory (upstream log, client spans with
	// traceparent, outbound and trust-store metrics); all may be nil.
	Log        *slog.Logger
	Tracer     *observability.Tracer
	Obs        *observability.Metrics
	TLSMetrics *tlsfiles.Metrics
	// TrustCheckEvery is how often the CA file is looked at again at most; zero means the outbound factory's
	// default (10 s). Tests shorten it.
	TrustCheckEvery time.Duration
	// WrapTransport wraps the outbound factory's transport. Tests set it to inject faults (maintest.Faulty);
	// production leaves it nil.
	WrapTransport func(http.RoundTripper) http.RoundTripper
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}
