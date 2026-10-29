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
// Q-HA-4). Until the OCI format exists nothing is registered below it and it
// answers 404 through the catch-all.
const ReservedOCIPrefix = "/v2"

// ReservedWellKnownPrefix is the host-root path for discovery documents that
// clients fetch there (RFC 8615): Terraform's first, on a bound host (R-EP-006).
// No other route may use it, and until a format registers one every path below
// it answers 404 through the catch-all.
const ReservedWellKnownPrefix = "/.well-known"

// patternPath returns the path part of a ServeMux pattern: it drops the optional
// method and the optional host.
func patternPath(pattern string) string {
	rest := pattern
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		rest = strings.TrimLeft(rest[i:], " \t")
	}
	if i := strings.IndexByte(rest, '/'); i > 0 {
		rest = rest[i:] // drop the host
	}
	return rest
}

// CheckRouteTable returns the violations of the route-table rules of R-ARCH-005
// and R-HA-041 over the patterns of the client listener:
//
//   - no pattern other than the OCI format's (ociPatterns) has a path that is
//     /v2 or starts with /v2/, whatever its method or host prefix;
//   - no pattern other than the catch-all and GET /{$} has a wildcard as its
//     first path segment, because /{seg}/… matches below /v2/ too.
//
// R-EP-006 adds the same rule for /.well-known/: only a format's own host-root
// patterns (also passed as ociPatterns, the format-registered exemptions) may
// lie below it.
//
// ServeMux's own conflict check does not replace this: a more specific pattern
// such as /v2/x does not conflict with /v2/ and would register without a panic.
func CheckRouteTable(patterns, ociPatterns []string) []string {
	var out []string
	for _, p := range patterns {
		if p == CatchAllPattern || p == RootPattern || slices.Contains(ociPatterns, p) {
			continue
		}
		path := patternPath(p)
		if path == ReservedOCIPrefix || strings.HasPrefix(path, ReservedOCIPrefix+"/") {
			out = append(out, fmt.Sprintf("%q lies below %s/, which is reserved for the OCI format (R-HA-041)", p, ReservedOCIPrefix))
			continue
		}
		if path == ReservedWellKnownPrefix || strings.HasPrefix(path, ReservedWellKnownPrefix+"/") {
			out = append(out, fmt.Sprintf("%q lies below %s/, which is reserved for discovery documents of formats (R-EP-006)", p, ReservedWellKnownPrefix))
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
