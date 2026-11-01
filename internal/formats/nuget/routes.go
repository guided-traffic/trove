package nuget

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// The names of the routes (R-NUGET-028): the handler name of the metrics and the request
// log, "nuget.<resource>" and, after a dot, the operation.
const (
	routeIndex             = "nuget.index"
	routeFlatIndex         = "nuget.flatcontainer.index"
	routeNupkg             = "nuget.flatcontainer.nupkg"
	routeNuspec            = "nuget.flatcontainer.nuspec"
	routeEmbedded          = "nuget.flatcontainer.embedded"
	routeRegistrationIndex = "nuget.registration.index"
	routeRegistrationPage  = "nuget.registration.page"
	routeRegistrationLeaf  = "nuget.registration.leaf"
	routeSearch            = "nuget.search"
	routeAutocomplete      = "nuget.autocomplete"
	routePush              = "nuget.push"
	routeDelete            = "nuget.delete"
	routeRelist            = "nuget.relist"
	routeSymbolPush        = "nuget.symbolpush"
	routeSymbols           = "nuget.symbols"
)

// buildRoutes is the whole endpoint map of R-NUGET-001, written once with the name of every
// handler, so that a work package that adds a handler moves its method out of handlers_stub.go
// into a file of its own and never edits this table.
//
// A route name is the handler name of R-NUGET-028 ("nuget.index", "nuget.flatcontainer", …)
// and, after a dot, the operation: "nuget.flatcontainer.nupkg". The request log and the
// metrics name the handler of a request by it.
//
// The writes are Classes local, so that a remote repository answers 405 and the dispatcher
// routes a write addressed to a virtual repository to its deployment target (R-NUGET-019).
// The reads stay open to every class the format supports. Every write is an API-key route:
// the authentication stage examines X-NuGet-ApiKey on it, and on no other (R-NUGET-012).
//
// Every route is wrapped by [Format.op], which records the access-log fields of R-NUGET-002.
func (f *Format) buildRoutes() []formats.Route {
	const (
		read   = formats.ActionRead
		deploy = formats.ActionDeploy
		del    = formats.ActionDelete
	)
	route := func(name, method, tmpl string, kind formats.RouteKind, action formats.Action,
		match func(string) (formats.Params, bool), examples []string, h formats.HandlerFunc,
	) formats.Route {
		r := formats.Route{
			Name: name, Method: method, Template: tmpl, Examples: examples, Action: action, Kind: kind,
			Match: match, Handler: f.op(h),
		}
		if action != read {
			r.Classes = []formats.Class{formats.Local}
			r.APIKey = true
		}
		return r
	}
	const (
		pkg  = "acme.widgets"
		ver  = "1.2.3"
		leaf = "v3/flatcontainer/" + pkg + "/" + ver + "/"
	)
	hiveExamples := func(suffix string) []string {
		out := make([]string, 0, len(hives()))
		for _, h := range hives() {
			out = append(out, "v3/"+h+"/"+pkg+"/"+suffix)
		}
		return out
	}
	return []formats.Route{
		// The service index at its three spellings (R-NUGET-001, R-NUGET-002).
		route(routeIndex, http.MethodGet, "/v3[/[index.json]]", formats.Computed, read,
			matchIndex, []string{"v3", "v3/", "v3/index.json"}, f.handleIndex),

		// The flat container (R-NUGET-008).
		route(routeFlatIndex, http.MethodGet, "/v3/flatcontainer/{id}/index.json", formats.Metadata, read,
			matchFlatIndex, []string{"v3/flatcontainer/" + pkg + "/index.json", "v3/flatcontainer/Acme.Widgets/index.json"}, f.handleFlatIndex),
		route(routeNupkg, http.MethodGet, "/v3/flatcontainer/{id}/{version}/{id}.{version}.nupkg", formats.Download, read,
			flatFile(hasSuffixFold(nupkgSuffix)), []string{leaf + pkg + "." + ver + ".nupkg", "v3/flatcontainer/ACME.Widgets/1.2.3.0/acme.widgets.1.2.3.nupkg"}, f.handleNupkg),
		route(routeNuspec, http.MethodGet, "/v3/flatcontainer/{id}/{version}/{id}.nuspec", formats.Download, read,
			flatFile(hasSuffixFold(nuspecSuffix)), []string{leaf + pkg + ".nuspec"}, f.handleNuspec),
		route(routeEmbedded, http.MethodGet, "/v3/flatcontainer/{id}/{version}/{icon|license|readme}", formats.Download, read,
			flatFile(isEmbedded), []string{leaf + "icon", leaf + "readme", leaf + "license"}, f.handleEmbedded),

		// The registrations, in the three hives (R-NUGET-009).
		route(routeRegistrationIndex, http.MethodGet, "/v3/{hive}/{id}/index.json", formats.Metadata, read,
			matchRegistrationIndex, hiveExamples("index.json"), f.handleRegistrationIndex),
		route(routeRegistrationPage, http.MethodGet, "/v3/{hive}/{id}/page/{lower}/{upper}.json", formats.Metadata, read,
			matchRegistrationPage, hiveExamples("page/1.0.0/2.0.0.json"), f.handleRegistrationPage),
		route(routeRegistrationLeaf, http.MethodGet, "/v3/{hive}/{id}/{version}.json", formats.Metadata, read,
			matchRegistrationLeaf, hiveExamples("1.2.3.json"), f.handleRegistrationLeaf),

		// Search and autocomplete (R-NUGET-010, R-NUGET-011): volatile, never cached.
		route(routeSearch, http.MethodGet, "/v3/search", formats.Volatile, read,
			matchResource(resSearch), []string{"v3/search"}, f.handleSearch),
		route(routeAutocomplete, http.MethodGet, "/v3/autocomplete", formats.Volatile, read,
			matchResource(resAutocomplete), []string{"v3/autocomplete"}, f.handleAutocomplete),

		// Push, unlist, relist and the symbol push (R-NUGET-006, R-NUGET-007, R-NUGET-020).
		route(routePush, http.MethodPut, "/v3/package", formats.Upload, deploy,
			matchResource(resPackage), []string{"v3/package"}, f.handlePush),
		route(routeDelete, http.MethodDelete, "/v3/package/{id}/{version}", formats.Delete, del,
			matchPackageVersion, []string{"v3/package/" + pkg + "/" + ver, "v3/package/Acme.Widgets/1.2.3.0"}, f.handleDelete),
		route(routeRelist, http.MethodPost, "/v3/package/{id}/{version}", formats.Admin, deploy,
			matchPackageVersion, []string{"v3/package/" + pkg + "/" + ver, "v3/package/Acme.Widgets/1.2.3.0"}, f.handleRelist),
		route(routeSymbolPush, http.MethodPut, "/v3/symbolpackage", formats.Upload, deploy,
			matchResource(resSymbolPackage), []string{"v3/symbolpackage"}, f.handleSymbolPush),

		// The symbol server (R-NUGET-021, P1): the key is <file>/<index>/<file>.
		route(routeSymbols, http.MethodGet, "/v3/symbols/{key...}", formats.Download, read,
			matchSymbolKey, []string{"v3/symbols/acme.widgets.pdb/0123456789abcdef0123456789abcdefFFFFFFFF/acme.widgets.pdb"}, f.handleSymbols),
	}
}

// op wraps a handler: it records the access-log fields of the request (R-NUGET-002). The
// counters of R-NUGET-028 are added where the handlers that feed them are written.
func (f *Format) op(h formats.HandlerFunc) formats.HandlerFunc {
	return func(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
		recordClientFacts(rq.Request)
		return h(ctx, rq, w)
	}
}
