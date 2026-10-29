package webish

import hs "example.test/fix/internal/httpserver"

func viaAlias() { _ = hs.NewEndpoint("x") }
