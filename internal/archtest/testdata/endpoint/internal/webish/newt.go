package webish

import "example.test/fix/internal/httpserver"

func viaNew() { _ = new(httpserver.Endpoint) }
