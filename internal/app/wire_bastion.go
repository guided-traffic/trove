package app

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/bastionmain"
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
	// st.Mark = …(the ShapeResolver, …)
	// <<< package 3
	return st
}
