package httpserver

import (
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/config"
)

// FamilyOther is the family of a route that belongs to no surface family: the
// test fixtures. Only the default endpoint registers it.
const FamilyOther = "other"

// FamilyOf classifies a ServeMux pattern into the route families of R-EP-005:
//
//	ui           /ui/…, GET /{$}, /auth/…
//	ci_exchange  POST /api/v1/auth/oidc/token and the npm trusted-publishing
//	             exchange (R-NPM-022) at the host root or below a registry URL
//	api          /api/v1/…
//	packages     /api/<format>/<repoKey>/…, /v2/…, /.well-known/…
//	files        /files/…
//
// Anything else is FamilyOther.
func FamilyOf(pattern string) string {
	if pattern == RootPattern {
		return config.SurfaceUI
	}
	return familyOfPath(patternPath(pattern))
}

// FamilyOfPath is [FamilyOf] for a request path, which unlike a pattern may
// contain spaces. The format dispatcher asks it for the paths below /api/<id>/
// that belong to a family of their own (npm's trusted-publishing exchange,
// R-NPM-022).
func FamilyOfPath(path string) string { return familyOfPath(path) }

// familyOfPath is FamilyOf for a path without method and host. A request path
// goes through it as it is: unlike a pattern it may contain spaces.
func familyOfPath(path string) string {
	switch {
	case path == "/ui" || strings.HasPrefix(path, "/ui/"),
		path == "/auth" || strings.HasPrefix(path, "/auth/"):
		return config.SurfaceUI
	case path == "/api/v1/auth/oidc/token", strings.Contains(path, "/-/npm/v1/oidc/"):
		return config.SurfaceCIExchange
	case path == "/api/v1" || strings.HasPrefix(path, "/api/v1/"):
		return config.SurfaceAPI
	case strings.HasPrefix(path, "/api/"):
		return config.SurfacePackages
	case path == "/files" || strings.HasPrefix(path, "/files/"):
		return config.SurfaceFiles
	case path == ReservedOCIPrefix || strings.HasPrefix(path, ReservedOCIPrefix+"/"),
		path == ReservedWellKnownPrefix || strings.HasPrefix(path, ReservedWellKnownPrefix+"/"):
		return config.SurfacePackages
	}
	return FamilyOther
}

// servesPattern reports whether the endpoint registers the pattern.
func (e *Endpoint) servesPattern(pattern string) bool {
	if pattern == CatchAllPattern {
		return true
	}
	f := FamilyOf(pattern)
	if f == FamilyOther {
		return e.IsDefault()
	}
	return e.Serves(f)
}

// servesPath reports whether the endpoint serves the family a request path
// belongs to. A path of a family outside the surface is answered like any
// unknown path (R-EP-005).
func (e *Endpoint) servesPath(path string) bool {
	switch f := familyOfPath(path); f {
	case FamilyOther:
		return true // the router has nothing for it unless the default endpoint registered it
	default:
		return e.Serves(f)
	}
}

// CheckSurface returns the patterns among the registered ones that the
// endpoint's surface does not allow (R-EP-005). The router drops such patterns
// itself; the check is the independent proof the route-table test runs over
// every endpoint, and it fails on a route registered around the router.
func CheckSurface(patterns []string, e *Endpoint) []string {
	var out []string
	for _, p := range patterns {
		if p == RootPattern {
			if !e.Serves(config.SurfaceUI) {
				out = append(out, fmt.Sprintf("endpoint %s: %q belongs to ui, which its surface does not hold", e.Name(), p))
			}
			continue
		}
		if !e.servesPattern(p) {
			out = append(out, fmt.Sprintf("endpoint %s: %q belongs to %s, which its surface does not hold", e.Name(), p, FamilyOf(p)))
		}
	}
	return out
}
