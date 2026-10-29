package cluster

import "example.test/fix/internal/proxy"

// Bad is infrastructure importing the domain layer.
var _ = proxy.Engine{}
