package app

import (
	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/packages/readme"
	"github.com/guided-traffic/trove/internal/packages/view"
)

// packagesAPI returns the services of the package operations of the management API
// (R-API-029, R-API-030): the read model over the registered formats' view
// adapters, the decision point as the classifier of what a caller may read, the
// README renderer with the operator's image origins, and the keys of ui.client_setup.
// A replica without a Host (the shell profile) or without a database answers the
// operations with 503: the zero value is returned.
func (a *App) packagesAPI() v1.PackageServices {
	host, ok := a.host.get()
	db := a.database()
	eng := part[authzArea](a.parts).engine
	repos := a.Repositories()
	if !ok || db == nil || eng == nil || repos == nil {
		return v1.PackageServices{}
	}
	svc, err := view.New(view.Options{DB: db, Adapters: formathost.NewViewAdapters(formats.List(), host)})
	if err != nil {
		a.log.Error("packages: the read model could not be built", "err", err)
		return v1.PackageServices{}
	}
	rd, err := readme.New(readme.Options{
		MaxSourceBytes: int(a.cfg.UI.Readme.MaxSourceBytes),
		ImageOrigins:   a.cfg.UI.Readme.ExternalImageOrigins,
		OwnOrigins:     a.ownOrigins(),
	})
	if err != nil {
		a.log.Error("packages: the README renderer could not be built", "err", err)
		return v1.PackageServices{}
	}
	return v1.PackageServices{
		View: svc, Decider: eng, Scoper: authz.ReadScoper(eng), Reader: repos, Readme: rd,
		TokenEnvVar: a.cfg.UI.ClientSetup.TokenEnvVar, TokenTTL: a.cfg.UI.ClientSetup.TokenTTL,
	}
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
