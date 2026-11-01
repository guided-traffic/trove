package nuget

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// The handlers of the operations the skeleton routes and does not serve yet (WP3.1c): each
// answers 501 nuget_not_implemented, so the format registers, the conformance kit's
// structural checks run and a client reads a failure of the server and not a missing
// package. The work package that builds an operation moves its method out of this file into
// a file of its own (handlers_<topic>.go); the route table (routes.go) names the methods and
// is not edited.
//
// Authorization has run when a handler is reached: the coarse check of the route's action on
// the repository (R-ARCH-011 step 2), so a refused caller never learns that an operation is
// not built.

func stub(what string) formats.HandlerFunc {
	return func(context.Context, *formats.Request, http.ResponseWriter) error { return notImplemented(what) }
}

func (f *Format) handleFlatIndex(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("the flat-container version list")(ctx, rq, w)
}

func (f *Format) handleNupkg(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("the package download")(ctx, rq, w)
}

func (f *Format) handleNuspec(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("the .nuspec download")(ctx, rq, w)
}

func (f *Format) handleEmbedded(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("the embedded files (P1, R-NUGET-024)")(ctx, rq, w)
}

func (f *Format) handleRegistrationIndex(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("the registration index")(ctx, rq, w)
}

func (f *Format) handleRegistrationPage(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("the registration page")(ctx, rq, w)
}

func (f *Format) handleRegistrationLeaf(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("the registration leaf")(ctx, rq, w)
}

func (f *Format) handleSearch(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("search")(ctx, rq, w)
}

func (f *Format) handleAutocomplete(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("autocomplete (P1, R-NUGET-011)")(ctx, rq, w)
}

func (f *Format) handleSymbolPush(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("the symbol push")(ctx, rq, w)
}

func (f *Format) handleSymbols(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("the symbol server (P1, R-NUGET-021)")(ctx, rq, w)
}
