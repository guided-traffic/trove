package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"
)

// ErrSourceNetworkUnavailable is matched by errors.Is when a client bound with
// WithLocalPrefix has no local address inside its prefix. The proxy engine maps
// it to 502 source_network_unavailable and does not count it toward the
// circuit (R-REPO-024).
var ErrSourceNetworkUnavailable = errors.New("no local address in the source network")

// LocalRefresh is how often a bound client enumerates the interface addresses
// again (R-REPO-024).
const LocalRefresh = 60 * time.Second

// LocalAddrs lists the unicast addresses of the interfaces of this host. It is
// the lister that WithLocalPrefix uses when it is given none.
func LocalAddrs() ([]netip.Addr, error) {
	nets, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(nets))
	for _, n := range nets {
		var ip net.IP
		switch v := n.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if a, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, a.Unmap())
		}
	}
	return out, nil
}

// WithLocalPrefix binds every connection of the client to the first interface
// address of this host inside p, taken when the option is applied and again
// when the last enumeration is LocalRefresh old. With no such address every
// request fails with ErrSourceNetworkUnavailable; there is no fallback to the
// kernel's choice (R-REPO-024, Q-ARCH-198). list returns the interface
// addresses in interface order; nil means LocalAddrs.
func WithLocalPrefix(p netip.Prefix, list func() ([]netip.Addr, error)) ClientOption {
	return func(s *clientSettings) { s.local = newLocalBinder(p, list, time.Now) }
}

// localBinder holds the address a client binds to.
type localBinder struct {
	prefix netip.Prefix
	list   func() ([]netip.Addr, error)
	now    func() time.Time

	mu   sync.Mutex
	at   time.Time // of the last enumeration; zero before the first
	addr netip.Addr
	err  error // why addr is not usable; nil when it is
}

func newLocalBinder(p netip.Prefix, list func() ([]netip.Addr, error), now func() time.Time) *localBinder {
	if list == nil {
		list = LocalAddrs
	}
	b := &localBinder{prefix: p.Masked(), list: list, now: now}
	b.mu.Lock()
	b.refresh()
	b.mu.Unlock()
	return b
}

// refresh enumerates the interface addresses. The caller holds mu. A lister
// that fails keeps the address of the last good enumeration, which is the best
// information there is; a lister that succeeds without an address in the
// prefix drops it, because that address is gone.
func (b *localBinder) refresh() {
	b.at = b.now()
	addrs, err := b.list()
	if err != nil {
		if !b.addr.IsValid() {
			b.err = fmt.Errorf("%w: listing the interface addresses: %w", ErrSourceNetworkUnavailable, err)
		}
		return
	}
	b.addr, b.err = netip.Addr{}, fmt.Errorf("%w %s", ErrSourceNetworkUnavailable, b.prefix)
	for _, a := range addrs {
		a = a.Unmap()
		if b.prefix.Contains(a.WithZone("")) {
			b.addr, b.err = a, nil
			return
		}
	}
}

// address returns the address to bind to, enumerating again when the last
// enumeration is LocalRefresh old.
func (b *localBinder) address() (*net.TCPAddr, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.now().Sub(b.at) >= LocalRefresh {
		b.refresh()
	}
	if b.err != nil {
		return nil, b.err
	}
	return &net.TCPAddr{IP: b.addr.AsSlice(), Zone: b.addr.Zone()}, nil
}

// dialFunc wraps d so that every connection leaves from the bound address. The
// dialer's Control hook, and so the egress policy, still runs.
func (b *localBinder) dialFunc(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		la, err := b.address()
		if err != nil {
			return nil, err
		}
		bound := *d
		bound.LocalAddr = la
		return bound.DialContext(ctx, network, addr)
	}
}
