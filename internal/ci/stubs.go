package ci

import (
	"github.com/guided-traffic/trove/internal/errs"
)

// NotImplemented is the answer of a CI service that a replica was built without: 501 ci_not_implemented, naming
// the work package of docs/developer/ci-oidc.md that builds it. Every work package's service is built and wired in
// internal/app/wire_ci.go; the answer remains for a service option left nil (the issuer service of `trove migrate`
// has no mapping store) and for the task handlers, which treat it as "nothing to do".
func NotImplemented(workPackage string) error {
	return errs.E(errs.NotImplemented, errs.CodeCINotImplemented,
		"This operation of the CI federation is not built in this release yet ("+workPackage+").")
}
