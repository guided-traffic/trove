package outbound

import "net/http"

// New is allowed to build a client.
func New() *http.Client { return &http.Client{} }
