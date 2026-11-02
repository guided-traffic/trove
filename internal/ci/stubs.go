package ci

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
)

// NotImplemented is the answer of a stub: 501 ci_not_implemented, naming the work package of
// docs/developer/ci-oidc.md that builds the method. The composition root wires the stubs below until
// the work packages replace them, so that the replica starts, the API document is complete and each
// operation answers 501 and not a panic.
func NotImplemented(workPackage string) error {
	return errs.E(errs.NotImplemented, errs.CodeCINotImplemented,
		"This operation of the CI federation is not built in this release yet ("+workPackage+").")
}

// StubExchanger is the stub of [Exchanger], every method answering [NotImplemented], until work package W4
// replaces it in internal/app/wire_ci.go and deletes this file's type.
type StubExchanger struct{}

var _ Exchanger = StubExchanger{}

// CheckAddress answers like Exchange: the stub refuses nothing and the exchange says it is not built.
func (StubExchanger) CheckAddress(context.Context, string) error { return nil }

func (StubExchanger) Exchange(context.Context, ExchangeRequest) (ExchangeResult, error) {
	return ExchangeResult{}, NotImplemented("W4")
}
