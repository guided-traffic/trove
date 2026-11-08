package webish2

import "example.test/fix/internal/httpserver"

func viaValue(r *httpserver.Router) {
	f := r.HandleRegistryUncleaned
	f("/v2/", nil)
}
