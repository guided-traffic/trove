package repository

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/observability"
)

// Limits of the remote section (R-REPO-024).
const (
	maxUpstreamURL   = 2048
	maxExtraHosts    = 20
	maxUsername      = 256
	maxPasswordBytes = 8 << 10
	maxContentTypes  = 20
)

// Warning codes of the remote section (R-REPO-008, R-REPO-024).
const (
	WarnPlainHTTPUpstream   = "plain_http_upstream"
	WarnSourceNetworkSingle = "source_network_single_address"
)

// The field codes of the remote section (R-REPO-008) and the JSON pointer of its
// secret field, which is also the primary-key part of its repository_secrets row.
const (
	codeInvalidUpstreamURL   = "invalid_upstream_url"
	codeInvalidSourceNetwork = "invalid_source_network"
	codeInvalidSecret        = "invalid_secret"
	fieldRemotePassword      = "/remote/password"
)

var (
	hostPattern = regexp.MustCompile(`^[a-z0-9*?.\-_]{1,253}$`)
	mediaType   = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)
)

// secretOp is what a request wants done to a secret field.
type secretOp uint8

const (
	secretSeal      secretOp = iota // seal the new value
	secretReference                 // store the reference
	secretKeep                      // {"set": true}: the stored value stays
)

// pendingSecret is a secret field of a validated document that still has to be
// written. It travels beside the document (Settings.pending) and never into its
// JSON, which holds only the marker or the reference.
type pendingSecret struct {
	field string
	op    secretOp
	value observability.Secret
	ref   SecretRef
}

// validateRemote validates the `remote` section of a remote repository against the
// rules of R-REPO-008 and R-REPO-024, in the pass that validates the rest of the
// document. It replaces d.Remote with the canonical section and keeps the secret
// values apart in d.pending, so that what marshalConfig stores holds none.
func validateRemote(v *validation, info catalog.Entry, d *Document) {
	in, err := decodeRemote(d.Remote)
	if err != nil {
		v.add("/remote", codeInvalidValue, "the section cannot be read")
		return
	}
	r := &remoteResolver{v: v, info: info}
	s := r.resolve(in)
	d.pending = r.pending
	raw, err := json.Marshal(s)
	if err != nil {
		v.add("/remote", codeInvalidValue, "the section cannot be written")
		return
	}
	d.Remote = raw
}

// remoteResolver turns a request section into the effective settings.
type remoteResolver struct {
	v       *validation
	info    catalog.Entry
	pending []pendingSecret
}

func (r *remoteResolver) warn(code, pointer, detail string) {
	r.v.warnings = append(r.v.warnings, Warning{Code: code, Pointer: pointer, Detail: detail})
}

func intOr(p *int, def int) int {
	if p != nil {
		return *p
	}
	return def
}

func boolOr(p *bool, def bool) bool {
	if p != nil {
		return *p
	}
	return def
}

func stringOr(p *string, def string) string {
	if p != nil {
		return *p
	}
	return def
}

// ptrOr is the catalog default of a period: the format's value, or 0 for a format
// without one (the period does not exist for it, Facts.DefaultMetadataTTLSeconds).
func ptrOr(p *int) int {
	if p != nil {
		return *p
	}
	return 0
}

func (r *remoteResolver) rng(pointer string, n, lo, hi int) {
	if n < lo || n > hi {
		r.v.add(pointer, codeOutOfRange, fmt.Sprintf("must be between %d and %d", lo, hi))
	}
}

func (r *remoteResolver) oneOf(pointer, got string, allowed ...string) {
	for _, a := range allowed {
		if got == a {
			return
		}
	}
	r.v.add(pointer, codeInvalidValue, "must be one of "+strings.Join(allowed, ", "))
}

func (r *remoteResolver) notApplicable(pointer, why string) {
	r.v.add(pointer, errs.CodeNotApplicable, why)
}

func (r *remoteResolver) resolve(in remoteInput) RemoteSettings {
	s := RemoteSettings{
		AuthMode:              stringOr(in.AuthMode, AuthNone),
		Username:              stringOr(in.Username, ""),
		ClientCertificate:     in.ClientCertificate,
		Headers:               in.Headers,
		ForwardAuthOnRedirect: boolOr(in.ForwardAuthOnRedirect, false),
		OutboundProxy:         OutboundProxy{Mode: ProxyDefault},
		ConnectTimeoutMs:      intOr(in.ConnectTimeoutMs, 10000),
		ReadTimeoutMs:         intOr(in.ReadTimeoutMs, 15000),
		CacheFillAction:       stringOr(in.CacheFillAction, FillRead),
	}
	if s.Headers == nil {
		s.Headers = []Header{}
	}
	r.url(&s, in)
	r.credentials(&s, in)
	if s.ClientCertificate != nil {
		r.notApplicable("/remote/clientCertificate", "client certificates for upstreams are not available yet (R-REPO-034)")
	}
	if len(s.Headers) > 0 {
		r.notApplicable("/remote/headers", "custom upstream request headers are not available yet (R-REPO-036)")
	}
	r.proxy(&s, in)
	r.rng("/remote/connectTimeoutMs", s.ConnectTimeoutMs, 1000, 120000)
	r.rng("/remote/readTimeoutMs", s.ReadTimeoutMs, 1000, 600000)
	r.sourceNetwork(&s, in)
	r.oneOf("/remote/cacheFillAction", s.CacheFillAction, FillRead, FillDeploy)
	r.cache(&s, in)
	r.integrity(&s, in)
	r.availability(&s, in)
	r.limits(&s, in)
	return s
}

// url resolves url and extraHosts. An omitted url takes the format's well-known
// upstream; an omitted extraHosts takes the format's proposal only when the
// upstream is that well-known one (Q-REPO-91).
func (r *remoteResolver) url(s *RemoteSettings, in remoteInput) {
	raw := stringOr(in.URL, r.info.DefaultUpstreamURL)
	known, _ := normalizeUpstreamURL(r.info.DefaultUpstreamURL)
	if raw == "" {
		r.v.add("/remote/url", codeInvalidUpstreamURL, "is required: the "+string(r.info.ID)+" format has no default upstream")
	} else if norm, why := normalizeUpstreamURL(raw); why != "" {
		r.v.add("/remote/url", codeInvalidUpstreamURL, why)
	} else {
		s.URL = norm
		if strings.HasPrefix(norm, "http://") {
			r.warn(WarnPlainHTTPUpstream, "/remote/url", "the upstream is reached over plain http: credentials and content cross the network unprotected")
		}
	}
	hosts := in.ExtraHosts
	if hosts == nil {
		hosts = []ExtraHost{}
		if s.URL != "" && s.URL == known {
			for _, h := range r.info.DefaultExtraHosts {
				hosts = append(hosts, ExtraHost{Pattern: h})
			}
		}
	}
	if len(hosts) > maxExtraHosts {
		r.v.add("/remote/extraHosts", codeOutOfRange, "at most "+strconv.Itoa(maxExtraHosts)+" entries")
	}
	for i, h := range hosts {
		hosts[i].Pattern = strings.ToLower(strings.TrimSpace(h.Pattern))
		if why := checkHostPattern(hosts[i].Pattern); why != "" {
			r.v.add("/remote/extraHosts/"+strconv.Itoa(i)+"/pattern", codeInvalidPattern, why)
		}
	}
	s.ExtraHosts = hosts
}

// checkHostPattern validates a host pattern of extraHosts: a lower-case host name
// with the wildcards * and ?, no port, no path.
func checkHostPattern(p string) string {
	if !hostPattern.MatchString(p) {
		return "must be a host name pattern in lower case: letters, digits, '.', '-', '_', '*' and '?', without port or path"
	}
	if _, err := path.Match(p, ""); err != nil {
		return "is not a valid pattern: " + err.Error()
	}
	return ""
}

// normalizeUpstreamURL checks the upstream URL and returns it normalized: scheme
// and host in lower case, the default port removed, an empty path "/" (R-REPO-024).
// The reason is empty when the URL is valid.
func normalizeUpstreamURL(raw string) (norm, why string) {
	if raw == "" {
		return "", "is empty"
	}
	if len(raw) > maxUpstreamURL {
		return "", "at most " + strconv.Itoa(maxUpstreamURL) + " characters"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "is not a valid URL"
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch {
	case u.Scheme != "http" && u.Scheme != "https", u.Hostname() == "":
		return "", "must be an absolute http or https URL with a host"
	case u.User != nil:
		return "", "must not carry user information: credentials belong in authMode, username and password"
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return "", "must not carry a fragment"
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" || (port == "80" && u.Scheme == "http") || (port == "443" && u.Scheme == "https") {
		u.Host = host
	} else {
		u.Host = host + ":" + port
	}
	if u.Path == "" && u.RawPath == "" {
		u.Path = "/"
	}
	return u.String(), ""
}

// credentials resolves authMode, username and password.
func (r *remoteResolver) credentials(s *RemoteSettings, in remoteInput) {
	r.oneOf("/remote/authMode", s.AuthMode, AuthNone, AuthBasic, AuthBearer)
	if utf8.RuneCountInString(s.Username) > maxUsername {
		r.v.add("/remote/username", codeOutOfRange, "at most "+strconv.Itoa(maxUsername)+" characters")
	}
	if strings.ContainsAny(s.Username, ":\r\n\x00") {
		r.v.add("/remote/username", codeInvalidValue, "must not contain ':' or control characters (RFC 7617)")
	}
	f := in.Password
	if f == nil {
		switch {
		case s.AuthMode == AuthBearer:
			r.v.add(fieldRemotePassword, codeInvalidSecret, "authMode bearer needs the token as the password")
		case s.AuthMode == AuthBasic && s.Username == "":
			r.v.add(fieldRemotePassword, codeInvalidSecret, "authMode basic needs a username or a password")
		}
		return
	}
	switch {
	case f.badSet:
		r.v.add(fieldRemotePassword, codeInvalidSecret, `"set" is the marker of a stored value and is only ever true`)
	case f.Value != nil && f.Ref != nil:
		r.v.add(fieldRemotePassword, codeInvalidSecret, "give either value or secretRef, not both")
	case f.Value != nil:
		r.sealValue(s, *f.Value)
	case f.Ref != nil:
		if why := f.Ref.Valid(); why != "" {
			r.v.add(fieldRemotePassword+"/secretRef", codeInvalidSecret, why)
			break
		}
		ref := *f.Ref
		s.Password = &SecretField{Ref: &ref}
		r.pending = append(r.pending, pendingSecret{field: fieldRemotePassword, op: secretReference, ref: ref})
	case f.Set:
		s.Password = &SecretField{Set: true}
		r.pending = append(r.pending, pendingSecret{field: fieldRemotePassword, op: secretKeep})
	default:
		r.v.add(fieldRemotePassword, codeInvalidSecret, `must hold "value" or "secretRef"`)
	}
}

func (r *remoteResolver) sealValue(s *RemoteSettings, v observability.Secret) {
	val := v.Reveal()
	switch {
	case val == "":
		r.v.add(fieldRemotePassword+"/value", codeInvalidSecret, "must not be empty")
	case len(val) > maxPasswordBytes:
		r.v.add(fieldRemotePassword+"/value", codeOutOfRange, "at most 8 KiB")
	case strings.ContainsAny(val, "\r\n\x00"):
		r.v.add(fieldRemotePassword+"/value", codeInvalidSecret, "must not contain line breaks or NUL: it travels in a request header")
	default:
		s.Password = &SecretField{Set: true}
		r.pending = append(r.pending, pendingSecret{field: fieldRemotePassword, op: secretSeal, value: v})
	}
}

// proxy resolves outboundProxy: default and direct are built, named proxies are
// R-REPO-037 (P1).
func (r *remoteResolver) proxy(s *RemoteSettings, in remoteInput) {
	p := in.OutboundProxy
	if p == nil {
		return
	}
	s.OutboundProxy.Mode = stringOr(p.Mode, ProxyDefault)
	r.oneOf("/remote/outboundProxy/mode", s.OutboundProxy.Mode, ProxyDefault, ProxyDirect, ProxyNamed)
	switch {
	case s.OutboundProxy.Mode == ProxyNamed:
		r.notApplicable("/remote/outboundProxy", "named outbound proxies are not available yet (R-REPO-037)")
	case p.Name != nil:
		r.v.add("/remote/outboundProxy/name", codeInvalidValue, "only the mode named takes a name")
	}
}

// sourceNetwork resolves the prefix a replica takes its local address from
// (R-REPO-024, Q-REPO-9). A single-address prefix is accepted with a warning.
func (r *remoteResolver) sourceNetwork(s *RemoteSettings, in remoteInput) {
	if in.SourceNetwork == nil {
		return
	}
	p, err := netip.ParsePrefix(strings.TrimSpace(*in.SourceNetwork))
	if err != nil {
		r.v.add("/remote/sourceNetwork", codeInvalidSourceNetwork, "must be an IPv4 or IPv6 CIDR prefix such as 10.99.0.0/24; a single address cannot fit every replica")
		return
	}
	p = p.Masked()
	s.SourceNetwork = &p
	if p.Bits() == p.Addr().BitLen() {
		r.warn(WarnSourceNetworkSingle, "/remote/sourceNetwork",
			"the prefix holds a single address, which fits one replica at most; name the network the replicas' addresses come from")
	}
}

func (r *remoteResolver) cache(s *RemoteSettings, in remoteInput) {
	var c remoteCacheInput
	if in.Cache != nil {
		c = *in.Cache
	}
	s.Cache = RemoteCache{
		StoreArtifacts:                boolOr(c.StoreArtifacts, true),
		MetadataTTLSeconds:            intOr(c.MetadataTTLSeconds, ptrOr(r.info.DefaultMetadataTTLSeconds)),
		MetadataRefreshTimeoutSeconds: intOr(c.MetadataRefreshTimeoutSeconds, 60),
		NotFoundTTLSeconds:            intOr(c.NotFoundTTLSeconds, ptrOr(r.info.DefaultNotFoundTTLSeconds)),
		MaxStaleSeconds:               intOr(c.MaxStaleSeconds, 0),
		UnusedRetentionHours:          intOr(c.UnusedRetentionHours, 0),
	}
	if !s.Cache.StoreArtifacts {
		r.notApplicable("/remote/cache/storeArtifacts", "pass-through mode is not available yet (R-REPO-044)")
	}
	r.rng("/remote/cache/metadataTtlSeconds", s.Cache.MetadataTTLSeconds, 0, 2592000)
	r.rng("/remote/cache/metadataRefreshTimeoutSeconds", s.Cache.MetadataRefreshTimeoutSeconds, 1, 600)
	r.rng("/remote/cache/notFoundTtlSeconds", s.Cache.NotFoundTTLSeconds, 0, 86400)
	r.rng("/remote/cache/maxStaleSeconds", s.Cache.MaxStaleSeconds, 0, 31536000)
	r.rng("/remote/cache/unusedRetentionHours", s.Cache.UnusedRetentionHours, 0, 87600)
	if h := s.Cache.UnusedRetentionHours; h > 0 && h <= 87600 {
		r.notApplicable("/remote/cache/unusedRetentionHours", "the cleanup of unused cache content is not available yet (R-REPO-046)")
	}
}

func (r *remoteResolver) integrity(s *RemoteSettings, in remoteInput) {
	var i remoteIntegrityInput
	if in.Integrity != nil {
		i = *in.Integrity
	}
	s.Integrity = RemoteIntegrity{
		ChecksumPolicy:              stringOr(i.ChecksumPolicy, ChecksumVerifyIfAvailable),
		BlockUnexpectedContentTypes: boolOr(i.BlockUnexpectedContentTypes, true),
		AllowedContentTypes:         i.AllowedContentTypes,
	}
	if s.Integrity.AllowedContentTypes == nil {
		s.Integrity.AllowedContentTypes = []string{}
	}
	r.oneOf("/remote/integrity/checksumPolicy", s.Integrity.ChecksumPolicy,
		ChecksumVerifyIfAvailable, ChecksumRequire, ChecksumIgnore, ChecksumPassThrough)
	if len(s.Integrity.AllowedContentTypes) > maxContentTypes {
		r.v.add("/remote/integrity/allowedContentTypes", codeOutOfRange, "at most "+strconv.Itoa(maxContentTypes)+" entries")
	}
	for k, t := range s.Integrity.AllowedContentTypes {
		if !mediaType.MatchString(t) {
			r.v.add("/remote/integrity/allowedContentTypes/"+strconv.Itoa(k), codeInvalidValue,
				"must be a lower-case media type such as text/html, without parameters")
		}
	}
}

func (r *remoteResolver) availability(s *RemoteSettings, in remoteInput) {
	var a availabilityInput
	if in.Availability != nil {
		a = *in.Availability
	}
	s.Availability = Availability{
		Mode:               stringOr(a.Mode, AvailabilityOnline),
		FailureThreshold:   intOr(a.FailureThreshold, 5),
		CircuitOpenSeconds: intOr(a.CircuitOpenSeconds, 300),
	}
	r.oneOf("/remote/availability/mode", s.Availability.Mode, AvailabilityOnline, AvailabilityOffline)
	r.rng("/remote/availability/failureThreshold", s.Availability.FailureThreshold, 1, 100)
	r.rng("/remote/availability/circuitOpenSeconds", s.Availability.CircuitOpenSeconds, 0, 86400)
}

func (r *remoteResolver) limits(s *RemoteSettings, in remoteInput) {
	var l remoteLimitsInput
	if in.Limits != nil {
		l = *in.Limits
	}
	s.Limits = RemoteLimits{
		MaxArtifactBytes: l.MaxArtifactBytes, MaxMetadataBytes: l.MaxMetadataBytes,
		MaxConcurrentFetches: intOr(l.MaxConcurrentFetches, 64),
	}
	for _, b := range []struct {
		pointer string
		v       *int64
	}{{"/remote/limits/maxArtifactBytes", s.Limits.MaxArtifactBytes}, {"/remote/limits/maxMetadataBytes", s.Limits.MaxMetadataBytes}} {
		if b.v != nil && *b.v < 1 {
			r.v.add(b.pointer, codeOutOfRange, "must be at least 1 byte, or null for the system value")
		}
	}
	r.rng("/remote/limits/maxConcurrentFetches", s.Limits.MaxConcurrentFetches, 1, 4096)
}
