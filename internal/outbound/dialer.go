package outbound

import (
	"net"
	"time"
)

// Dialer is the socket dialer type. Code outside this package that must hand a
// dialer to a driver (the Valkey client, for example) names this alias and
// builds the value with NewDialer, so that internal/outbound stays the only
// place that dials (R-ARCH-002).
type Dialer = net.Dialer

// NewDialer returns a dialer for infrastructure connections whose destination
// is operator configuration (PostgreSQL, Valkey and its Sentinels). It applies
// no address policy: that policy guards destinations that other people can
// name, which is what Factory.Client does for them. resolver may be nil.
func NewDialer(timeout time.Duration, resolver *net.Resolver) Dialer {
	return Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Resolver: resolver}
}
