package npm

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

func TestR_NPM_001_RouteTable(t *testing.T) {
	f := New()
	cases := []struct {
		method, path, route string
		name                string
	}{
		{"GET", "", "npm.root", ""},
		{"GET", "lodash", "npm.document.get", "lodash"},
		{"HEAD", "lodash", "npm.document.get", "lodash"},
		{"GET", "@acme%2fui", "npm.document.get", "@acme/ui"},
		{"GET", "@acme/ui", "npm.document.get", "@acme/ui"},
		{"GET", "%40acme%2Fui", "npm.document.get", "@acme/ui"},
		{"GET", "lodash/4.17.21", "npm.version.get", "lodash"},
		{"GET", "@acme/ui/latest", "npm.version.get", "@acme/ui"},
		{"GET", "@acme%2fui/1.2.0-rc.1", "npm.version.get", "@acme/ui"},
		{"GET", "lodash/-/lodash-4.17.21.tgz", "npm.tarball.get", "lodash"},
		{"HEAD", "@acme/ui/-/ui-1.2.0.tgz", "npm.tarball.get", "@acme/ui"},
		{"GET", "@acme%2fui/-/ui-1.2.0.tgz", "npm.tarball.get", "@acme/ui"},
		{"PUT", "lodash", "npm.publish", "lodash"},
		{"PUT", "@acme%2fui", "npm.publish", "@acme/ui"},
		{"PUT", "@acme/ui", "npm.publish", "@acme/ui"},
		{"PUT", "lodash/-rev/3-abc", "npm.unpublish.put", "lodash"},
		{"DELETE", "@acme%2fui/-rev/3-abc", "npm.unpublish.delete", "@acme/ui"},
		{"DELETE", "@acme/ui/-/ui-1.2.0.tgz/-rev/3-abc", "npm.unpublish.tarball", "@acme/ui"},
		{"GET", "-/package/lodash/dist-tags", "npm.disttags.get", "lodash"},
		{"GET", "-/package/@acme%2fui/dist-tags", "npm.disttags.get", "@acme/ui"},
		{"GET", "-/package/@acme/ui/dist-tags", "npm.disttags.get", "@acme/ui"},
		{"PUT", "-/package/@acme/ui/dist-tags/next", "npm.disttag.put", "@acme/ui"},
		{"DELETE", "-/package/lodash/dist-tags/next", "npm.disttag.delete", "lodash"},
		{"GET", "-/ping", "npm.ping", ""},
		{"GET", "-/whoami", "npm.whoami", ""},
		{"POST", "-/v1/login", "npm.deferred.post", ""},
		{"GET", "-/v1/search", "npm.deferred.get", ""},
		{"PUT", "-/user/org.couchdb.user:alice", "npm.deferred.put", ""},
		{"GET", "-/user/org.couchdb.user:alice", "npm.deferred.get", ""},
		{"DELETE", "-/user/token/abc", "npm.deferred.delete", ""},
		{"POST", "-/npm/v1/security/advisories/bulk", "npm.deferred.post", ""},
		{"POST", "-/npm/v1/security/audits/quick", "npm.deferred.post", ""},
		{"POST", "-/npm/v1/oidc/token/exchange/package/%40acme%2fui", "npm.deferred.post", ""},
		{"GET", "-/package/@acme/ui/access", "npm.deferred.get", ""},
		{"GET", "-/org/acme/user", "npm.deferred.get", ""},
		{"POST", "-/stage/package/lodash", "npm.deferred.post", ""},
		{"GET", "-/external/npm-remote/lodash", "npm.deferred.get", ""},
	}
	for _, c := range cases {
		t.Run("R-NPM-001 "+c.method+" "+c.path, func(t *testing.T) {
			r, p, err := f.Match(c.method, c.path)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if r.Name != c.route || p["name"] != c.name {
				t.Fatalf("route %s name %q, want %s %q", r.Name, p["name"], c.route, c.name)
			}
		})
	}
	t.Run("R-NPM-001 a path that is no route is 404, a method that is not allowed is 405 with Allow", func(t *testing.T) {
		for _, c := range []struct {
			method, path string
			status       int
			allow        string
		}{
			{"GET", "-/nope", 404, ""},
			{"GET", "-/package/lodash/unknown", 404, ""},
			{"GET", "lodash/-/a/b", 404, ""},
			{"GET", "lodash/-rev/3-abc", 405, "DELETE, PUT"},
			{"POST", "lodash", 405, "GET, HEAD, PUT"},
			{"DELETE", "-/ping", 405, "GET, HEAD"},
			{"PUT", "-/whoami", 405, "GET, HEAD"},
			{"PATCH", "-/v1/login", 405, "DELETE, GET, HEAD, POST, PUT"},
			{"PUT", "-/package/lodash/dist-tags", 405, "GET, HEAD"},
			{"POST", "", 405, "GET, HEAD"},
			{"GET", "@acme/", 404, ""},
		} {
			_, _, err := f.Match(c.method, c.path)
			e, ok := errs.As(err)
			if !ok || e.Status() != c.status || strings.Join(e.Allow, ", ") != c.allow {
				t.Errorf("%s %q: %v, want %d %q", c.method, c.path, err, c.status, c.allow)
			}
		}
	})
	t.Run("R-NPM-001 R-NPM-002 a name that is none is refused before any route sees it", func(t *testing.T) {
		for _, p := range []string{"a%2525b", "%2e%2e", "a/b/c", "@acme", "@/ui", "@acme/%2e", "-", "a%00b", "a%5cb", "%ff"} {
			for _, m := range []string{"GET", "PUT"} {
				_, _, err := f.Match(m, p)
				if err == nil {
					t.Errorf("%s %q matched", m, p)
					continue
				}
				if k := errs.KindOf(err); k != errs.Invalid && k != errs.NotFound {
					t.Errorf("%s %q: %v", m, p, err)
				}
			}
		}
		_, _, err := f.Match("GET", "a%2525b")
		if e, _ := errs.As(err); e == nil || e.Code != "npm_invalid_name" {
			t.Errorf("a%%2525b: %v", err)
		}
	})
	t.Run("R-NPM-001 the escaped slash stays inside its segment", func(t *testing.T) {
		// "@acme%2fui/-/x.tgz" is the tarball of @acme/ui; "@acme/ui%2f-%2fx.tgz" is not.
		if _, _, err := f.Match("GET", "@acme/ui%2f-%2fx.tgz"); err == nil {
			r, p, _ := f.Match("GET", "@acme/ui%2f-%2fx.tgz")
			t.Errorf("matched %s %v", r.Name, p)
		}
	})
	t.Run("R-NPM-001 every route has a name, a handler and examples that match only itself", func(t *testing.T) {
		if err := formats.Validate(f); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, r := range f.Routes() {
			if !strings.HasPrefix(r.Name, "npm.") || seen[r.Name] {
				t.Errorf("route name %q", r.Name)
			}
			seen[r.Name] = true
		}
	})
	t.Run("R-NPM-001 the actions of the coarse check follow R-AUTHZ-007", func(t *testing.T) {
		want := map[string]formats.Action{
			"npm.document.get": formats.ActionRead, "npm.version.get": formats.ActionRead, "npm.tarball.get": formats.ActionRead,
			"npm.disttags.get": formats.ActionRead, "npm.ping": formats.ActionRead, "npm.whoami": formats.ActionRead,
			"npm.publish": formats.ActionDeploy, "npm.disttag.put": formats.ActionDeploy, "npm.disttag.delete": formats.ActionDeploy,
			"npm.unpublish.put": formats.ActionDelete, "npm.unpublish.delete": formats.ActionDelete, "npm.unpublish.tarball": formats.ActionDelete,
		}
		for _, r := range f.Routes() {
			if a, ok := want[r.Name]; ok && r.Action != a {
				t.Errorf("%s: action %s, want %s", r.Name, r.Action, a)
			}
		}
	})
}

func TestR_NPM_001_RootAnswersAnEmptyObject(t *testing.T) {
	f := New()
	for _, m := range []string{"GET", "HEAD"} {
		w := serve(t, f, request(t, f, &fakeHost{}, m, "", ""))
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{}" && m == "GET" {
			t.Errorf("%s: %d %q", m, w.Code, w.Body.String())
		}
	}
}

func TestR_NPM_043_UnimplementedEndpointsAnswerPredictably(t *testing.T) {
	f := New()
	for _, p := range deferredExamples() {
		for _, m := range []string{"GET", "PUT", "DELETE", "POST"} {
			want := 404
			if m == "POST" {
				want = 501
			}
			t.Run("R-NPM-043 "+m+" "+p, func(t *testing.T) {
				w := serve(t, f, request(t, f, &fakeHost{}, m, p, ""))
				if w.Code != want {
					t.Fatalf("status %d, want %d (%s)", w.Code, want, w.Body.String())
				}
				b := errorBodyOf(t, w)
				if want == 501 && b.Code != "npm_not_implemented" {
					t.Fatalf("code %q", b.Code)
				}
			})
		}
	}
}

func TestR_NPM_005_ErrorShapeOverAllRoutes(t *testing.T) {
	f := New()
	denied := &fakeHost{authorize: func(*formats.Request, formats.Target) error {
		return errs.E(errs.Unauthenticated, errs.CodeCredentialsRequired, "credentials are required", errs.WithChallenge(`Bearer realm="trove"`))
	}}
	for _, r := range f.Routes() {
		for _, ex := range r.Examples {
			t.Run("R-NPM-005 "+r.Name+" "+ex, func(t *testing.T) {
				rq := request(t, f, denied, r.Method, ex, "{}")
				w := serve(t, f, rq)
				if w.Code < 400 {
					return // the handler answers without an error (the root, nothing to refuse)
				}
				b := errorBodyOf(t, w)
				if b.Error != b.Message {
					t.Fatalf("error %q and message %q differ", b.Error, b.Message)
				}
				if h := strings.ToLower(w.Header().Get("WWW-Authenticate")); strings.Contains(h, "otp") || strings.Contains(h, "ipaddress") {
					t.Fatalf("WWW-Authenticate %q", h)
				}
				if w.Code == 401 && w.Header().Get("WWW-Authenticate") == "" {
					t.Fatal("a 401 without a challenge")
				}
				if w.Header().Get("Content-Disposition") != "" {
					t.Fatalf("an error carries Content-Disposition %q", w.Header().Get("Content-Disposition"))
				}
			})
		}
	}
}

func TestR_NPM_005_EveryStatusOfTheErrorTable(t *testing.T) {
	f := New()
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{errs.E(errs.Invalid, errs.CodeNPMInvalidName, "bad name"), 400, "npm_invalid_name"},
		{errs.E(errs.Invalid, errs.CodeNPMInvalidVersion, "bad version"), 400, "npm_invalid_version"},
		{errs.E(errs.Invalid, errs.CodeNPMInvalidTag, "bad tag"), 400, "npm_invalid_tag"},
		{errs.E(errs.Invalid, errs.CodeNPMBadPublish, "bad publish"), 400, "npm_bad_publish"},
		{errs.E(errs.Invalid, errs.CodeNPMManifestMismatch, "mismatch"), 400, "npm_manifest_mismatch"},
		{errs.E(errs.MethodNotAllowed, errs.CodeNPMClassNotWritable, "remote", errs.WithAllow("GET", "HEAD")), 405, "npm_class_not_writable"},
		{errs.E(errs.Conflict, errs.CodeNPMVersionExists, "exists"), 409, "npm_version_exists"},
		{errs.E(errs.Conflict, errs.CodeNPMVersionUnpublished, "gone"), 409, "npm_version_unpublished"},
		{errs.E(errs.Conflict, errs.CodeNPMStaleRev, "stale"), 409, "npm_stale_rev"},
		{errs.E(errs.TooLarge, errs.CodeNPMTooLarge, "big"), 413, "npm_too_large"},
		{errs.E(errs.NotImplemented, errs.CodeNPMNotImplemented, "no"), 501, "npm_not_implemented"},
		{errs.E(errs.NotImplemented, errs.CodeNPMAuditUnavailable, "no"), 501, "npm_audit_unavailable"},
		{errs.E(errs.UpstreamFailed, errs.CodeNPMInvalidUpstreamDocument, "bad doc"), 502, "npm_invalid_upstream_document"},
		{errs.E(errs.InsufficientStorage, errs.CodeInsufficientStorage, "quota"), 507, "insufficient_storage"},
		{errs.E(errs.Forbidden, errs.CodePackageOwnedByOtherOwner, "owned"), 403, "package_owned_by_other_owner"},
		{errs.E(errs.Unavailable, errs.CodeUnavailable, "later", errs.WithRetryAfter(7e9)), 503, "unavailable"},
		{errs.E(errs.RateLimited, errs.CodeRateLimited, "slow", errs.WithRetryAfter(2e9)), 429, "rate_limited"},
		{context.DeadlineExceeded, 500, "internal"},
	}
	for _, c := range cases {
		t.Run("R-NPM-005 "+c.code, func(t *testing.T) {
			rq := request(t, f, &fakeHost{}, "GET", "-/ping", "")
			w := httpRecorder()
			f.WriteError(w, rq.Request, c.err)
			if w.Code != c.status {
				t.Fatalf("status %d, want %d", w.Code, c.status)
			}
			if b := errorBodyOf(t, w); b.Code != c.code {
				t.Fatalf("code %q, want %q", b.Code, c.code)
			}
			switch c.status {
			case 503, 429:
				if w.Header().Get("Retry-After") == "" {
					t.Fatal("no Retry-After")
				}
			case 405:
				if w.Header().Get("Allow") != "GET, HEAD" {
					t.Fatalf("Allow %q", w.Header().Get("Allow"))
				}
			}
		})
	}
}

func TestR_NPM_004_NoOneTimePasswordChallenge(t *testing.T) {
	f := New()
	for _, c := range []struct{ in, want string }{
		{"", `Basic realm="trove"`},
		{`Bearer realm="trove"`, `Bearer realm="trove"`},
		{`Basic realm="trove"`, `Basic realm="trove"`},
		{`Bearer realm="trove", error="invalid_token"`, `Bearer realm="trove", error="invalid_token"`},
		{`OTP realm="trove"`, `Basic realm="trove"`},
		{`Bearer ipaddress="1.2.3.4"`, `Basic realm="trove"`},
		{`Basic realm="trove", otp`, `Basic realm="trove"`},
	} {
		rq := request(t, f, &fakeHost{}, "GET", "-/ping", "")
		w := httpRecorder()
		opts := []errs.Option{}
		if c.in != "" {
			opts = append(opts, errs.WithChallenge(c.in))
		}
		f.WriteError(w, rq.Request, errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "no credential", opts...))
		if got := w.Header().Get("WWW-Authenticate"); got != c.want || w.Code != 401 {
			t.Errorf("challenge %q: %d %q, want %q", c.in, w.Code, got, c.want)
		}
	}
}

func TestR_NPM_001_RouteKindsKeepTheContentRules(t *testing.T) {
	f := New()
	// Only a route of kind Download, Metadata or Computed may return package-derived
	// bytes (R-ARCH-007, kit check 7); the writes answer with a status and a small JSON.
	for _, r := range f.Routes() {
		switch r.Method {
		case http.MethodPut, http.MethodDelete, http.MethodPost:
			if r.Kind.ServesContent() {
				t.Errorf("%s (%s) is a write and has the kind %s", r.Name, r.Method, r.Kind)
			}
		}
	}
	if !slices.Contains([]string{"download"}, mustRoute(t, f, "GET", "lodash/-/lodash-1.0.0.tgz").Kind.String()) {
		t.Error("the tarball route is not a download")
	}
}

func mustRoute(t *testing.T, f *Format, method, path string) *formats.Route {
	t.Helper()
	r, _, err := f.Match(method, path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
