package ansible

import (
	"net/http"
	"slices"

	"github.com/guided-traffic/trove/internal/formats"
)

// The names of the routes (R-ANS-003 to R-ANS-009, R-ANS-020): "ansible.<resource>.<operation>". The request log and the metrics
// name the handler of a request by it, and a class file registers its handler by it (handlers.go).
const (
	routeRoot         = "ansible.root.get"
	routeCollection   = "ansible.collection.get"
	routeVersions     = "ansible.versions.list"
	routeVersion      = "ansible.version.get"
	routeDocs         = "ansible.version.docs"
	routeArtifact     = "ansible.artifact.get"
	routePublish      = "ansible.collection.publish"
	routeImport       = "ansible.import.get"
	routeRolesFind    = "ansible.roles.find"
	routeRoleVersions = "ansible.role.versions"
	routeRoleArchive  = "ansible.role.archive"
)

// buildRoutes is the whole endpoint map of the format, written once with the name of every handler. A route serves the classes its
// Classes name; a route none of whose classes is registered is left out, so the table grows with the classes without an edit (the
// registry of class_*.go files, format.go). A handler is looked up by class and route name (handlers.go); an unregistered one answers
// 501.
//
// Every route declares TokenScheme: the Galaxy clients send a configured token as "Authorization: Token <token>", and the
// authentication stage reads that scheme only where the dispatcher's own match says the route takes it (Q-ANS-4, R-ANS-006, ADR-191).
//
// The publish is served by the local and the virtual class; a remote repository answers 405 (the dispatcher's, with the class named,
// which WriteError turns into the sentence of R-ANS-008). The role routes are served by remote and virtual repositories (R-ANS-014).
func (f *Format) buildRoutes() []formats.Route {
	const (
		read   = formats.ActionRead
		deploy = formats.ActionDeploy
	)
	local, remote, virtual := formats.Local, formats.Remote, formats.Virtual
	route := func(name, method, tmpl string, kind formats.RouteKind, action formats.Action, classes []formats.Class,
		match func(string) (formats.Params, bool), examples ...string,
	) formats.Route {
		return formats.Route{
			Name: name, Method: method, Template: tmpl, Examples: examples, Action: action, Kind: kind, Classes: classes,
			Match: match, Handler: f.op(name), TokenScheme: true,
		}
	}
	all := []formats.Route{
		route(routeRoot, http.MethodGet, "/", formats.Computed, read, nil, matchRoot, "", "/"),
		route(routeCollection, http.MethodGet, "v3/collections/{ns}/{name}", formats.Metadata, read, nil, matchCollection,
			"v3/collections/acme/tools", "v3/collections/acme/tools/", "v3/collections/Community/General/"),
		route(routeVersions, http.MethodGet, "v3/collections/{ns}/{name}/versions", formats.Metadata, read, nil, matchVersions,
			"v3/collections/acme/tools/versions", "v3/collections/acme/tools/versions/"),
		route(routeVersion, http.MethodGet, "v3/collections/{ns}/{name}/versions/{version}", formats.Metadata, read, nil, matchVersion,
			"v3/collections/acme/tools/versions/1.0.0", "v3/collections/acme/tools/versions/1.0.0-rc.1+build.5/"),
		route(routeDocs, http.MethodGet, "v3/collections/{ns}/{name}/versions/{version}/docs-blob", formats.Metadata, read, nil, matchDocs,
			"v3/collections/acme/tools/versions/1.0.0/docs-blob", "v3/collections/acme/tools/versions/1.0.0-rc.1+build.5/docs-blob/"),
		route(routeArtifact, http.MethodGet, "v3/artifacts/collections/{file}", formats.Download, read, nil, matchArtifact,
			"v3/artifacts/collections/acme-tools-1.0.0.tar.gz", "v3/artifacts/collections/acme-tools-1.0.0-rc.1.tar.gz"),
		route(routePublish, http.MethodPost, "v3/artifacts/collections", formats.Upload, deploy, []formats.Class{local, virtual}, matchPublish,
			"v3/artifacts/collections", "v3/artifacts/collections/"),
		route(routeImport, http.MethodGet, "v3/imports/collections/{id}", formats.Metadata, read, []formats.Class{local, virtual}, matchImport,
			"v3/imports/collections/6f1c0000-0000-4000-8000-000000000002", "v3/imports/collections/6f1c0000-0000-4000-8000-000000000002/"),
		route(routeRolesFind, http.MethodGet, "v1/roles", formats.Volatile, read, []formats.Class{remote, virtual}, matchRoleFind,
			"v1/roles", "v1/roles/"),
		route(routeRoleVersions, http.MethodGet, "v1/roles/{id}/versions", formats.Metadata, read, []formats.Class{remote, virtual}, matchRoleVersions,
			"v1/roles/16/versions", "v1/roles/16/versions/"),
		route(routeRoleArchive, http.MethodGet, "v1/roles/archives/{id}/{file}", formats.Download, read, []formats.Class{remote, virtual}, matchRoleArchive,
			"v1/roles/archives/16/v1.0.0.tar.gz"),
	}
	return f.forClasses(all)
}

// forClasses keeps the routes of the classes the format registers and narrows their Classes to those; a route none of whose classes is
// registered is left out.
func (f *Format) forClasses(all []formats.Route) []formats.Route {
	have := f.Classes()
	var out []formats.Route
	for _, r := range all {
		if len(r.Classes) > 0 {
			var kept []formats.Class
			for _, c := range r.Classes {
				if slices.Contains(have, c) {
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

// Match resolves a method and the escaped repository-relative path to one route (R-ANS-003).
func (f *Format) Match(method, escapedPath string) (*formats.Route, formats.Params, error) {
	return formats.MatchRoutes(f.routes, method, escapedPath)
}
