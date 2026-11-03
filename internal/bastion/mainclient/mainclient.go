// Package mainclient is the Bastion's client of the main installation (R-BAS-006, R-BAS-008, R-BAS-021,
// R-BAS-025, R-BAS-027): the four operations of the interface and the forwarding of a caller's request. It is
// the only package of the Bastion that talks to the main installation, so the budget, the interface headers,
// the forwarded-for headers, the token and the error classes are applied in one place.
//
// Work package 4 implements [Client] over net/http with TLS (the system roots plus bastion.main.ca_file,
// R-BAS-044) and hands it to the engine; the tests of the other packages use [Fake]-style doubles of the
// interface. Everything here is a contract: [Stub] is the placeholder that answers [ErrNotBuilt].
package mainclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
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
// body was read to its end or closed. The engine owns the implementation (package 4), mainclient only calls it.
type Budget interface {
	Acquire(ctx context.Context, class Class) (release func(), err error)
}

// Token is the Bastion token in use and what is known about it (R-BAS-006). The engine's token reloader owns
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
	// for the caller, and no answer is kept (R-BAS-021).
	ErrUnreachable = errors.New("mainclient: the main installation did not answer")
	// ErrNotBuilt is what [Stub] answers.
	ErrNotBuilt = errors.New("mainclient: not built yet (work package 4)")
)

// MismatchError carries the version the main installation spoke, when it said (the problem member
// interfaceVersion, or the answer header); 0 when it did not.
type MismatchError struct{ Main int }

// Error implements error.
func (e *MismatchError) Error() string { return ErrInterfaceMismatch.Error() }

// Is matches [ErrInterfaceMismatch].
func (e *MismatchError) Is(target error) bool { return target == ErrInterfaceMismatch }

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
	// (a proxy in front of the main installation), which the engine treats as an error when it is a 5xx.
	Marking iface.Marking
	Marked  bool
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

// Options are what the implementation needs; the engine builds them from the configuration.
type Options struct {
	// URL is bastion.main.url.
	URL string
	// BaseURL is server.base_url: the scheme, host and port the client sends as the forwarded host
	// (R-BAS-017).
	BaseURL string
	// Replica is the pod name for iface.HeaderReplica; Release is the release for the User-Agent.
	Replica string
	Release string
	Tokens  TokenSource
	Budget  Budget
	// ConnectTimeout and ResponseHeaderTimeout are bastion.main.connect_timeout and response_header_timeout.
	ConnectTimeout        time.Duration
	ResponseHeaderTimeout time.Duration
}

// Stub is the placeholder client of the contract work: every method answers [ErrNotBuilt].
type Stub struct{}

var _ Client = Stub{}

// Handshake implements [Client].
func (Stub) Handshake(context.Context) (iface.Handshake, error) {
	return iface.Handshake{}, ErrNotBuilt
}

// Check implements [Client].
func (Stub) Check(context.Context, Class, iface.CheckRequest) (iface.CheckAnswer, error) {
	return iface.CheckAnswer{}, ErrNotBuilt
}

// Feed implements [Client].
func (Stub) Feed(context.Context, iface.Cursor, int) (iface.FeedAnswer, error) {
	return iface.FeedAnswer{}, ErrNotBuilt
}

// Downloads implements [Client].
func (Stub) Downloads(context.Context, iface.DownloadsRequest) error { return ErrNotBuilt }

// Do implements [Client].
func (Stub) Do(context.Context, Forward) (*Response, error) { return nil, ErrNotBuilt }
