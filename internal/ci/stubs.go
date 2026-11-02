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
	// StubMappings is the stub of [Mappings] (W3).
	StubMappings struct{}
	// StubExchanger is the stub of [Exchanger] (W4).
	StubExchanger struct{}
	// StubRevoker is the stub of [Revoker] (W5).
	StubRevoker struct{}
	// StubIssuances is the stub of [Issuances] (W5).
	StubIssuances struct{}
)

var (
	_ Mappings  = StubMappings{}
	_ Exchanger = StubExchanger{}
	_ Revoker   = StubRevoker{}
	_ Issuances = StubIssuances{}
)

func (StubMappings) List(context.Context, string) ([]Mapping, error) {
	return nil, NotImplemented("W3")
}

func (StubMappings) Get(context.Context, string, string) (Mapping, error) {
	return Mapping{}, NotImplemented("W3")
}

func (StubMappings) Create(context.Context, Actor, string, MappingInput) (MappingResult, error) {
	return MappingResult{}, NotImplemented("W3")
}

func (StubMappings) Replace(context.Context, Actor, string, string, int64, MappingInput) (MappingResult, error) {
	return MappingResult{}, NotImplemented("W3")
}

func (StubMappings) Delete(context.Context, Actor, string, string, int64) error {
	return NotImplemented("W3")
}

func (StubMappings) Evaluate(context.Context, string, EvaluateRequest) (EvaluateResult, error) {
	return EvaluateResult{}, NotImplemented("W3")
}

func (StubMappings) Snippet(context.Context, string, SnippetRequest) (string, error) {
	return "", NotImplemented("W3")
}

func (StubMappings) Set(context.Context, string) (MappingSet, error) {
	return MappingSet{}, NotImplemented("W3")
}

func (StubMappings) SetTx(context.Context, meta.Tx, string) (MappingSet, error) {
	return MappingSet{}, NotImplemented("W3")
}

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

func (StubMappings) ListIn(context.Context, string) ([]Mapping, error) {
	return nil, NotImplemented("W3")
}

func (StubMappings) GetIn(context.Context, string, string, string) (Mapping, error) {
	return Mapping{}, NotImplemented("W3")
}

func (StubMappings) CreateIn(context.Context, Actor, string, MappingInput) (MappingResult, error) {
	return MappingResult{}, NotImplemented("W3")
}

func (StubMappings) ReplaceIn(context.Context, Actor, string, string, string, int64, MappingInput) (MappingResult, error) {
	return MappingResult{}, NotImplemented("W3")
}

func (StubMappings) DeleteIn(context.Context, Actor, string, string, string, int64) error {
	return NotImplemented("W3")
}

func (StubMappings) EvaluateIn(context.Context, string, string, EvaluateRequest) (EvaluateResult, error) {
	return EvaluateResult{}, NotImplemented("W3")
}

func (StubMappings) CountIn(context.Context, string) (int, error) {
	return 0, NotImplemented("W3")
}

func (StubIssuances) ListForProject(context.Context, string, IssuanceFilter) (IssuancePage, error) {
	return IssuancePage{}, NotImplemented("W5")
}

func (StubMappings) DeleteForIssuerTx(context.Context, meta.Tx, Actor, string, string) (int, error) {
	return 0, NotImplemented("W3")
}
