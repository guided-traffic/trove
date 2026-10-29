package webish

import "example.test/fix/internal/httpserver"

// Reading an endpoint is allowed.
func name(e *httpserver.Endpoint) string { return e.Name() }
