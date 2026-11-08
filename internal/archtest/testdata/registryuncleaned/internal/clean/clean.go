package clean

import "example.test/fix/internal/httpserver"

func fine(r *httpserver.Router) { r.HandleUncleaned("/files/", nil) }
