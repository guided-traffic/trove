package outbound

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// ErrEgressDenied is matched by errors.Is for a connection refused by the
// egress address policy. The proxy engine maps it to 502 egress_denied.
var ErrEgressDenied = errors.New("egress denied by address policy")

// EgressDeniedError names the refused address and the rule that refused it.
type EgressDeniedError struct {
	Addr netip.Addr
	Rule string
}

func (e *EgressDeniedError) Error() string {
	return fmt.Sprintf("egress denied: address %s is in %s", e.Addr, e.Rule)
}

// Is reports ErrEgressDenied.
func (e *EgressDeniedError) Is(target error) bool { return target == ErrEgressDenied }

// addressPolicy is the egress policy of R-REPO-038: addresses in allow are
// permitted even inside a denied range; addresses in deny are refused;
// everything else is permitted. IPv4-mapped IPv6 addresses are unmapped first,
// so ::ffff:127.0.0.1 is checked as 127.0.0.1.
type addressPolicy struct {
	deny, allow []netip.Prefix
}

func parsePrefixes(cidrs []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR: %w", c, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func (p *addressPolicy) check(a netip.Addr) error {
	a = a.Unmap()
	for _, pre := range p.allow {
		if pre.Contains(a) {
			return nil
		}
	}
	for _, pre := range p.deny {
		if pre.Contains(a) {
			return &EgressDeniedError{Addr: a, Rule: pre.String()}
		}
	}
	return nil
}

// control returns the net.Dialer Control hook: it runs after DNS resolution and
// before connect, with the literal IP that is about to be dialed.
func (p *addressPolicy) control(onDeny func()) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			host = address
		}
		a, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("egress check: %q is not an IP address", host)
		}
		if err := p.check(a); err != nil {
			if onDeny != nil {
				onDeny()
			}
			return err
		}
		return nil
	}
}
