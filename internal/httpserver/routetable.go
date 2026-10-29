package httpserver

import (
	"fmt"
	"slices"
	"strings"
)

// ReservedOCIPrefix is the path the OCI distribution API owns (R-HA-041,
// Q-HA-4). Until the OCI format exists nothing is registered below it and it
// answers 404 through the catch-all.
const ReservedOCIPrefix = "/v2"

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
		first, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if strings.HasPrefix(first, "{") {
			out = append(out, fmt.Sprintf("%q starts with a wildcard segment, which also matches below %s/ (R-HA-041)", p, ReservedOCIPrefix))
		}
	}
	return out
}
