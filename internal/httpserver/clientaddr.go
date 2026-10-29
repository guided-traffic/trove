package httpserver

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// Resolver resolves the client address (R-ARCH-006) and the base URL
// (R-OPS-005) of a request from the TCP peer and the trusted proxies.
type Resolver struct {
	trusted    []netip.Prefix
	baseURL    string
	additional map[string]string // normalised origin -> origin
}

// NewResolver parses server.trusted_proxies (addresses or CIDRs), server.base_url
// and server.additional_base_urls. baseURL may be empty (development only).
func NewResolver(trustedProxies []string, baseURL string, additionalBaseURLs []string) (*Resolver, error) {
	r := &Resolver{additional: map[string]string{}}
	for _, s := range trustedProxies {
		if p, err := netip.ParsePrefix(s); err == nil {
			r.trusted = append(r.trusted, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is neither an address nor a CIDR", s)
		}
		a = a.Unmap()
		r.trusted = append(r.trusted, netip.PrefixFrom(a, a.BitLen()))
	}
	if baseURL != "" {
		o, err := normaliseOrigin(baseURL)
		if err != nil {
			return nil, err
		}
		r.baseURL = o
	}
	for _, s := range additionalBaseURLs {
		o, err := normaliseOrigin(s)
		if err != nil {
			return nil, err
		}
		r.additional[o] = o
	}
	return r, nil
}

// normaliseOrigin lower-cases scheme and host and drops the trailing slash and a
// default port.
func normaliseOrigin(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%q is not an origin", s)
	}
	return originOf(u.Scheme, u.Host), nil
}

func originOf(scheme, host string) string {
	scheme, host = strings.ToLower(scheme), strings.ToLower(host)
	if h, p, err := net.SplitHostPort(host); err == nil {
		if (scheme == "http" && p == "80") || (scheme == "https" && p == "443") {
			host = h
			if strings.Contains(h, ":") {
				host = "[" + h + "]"
			}
		}
	}
	return scheme + "://" + host
}

// isTrusted reports whether a is one of the trusted proxies.
func (r *Resolver) isTrusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Peer returns the TCP peer of the request; the zero Addr when RemoteAddr is
// not an IP address (a unix socket, a test).
func Peer(req *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(req.RemoteAddr); err == nil {
		return ap.Addr().Unmap().WithZone("")
	}
	if a, err := netip.ParseAddr(req.RemoteAddr); err == nil {
		return a.Unmap().WithZone("")
	}
	return netip.Addr{}
}

// Resolve computes the RequestInfo of a request.
//
// The client address is the peer, unless the peer is a trusted proxy. Then it is
// the right-most address of Forwarded: for=… (RFC 7239), or of X-Forwarded-For
// when Forwarded has none, that is not itself a trusted proxy. Entries that are
// not addresses ("unknown", obfuscated identifiers) cannot be attributed: the
// walk stops there and the peer stays the client, so a trusted proxy's address
// is never replaced by something a client wrote. A chain made only of trusted
// proxies resolves to its left-most address.
//
// Forwarded scheme and host count only when the peer is trusted and the
// forwarded origin equals one of server.additional_base_urls (or server.base_url
// itself); in every other case the base URL is server.base_url, or, with none
// configured, derived from the request for development.
func (r *Resolver) Resolve(req *http.Request) RequestInfo {
	peer := Peer(req)
	info := RequestInfo{Peer: peer, Client: peer}
	trusted := peer.IsValid() && r.isTrusted(peer)

	if trusted {
		if chain := forwardedFor(req.Header); len(chain) > 0 {
			info.Client = r.walk(chain, peer)
		}
	}

	switch {
	case r.baseURL != "":
		info.BaseURL, info.BaseURLSource = r.baseURL, "config"
		if trusted {
			if o, ok := forwardedOrigin(req.Header); ok {
				if n, ok := r.additional[o]; ok {
					info.BaseURL = n
				} else if o == r.baseURL {
					info.BaseURL = o
				}
			}
		}
	default:
		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}
		host := req.Host
		info.BaseURL, info.BaseURLSource = originOf(scheme, host), "request"
		if trusted {
			if o, ok := forwardedOrigin(req.Header); ok {
				if n, ok := r.additional[o]; ok {
					info.BaseURL, info.BaseURLSource = n, "config"
				}
			}
		}
	}
	return info
}

// walk applies the right-most-untrusted rule to a forwarding chain.
func (r *Resolver) walk(chain []string, peer netip.Addr) netip.Addr {
	for i := len(chain) - 1; i >= 0; i-- {
		a, ok := parseNode(chain[i])
		if !ok {
			return peer
		}
		if !r.isTrusted(a) {
			return a
		}
		if i == 0 {
			return a
		}
	}
	return peer
}

// parseNode parses an address with an optional port and brackets, as it appears
// in X-Forwarded-For and in the for= parameter of Forwarded.
func parseNode(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"`))
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	if strings.HasPrefix(s, "[") {
		if i := strings.IndexByte(s, ']'); i > 0 {
			if a, err := netip.ParseAddr(s[1:i]); err == nil {
				return a.Unmap().WithZone(""), true
			}
		}
	}
	return netip.Addr{}, false
}

// forwardedFor returns the for= nodes of Forwarded, or, when that header has
// none, the entries of X-Forwarded-For, left to right.
func forwardedFor(h http.Header) []string {
	var out []string
	for _, el := range forwardedElements(h) {
		if v, ok := el["for"]; ok {
			out = append(out, v)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, line := range h.Values("X-Forwarded-For") {
		for _, part := range strings.Split(line, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// forwardedElements parses the Forwarded header (RFC 7239 section 4) into one
// lower-cased key/value map per element. Quoted strings keep commas and
// semicolons inside them.
func forwardedElements(h http.Header) []map[string]string {
	var out []map[string]string
	for _, line := range h.Values("Forwarded") {
		for _, elem := range splitQuoted(line, ',') {
			m := map[string]string{}
			for _, pair := range splitQuoted(elem, ';') {
				k, v, ok := strings.Cut(pair, "=")
				if !ok {
					continue
				}
				m[strings.ToLower(strings.TrimSpace(k))] = unquote(strings.TrimSpace(v))
			}
			if len(m) > 0 {
				out = append(out, m)
			}
		}
	}
	return out
}

func splitQuoted(s string, sep byte) []string {
	var out []string
	start, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && quoted:
			i++
		case s[i] == '"':
			quoted = !quoted
		case s[i] == sep && !quoted:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.ReplaceAll(s[1:len(s)-1], `\"`, `"`)
	}
	return s
}

// forwardedOrigin returns the origin the right-most trusted hop reports, from
// Forwarded proto/host or from X-Forwarded-Proto, -Host and -Port.
func forwardedOrigin(h http.Header) (string, bool) {
	var proto, host string
	if els := forwardedElements(h); len(els) > 0 {
		last := els[len(els)-1]
		proto, host = last["proto"], last["host"]
	}
	if proto == "" {
		proto = lastValue(h, "X-Forwarded-Proto")
	}
	if host == "" {
		host = lastValue(h, "X-Forwarded-Host")
		if port := lastValue(h, "X-Forwarded-Port"); host != "" && port != "" {
			if _, _, err := net.SplitHostPort(host); err != nil {
				host = net.JoinHostPort(strings.Trim(host, "[]"), port)
			}
		}
	}
	if proto == "" || host == "" {
		return "", false
	}
	if proto != "http" && proto != "https" {
		return "", false
	}
	u, err := url.Parse(proto + "://" + host)
	if err != nil || u.Host == "" || u.Path != "" || u.User != nil {
		return "", false
	}
	return originOf(proto, u.Host), true
}

// lastValue returns the right-most comma-separated entry over all lines of a header.
func lastValue(h http.Header, name string) string {
	vals := h.Values(name)
	if len(vals) == 0 {
		return ""
	}
	parts := strings.Split(vals[len(vals)-1], ",")
	return strings.TrimSpace(parts[len(parts)-1])
}
