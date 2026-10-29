package app

import "example.test/fix/internal/httpserver"

// The composition root builds endpoints and chains.
func build() { _ = httpserver.NewEndpoint("x"); _ = httpserver.Chain{} }
