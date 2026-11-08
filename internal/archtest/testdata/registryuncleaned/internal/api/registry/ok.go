package registry

import "example.test/fix/internal/httpserver"

func mount(r *httpserver.Router) { r.HandleRegistryUncleaned("/v2/", nil) }
