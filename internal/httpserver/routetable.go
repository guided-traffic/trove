package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/config"
)

// ReservedOCIPrefix is the path the OCI distribution API owns (R-HA-041,
// Q-HA-4). Only the registry root's one pattern, mounted through the enforcement
// point of the registry tokens' grants, is registered below it; an endpoint that
// serves no packages family has none and answers 404 through the catch-all.
const ReservedOCIPrefix = "/v2"

// ReservedWellKnownPrefix is the host-root path for discovery documents that
// clients fetch there (RFC 8615): Terraform's first, on a bound host (R-EP-006).
// No other route may use it, and until a format registers one every path below
// it answers 404 through the catch-all.
const ReservedWellKnownPrefix = "/.well-known"

// PatternPath returns the path part of a ServeMux pattern: it drops the optional
// method and the optional host. It is the one parser of a pattern's path: the
// route-table rules, the surface filter and the registry's enforcement point read
// patterns through it.
func PatternPath(pattern string) string {
	rest := pattern
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		rest = strings.TrimLeft(rest[i:], " \t")
	}
	if i := strings.IndexByte(rest, '/'); i > 0 {
		rest = rest[i:] // drop the host
	}
	return rest
}

// LiteralPatternPath is [PatternPath] with the literal segments of the path unescaped, as ServeMux compares them: "/v%32/x"
// serves "/v2/x". A segment with a wildcard is kept as written. The route-table rules and the registry's enforcement point
// read a pattern's path through it, so an escaped spelling is judged as what it serves.
func LiteralPatternPath(pattern string) string {
	segs := strings.Split(PatternPath(pattern), "/")
	for i, seg := range segs {
		if strings.Contains(seg, "{") {
			continue
		}
		if u, err := url.PathUnescape(seg); err == nil {
			segs[i] = u
		}
	}
	return strings.Join(segs, "/")
}

// CheckRouteTable returns the violations of the route-table rules of R-ARCH-005
// and R-HA-041 over the patterns of the client listener:
//
//   - no pattern other than the OCI format's has a path that is /v2 or starts with
//     /v2/, whatever its method or host prefix; the OCI format's are exactly
//     registryPatterns, the patterns mounted through the enforcement point of the
//     registry tokens' grants (registry.Enforcer.Patterns, Q-OCI-56);
//   - no pattern other than the catch-all and GET /{$} has a wildcard as its
//     first path segment, because /{seg}/… matches below /v2/ too.
//
// R-EP-006 adds the same rule for /.well-known/: only the host-root patterns a
// format registers for its discovery documents (hostRootPatterns) may lie below it.
// Each list exempts only below its own prefix: a registry pattern outside /v2/ or a
// host-root pattern outside /.well-known/ is checked like any other.
//
// A pattern's text is not the whole rule: ServeMux unescapes the literal segments of a pattern, so "/v%32/{rest...}" serves
// /v2/…, and a host-only pattern such as "example.com/" serves every path of that host. Each pattern that is not exempt is
// therefore also put into a scratch ServeMux and probed with the paths of the registry root (registryProbes), for every method
// and with its own host, and refused when the mux would serve any of them (R-HA-041, Q-OCI-101).
//
// ServeMux's own conflict check does not replace this: a more specific pattern
// such as /v2/x does not conflict with /v2/ and would register without a panic.
func CheckRouteTable(patterns, registryPatterns, hostRootPatterns []string) []string {
	var out []string
	for _, p := range patterns {
		if p == CatchAllPattern || p == RootPattern {
			continue
		}
		path := LiteralPatternPath(p)
		if below(path, ReservedOCIPrefix) && slices.Contains(registryPatterns, p) ||
			below(path, ReservedWellKnownPrefix) && slices.Contains(hostRootPatterns, p) {
			continue
		}
		if below(path, ReservedOCIPrefix) {
			out = append(out, fmt.Sprintf("%q lies below %s/, which is reserved for the OCI format (R-HA-041)", p, ReservedOCIPrefix))
			continue
		}
		if below(path, ReservedWellKnownPrefix) {
			out = append(out, fmt.Sprintf("%q lies below %s/, which is reserved for discovery documents of formats (R-EP-006)", p, ReservedWellKnownPrefix))
			continue
		}
		if probe := matchesRegistry(p); probe != "" {
			out = append(out, fmt.Sprintf("%q also serves %s, a path below %s/, which is reserved for the OCI format (R-HA-041)", p, probe, ReservedOCIPrefix))
			continue
		}
		first, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if strings.HasPrefix(first, "{") {
			out = append(out, fmt.Sprintf("%q starts with a wildcard segment, which also matches below %s/ (R-HA-041)", p, ReservedOCIPrefix))
			continue
		}
		if probe := matchesOtherFamily(p); probe != "" {
			out = append(out, fmt.Sprintf("%q belongs to %s but also matches %s, a path of the %s family: a surface that serves one family would serve the other (R-EP-005)",
				p, FamilyOf(p), probe, familyOfPath(probe)))
		}
	}
	return out
}

// CheckRouter returns the violations of the route table of a router: those of [CheckRouteTable] over its patterns and the
// ways to serve a path that no pattern names. A prefix of HandleUncleaned or HandleRegistryUncleaned that is /v2, lies below
// it or encloses it (a prefix of "/v2/", such as "/") is admitted only when it is one of registryUncleaned, the prefixes the
// registry's enforcement point mounted (registry.Enforcer.UncleanedPrefixes); a fallback handler (SetFallback) answers the
// paths no pattern claims, /v2/ among them, and is refused (R-HA-041, Q-OCI-101).
func CheckRouter(r *Router, registryPatterns, registryUncleaned, hostRootPatterns []string) []string {
	out := CheckRouteTable(r.Patterns(), registryPatterns, hostRootPatterns)
	for _, prefix := range r.UncleanedPrefixes() {
		if !belowRegistry(prefix) && !strings.HasPrefix(ReservedOCIPrefix+"/", literalPrefix(prefix)) {
			continue
		}
		if !belowRegistry(prefix) || !slices.Contains(registryUncleaned, prefix) {
			out = append(out, fmt.Sprintf("the uncleaned prefix %q serves paths below %s/, which is reserved for the OCI format (R-HA-041)", prefix, ReservedOCIPrefix))
		}
	}
	if r.HasFallback() {
		out = append(out, fmt.Sprintf("a fallback handler answers every request no route claims, %s/ among them, which is reserved for the OCI format (R-HA-041)", ReservedOCIPrefix))
	}
	return out
}

// below reports whether path is prefix or lies below prefix + "/".
func below(path, prefix string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") }

// registryProbes are concrete paths of the registry root: the bare prefix, the version check, the two core routes and a
// repository route with a nested image name.
func registryProbes() []string {
	return []string{"/v2", "/v2/", "/v2/_token", "/v2/_catalog", "/v2/a/b/c/manifests/x"}
}

// routeProbeMethods are the methods a pattern is probed with.
func routeProbeMethods() []string {
	return []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}
}

// patternHost returns the host of a ServeMux pattern, "" when it has none.
func patternHost(pattern string) string {
	rest := pattern
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		rest = strings.TrimLeft(rest[i:], " \t")
	}
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return rest[:i]
	}
	return ""
}

// matchesRegistry returns a probe path of the registry root that a pattern serves, or "". The pattern is tried in a
// ServeMux of its own with every method and with its own host and with none; a redirect counts, because it leads to the path
// the mux then serves.
func matchesRegistry(pattern string) (probe string) {
	defer func() {
		if recover() != nil {
			probe = "" // a pattern ServeMux refuses is reported when it is registered
		}
	}()
	mux := http.NewServeMux()
	mux.Handle(pattern, http.NotFoundHandler())
	hosts := []string{"", "example.com"}
	if h := patternHost(pattern); h != "" {
		hosts = append(hosts, h)
	}
	methods := routeProbeMethods()
	if m, _, ok := strings.Cut(pattern, " "); ok && m != "" && !strings.Contains(m, "/") && !slices.Contains(methods, m) {
		methods = append(methods, m) // the pattern's own method, whatever it is
	}
	for _, path := range registryProbes() {
		for _, method := range methods {
			for _, host := range hosts {
				req := &http.Request{Method: method, URL: &url.URL{Path: path}, Host: host}
				if _, p := mux.Handler(req); p != "" {
					return path
				}
			}
		}
	}
	return ""
}

// familyProbes are concrete paths of the management API and of the token
// exchange. The surface filter works on registered patterns, not on request
// paths, so a pattern of another family that can match one of them would serve
// it on an endpoint whose surface leaves the API out.
func familyProbes() []string {
	return []string{
		"/api/v1/", "/api/v1/system/ping", "/api/v1/a/b/c", "/api/v1/auth/oidc/token",
		"/-/npm/v1/oidc/token/exchange/package/x", "/x/-/npm/v1/oidc/token/exchange/package/y",
	}
}

// matchesOtherFamily returns a probe path of the API or exchange family that a
// pattern of any other family can match, or "". A pattern is tried in a ServeMux
// of its own, with GET and POST.
func matchesOtherFamily(pattern string) (probe string) {
	switch FamilyOf(pattern) {
	case config.SurfaceAPI, config.SurfaceCIExchange, config.SurfaceUI:
		return ""
	}
	defer func() {
		if recover() != nil {
			probe = "" // a pattern ServeMux refuses is reported when it is registered
		}
	}()
	mux := http.NewServeMux()
	mux.Handle(pattern, http.NotFoundHandler())
	for _, path := range familyProbes() {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req := &http.Request{Method: method, URL: &url.URL{Path: path}, Host: "example.com"}
			if _, p := mux.Handler(req); p == pattern {
				return path
			}
		}
	}
	return ""
}
