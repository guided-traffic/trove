package httpserver

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/config"
)

// Endpoint is the immutable snapshot of one client endpoint (R-EP-002): its
// name, hosts, surface, credential rules, exposure mode, cache-fill policy and
// rate-limit class. Only NewEndpoint builds one, from the validated
// configuration, and only the chain of the endpoint's listener places it in a
// request context (EndpointFrom). Nothing derives it from request data.
//
// The credential rules, the exposure mode, the cache-fill policy and the
// rate-limit class are carried for the later stages that enforce them
// (R-EP-010, R-EP-012, R-EP-015, R-EP-019); the MVP chain enforces the hosts,
// the surface, the in-flight limit and the replica header.
type Endpoint struct {
	name    string
	def     bool
	listen  string
	baseURL string // normalised origin, "" for a default endpoint without one

	hosts    []string
	hostSet  map[string]struct{}
	wildcard string
	check    bool
	// additional maps the host of a server.additional_base_urls entry to its
	// origin (default endpoint only).
	additional map[string]string

	hostRoots    map[string]string
	wildcardRoot string

	surface         []string
	tokenTypes      []string
	boundTokensOnly bool
	anonymous       bool
	exposure        string
	cacheFill       bool
	rateClass       string
	replicaHeader   string
	maxInFlight     int
	pool            string
	trustedProxies  []string
}

// NewEndpoint builds the snapshot of a resolved endpoint. additionalBaseURLs are
// server.additional_base_urls; they matter to the default endpoint only.
func NewEndpoint(r config.ResolvedEndpoint, additionalBaseURLs []string) (*Endpoint, error) {
	e := &Endpoint{
		name: r.Name, def: r.Default, listen: r.Listen, hosts: slices.Clone(r.Hosts), hostSet: map[string]struct{}{},
		wildcard: strings.ToLower(r.WildcardHost), check: r.CheckHost, additional: map[string]string{},
		hostRoots: map[string]string{}, wildcardRoot: r.WildcardRoot,
		surface: slices.Clone(r.Surface), tokenTypes: slices.Clone(r.TokenTypes), boundTokensOnly: r.BoundTokensOnly,
		anonymous: r.Anonymous, exposure: r.Exposure, cacheFill: r.CacheFill, rateClass: r.RateLimitClass,
		replicaHeader: r.ReplicaHeader, maxInFlight: r.MaxInFlight, pool: r.Pool, trustedProxies: slices.Clone(r.TrustedProxies),
	}
	if r.BaseURL != "" {
		o, err := normaliseOrigin(r.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("endpoint %s: base_url: %w", r.Name, err)
		}
		e.baseURL = o
	}
	for _, h := range r.Hosts {
		e.hostSet[strings.ToLower(h)] = struct{}{}
	}
	if r.Default {
		for _, s := range additionalBaseURLs {
			o, err := normaliseOrigin(s)
			if err != nil {
				return nil, err
			}
			if u, err := url.Parse(o); err == nil {
				e.additional[strings.ToLower(u.Hostname())] = o
			}
		}
	}
	for h, repo := range r.HostRoots {
		e.hostRoots[strings.ToLower(h)] = repo
	}
	return e, nil
}

// Name is the endpoint's name ("default" for the default endpoint).
func (e *Endpoint) Name() string {
	if e == nil {
		return ""
	}
	return e.name
}

// IsDefault reports whether this is the default endpoint.
func (e *Endpoint) IsDefault() bool { return e != nil && e.def }

// Listen is the configured listen address.
func (e *Endpoint) Listen() string { return e.listen }

// BaseURL is the endpoint's base URL (an origin without trailing slash).
func (e *Endpoint) BaseURL() string { return e.baseURL }

// Hosts returns the exact hosts of the host set.
func (e *Endpoint) Hosts() []string { return slices.Clone(e.hosts) }

// WildcardHost returns the wildcard host, "" when none.
func (e *Endpoint) WildcardHost() string { return e.wildcard }

// ChecksHost reports whether the endpoint answers 421 for a host outside its set.
func (e *Endpoint) ChecksHost() bool { return e != nil && e.check }

// Surface returns the route families the endpoint serves.
func (e *Endpoint) Surface() []string { return slices.Clone(e.surface) }

// Serves reports whether the surface holds the family.
func (e *Endpoint) Serves(family string) bool { return e != nil && slices.Contains(e.surface, family) }

// TokenTypes returns the token types the endpoint accepts (R-EP-010).
func (e *Endpoint) TokenTypes() []string { return slices.Clone(e.tokenTypes) }

// BoundTokensOnly reports whether the endpoint refuses unbound tokens (R-EP-010).
func (e *Endpoint) BoundTokensOnly() bool { return e.boundTokensOnly }

// AnonymousAllowed reports whether a request may be anonymous on this endpoint (R-EP-010).
func (e *Endpoint) AnonymousAllowed() bool { return e.anonymous }

// ExposureMode is "all" or "rules" (R-EP-012).
func (e *Endpoint) ExposureMode() string { return e.exposure }

// CacheFill reports whether the endpoint may make trove contact an upstream (R-EP-019).
func (e *Endpoint) CacheFill() bool { return e.cacheFill }

// RateLimitClass names the endpoint's rate-limit class, "" for none (R-EP-015).
func (e *Endpoint) RateLimitClass() string { return e.rateClass }

// ReplicaHeader is the endpoint's replica_header mode (R-OPS-002).
func (e *Endpoint) ReplicaHeader() string { return e.replicaHeader }

// MaxInFlight is the in-flight limit of this endpoint on one replica (R-EP-002).
func (e *Endpoint) MaxInFlight() int { return e.maxInFlight }

// Pool is the pool name that serves the endpoint, "" for the main web Deployment.
func (e *Endpoint) Pool() string { return e.pool }

// TrustedProxies returns the addresses or CIDRs whose forwarded host counts.
func (e *Endpoint) TrustedProxies() []string { return slices.Clone(e.trustedProxies) }

// matchHost reports whether host (lower case, no port) lies in the host set and
// returns the label a wildcard host matched.
func (e *Endpoint) matchHost(host string) (label string, ok bool) {
	if _, ok := e.hostSet[host]; ok {
		return "", true
	}
	if e.wildcard != "" {
		if l, ok := config.WildcardLabel(e.wildcard, host); ok {
			return l, true
		}
	}
	return "", false
}

// inSet reports whether host lies in the host set.
func (e *Endpoint) inSet(host string) bool {
	_, ok := e.matchHost(host)
	return ok
}

// baseURLFor returns the response base URL for an effective host in the set
// (R-EP-003): the endpoint's own base URL for its host, an additional base URL
// for its host, otherwise the base URL's scheme and port on the host.
func (e *Endpoint) baseURLFor(host string) string {
	if e.baseURL != "" {
		u, err := url.Parse(e.baseURL)
		if err == nil {
			if strings.EqualFold(u.Hostname(), host) {
				return e.baseURL
			}
			if o, ok := e.additional[host]; ok {
				return o
			}
			h := host
			if strings.Contains(h, ":") {
				h = "[" + h + "]"
			}
			if p := u.Port(); p != "" {
				h = net.JoinHostPort(host, p)
			}
			return originOf(u.Scheme, h)
		}
	}
	if o, ok := e.additional[host]; ok {
		return o
	}
	return ""
}

// rootFor returns the repository a host's root is bound to (R-EP-006): an exact
// binding of host_roots, else the host label under wildcard_root: label.
func (e *Endpoint) rootFor(host, label string) string {
	if repo, ok := e.hostRoots[host]; ok {
		return repo
	}
	if e.wildcardRoot == config.WildcardRootLabel && label != "" {
		return label
	}
	return ""
}

// HostRoot returns the repository key the host is bound to, "" when unbound. It
// is the lookup that middleware step 5 stores in the request information
// (R-EP-006); a key that names no repository behaves as no binding, which the
// consumer decides per request.
func (e *Endpoint) HostRoot(host string) string {
	if e == nil {
		return ""
	}
	host = strings.ToLower(host)
	label, _ := e.matchHost(host)
	return e.rootFor(host, label)
}

type ctxEndpoint struct{}

// withEndpoint places the endpoint in ctx. It is unexported: only the chain of the endpoint's
// listener calls it (R-EP-002), and the archtest check allows only internal/app
// to build the endpoint and the chain.
func withEndpoint(ctx context.Context, e *Endpoint) context.Context {
	return context.WithValue(ctx, ctxEndpoint{}, e)
}

// EndpointFrom returns the endpoint whose listener accepted the request; nil
// outside a chain that is bound to an endpoint. The methods of a nil *Endpoint
// that report policy are not safe to call: check for nil first.
func EndpointFrom(ctx context.Context) *Endpoint {
	e, _ := ctx.Value(ctxEndpoint{}).(*Endpoint)
	return e
}

// RootRepository returns the repository the effective host's root is bound to,
// "" when the host is unbound (R-EP-006).
func RootRepository(ctx context.Context) string { return InfoFrom(ctx).RootRepository }

// hostOnly lower-cases a Host or forwarded host value and removes its port, the
// brackets of an IPv6 literal and a trailing dot. It returns "" for a value no
// DNS name can match.
func hostOnly(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	// Only ASCII text can be a host name here: a non-ASCII byte (a Unicode letter
	// whose lower case is ASCII, an IDN that arrived undecoded) or a control byte
	// is no host (R-EP-003). HTTP/2 does not validate :authority, so this is the
	// only gate on that path.
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x20 || c >= 0x7f {
			return ""
		}
	}
	if h, _, err := net.SplitHostPort(v); err == nil {
		v = h
	}
	v = strings.Trim(v, "[]")
	v = strings.TrimSuffix(strings.ToLower(v), ".")
	if strings.ContainsAny(v, " /\\@?#,") {
		return ""
	}
	return v
}
