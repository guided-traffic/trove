package config

import (
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Endpoint names and limits (R-EP-001).
const (
	// DefaultEndpoint names the endpoint of server.listen.
	DefaultEndpoint = "default"
	// MaxFurtherEndpoints bounds the endpoint label of the metrics to 17 values.
	MaxFurtherEndpoints = 16
	// DefaultFurtherMaxInFlight is the in-flight limit of a further endpoint.
	DefaultFurtherMaxInFlight = 1024
	// DefaultFurtherRateLimitClass is the class a further endpoint uses unless it
	// names another one (R-EP-015).
	DefaultFurtherRateLimitClass = "external"
)

// The route families of a surface (R-EP-005).
const (
	SurfaceUI         = "ui"
	SurfaceAPI        = "api"
	SurfaceCIExchange = "ci_exchange"
	SurfacePackages   = "packages"
	SurfaceFiles      = "files"
)

// Exposure modes (R-EP-012), token types (R-AUTH-022) and wildcard-root modes
// (R-EP-006) an endpoint names.
const (
	ExposureAll   = "all"
	ExposureRules = "rules"

	WildcardRootNone  = "none"
	WildcardRootLabel = "label"
)

var (
	endpointNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)
	repoKeyRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)
	dnsLabelRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Surfaces returns the route families in their fixed order.
func Surfaces() []string {
	return []string{SurfaceUI, SurfaceAPI, SurfaceCIExchange, SurfacePackages, SurfaceFiles}
}

// ValidEndpointName reports whether s is an endpoint name or pool name
// (R-EP-001).
func ValidEndpointName(s string) bool { return endpointNameRe.MatchString(s) }

// Endpoint is one entry of server.endpoints (R-EP-001). A key that is unset
// takes the default of the endpoint's kind, which differs between the default
// endpoint and a further one (Q-EP-5, Q-EP-10); ResolveEndpoints applies them.
// Pointers mark the keys whose zero value is a legitimate setting.
type Endpoint struct {
	Listen         string            `yaml:"listen" json:"listen,omitempty"`
	BaseURL        string            `yaml:"base_url" json:"base_url,omitempty"`
	Hosts          []string          `yaml:"hosts" json:"hosts,omitempty"`
	WildcardHost   string            `yaml:"wildcard_host" json:"wildcard_host,omitempty"`
	HostRoots      map[string]string `yaml:"host_roots" json:"host_roots,omitempty"`
	WildcardRoot   string            `yaml:"wildcard_root" json:"wildcard_root,omitempty"`
	TrustedProxies []string          `yaml:"trusted_proxies" json:"trusted_proxies,omitempty"`
	Pool           string            `yaml:"pool" json:"pool,omitempty"`
	MaxInFlight    int               `yaml:"max_in_flight" json:"max_in_flight,omitempty"`
	TLS            EndpointTLS       `yaml:"tls" json:"tls"`
	Surface        []string          `yaml:"surface" json:"surface,omitempty"`
	Auth           EndpointAuth      `yaml:"auth" json:"auth"`
	Exposure       string            `yaml:"exposure" json:"exposure,omitempty"`
	CacheFill      *bool             `yaml:"cache_fill" json:"cache_fill,omitempty"`
	RateLimitClass *string           `yaml:"rate_limit_class" json:"rate_limit_class,omitempty"`
	ReplicaHeader  string            `yaml:"replica_header" json:"replica_header,omitempty"`
}

// EndpointTLS is the TLS block of a further endpoint. The minimum version and
// the reload interval are server.tls's; client certificates are required when a
// client_ca_file is set and never inherited (R-EP-004).
type EndpointTLS struct {
	Certificates []TLSCertificate `yaml:"certificates" json:"certificates,omitempty"`
	ClientCAFile string           `yaml:"client_ca_file" json:"client_ca_file,omitempty"`
}

// EndpointAuth is the credential policy of an endpoint (R-EP-010). The MVP
// carries it in the endpoint snapshot; the authentication stage that enforces it
// arrives with the identity milestone.
type EndpointAuth struct {
	TokenTypes      []string `yaml:"token_types" json:"token_types,omitempty"`
	BoundTokensOnly *bool    `yaml:"bound_tokens_only" json:"bound_tokens_only,omitempty"`
	Anonymous       *bool    `yaml:"anonymous" json:"anonymous,omitempty"`
}

// EndpointRateLimit is a rate-limit class (R-EP-015).
type EndpointRateLimit struct {
	RequestsPerSecond float64 `yaml:"requests_per_second" json:"requests_per_second"`
	Burst             int     `yaml:"burst" json:"burst"`
	Key               string  `yaml:"key" json:"key"`
}

// ResolvedEndpoint is an endpoint with every default applied.
type ResolvedEndpoint struct {
	Name string
	// Default is the endpoint of server.listen.
	Default bool
	Listen  string
	BaseURL string
	// Hosts are the exact host names of the host set (lower case, no port, no
	// duplicates): the host of the base URL, the hosts and, for the default
	// endpoint, the hosts of server.additional_base_urls.
	Hosts        []string
	WildcardHost string
	// CheckHost is true for a further endpoint and for the default endpoint when
	// hosts or wildcard_host is configured (R-EP-003).
	CheckHost      bool
	HostRoots      map[string]string
	WildcardRoot   string
	TrustedProxies []string
	Pool           string
	MaxInFlight    int
	// TLS is the effective block of the listener.
	TLS             ServerTLS
	Surface         []string
	TokenTypes      []string
	BoundTokensOnly bool
	Anonymous       bool
	Exposure        string
	CacheFill       bool
	RateLimitClass  string
	ReplicaHeader   string
}

// Serves reports whether the surface holds the family.
func (e ResolvedEndpoint) Serves(family string) bool { return slices.Contains(e.Surface, family) }

func boolp(b bool) *bool          { return &b }
func strp(s string) *string       { return &s }
func defaultTokenTypes() []string { return []string{"pat", "sat", "cit"} }

// ResolveEndpoints returns the default endpoint first and the further endpoints
// by name, each with every default applied. It assumes a validated section.
func (s *Server) ResolveEndpoints() []ResolvedEndpoint {
	out := []ResolvedEndpoint{s.resolveEndpoint(DefaultEndpoint)}
	for _, n := range s.furtherNames() {
		out = append(out, s.resolveEndpoint(n))
	}
	return out
}

func (s *Server) furtherNames() []string {
	var names []string
	for n := range s.Endpoints {
		if n != DefaultEndpoint {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func (s *Server) resolveEndpoint(name string) ResolvedEndpoint {
	ep := s.Endpoints[name]
	def := name == DefaultEndpoint
	r := ResolvedEndpoint{
		Name: name, Default: def, HostRoots: ep.HostRoots, WildcardHost: strings.ToLower(ep.WildcardHost),
		WildcardRoot: orStr(ep.WildcardRoot, WildcardRootNone),
	}
	if r.HostRoots == nil {
		r.HostRoots = map[string]string{}
	}
	hosts := hostNames(ep.Hosts)
	if def {
		r.Listen, r.BaseURL, r.MaxInFlight = s.Listen, s.BaseURL, s.MaxInFlight
		r.TrustedProxies, r.TLS = s.TrustedProxies, s.TLS
		r.Surface = orList(ep.Surface, Surfaces())
		r.TokenTypes = orList(ep.Auth.TokenTypes, defaultTokenTypes())
		r.BoundTokensOnly, r.Anonymous = orBool(ep.Auth.BoundTokensOnly, false), orBool(ep.Auth.Anonymous, true)
		r.Exposure = orStr(ep.Exposure, ExposureAll)
		r.CacheFill = orBool(ep.CacheFill, true)
		r.RateLimitClass = orStrp(ep.RateLimitClass, "")
		r.ReplicaHeader = orStr(ep.ReplicaHeader, s.ReplicaHeader)
		r.CheckHost = len(hosts) > 0 || r.WildcardHost != ""
		for _, u := range s.AdditionalBaseURLs {
			hosts = append(hosts, originHost(u))
		}
	} else {
		r.Listen, r.BaseURL = ep.Listen, ep.BaseURL
		r.Pool = ep.Pool
		r.MaxInFlight = orInt(ep.MaxInFlight, DefaultFurtherMaxInFlight)
		r.TrustedProxies = ep.TrustedProxies
		if len(r.TrustedProxies) == 0 {
			r.TrustedProxies = s.TrustedProxies
		}
		r.TLS = s.TLS
		r.TLS.ClientCAFile, r.TLS.ClientAuth = "", ClientAuthNone
		if len(ep.TLS.Certificates) > 0 {
			r.TLS.Certificates = ep.TLS.Certificates
		}
		if ep.TLS.ClientCAFile != "" {
			r.TLS.ClientCAFile, r.TLS.ClientAuth = ep.TLS.ClientCAFile, ClientAuthRequire
		}
		r.Surface = orList(ep.Surface, []string{SurfacePackages})
		r.TokenTypes = orList(ep.Auth.TokenTypes, []string{"sat"})
		r.BoundTokensOnly, r.Anonymous = orBool(ep.Auth.BoundTokensOnly, true), orBool(ep.Auth.Anonymous, false)
		r.Exposure = orStr(ep.Exposure, ExposureRules)
		r.CacheFill = orBool(ep.CacheFill, false)
		r.RateLimitClass = orStrp(ep.RateLimitClass, DefaultFurtherRateLimitClass)
		r.ReplicaHeader = orStr(ep.ReplicaHeader, ReplicaHeaderNever)
		r.CheckHost = true
	}
	if h := originHost(r.BaseURL); h != "" {
		hosts = append([]string{h}, hosts...)
	}
	r.Hosts = dedupe(hosts)
	return r
}

// ReplicaHeaderNever is the value of replica_header that sends no header.
const ReplicaHeaderNever = "never"

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func orStrp(p *string, d string) string {
	if p == nil {
		return d
	}
	return *p
}

func orBool(p *bool, d bool) bool {
	if p == nil {
		return d
	}
	return *p
}

func orInt(n, d int) int {
	if n == 0 {
		return d
	}
	return n
}

func orList(l, d []string) []string {
	if l == nil {
		return slices.Clone(d)
	}
	return l
}

func hostNames(in []string) []string {
	out := make([]string, 0, len(in))
	for _, h := range in {
		out = append(out, strings.ToLower(strings.TrimSpace(h)))
	}
	return out
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, h := range in {
		if h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// originHost returns the lower-cased host of an origin without its port; empty
// when raw is not a URL.
func originHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// WildcardLabel reports whether host lies under the wildcard host "*.<domain>"
// and returns the matched left-most label. It matches exactly one label, as a
// wildcard certificate does (RFC 6125 section 6.4.3, R-EP-003).
func WildcardLabel(wildcard, host string) (string, bool) {
	domain, ok := strings.CutPrefix(wildcard, "*.")
	if !ok || domain == "" {
		return "", false
	}
	label, ok := strings.CutSuffix(host, "."+domain)
	if !ok || label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}

// validHostName reports whether h is a DNS name: labels of letters, digits and
// hyphens, no port, and not an IP literal.
func validHostName(h string) bool {
	if h == "" || len(h) > 253 || net.ParseIP(h) != nil {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if !dnsLabelRe.MatchString(l) {
			return false
		}
	}
	return true
}

// validWildcardHost reports whether w is "*.<domain>" with at least two labels
// in the domain (Q-EP-6).
func validWildcardHost(w string) bool {
	d, ok := strings.CutPrefix(w, "*.")
	return ok && strings.Count(d, ".") >= 1 && validHostName(d)
}

// defaultOwnedKeys maps a key the default endpoint may not set to the server key
// that owns the value (R-EP-001).
func defaultOwnedKeys(ep Endpoint) []struct{ key, owner string } {
	var out []struct{ key, owner string }
	add := func(set bool, key, owner string) {
		if set {
			out = append(out, struct{ key, owner string }{key, owner})
		}
	}
	add(ep.Listen != "", "listen", "server.listen")
	add(ep.BaseURL != "", "base_url", "server.base_url")
	add(ep.MaxInFlight != 0, "max_in_flight", "server.max_in_flight")
	add(len(ep.TLS.Certificates) > 0 || ep.TLS.ClientCAFile != "", "tls", "server.tls")
	add(len(ep.TrustedProxies) > 0, "trusted_proxies", "server.trusted_proxies")
	add(ep.Pool != "", "pool", "the main web Deployment, which serves the default endpoint")
	return out
}

// validateEndpoints checks server.endpoints and server.endpoint_rate_limits
// (R-EP-001 to R-EP-006). Every violation is reported in one pass.
func (s *Server) validateEndpoints(r *Report) {
	for class, l := range s.EndpointRateLimits {
		k := "endpoint_rate_limits." + class
		switch {
		case !endpointNameRe.MatchString(class):
			r.Add(k, "class name must match %s", endpointNameRe)
		case l.RequestsPerSecond <= 0:
			r.Add(k+".requests_per_second", "must be positive")
		case l.Burst < 1:
			r.Add(k+".burst", "must be at least 1")
		case l.Key != "principal" && l.Key != "address":
			r.Add(k+".key", "%q is neither principal nor address", l.Key)
		}
	}

	var further []string
	for name := range s.Endpoints {
		if name != DefaultEndpoint {
			further = append(further, name)
		}
	}
	sort.Strings(further)
	if len(further) > MaxFurtherEndpoints {
		r.Add("endpoints", "has %d further endpoints; at most %d may be configured", len(further), MaxFurtherEndpoints)
	}
	for _, n := range further {
		if !endpointNameRe.MatchString(n) {
			r.Add("endpoints."+n, "name must match %s", endpointNameRe)
		}
	}
	if d, ok := s.Endpoints[DefaultEndpoint]; ok {
		for _, o := range defaultOwnedKeys(d) {
			r.Add("endpoints.default."+o.key, "is not allowed on the default endpoint; it is owned by %s", o.owner)
		}
	}

	// A name that fails its syntax is already reported; keep checking the rest.
	ports := map[string]string{}
	claim := func(key, addr string) {
		abs := "server." + key
		_, port, err := net.SplitHostPort(addr)
		if err != nil || port == "0" || port == "" {
			return
		}
		if prev, dup := ports[port]; dup {
			r.Add(key, "uses port %s, which %s already uses", port, prev)
			return
		}
		ports[port] = abs
	}
	for _, l := range []struct{ key, addr string }{{"listen", s.Listen}, {"ops_listen", s.OpsListen}} {
		if _, port, err := net.SplitHostPort(l.addr); err == nil && port != "0" && port != "" {
			ports[port] = "server." + l.key
		}
	}

	names := append([]string{DefaultEndpoint}, further...)
	resolved := map[string]ResolvedEndpoint{}
	for _, n := range names {
		resolved[n] = s.resolveEndpoint(n)
	}
	for _, n := range further {
		ep := s.Endpoints[n]
		base := "endpoints." + n
		if ep.Listen == "" {
			r.Add(base+".listen", "is required on a further endpoint")
		} else if _, _, err := net.SplitHostPort(ep.Listen); err != nil {
			r.Add(base+".listen", "%q is not a host:port address", ep.Listen)
		} else {
			claim(base+".listen", ep.Listen)
		}
		if ep.BaseURL == "" {
			r.Add(base+".base_url", "is required on a further endpoint")
		} else {
			checkOrigin(r, base+".base_url", ep.BaseURL)
		}
		if ep.Pool != "" && !endpointNameRe.MatchString(ep.Pool) {
			r.Add(base+".pool", "%q must match %s", ep.Pool, endpointNameRe)
		}
		if ep.MaxInFlight < 0 {
			r.Add(base+".max_in_flight", "must be at least 1")
		}
		for _, c := range ep.TrustedProxies {
			s.checkProxy(r, base+".trusted_proxies", c)
		}
	}

	for _, n := range names {
		ep := s.Endpoints[n]
		e := resolved[n]
		base := "endpoints." + n
		s.validateEndpointHosts(r, base, ep, e)
		s.validateEndpointPolicy(r, base, ep, e)
		if n != DefaultEndpoint {
			validateTLSBlock(r, base+".tls", e.TLS)
		}
	}
	s.validateHostOverlap(r, names, resolved)
}

// validateEndpointHosts checks the hosts, the wildcard host and the host-root
// bindings of one endpoint (R-EP-003, R-EP-006).
func (s *Server) validateEndpointHosts(r *Report, base string, ep Endpoint, e ResolvedEndpoint) {
	for _, h := range hostNames(ep.Hosts) {
		if !validHostName(h) {
			r.Add(base+".hosts", "%q is not a DNS name without a port", h)
		}
	}
	if ep.WildcardHost != "" && !validWildcardHost(e.WildcardHost) {
		r.Add(base+".wildcard_host", "%q must be *.<domain> with at least two labels in the domain", ep.WildcardHost)
	}
	switch e.WildcardRoot {
	case WildcardRootNone:
	case WildcardRootLabel:
		if ep.WildcardHost == "" {
			r.Add(base+".wildcard_root", "is label, which needs a wildcard_host")
		}
	default:
		r.Add(base+".wildcard_root", "%q is neither none nor label", ep.WildcardRoot)
	}
	for host, repo := range ep.HostRoots {
		k := base + ".host_roots"
		if !slices.Contains(e.Hosts, strings.ToLower(host)) {
			r.Add(k, "%q is not an exact host of endpoint %s (its hosts are %v)", host, e.Name, e.Hosts)
		}
		if !repoKeyRe.MatchString(repo) {
			r.Add(k, "host %q names %q, which is not a repository key (%s)", host, repo, repoKeyRe)
		}
	}
}

// validateEndpointPolicy checks surface, credentials, exposure, class and header.
func (s *Server) validateEndpointPolicy(r *Report, base string, ep Endpoint, e ResolvedEndpoint) {
	if ep.Surface != nil && len(ep.Surface) == 0 {
		r.Add(base+".surface", "is empty; list at least one of %s", strings.Join(Surfaces(), ", "))
	}
	for _, f := range e.Surface {
		switch {
		case !slices.Contains(Surfaces(), f):
			r.Add(base+".surface", "%q is not a route family (%s)", f, strings.Join(Surfaces(), ", "))
		case f == SurfaceUI && !e.Default:
			r.Add(base+".surface", "lists ui on a further endpoint: surface_not_supported; sign-in and the UI belong to the default endpoint (Q-EP-13)")
		}
	}
	for _, t := range e.TokenTypes {
		if t != "pat" && t != "sat" && t != "cit" {
			r.Add(base+".auth.token_types", "%q is not a token type (pat, sat, cit)", t)
		}
	}
	if e.Exposure != ExposureAll && e.Exposure != ExposureRules {
		r.Add(base+".exposure", "%q is neither all nor rules", e.Exposure)
	}
	switch e.ReplicaHeader {
	case "authenticated", "always", ReplicaHeaderNever:
	default:
		r.Add(base+".replica_header", "%q is not authenticated, always or never", e.ReplicaHeader)
	}
	if e.RateLimitClass != "" {
		if _, ok := s.EndpointRateLimits[e.RateLimitClass]; !ok {
			r.Add(base+".rate_limit_class", "%q names no entry of server.endpoint_rate_limits", e.RateLimitClass)
		}
	}
}

func (s *Server) checkProxy(r *Report, key, c string) {
	_, n, err := net.ParseCIDR(c)
	switch {
	case err != nil && net.ParseIP(c) == nil:
		r.Add(key, "%q is neither an address nor a CIDR", c)
	case err == nil:
		if ones, _ := n.Mask.Size(); ones == 0 {
			r.Add(key, "%q covers every address and would trust every peer; list the edge's own addresses", c)
		}
	}
}

// validateHostOverlap reports a host that two endpoints share, an exact host
// that another endpoint's wildcard matches, a wildcard two endpoints share, and
// an endpoint host equal to the content origin's host (R-EP-001).
func (s *Server) validateHostOverlap(r *Report, names []string, resolved map[string]ResolvedEndpoint) {
	content := ""
	if s.ContentBaseURL != "" {
		content = originHost(s.ContentBaseURL)
	}
	for i, a := range names {
		ea := resolved[a]
		for _, h := range ea.Hosts {
			if content != "" && h == content {
				r.Add("endpoints."+a+".hosts", "host %q is the host of server.content_base_url", h)
			}
		}
		for _, b := range names[i+1:] {
			eb := resolved[b]
			for _, h := range ea.Hosts {
				if slices.Contains(eb.Hosts, h) {
					r.Add("endpoints."+b+".hosts", "host %q is also a host of endpoint %s", h, a)
				}
				if _, ok := WildcardLabel(eb.WildcardHost, h); ok {
					r.Add("endpoints."+b+".wildcard_host", "%q matches host %q of endpoint %s", eb.WildcardHost, h, a)
				}
			}
			for _, h := range eb.Hosts {
				if _, ok := WildcardLabel(ea.WildcardHost, h); ok {
					r.Add("endpoints."+a+".wildcard_host", "%q matches host %q of endpoint %s", ea.WildcardHost, h, b)
				}
			}
			if ea.WildcardHost != "" && ea.WildcardHost == eb.WildcardHost {
				r.Add("endpoints."+b+".wildcard_host", "%q is also the wildcard host of endpoint %s", eb.WildcardHost, a)
			}
		}
		if ea.WildcardHost != "" && content != "" {
			if _, ok := WildcardLabel(ea.WildcardHost, content); ok {
				r.Add("endpoints."+a+".wildcard_host", "%q matches the host of server.content_base_url", ea.WildcardHost)
			}
		}
	}
}

// fillEndpointDefaults writes every default into the entries that are present,
// so the effective view shows the value each key has (R-EP-001). It is
// idempotent. The default endpoint's entry is not created when absent.
func (s *Server) fillEndpointDefaults() {
	if len(s.Endpoints) == 0 {
		return
	}
	for name, ep := range s.Endpoints {
		r := s.resolveEndpoint(name)
		if name != DefaultEndpoint {
			ep.MaxInFlight = r.MaxInFlight
			ep.TrustedProxies = r.TrustedProxies
			ep.Pool = r.Pool
			if len(ep.Hosts) == 0 {
				ep.Hosts = []string{}
			}
		}
		ep.WildcardRoot = r.WildcardRoot
		ep.Surface = r.Surface
		ep.Auth = EndpointAuth{TokenTypes: r.TokenTypes, BoundTokensOnly: boolp(r.BoundTokensOnly), Anonymous: boolp(r.Anonymous)}
		ep.Exposure = r.Exposure
		ep.CacheFill = boolp(r.CacheFill)
		ep.RateLimitClass = strp(r.RateLimitClass)
		ep.ReplicaHeader = r.ReplicaHeader
		s.Endpoints[name] = ep
	}
}
