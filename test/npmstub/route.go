package npmstub

import (
	"net/url"
	"strings"
)

// route is what a request path addresses.
type route struct {
	kind string // a Kind constant
	name string // package name, "@scope/name" for scoped
	file string // tarball file name
}

// parseRoute classifies an escaped request path. A scoped name arrives as "@scope%2fname" from npm
// and as "@scope/name" from other clients; both address the same package. The decoded name is
// only ever a map key here, never a file path.
func parseRoute(escapedPath string) route {
	switch escapedPath {
	case "/-/ping":
		return route{kind: KindPing}
	case auditPath:
		return route{kind: KindAudit}
	}
	trimmed := strings.TrimPrefix(escapedPath, "/")
	segments := strings.Split(trimmed, "/")
	for i, s := range segments {
		d, err := url.PathUnescape(s)
		if err != nil {
			return route{kind: KindOther}
		}
		segments[i] = d
	}
	if len(segments) == 0 || segments[0] == "" || segments[0] == "-" {
		return route{kind: KindOther}
	}
	name, rest := segments[0], segments[1:]
	if strings.HasPrefix(name, "@") && !strings.Contains(name, "/") {
		if len(rest) == 0 {
			return route{kind: KindOther}
		}
		name, rest = name+"/"+rest[0], rest[1:]
	}
	switch {
	case len(rest) == 0:
		return route{kind: KindDocument, name: name}
	case len(rest) == 2 && rest[0] == "-" && strings.HasSuffix(rest[1], ".tgz") && !strings.Contains(rest[1], "/"):
		return route{kind: KindTarball, name: name, file: rest[1]}
	}
	return route{kind: KindOther, name: name}
}
