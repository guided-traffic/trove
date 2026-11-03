package v1

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// BastionServices are what the four operations of the Bastion interface need (R-BAS-010, R-BAS-011, R-BAS-015,
// R-BAS-016). The composition root fills it; a member that is nil answers the operations that need it with
// 503 unavailable instead of a panic.
type BastionServices struct {
	// Config is the bastions section: the bounds of a check, the feed and the intake.
	Config config.Bastions
	// Release is the release of this installation and BaseURL its server.base_url (the handshake's
	// installation member).
	Release string
	BaseURL string
	// Anonymous reports the effective anonymous switch of the default endpoint: the YAML ceiling, the runtime
	// setting and the endpoint's own rule (R-AUTH-037).
	Anonymous func(ctx context.Context) (bool, error)
	// Authenticate is the authentication stage of the default endpoint without the Bastion short-circuit:
	// the check runs the caller's credential through it exactly as a direct request is (R-AUTH-023,
	// R-AUTH-038). The request it gets carries the caller's address in its context.
	Authenticate func(*http.Request) (httpserver.Principal, error)
	// Shapes computes the read shapes (work package 3).
	Shapes bastionmain.ShapeResolver
	// Feed, Contacts and Downloads are the revocation feed, the last-contact recorder and the download intake.
	Feed      *bastionmain.Feed
	Contacts  *bastionmain.Contacts
	Downloads *bastionmain.Intake
}

// writeInterface answers an operation of the interface: the body is the wire form of the type
// (iface.Marshal), never cached, and the answer names the interface version. The recognition stage has set the
// version header already; setting it again keeps the handler correct without the stage.
func writeInterface(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, err := iface.Marshal(v)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.writeInterface"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", iface.MediaType)
	h.Set("Cache-Control", "no-store")
	h.Set(iface.HeaderInterface, strconv.Itoa(iface.InterfaceVersion))
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// bastionLog is the request's logger.
func bastionLog(r *http.Request) *slog.Logger { return httpserver.Logger(r.Context()) }

// BastionPrincipal is the principal of a request on an interface route: the registration whose token the
// recognition stage checked. It holds no grant and decides nothing; the request log and the rate limits name it.
func BastionPrincipal(rec bastionmain.Recognition) httpserver.Principal {
	return httpserver.Principal{
		Kind: httpserver.KindBastion, ID: rec.BastionID, Name: rec.Bastion,
		Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: rec.TokenID, TokenType: httpserver.TokenType(iface.TokenTypeBST)},
	}
}

// BastionRouteAuth wraps the authentication stage of an endpoint's chain: a request on an interface route
// carries the proof of the recognition stage and no credential of a caller, so it is admitted as the
// registration whatever the anonymous switches say (R-BAS-008, Q-BAS-52); the authentication stage is where an
// anonymous request of an endpoint that admits none is refused, and it must not refuse the Bastion.
//
// A recognized write probe (R-BAS-014) is admitted as the anonymous caller for routing only, whatever the
// anonymous switches say: it carries no credential of a caller by design, and the marking stage answers it at
// the route match - 403 bastion_read_only or 204 - before any decision, handler or body read (Q-BAS-118). The
// recognition stage admits a forward only below /api/<format>/ and /files/, whose routes all tell the marking
// stage their kind first, so a probe reaches no handler. Every other request is the stage's. admitted, when
// set, is told of the registration on an interface route (the request log names it).
func BastionRouteAuth(stage func(*http.Request) (httpserver.Principal, error), admitted func(*http.Request, httpserver.Principal)) func(*http.Request) (httpserver.Principal, error) {
	return func(r *http.Request) (httpserver.Principal, error) {
		rec, ok := bastionmain.RecognitionFrom(r.Context())
		switch {
		case ok && rec.Route:
			p := BastionPrincipal(rec)
			if admitted != nil {
				admitted(r, p)
			}
			return p, nil
		case ok && rec.Probe:
			return httpserver.AnonymousPrincipal(), nil
		}
		return stage(r)
	}
}
