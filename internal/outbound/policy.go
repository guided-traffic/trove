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

// Rules of the policy that are not a configured CIDR.
const (
	// ruleZoned refuses IPv6 addresses with a zone (fe80::1%eth0, ::1%1),
	// whatever allow_cidrs says. A zone scopes an address to one interface of
	// this host, which a remote repository never needs, and
	// netip.Prefix.Contains is false for every zoned address, so a zoned form
	// would otherwise pass every CIDR rule unchecked.
	ruleZoned = "zoned IPv6 addresses"
)

// nat64 is the well-known NAT64 prefix of RFC 6052. An address in it reaches
// the IPv4 address in its last 32 bits through the cluster's NAT64 gateway.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// addressPolicy is the egress policy of R-REPO-038: addresses in allow are
// permitted even inside a denied range; addresses in deny are refused;
// everything else is permitted. Before the CIDR rules run, a zoned IPv6
// address is refused, an IPv4-mapped IPv6 address is unmapped (so
// ::ffff:127.0.0.1 is checked as 127.0.0.1), and an address of the NAT64
// prefix is checked as the IPv4 address it translates to as well (so
// 64:ff9b::a9fe:a9fe is refused like 169.254.169.254).
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
	if a.Zone() != "" {
		return &EgressDeniedError{Addr: a, Rule: ruleZoned}
	}
	a = a.Unmap()
	allowed, denied := p.match(a)
	if denied != nil {
		return denied
	}
	if allowed {
		return nil
	}
	if a.Is6() && nat64.Contains(a) {
		b := a.As16()
		v4 := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		if _, denied := p.match(v4); denied != nil {
			return &EgressDeniedError{Addr: a, Rule: denied.Rule + " through the NAT64 prefix " + nat64.String()}
		}
	}
	return nil
}

// match applies the CIDR rules to one address: allowed is true when an allow
// rule contains it; denied is non-nil when a deny rule does and no allow rule.
func (p *addressPolicy) match(a netip.Addr) (allowed bool, denied *EgressDeniedError) {
	for _, pre := range p.allow {
		if pre.Contains(a) {
			return true, nil
		}
	}
	for _, pre := range p.deny {
		if pre.Contains(a) {
			return false, &EgressDeniedError{Addr: a, Rule: pre.String()}
		}
	}
	return false, nil
}

// control returns the net.Dialer Control hook: it runs after DNS resolution and
// before connect, with the literal IP that is about to be dialed (including a
// zone, which check refuses).
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
