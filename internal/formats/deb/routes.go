package deb

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// The names of the routes (R-DEB-003): "deb.<resource>[.<operation>]". The request log and the metrics name the handler of a request
// by it, and a work package registers its handler by it (handlers.go).
const (
	routeDists       = "deb.dists.get"
	routeByHash      = "deb.dists.by_hash"
	routePoolGet     = "deb.pool.get"
	routePoolDelete  = "deb.pool.delete"
	routeMemberGet   = "deb.members.get"
	routeFlatRelease = "deb.flat.release"
	routeFlatIndex   = "deb.flat.index"
	routeFlatFile    = "deb.flat.file"
	routeSigningKey  = "deb.signing_key.get"
	routeUpload      = "deb.upload.put"
	flatRootParam    = "flatRoot"
	rootFlatPrefix   = "./"
)

// segs splits an escaped repository-relative path into decoded segments. A segment that decodes to something with a '/', or to the
// empty string, is no match: a %2F does not turn into a separator here.
func segs(escaped string) ([]string, bool) {
	p := strings.TrimPrefix(escaped, "/")
	if p == "" {
		return nil, false
	}
	parts := strings.Split(p, "/")
	for i, s := range parts {
		d, err := url.PathUnescape(s)
		if err != nil || d == "" || strings.Contains(d, "/") {
			return nil, false
		}
		parts[i] = d
	}
	return parts, true
}

// byHashTail reports whether the segments end in by-hash/SHA256/<64 hex digits> and returns the directory before it.
func byHashTail(s []string) (dir []string, hex string, ok bool) {
	n := len(s)
	if n < 3 || s[n-3] != DirByHash || s[n-2] != HashSHA256 || !ValidSHA256Hex(s[n-1]) {
		return nil, "", false
	}
	return s[:n-3], s[n-1], true
}

// matchDists matches every path below dists/ that is no by-hash file: the release documents, the indexes, and for a remote repository
// the pdiff patches and every other file of any suite and component name the upstream has (R-DEB-003). One route serves all classes,
// because Match is blind to the class and two routes that overlap in path would match one request twice (Q-DEB-45); a local or virtual
// repository answers 404 for a path below dists/ that is not a file of its published generation (R-DEB-003), which is the handler's
// decision, and the remote's suite may hold slashes ("stable/updates").
func matchDists(escaped string) (formats.Params, bool) {
	s, ok := segs(escaped)
	if !ok || len(s) < 3 || s[0] != dirDists {
		return nil, false
	}
	if _, _, byHash := byHashTail(s[1:]); byHash {
		return nil, false
	}
	return formats.Params{"rest": strings.Join(s[1:], "/")}, true
}

// matchByHash matches dists/<suite>/<dir>/by-hash/SHA256/<hex>: <dir> may be empty, and the suite of a remote may hold slashes, so the
// parameter rest is everything between dists/ and by-hash/ (R-DEB-012, R-DEB-021).
func matchByHash(escaped string) (formats.Params, bool) {
	s, ok := segs(escaped)
	if !ok || len(s) < 5 || s[0] != dirDists {
		return nil, false
	}
	dir, hex, ok := byHashTail(s[1:])
	if !ok || len(dir) == 0 {
		return nil, false
	}
	return formats.Params{"rest": strings.Join(dir, "/"), "hash": hex}, true
}

// matchPool matches pool/<path>.
func matchPool(escaped string) (formats.Params, bool) {
	s, ok := segs(escaped)
	if !ok || len(s) < 2 || s[0] != dirPool {
		return nil, false
	}
	return formats.Params{"path": strings.Join(s[1:], "/")}, true
}

// matchMember matches members/<memberKey>/<path> of a virtual repository.
func matchMember(escaped string) (formats.Params, bool) {
	s, ok := segs(escaped)
	if !ok || len(s) < 3 || s[0] != dirMembers || !ValidSuite(s[1]) {
		return nil, false
	}
	return formats.Params{"member": s[1], "path": strings.Join(s[2:], "/")}, true
}

// matchUpload matches upload/<file>; the file name must end in .deb (the handler answers 400 deb_invalid_file_name otherwise).
func matchUpload(escaped string) (formats.Params, bool) {
	s, ok := segs(escaped)
	if !ok || len(s) != 2 || s[0] != dirUpload {
		return nil, false
	}
	return formats.Params{"file": s[1]}, true
}

// flatSegments are the segments of a path of a flat repository of a remote: outside dists/, pool/, members/, upload/ and the signing
// key. The directory may be empty (the root of the flat repository).
func flatSegments(escaped string) (dir []string, file string, ok bool) {
	s, ok := segs(escaped)
	if !ok || reservedFirst(s[0]) {
		return nil, "", false
	}
	return s[:len(s)-1], s[len(s)-1], true
}

func flatParams(dir []string, file string) formats.Params {
	return formats.Params{"dir": strings.Join(dir, "/"), "file": file}
}

// matchFlatRelease matches <dir>/InRelease, <dir>/Release and <dir>/Release.gpg (R-DEB-003).
func matchFlatRelease(escaped string) (formats.Params, bool) {
	dir, file, ok := flatSegments(escaped)
	if !ok || !IsReleaseFile(file) {
		return nil, false
	}
	return flatParams(dir, file), true
}

// matchFlatIndex matches an index file of a flat repository: the file name is an index name, or the file is in a *.diff directory.
func matchFlatIndex(escaped string) (formats.Params, bool) {
	dir, file, ok := flatSegments(escaped)
	if !ok || IsReleaseFile(file) || !isFlatIndex(dir, file) {
		return nil, false
	}
	return flatParams(dir, file), true
}

// matchFlatFile matches every other file of a flat repository: a package file.
func matchFlatFile(escaped string) (formats.Params, bool) {
	dir, file, ok := flatSegments(escaped)
	if !ok || IsReleaseFile(file) || isFlatIndex(dir, file) {
		return nil, false
	}
	return flatParams(dir, file), true
}

func isFlatIndex(dir []string, file string) bool {
	if n := len(dir); n > 0 && strings.HasSuffix(dir[n-1], ".diff") {
		return true
	}
	return IsIndexName(file)
}

// matchSigningKey matches signing-key.asc.
func matchSigningKey(escaped string) (formats.Params, bool) {
	s, ok := segs(escaped)
	if !ok || len(s) != 1 || s[0] != SigningKeyFile {
		return nil, false
	}
	return formats.Params{}, true
}

// buildRoutes is the whole endpoint map of R-DEB-003, written once with the name of every handler. A route serves the classes its
// Classes name; a route of a class the format does not register yet is left out, so the table grows with the classes without an edit
// (the registry of class_*.go files, format.go). A handler is looked up by route name (handlers.go); an unregistered route answers 501.
//
// The writes are Classes local, so that a remote repository answers 405 and the dispatcher routes a write addressed to a virtual
// repository to its deployment target (R-DEB-028).
func (f *Format) buildRoutes() []formats.Route {
	const (
		read   = formats.ActionRead
		deploy = formats.ActionDeploy
		del    = formats.ActionDelete
		hex    = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	local, remote, virtual := formats.Local, formats.Remote, formats.Virtual
	route := func(name, tmpl string, kind formats.RouteKind, action formats.Action, classes []formats.Class,
		match func(string) (formats.Params, bool), examples ...string,
	) formats.Route {
		method := http.MethodGet
		switch kind {
		case formats.Upload:
			method = http.MethodPut
		case formats.Delete:
			method = http.MethodDelete
		}
		return formats.Route{
			Name: name, Method: method, Template: tmpl, Examples: examples, Action: action, Kind: kind,
			Classes: classes, Match: match, Handler: f.handlerFor(name),
		}
	}
	all := []formats.Route{
		route(routeDists, "/dists/{rest...}", formats.Metadata, read, nil, matchDists,
			"dists/stable/InRelease", "dists/stable/Release.gpg", "dists/stable/main/binary-amd64/Packages.gz",
			"dists/stable/contrib/binary-all/Packages.xz", "dists/bookworm/main/binary-amd64/Packages.diff/Index"),
		route(routeByHash, "/dists/{rest...}/by-hash/SHA256/{hash}", formats.Download, read, nil, matchByHash,
			"dists/stable/main/binary-amd64/by-hash/SHA256/"+hex, "dists/stable/updates/main/binary-amd64/by-hash/SHA256/"+hex),

		route(routePoolGet, "/pool/{path...}", formats.Download, read, nil, matchPool,
			"pool/h/hello/hello_2.10-3_amd64.deb", "pool/libx/libxml2/libxml2-utils_2.9.14%2Bdfsg-1.3_amd64.deb"),
		route(routePoolDelete, "/pool/{path...}", formats.Delete, del, []formats.Class{local}, matchPool,
			"pool/h/hello/hello_2.10-3_amd64.deb"),
		route(routeMemberGet, "/members/{member}/{path...}", formats.Download, read, []formats.Class{virtual}, matchMember,
			"members/deb-local/pool/h/hello/hello_2.10-3_amd64.deb"),

		route(routeFlatRelease, "/{dir...}/{file}", formats.Metadata, read, []formats.Class{remote}, matchFlatRelease,
			"binary/InRelease", "InRelease", "sub/dir/Release.gpg"),
		route(routeFlatIndex, "/{dir...}/{file}", formats.Metadata, read, []formats.Class{remote}, matchFlatIndex,
			"binary/Packages.gz", "Packages.xz", "binary/Packages.diff/Index", "Contents-amd64.gz"),
		route(routeFlatFile, "/{dir...}/{file}", formats.Download, read, []formats.Class{remote}, matchFlatFile,
			"binary/trove-hello_1.0_all.deb", "trove-hello_1.0_all.deb"),

		route(routeSigningKey, "/signing-key.asc", formats.Computed, read, nil, matchSigningKey, "signing-key.asc"),
		route(routeUpload, "/upload/{file}", formats.Upload, deploy, []formats.Class{local}, matchUpload,
			"upload/hello_2.10-3_amd64.deb"),
	}
	return f.forClasses(all)
}

// forClasses keeps the routes of the classes the format registers and narrows their Classes to those; a route none of whose classes
// is registered is left out.
func (f *Format) forClasses(all []formats.Route) []formats.Route {
	have := f.Classes()
	var out []formats.Route
	for _, r := range all {
		if len(r.Classes) > 0 {
			var kept []formats.Class
			for _, c := range r.Classes {
				if hasClass(have, c) {
					kept = append(kept, c)
				}
			}
			if len(kept) == 0 {
				continue
			}
			r.Classes = kept
		}
		out = append(out, r)
	}
	return out
}

func hasClass(list []formats.Class, c formats.Class) bool {
	for _, k := range list {
		if k == c {
			return true
		}
	}
	return false
}

// Match resolves a method and the escaped repository-relative path to one route (R-DEB-003, Q-DEB-39). For the flat root of a remote
// repository, `deb <repo URL> ./`, APT requests `<repo URL>/./InRelease`; the dispatcher hands the format the path as received when its
// first segment is ".", and a leading "./" is read here as the root directory of the flat repository: it matches only the routes of
// flat repositories. Any other dot or empty segment is refused as formats.MatchRoutes refuses it (400 invalid_path).
func (f *Format) Match(method, escapedPath string) (*formats.Route, formats.Params, error) {
	rest, root := strings.CutPrefix(escapedPath, rootFlatPrefix)
	if !root {
		return formats.MatchRoutes(f.routes, method, escapedPath)
	}
	r, p, err := formats.MatchRoutes(f.routes, method, rest)
	if err != nil {
		return nil, nil, err
	}
	if !strings.HasPrefix(r.Name, "deb.flat.") {
		return nil, nil, errs.E(errs.Invalid, errs.CodeInvalidPath, "A leading \"./\" names the root of a flat repository.")
	}
	p[flatRootParam] = "true"
	return r, p, nil
}
