package app

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/bastionmark"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The wiring of the main installation's side of the trove Bastion (docs/developer/bastion.md, R-BAS-004 to
// R-BAS-017). The contract work wires nothing but the seams' places: the chain stage of the default endpoint,
// which is empty until the work packages fill it, and the flag that makes the unbuilt operations of the
// management API answer 501 (v1.Options.BastionStubs).
//
// Each work package edits its own marked block, and only that one, and adds the fields it needs to App in
// its own block of app.go:
//
//   - package 1 (registrations, the bst token type): builds the bastionmain.TokenAuthenticator;
//   - package 2 (interface handlers, recognition): the Recognize stage and the interface services;
//   - package 3 (marking, write probe): builds the bastionmain.ShapeResolver (package 2's check uses it) and
//     the Mark stage.

// bastionStage returns the Bastion stage of an endpoint's chain (httpserver.Chain.Bastion): only the default
// endpoint serves the Bastion interface and recognizes Bastion requests (R-BAS-005), so every other endpoint
// gets none.
func (a *App) bastionStage(isDefault bool) func(http.Handler) http.Handler {
	if !isDefault {
		return nil
	}
	return a.bastionStages().Chain()
}

// bastionStages assembles the two stages. A stage whose package is not built is nil, and a nil stage is
// skipped, so a main installation without the Bastion work in this build behaves as before.
func (a *App) bastionStages() bastionmain.Stages {
	var st bastionmain.Stages
	// >>> package 2
	// st.Recognize = …(the TokenAuthenticator of package 1, …)
	// <<< package 2

	// >>> package 3
	if shapes := a.bastionShapes(); shapes != nil {
		st.Mark = bastionmark.New(bastionmark.Options{
			Shapes: shapes, MainURL: a.cfg.Server.BaseURL, Metrics: a.bastionMetrics(), Log: a.log.With("component", "bastion"),
		}).Stage()
	}
	// <<< package 3
	return st
}

// >>> package 1

// bastionRegistrations builds the registration service behind /api/v1/bastions (R-BAS-004). It is nil on a
// replica that has no identity area (a worker), whose API operations then answer 503.
func (a *App) bastionRegistrations() *bastionmain.Registrations {
	in, id := part[infra](a.parts), part[identityArea](a.parts)
	if in.db == nil || id.tokens == nil {
		return nil
	}
	return bastionmain.NewRegistrations(bastionmain.RegistrationOptions{
		DB: in.db, Notifier: in.changes, Recorder: in.rec, Tokens: id.tokens, Max: a.cfg.Bastions.MaxRegistrations,
	})
}

// bastionAuthenticator builds the bastionmain.TokenAuthenticator over the token service (R-BAS-005): the
// recognition stage of package 2 validates a Bastion token with it. It is nil where the token service is not
// built.
func (a *App) bastionAuthenticator() bastionmain.TokenAuthenticator {
	id := part[identityArea](a.parts)
	if id.tokens == nil {
		return nil
	}
	return bastionmain.NewAuthenticator(id.tokens, func(ctx context.Context) string { return httpserver.SourceFrom(ctx).ClientAddr })
}

// <<< package 1
// >>> package 3

// bastionShapes returns the ShapeResolver over the decision point (R-BAS-012): the Mark stage asks it for the
// caller's shape on the addressed repository and the check operation for a credential it has validated. A
// replica without the authorization area has none.
func (a *App) bastionShapes() bastionmain.ShapeResolver {
	eng := part[authzArea](a.parts).engine
	if eng == nil {
		return nil
	}
	return bastionmain.NewShapeResolver(eng)
}

// bastionMetrics returns the metrics of the main installation's side, registered once: the Mark stage counts
// the forwarded requests on the value the interface handlers count the interface requests on.
func (a *App) bastionMetrics() *bastionmain.Metrics {
	a.bastionMxOnce.Do(func() {
		m, err := bastionmain.NewMetrics(a.tele.metrics.Registry())
		if err != nil {
			a.log.Error("the Bastion metrics could not be registered", "error", err)
			return
		}
		a.bastionMx = m
	})
	return a.bastionMx
}

// <<< package 3
