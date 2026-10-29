package webish

import "example.test/fix/internal/httpserver"

func viaLiteral() { _ = &httpserver.Endpoint{}; var c httpserver.Chain; _ = c }
