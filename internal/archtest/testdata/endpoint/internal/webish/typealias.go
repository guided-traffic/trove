package webish

import "example.test/fix/internal/httpserver"

type chainAlias = httpserver.Chain

func viaTypeAlias() { _ = chainAlias{} }
