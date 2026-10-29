package webish

import "example.test/fix/internal/httpserver"

var _ = func() { f := httpserver.NewEndpoint; _ = f }
