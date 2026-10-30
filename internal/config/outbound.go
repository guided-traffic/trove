package config

import (
	"net"
	"strings"
	"time"
)

// Proxy holds the remote-fetch settings: the outbound client factory
// (proxy.outbound, R-ARCH-022) and the keys of the pull-through engine of M2
// (R-REPO-020 to R-REPO-042). The per-repository settings of a remote repository
// (timeouts, cache periods, limits) are in the repository document; the values
// here are the system-wide defaults and bounds.
type Proxy struct {
	Outbound Outbound `yaml:"outbound"`

	// >>> M2 proxy

	// LockPollIntervalMs and the other integer keys keep the unit in their name
	// because the specification and its acceptance criteria name them so
	// (Q-REPO-85); LockPollInterval and the other methods return them as durations.
	LockPollIntervalMs       int         `yaml:"lock_poll_interval_ms" default:"1000" validate:"min=50,max=60000" doc:"How long a waiter for a flight lock waits for the outcome message before it checks the lock key again (R-REPO-026 step 4)."`
	FetchWaitTimeoutSeconds  int         `yaml:"fetch_wait_timeout_seconds" default:"120" validate:"min=1,max=3600" doc:"How long a request waits for another replica's fetch of the same path; after it the request fetches without the cross-replica lock (R-REPO-026)."`
	NegativeL1Entries        int         `yaml:"negative_l1_entries" default:"100000" validate:"min=1,max=10000000" doc:"Per replica: entries of the in-process copy of the negative cache of remote repositories (R-REPO-028); about 100 bytes each."`
	MaxRedirects             int         `yaml:"max_redirects" default:"5" validate:"min=0,max=20" doc:"Redirect hops followed per upstream request; one more answers 502 too_many_redirects (R-REPO-039)."`
	MaxArtifactBytes         ByteSize    `yaml:"max_artifact_bytes" default:"10GiB" validate:"min=1KiB" doc:"System value of limits.maxArtifactBytes: a larger upstream artifact is refused with 502 upstream_item_too_large (R-REPO-040)."`
	MaxMetadataBytes         ByteSize    `yaml:"max_metadata_bytes" default:"256MiB" validate:"min=1KiB" doc:"System value of limits.maxMetadataBytes (R-REPO-040)."`
	MaxConcurrentFetches     int         `yaml:"max_concurrent_fetches" default:"256" validate:"min=1,max=100000" doc:"Per replica: upstream fetches in flight over all remote repositories (R-REPO-040)."`
	QueueTimeoutSeconds      int         `yaml:"queue_timeout_seconds" default:"10" validate:"min=0,max=600" doc:"How long a fetch waits for a free slot before it answers 503 with Retry-After: 5; 0 refuses at once (R-REPO-040)."`
	GlobalOffline            bool        `yaml:"global_offline" default:"false" doc:"No remote repository contacts its upstream; the runtime switch cannot lift it (R-REPO-031)."`
	BlockedContentTypes      []string    `yaml:"blocked_content_types" default:"[\"text/html\",\"application/xhtml+xml\"]" doc:"Media types an upstream answer may not have where integrity.blockUnexpectedContentTypes is true (R-REPO-042); lower-case type/subtype without parameters."`
	MergedMetadataCacheBytes ByteSize    `yaml:"merged_metadata_cache_bytes" default:"256MiB" validate:"min=0" doc:"Per replica: merged package documents of virtual repositories (R-REPO-020); 0 turns the cache off."`
	Claims                   ProxyClaims `yaml:"claims"`

	// <<< M2 proxy
}

// ProxyClaims holds the system-wide switch of the namespace-claim rule for remote
// repositories (R-REPO-023, Q-REPO-4).
type ProxyClaims struct {
	RemoteRefusal bool `yaml:"remote_refusal" default:"true" doc:"A remote repository refuses a name that a global claim covers, also when addressed directly."`
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

// LockPollInterval is proxy.lock_poll_interval_ms as a duration.
func (p *Proxy) LockPollInterval() time.Duration {
	return time.Duration(p.LockPollIntervalMs) * time.Millisecond
}

// FetchWaitTimeout is proxy.fetch_wait_timeout_seconds as a duration.
func (p *Proxy) FetchWaitTimeout() time.Duration {
	return time.Duration(p.FetchWaitTimeoutSeconds) * time.Second
}

// QueueTimeout is proxy.queue_timeout_seconds as a duration.
func (p *Proxy) QueueTimeout() time.Duration {
	return time.Duration(p.QueueTimeoutSeconds) * time.Second
}

// Validate checks the rules across the keys of the section: a waiter polls more
// often than its wait ends, and a blocked content type is a bare media type.
func (p *Proxy) Validate(r *Report) {
	if p.LockPollInterval() >= p.FetchWaitTimeout() {
		r.Add("lock_poll_interval_ms", "(%s) must be below fetch_wait_timeout_seconds (%s): a waiter polls the lock at least once before its wait ends",
			p.LockPollInterval(), p.FetchWaitTimeout())
	}
	for _, t := range p.BlockedContentTypes {
		if !validMediaType(t) {
			r.Add("blocked_content_types", "%q must be a lower-case media type such as text/html, without parameters", t)
		}
	}
}

// validMediaType reports whether s is type/subtype in lower case, the form the
// engine compares a response's parsed media type with.
func validMediaType(s string) bool {
	typ, sub, ok := strings.Cut(s, "/")
	if !ok || typ == "" || sub == "" || strings.Contains(sub, "/") {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '/', c == '.', c == '+', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// Validate checks that every CIDR parses and that the trust store is not empty.
func (o *Outbound) Validate(r *Report) {
	if !o.TLS.UseSystemRoots && len(o.TLS.CAFiles) == 0 {
		r.Add("tls.use_system_roots", "is false and tls.ca_files is empty: no upstream certificate could verify")
	}
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
