package webish

import "example.test/fix/internal/httpserver"

func direct(r *httpserver.Router) { r.HandleRegistryUncleaned("/v2/", nil) }
