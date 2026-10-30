package app

import (
	"context"
	"fmt"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/packages/readme"
	"github.com/guided-traffic/trove/internal/packages/view"
)

// AreaPackagesAPI is the services of the package screens' operations of the management
// API (R-API-029, R-API-030).
const AreaPackagesAPI Area = "packages-api"

// packagesAPIArea is the package operations' services of a replica with a client listener.
type packagesAPIArea struct{ services v1.PackageServices }

// wirePackagesAPI builds the services of the package operations of the management API: the
// read model over the registered formats' view adapters, the decision point as the
// classifier of what a caller may read, the README renderer with the operator's image
// origins, and the keys of ui.client_setup. A part that cannot be built, such as a README
// renderer the ui.readme keys do not allow, stops the start-up: the operations are not served
// as 503 by a replica that says it is ready (Q-ARCH-209).
func (a *App) wirePackagesAPI(_ context.Context, in infra, az authzArea, repo repositoryArea, fh formatHostArea) (packagesAPIArea, error) {
	if err := requireInputs(AreaPackagesAPI, given("format host", fh.host), given("decision point", az.engine),
		given("repository reader", repo.reader)); err != nil {
		return packagesAPIArea{}, err
	}
	svc, err := view.New(view.Options{DB: in.db, Adapters: formathost.NewViewAdapters(formats.List(), fh.host)})
	if err != nil {
		return packagesAPIArea{}, fmt.Errorf("%s: the read model: %w", AreaPackagesAPI, err)
	}
	rd, err := readme.New(readme.Options{
		MaxSourceBytes: int(a.cfg.UI.Readme.MaxSourceBytes),
		ImageOrigins:   a.cfg.UI.Readme.ExternalImageOrigins,
		OwnOrigins:     a.ownOrigins(),
	})
	if err != nil {
		return packagesAPIArea{}, fmt.Errorf("%s: the README renderer: %w", AreaPackagesAPI, err)
	}
	return packagesAPIArea{services: v1.PackageServices{
		View: svc, Decider: az.engine, Scoper: authz.ReadScoper(az.engine), Reader: repo.reader, Readme: rd,
		TokenEnvVar: a.cfg.UI.ClientSetup.TokenEnvVar, TokenTTL: a.cfg.UI.ClientSetup.TokenTTL,
	}}, nil
}

// ownOrigins are the origins of trove itself: the base URL of the server, its
// additional base URLs and the base URL of every further endpoint. A README image
// from one of them is dropped (R-UI-008).
func (a *App) ownOrigins() []string {
	out := []string{a.cfg.Server.BaseURL}
	out = append(out, a.cfg.Server.AdditionalBaseURLs...)
	for _, e := range a.cfg.Server.Endpoints {
		out = append(out, e.BaseURL)
	}
	return out
}
