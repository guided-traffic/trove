package bastionmain

import (
	"errors"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
)

// Stages are the two middleware of the Bastion interface in the chain of the client listener of the default
// endpoint (R-ARCH-005, Q-BAS-52). The composition root passes Chain() to httpserver.Chain.Bastion, which
// runs it after the client address and the CORS preflight and before the authentication stage:
//
//	client address → Recognize → Mark → authentication → rate limits → router
//
// Recognize (work package 2) looks for a Bastion token, answers 401 bastion_token_invalid and 400
// bastion_interface_mismatch, stores the [Recognition] and, on an interface route, moves the validated token
// out of Authorization so that the authentication stage sees an anonymous request (the shared authorization
// step admits the route on the Recognition alone). Mark (work package 3) acts only on a request that carries a
// Recognition: it wraps the response writer to add the marking headers (iface.Marking) at the status line,
// installs the [RouteObserver], and strips every iface.HeaderPrefix header from any other response. A nil
// stage is skipped; with both nil the chain is the chain of a main installation without Bastions.
type Stages struct {
	Recognize func(http.Handler) http.Handler
	Mark      func(http.Handler) http.Handler
}

// Chain composes the stages into the one function httpserver.Chain.Bastion takes; nil when both are nil.
func (s Stages) Chain() func(http.Handler) http.Handler {
	if s.Recognize == nil && s.Mark == nil {
		return nil
	}
	return func(next http.Handler) http.Handler {
		if s.Mark != nil {
			next = s.Mark(next)
		}
		if s.Recognize != nil {
			next = s.Recognize(next)
		}
		return next
	}
}

// ErrNotImplemented is the error of an operation of the Bastion work that is not built yet; it renders as
// 501 bastion_not_implemented.
var ErrNotImplemented = errors.New("bastionmain: not implemented")

// Stub returns the error of an operation whose work package is not built: requirement names the R-ID that
// fills it ("R-BAS-004"). Every stub of the contract work calls it, so a grep for it lists what is open.
func Stub(requirement string) error {
	return errs.E(errs.NotImplemented, errs.CodeBastionNotImplemented,
		"This Bastion operation is not built in this release yet ("+requirement+").", errs.WithCause(ErrNotImplemented))
}
