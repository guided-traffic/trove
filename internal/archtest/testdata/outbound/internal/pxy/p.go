package pxy

import "net/http"

// Bad builds a client outside internal/outbound.
func Bad() *http.Client { return &http.Client{} }
