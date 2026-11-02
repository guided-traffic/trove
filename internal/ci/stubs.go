package ci

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// NotImplemented is the answer of a stub: 501 ci_not_implemented, naming the work package of
// docs/developer/ci-oidc.md that builds the method. The composition root wires the stubs below until
// the work packages replace them, so that the replica starts, the API document is complete and each
// operation answers 501 and not a panic.
func NotImplemented(workPackage string) error {
	return errs.E(errs.NotImplemented, errs.CodeCINotImplemented,
		"This operation of the CI federation is not built in this release yet ("+workPackage+").")
}

// The stubs: one type per service, every method answering [NotImplemented]. A work package replaces
// its stub in internal/app/wire_ci.go and deletes its type from this file.
type (
	// StubExchanger is the stub of [Exchanger] (W4).
	StubExchanger struct{}
	// StubRevoker is the stub of [Revoker] (W5).
	StubRevoker struct{}
	// StubIssuances is the stub of [Issuances] (W5).
	StubIssuances struct{}
)

var (
	_ Exchanger = StubExchanger{}
	_ Revoker   = StubRevoker{}
	_ Issuances = StubIssuances{}
)

// CheckAddress answers like Exchange: the stub refuses nothing and the exchange says it is not built.
func (StubExchanger) CheckAddress(context.Context, string) error { return nil }

func (StubExchanger) Exchange(context.Context, ExchangeRequest) (ExchangeResult, error) {
	return ExchangeResult{}, NotImplemented("W4")
}

func (StubRevoker) RevokeMappingTx(context.Context, meta.Tx, Actor, Mapping, RevokeCause) (Revoked, error) {
	return Revoked{}, NotImplemented("W5")
}

func (StubRevoker) RevokeIssuerTx(context.Context, meta.Tx, Actor, string, string, RevokeCause) (Revoked, error) {
	return Revoked{}, NotImplemented("W5")
}

func (StubRevoker) RevokeMatching(context.Context, Actor, RevokeFilter) (Revoked, error) {
	return Revoked{}, NotImplemented("W5")
}

func (StubIssuances) List(context.Context, IssuanceFilter) (IssuancePage, error) {
	return IssuancePage{}, NotImplemented("W5")
}

func (StubIssuances) Get(context.Context, string) (Issuance, Usage, error) {
	return Issuance{}, Usage{}, NotImplemented("W5")
}

func (StubIssuances) CleanupReplay(context.Context) (int64, error) {
	return 0, NotImplemented("W5")
}

func (StubIssuances) RetainIssuances(context.Context) (int64, error) {
	return 0, NotImplemented("W5")
}

func (StubIssuances) ListForProject(context.Context, string, IssuanceFilter) (IssuancePage, error) {
	return IssuancePage{}, NotImplemented("W5")
}
