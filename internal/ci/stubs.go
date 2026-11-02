package ci

import (
	"context"

	"github.com/guided-traffic/trove/internal/ci/subject"
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
	// StubIssuers is the stub of [Issuers] (W1).
	StubIssuers struct{}
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
	_ Issuers   = StubIssuers{}
	_ Mappings  = StubMappings{}
	_ Exchanger = StubExchanger{}
	_ Revoker   = StubRevoker{}
	_ Issuances = StubIssuances{}
)

func (StubIssuers) List(context.Context) ([]Issuer, error) { return nil, NotImplemented("W1") }

func (StubIssuers) Get(context.Context, string) (Issuer, error) {
	return Issuer{}, NotImplemented("W1")
}

func (StubIssuers) Create(context.Context, Actor, IssuerInput, bool) (Issuer, error) {
	return Issuer{}, NotImplemented("W1")
}

func (StubIssuers) Replace(context.Context, Actor, string, int64, IssuerInput) (Issuer, error) {
	return Issuer{}, NotImplemented("W1")
}

func (StubIssuers) Delete(context.Context, Actor, string, bool, int64) error {
	return NotImplemented("W1")
}

func (StubIssuers) Keys(context.Context, string) (KeyStatus, error) {
	return KeyStatus{}, NotImplemented("W1")
}

func (StubIssuers) RefreshKeys(context.Context, Actor, string, RefreshTrigger) (KeyStatus, error) {
	return KeyStatus{}, NotImplemented("W1")
}

func (StubIssuers) Resolve(context.Context, string) (Issuer, bool, error) {
	return Issuer{}, false, NotImplemented("W1")
}

func (StubIssuers) LockShareTx(context.Context, meta.Tx, string) (Issuer, error) {
	return Issuer{}, NotImplemented("W1")
}

func (StubIssuers) Reload(context.Context, Issuer) {}

func (StubIssuers) KeySource(Issuer) subject.KeySource { return stubKeys{} }

func (StubIssuers) RefreshDue(context.Context) (int, error) { return 0, NotImplemented("W1") }

func (StubIssuers) ApplyDeclared(context.Context, meta.Tx, []DeclaredIssuer) error {
	return NotImplemented("W1")
}

type stubKeys struct{}

func (stubKeys) Keys(context.Context, string, string) ([]subject.Key, error) {
	return nil, subject.ErrKeysUnavailable
}

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

func (StubIssuers) Choices(context.Context) ([]IssuerChoice, error) {
	return nil, NotImplemented("W1")
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
