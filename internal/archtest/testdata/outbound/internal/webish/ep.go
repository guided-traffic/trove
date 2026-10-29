package webish

import "example.test/fix/internal/httpserver"

// A handler package must not make up the endpoint of a request.
func forge() { _ = httpserver.Endpoint{}; _ = httpserver.WithEndpoint }
