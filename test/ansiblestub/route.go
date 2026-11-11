package ansiblestub

import (
	"net"
	"strconv"
	"strings"
)

// The five hosts of the stub. A request is for the GitHub stand-in, the codeload host, the unlisted host or the realm when its Host
// names one of them, and for the Galaxy otherwise.
const (
	siteGalaxy   = "galaxy"
	siteGitHub   = "github"
	siteCodeload = "codeload"
	siteUnlisted = "unlisted"
	siteRealm    = "realm"
)

// Kinds of route on the Galaxy host.
const (
	kRoot         = "root"
	kIndex        = "index"
	kCollection   = "collection"
	kVersions     = "versions"
	kVersion      = "version"
	kDocs         = "docs-blob"
	kArtifact     = "artifact" // below /api/v3/plugin/ansible/content/published/collections/artifacts/
	kPulp         = "pulp"     // the signed address /api/pulp/content/published/<file> that the public Galaxy redirects to
	kRoleLookup   = "role-lookup"
	kRoleDoc      = "role-doc"
	kRoleVersions = "role-versions"
	kOther        = "other"
)

// pluginRoot is where the public Galaxy serves the collection documents; /api/v3/collections/ redirects to its index/ below.
const pluginRoot = "/api/v3/plugin/ansible/content/published/collections/"

// route is a parsed path of the Galaxy host.
type route struct {
	kind string
	// short is true for a path below /api/v3/collections/, which the public Galaxy redirects.
	short          bool
	ns, name, vers string
	file           string
	roleID         int
}

// class is the class of request of the route, for the switches and the counters.
func (rt route) class() string {
	switch rt.kind {
	case kRoot:
		return ClassRoot
	case kIndex:
		return ClassIndex
	case kCollection:
		return ClassCollection
	case kVersions:
		return ClassVersions
	case kVersion:
		return ClassVersion
	case kDocs:
		return ClassDocs
	case kArtifact, kPulp:
		return ClassArtifact
	case kRoleLookup, kRoleDoc, kRoleVersions:
		return ClassRole
	}
	return ClassOther
}

// parseGalaxy parses the path of a request to the Galaxy host. A trailing slash is optional.
func parseGalaxy(path string) route {
	segs := splitPath(path)
	if len(segs) == 0 || segs[0] != "api" {
		return route{kind: kOther}
	}
	segs = segs[1:]
	switch {
	case len(segs) == 0:
		return route{kind: kRoot}
	case segs[0] == "v3" && len(segs) >= 2 && segs[1] == "collections":
		rt := collectionRoute(segs[2:])
		rt.short = true
		return rt
	case segs[0] == "v3" && hasPrefix(segs[1:], "plugin", "ansible", "content", "published", "collections"):
		rest := segs[6:]
		switch {
		case len(rest) == 2 && rest[0] == "artifacts":
			return route{kind: kArtifact, file: rest[1]}
		case len(rest) >= 1 && rest[0] == "index":
			return collectionRoute(rest[1:])
		}
	case hasPrefix(segs, "pulp", "content", "published") && len(segs) == 4:
		return route{kind: kPulp, file: segs[3]}
	case segs[0] == "v1" && len(segs) >= 2 && segs[1] == "roles":
		return roleRoute(segs[2:])
	}
	return route{kind: kOther}
}

func hasPrefix(segs []string, prefix ...string) bool {
	if len(segs) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if segs[i] != p {
			return false
		}
	}
	return true
}

func collectionRoute(rest []string) route {
	switch {
	case len(rest) == 0:
		return route{kind: kIndex}
	case len(rest) == 2:
		return route{kind: kCollection, ns: rest[0], name: rest[1]}
	case len(rest) == 3 && rest[2] == "versions":
		return route{kind: kVersions, ns: rest[0], name: rest[1]}
	case len(rest) == 4 && rest[2] == "versions":
		return route{kind: kVersion, ns: rest[0], name: rest[1], vers: rest[3]}
	case len(rest) == 5 && rest[2] == "versions" && rest[4] == "docs-blob":
		return route{kind: kDocs, ns: rest[0], name: rest[1], vers: rest[3]}
	}
	return route{kind: kOther}
}

func roleRoute(rest []string) route {
	switch {
	case len(rest) == 0:
		return route{kind: kRoleLookup}
	case len(rest) == 1:
		if id, err := strconv.Atoi(rest[0]); err == nil {
			return route{kind: kRoleDoc, roleID: id}
		}
	case len(rest) == 2 && rest[1] == "versions":
		if id, err := strconv.Atoi(rest[0]); err == nil {
			return route{kind: kRoleVersions, roleID: id}
		}
	}
	return route{kind: kOther}
}

// splitPath splits an escaped-or-not path into its non-empty segments.
func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// hostName is the lower-case host of a Host header without its port.
func hostName(hostport string) string {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

// hostPort is the port of a Host header, or empty.
func hostPort(hostport string) string {
	if _, p, err := net.SplitHostPort(hostport); err == nil {
		return p
	}
	return ""
}

// siteOf names the host a request is for.
func (s *Server) siteOf(host string) string {
	switch host {
	case s.cfg.GitHubHost:
		return siteGitHub
	case s.cfg.CodeloadHost:
		return siteCodeload
	case s.cfg.UnlistedHost:
		return siteUnlisted
	case s.cfg.RealmHost:
		return siteRealm
	}
	return siteGalaxy
}

// siteBase is the "https://host[:port]" of a site, with the port of the request that named it.
func (s *Server) siteBase(site, requestHost string) string {
	var host string
	switch site {
	case siteGitHub:
		host = s.cfg.GitHubHost
	case siteCodeload:
		host = s.cfg.CodeloadHost
	case siteUnlisted:
		host = s.cfg.UnlistedHost
	case siteRealm:
		host = s.cfg.RealmHost
	default:
		return "https://" + requestHost
	}
	if p := hostPort(requestHost); p != "" {
		return "https://" + net.JoinHostPort(host, p)
	}
	return "https://" + host
}

// roleArchive parses the path of a role archive: /<user>/<repo>/archive/<tag>.tar.gz on the GitHub stand-in and the unlisted host,
// /<user>/<repo>/tar.gz/<tag> on the codeload host.
func roleArchive(site, path string) (user, repo, tag string, ok bool) {
	segs := splitPath(path)
	switch {
	case site == siteCodeload && len(segs) == 4 && segs[2] == "tar.gz":
		return segs[0], segs[1], segs[3], true
	case site != siteCodeload && len(segs) == 4 && segs[2] == "archive" && strings.HasSuffix(segs[3], ".tar.gz"):
		return segs[0], segs[1], strings.TrimSuffix(segs[3], ".tar.gz"), true
	}
	return "", "", "", false
}
