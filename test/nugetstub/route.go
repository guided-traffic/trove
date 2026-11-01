package nugetstub

import (
	"net/url"
	"strings"
)

// Path prefixes of the resources. The service index lists them under the hosts of the stub.
const (
	indexPath        = "/v3/index.json"
	registrationPath = "/v3/registration/"    // plain hive
	registrationGz   = "/v3/registration-gz/" // gzip hive
	flatPath         = "/v3-flatcontainer/"
	searchPath       = "/query"
)

// route is what a request path addresses. id and version are lowercase and only ever map keys.
type route struct {
	kind    string // a Kind constant
	id      string
	version string // a version, or "lower/upper" for a registration page
	hive    string // registration hive prefix, for the registration kinds
}

// parseRoute classifies an escaped request path.
func parseRoute(escapedPath string) route {
	switch escapedPath {
	case indexPath:
		return route{kind: KindIndex}
	case searchPath:
		return route{kind: KindSearch}
	}
	for _, hive := range []string{registrationPath, registrationGz} {
		if rest, ok := strings.CutPrefix(escapedPath, hive); ok {
			rt := parseRegistration(rest)
			rt.hive = hive
			return rt
		}
	}
	if rest, ok := strings.CutPrefix(escapedPath, flatPath); ok {
		return parseFlat(rest)
	}
	return route{kind: KindOther}
}

func split(rest string) ([]string, bool) {
	segments := strings.Split(rest, "/")
	for i, s := range segments {
		d, err := url.PathUnescape(s)
		if err != nil || d == "" || d == "." || d == ".." || strings.ContainsAny(d, `\`) {
			return nil, false
		}
		segments[i] = strings.ToLower(d)
	}
	return segments, true
}

func parseRegistration(rest string) route {
	s, ok := split(rest)
	if !ok {
		return route{kind: KindOther}
	}
	id := s[0]
	switch {
	case len(s) == 2 && s[1] == "index.json":
		return route{kind: KindRegIndex, id: id}
	case len(s) == 4 && s[1] == "page" && strings.HasSuffix(s[3], ".json"):
		return route{kind: KindRegPage, id: id, version: s[2] + "/" + strings.TrimSuffix(s[3], ".json")}
	case len(s) == 2 && strings.HasSuffix(s[1], ".json"):
		return route{kind: KindRegLeaf, id: id, version: strings.TrimSuffix(s[1], ".json")}
	}
	return route{kind: KindOther, id: id}
}

func parseFlat(rest string) route {
	s, ok := split(rest)
	if !ok {
		return route{kind: KindOther}
	}
	id := s[0]
	switch {
	case len(s) == 2 && s[1] == "index.json":
		return route{kind: KindVersions, id: id}
	case len(s) == 3 && s[2] == id+"."+s[1]+".nupkg":
		return route{kind: KindNupkg, id: id, version: s[1]}
	case len(s) == 3 && s[2] == id+".nuspec":
		return route{kind: KindNuspec, id: id, version: s[1]}
	}
	return route{kind: KindOther, id: id}
}
