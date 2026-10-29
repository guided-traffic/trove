package config

import "net"

// Proxy holds the remote-fetch settings. Only proxy.outbound exists in M0
// (R-ARCH-022); the owner of the proxy engine adds the remaining keys here.
type Proxy struct {
	Outbound Outbound `yaml:"outbound"`
}

// Outbound configures the outbound HTTP client factory (R-ARCH-022, R-REPO-035
// to R-REPO-038). Every outbound request of the process is made by a client
// built from these settings.
type Outbound struct {
	TLS                  OutboundTLS           `yaml:"tls"`
	ProxyFromEnvironment bool                  `yaml:"proxy_from_environment" default:"false"`
	AddressPolicy        OutboundAddressPolicy `yaml:"address_policy"`
}

// OutboundTLS is the trust store of outbound clients: system roots plus
// configured CA bundles, with no switch to disable verification.
type OutboundTLS struct {
	UseSystemRoots bool     `yaml:"use_system_roots" default:"true"`
	CAFiles        []string `yaml:"ca_files" default:"[]"`
	MinVersion     string   `yaml:"min_version" default:"1.2" validate:"oneof=1.2|1.3"`
}

// OutboundAddressPolicy is the egress address policy, checked at connect time.
// Independent of the lists, internal/outbound refuses zoned IPv6 addresses,
// checks IPv4-mapped addresses as IPv4 and checks addresses of the NAT64 prefix
// 64:ff9b::/96 as the IPv4 address they translate to as well. The default deny
// list holds loopback, unspecified, link-local (with 169.254.169.254), the IPv6
// metadata address of one cloud, the metadata addresses of two others
// (100.100.100.200, 168.63.129.16), the deprecated IPv4-compatible range ::/96,
// multicast and broadcast.
type OutboundAddressPolicy struct {
	DenyCIDRs  []string `yaml:"deny_cidrs" default:"[\"127.0.0.0/8\",\"::1/128\",\"0.0.0.0/8\",\"::/128\",\"::/96\",\"169.254.0.0/16\",\"fe80::/10\",\"fd00:ec2::254/128\",\"100.100.100.200/32\",\"168.63.129.16/32\",\"224.0.0.0/4\",\"ff00::/8\",\"255.255.255.255/32\"]"`
	AllowCIDRs []string `yaml:"allow_cidrs" default:"[]"`
}

// Validate checks that every CIDR parses.
func (o *Outbound) Validate(r *Report) {
	for _, list := range []struct {
		key   string
		cidrs []string
	}{{"address_policy.deny_cidrs", o.AddressPolicy.DenyCIDRs}, {"address_policy.allow_cidrs", o.AddressPolicy.AllowCIDRs}} {
		for _, c := range list.cidrs {
			if !validCIDR(c) {
				r.Add(list.key, "%q is not a CIDR", c)
			}
		}
	}
}

func validCIDR(c string) bool {
	_, _, err := net.ParseCIDR(c)
	return err == nil
}
