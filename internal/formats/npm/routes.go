package npm

import (
	"context"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// buildRoutes is the whole endpoint map of R-NPM-001, written once with the name of
// every handler, so that a work package that adds a handler adds a handler file and
// never edits this table (handlers_*.go).
//
// The classes of the table are all classes of the format: until the format lists
// the remote and the virtual class, a repository is local. Writes will then name
// the classes that take them (a remote repository answers 405 npm_class_not_writable,
// a virtual one routes to its deployment target), the reads stay open.
//
// Every route is wrapped by [Format.op], which records the access-log fields of
// R-NPM-005 and counts the request (R-NPM-044).
func (f *Format) buildRoutes() []formats.Route {
	const (
		read   = formats.ActionRead
		deploy = formats.ActionDeploy
		del    = formats.ActionDelete
	)
	route := func(id, method, tmpl string, kind formats.RouteKind, action formats.Action,
		match func(string) (formats.Params, bool), examples []string, h formats.HandlerFunc,
	) formats.Route {
		return formats.Route{
			Name: "npm." + id, Method: method, Template: tmpl, Examples: examples, Action: action, Kind: kind,
			Match: match, Handler: f.op(id, h),
		}
	}
	names := []string{"lodash", "@acme%2fui", "@acme/ui", "%40acme%2Fui"}
	with := func(suffix string) []string {
		out := make([]string, len(names))
		for i, n := range names {
			out[i] = n + suffix
		}
		return out
	}
	return []formats.Route{
		// The registry URL itself answers 200 {} (R-NPM-001).
		route("root", http.MethodGet, "/", formats.Computed, read, matchRoot, []string{""}, f.handleRoot),

		// Documents, versions and tarballs (R-NPM-010, R-NPM-011, R-NPM-014, R-NPM-015).
		route("document.get", http.MethodGet, "/{name}", formats.Metadata, read,
			matchName(tailDocument), names, f.handleDocument),
		route("version.get", http.MethodGet, "/{name}/{version-or-tag}", formats.Metadata, read,
			matchName(tailVersion), with("/1.2.0"), f.handleVersion),
		route("tarball.get", http.MethodGet, "/{name}/-/{file}", formats.Download, read,
			matchName(tailTarball), with("/-/ui-1.2.0.tgz"), f.handleTarball),

		// Publish and the metadata update that shares its path (R-NPM-007, R-NPM-018).
		route("publish", http.MethodPut, "/{name}", formats.Upload, deploy,
			matchName(tailDocument), names, f.handlePublish),

		// Unpublish (R-NPM-017): the revisioned writes need delete and the current rev.
		route("unpublish.put", http.MethodPut, "/{name}/-rev/{rev}", formats.Delete, del,
			matchName(tailRev), with("/-rev/3-abc"), f.handleUnpublishPut),
		route("unpublish.delete", http.MethodDelete, "/{name}/-rev/{rev}", formats.Delete, del,
			matchName(tailRev), with("/-rev/3-abc"), f.handleUnpublishPackage),
		route("unpublish.tarball", http.MethodDelete, "/{name}/-/{file}/-rev/{rev}", formats.Delete, del,
			matchName(tailTarballRev), with("/-/ui-1.2.0.tgz/-rev/3-abc"), f.handleUnpublishTarball),

		// Dist-tags (R-NPM-016): the read is a document, the writes change what clients install.
		route("disttags.get", http.MethodGet, "/-/package/{name}/dist-tags", formats.Metadata, read,
			packageOp(tailDistTags), []string{"-/package/lodash/dist-tags", "-/package/@acme%2fui/dist-tags", "-/package/@acme/ui/dist-tags"}, f.handleDistTags),
		route("disttag.put", http.MethodPut, "/-/package/{name}/dist-tags/{tag}", formats.Admin, deploy,
			packageOp(tailDistTag), []string{"-/package/lodash/dist-tags/next", "-/package/@acme%2fui/dist-tags/next"}, f.handleDistTagSet),
		route("disttag.delete", http.MethodDelete, "/-/package/{name}/dist-tags/{tag}", formats.Admin, deploy,
			packageOp(tailDistTag), []string{"-/package/lodash/dist-tags/next", "-/package/@acme%2fui/dist-tags/next"}, f.handleDistTagDelete),

		// Identity (R-NPM-019).
		route("ping", http.MethodGet, "/-/ping", formats.Computed, read, matchOp("ping"), []string{"-/ping"}, f.handlePing),
		route("whoami", http.MethodGet, "/-/whoami", formats.Computed, read, matchOp("whoami"), []string{"-/whoami"}, f.handleWhoami),

		// What npm defines and trove does not implement (R-NPM-043): 404 for a GET, PUT
		// and DELETE, 501 for a POST, so that npm's fetch layer, which retries a GET
		// and a PUT on a 5xx but never a POST, fails at once. The four routes share one
		// matcher; their names carry the method.
		route("deferred.get", http.MethodGet, "/-/{deferred...}", formats.Computed, read, matchDeferred, deferredExamples(), f.handleDeferred),
		route("deferred.put", http.MethodPut, "/-/{deferred...}", formats.Admin, read, matchDeferred, deferredExamples(), f.handleDeferred),
		route("deferred.delete", http.MethodDelete, "/-/{deferred...}", formats.Admin, read, matchDeferred, deferredExamples(), f.handleDeferred),
		route("deferred.post", http.MethodPost, "/-/{deferred...}", formats.Admin, read, matchDeferred, deferredExamples(), f.handleDeferred),
	}
}

// deferredExamples are paths of the endpoints of R-NPM-043: login, user, token and
// search endpoints, the security, key, attestation and OIDC endpoints, the
// organization, team and package-access endpoints, the external-dependency path,
// staged publishing and the listing of all packages.
func deferredExamples() []string {
	return []string{
		"-/v1/login", "-/v1/search", "-/user/org.couchdb.user:alice", "-/user/token/abc",
		"-/npm/v1/security/advisories/bulk", "-/npm/v1/security/audits/quick", "-/npm/v1/keys", "-/npm/v1/attestations/lodash@1.0.0",
		"-/npm/v1/oidc/token/exchange/package/lodash", "-/npm/v1/tokens", "-/npm/v1/user",
		"-/org/acme/user", "-/team/acme/dev", "-/all", "-/external/npm-remote/lodash", "-/stage/package/lodash",
		"-/package/lodash/access", "-/package/@acme%2fui/visibility", "-/package/@acme/ui/collaborators",
	}
}

// op wraps a handler: it records the access-log fields of the request and counts
// it with its repository class, operation and status (R-NPM-044).
func (f *Format) op(operation string, h formats.HandlerFunc) formats.HandlerFunc {
	return func(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
		recordClientFacts(rq.Request)
		met := f.replicaOf(rq.Host).met
		if met == nil {
			return h(ctx, rq, w)
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		err := h(ctx, rq, sw)
		status := sw.status
		switch {
		case err != nil && !sw.wrote:
			status = errs.Wrap(err, "").Status()
		case status == 0:
			status = http.StatusOK
		}
		met.observe(string(rq.Repo.Class), operation, status, time.Since(start))
		return err
	}
}

// statusWriter remembers the status of the response.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if code >= 200 && !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// handleRoot answers a GET on the registry URL with 200 {} (R-NPM-001).
func (f *Format) handleRoot(_ context.Context, _ *formats.Request, w http.ResponseWriter) error {
	return writeJSON(w, http.StatusOK, struct{}{})
}

// handleDeferred answers the endpoints npm defines and trove does not implement.
func (f *Format) handleDeferred(_ context.Context, rq *formats.Request, _ http.ResponseWriter) error {
	return notImplemented(rq.Method)
}
