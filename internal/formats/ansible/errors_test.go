package ansible

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

func writeError(t *testing.T, method, target string, err error) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	New().WriteError(w, httptest.NewRequest(method, target, nil), err)
	return w
}

type v3Body struct {
	Errors []struct{ Status, Code, Title, Detail string } `json:"errors"`
}

// TestR_ANS_007_ErrorsHaveTheGalaxyShapeOfTheirRoute: v3 and the root render errors[] with status, code, title and detail; v1 renders
// {"default": …}; the code is the registry code without the format prefix, and the kinds a Galaxy names its own way keep their names.
func TestR_ANS_007_ErrorsHaveTheGalaxyShapeOfTheirRoute(t *testing.T) {
	const v3, v1 = "/api/ansible/web/ansible-local/v3/collections/nope/nope/", "/api/ansible/web/ansible-local/v1/roles/1/versions/"
	for _, tc := range []struct {
		name         string
		method, path string
		err          error
		status       int
		code         string
		detail       string
	}{
		{"not found", "GET", v3, errs.E(errs.NotFound, errs.CodeNotFound, "The collection nope.nope does not exist."), 404, "not_found", "The collection nope.nope does not exist."},
		{"unauthenticated", "GET", v3, errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "Sign in."), 401, "not_authenticated", "Sign in."},
		{"forbidden", "POST", v3, errs.E(errs.Forbidden, errs.CodeForbidden, "The token may not deploy."), 403, "permission_denied", "The token may not deploy."},
		{"invalid name", "POST", v3, errs.E(errs.Unprocessable, errs.CodeAnsibleInvalidName, "The namespace is invalid."), 422, "invalid_name", "The namespace is invalid."},
		{"invalid version", "POST", v3, errs.E(errs.Unprocessable, errs.CodeAnsibleInvalidVersion, "The version is invalid."), 422, "invalid_version", "The version is invalid."},
		{"version exists", "POST", v3, errs.E(errs.Conflict, errs.CodeAnsibleVersionExists, "1.0.0 exists."), 409, "version_exists", "1.0.0 exists."},
		{"version tombstoned", "POST", v3, errs.E(errs.Conflict, errs.CodeAnsibleVersionTombstoned, "1.0.0 was deleted."), 409, "version_tombstoned", "1.0.0 was deleted."},
		{"sha256 mismatch", "POST", v3, errs.E(errs.Invalid, errs.CodeAnsibleSHA256Mismatch, "The digest differs."), 400, "sha256_mismatch", "The digest differs."},
		{"invalid request", "POST", v3, errs.E(errs.Invalid, errs.CodeAnsibleInvalidRequest, "The part file is missing."), 400, "invalid_request", "The part file is missing."},
		{"invalid archive", "POST", v3, errs.E(errs.Unprocessable, errs.CodeAnsibleInvalidArchive, "A link member."), 422, "invalid_archive", "A link member."},
		{"too large", "POST", v3, errs.E(errs.TooLarge, errs.CodeTooLarge, "Over the limit."), 413, "payload_too_large", "Over the limit."},
		{"no deploy target", "POST", v3, errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget, "No target."), 405, "no_deploy_target", "No target."},
		{"upstream failed", "GET", v3, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream answered 500."), 502, "upstream_unavailable", "The upstream answered 500."},
		{"unavailable", "GET", v3, errs.E(errs.Unavailable, errs.CodeUnavailable, "Try later."), 503, "upstream_unavailable", "Try later."},
		{"not implemented", "GET", v3, errs.E(errs.NotImplemented, errs.CodeAnsibleNotImplemented, "Not yet."), 501, "not_implemented", "Not yet."},
		{"an error that is none of ours", "GET", v3, errs.E(errs.Internal, errs.CodeInternal, "secret detail"), 500, "internal", "The server failed to process the request."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := writeError(t, tc.method, tc.path, tc.err)
			if w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status %d, content type %q", w.Code, w.Header().Get("Content-Type"))
			}
			var b v3Body
			if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil || len(b.Errors) != 1 {
				t.Fatalf("body %s: %v", w.Body.String(), err)
			}
			e := b.Errors[0]
			if e.Code != tc.code || e.Detail != tc.detail || e.Status != strconv.Itoa(tc.status) || e.Title == "" || e.Title[len(e.Title)-1] != '.' {
				t.Errorf("error = %+v, want code %s detail %q status %d", e, tc.code, tc.detail, tc.status)
			}
		})
	}
	t.Run("the v1 shape", func(t *testing.T) {
		w := writeError(t, "GET", v1, errs.E(errs.NotFound, errs.CodeNotFound, "No such role."))
		var b struct{ Default string }
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil || w.Code != 404 || b.Default != "No such role." {
			t.Fatalf("%d %s %v", w.Code, w.Body.String(), err)
		}
	})
}

func TestR_ANS_007_ChallengeAllowAndRetryAfter(t *testing.T) {
	const p = "/api/ansible/web/ansible-local/v3/collections/a/b/"
	w := writeError(t, "GET", p, errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "x", errs.WithChallenge(`Bearer realm="trove"`)))
	if got := w.Header().Get("WWW-Authenticate"); got != `Basic realm="trove"` {
		t.Errorf("WWW-Authenticate = %q: a Galaxy client that retries on a challenge sends Basic", got)
	}
	w = writeError(t, "DELETE", p, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "x", errs.WithAllow("GET", "HEAD")))
	if got := w.Header().Get("Allow"); got != "GET, HEAD" || w.Code != 405 {
		t.Errorf("Allow = %q, status %d", got, w.Code)
	}
	w = writeError(t, "GET", p, errs.E(errs.Unavailable, errs.CodeUnavailable, "x", errs.WithRetryAfter(90*time.Second)))
	if got := w.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want the bound of 30", got)
	}
	w = writeError(t, "HEAD", p, errs.E(errs.NotFound, errs.CodeNotFound, "x"))
	if w.Code != 404 || w.Body.Len() != 0 {
		t.Errorf("HEAD: %d with %d bytes", w.Code, w.Body.Len())
	}
}

// TestR_ANS_014_ALocalRepositoryHasNoRoleAPI: a request below v1/ that no route of a local repository accepts answers the 404 of a
// missing API in the v1 shape, with the sentence that says why (also when the dispatcher refused the class with a 405).
func TestR_ANS_014_ALocalRepositoryHasNoRoleAPI(t *testing.T) {
	const p = "/api/ansible/web/ansible-local/v1/roles/"
	for name, err := range map[string]error{
		"no route": errs.E(errs.NotFound, errs.CodeNotFound, noRoute),
		"the dispatcher's refusal of the class": errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "A local repository does not serve this request.",
			errs.WithAllow("GET"), errs.WithExtension("repositoryClass", "local")),
	} {
		t.Run(name, func(t *testing.T) {
			w := writeError(t, "GET", p, err)
			var b struct{ Default string }
			if json.Unmarshal(w.Body.Bytes(), &b) != nil || w.Code != 404 || b.Default != detailNoRoles || w.Header().Get("Allow") != "" {
				t.Fatalf("%d %s Allow %q", w.Code, w.Body.String(), w.Header().Get("Allow"))
			}
		})
	}
}

// TestR_ANS_008_APublishToARemoteSaysWhy: the 405 the dispatcher gives a POST to a remote repository carries the reason in detail.
func TestR_ANS_008_APublishToARemoteSaysWhy(t *testing.T) {
	err := errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "A remote repository does not serve this request.", errs.WithAllow("GET"),
		errs.WithExtension("repositoryClass", "remote"))
	w := writeError(t, "POST", "/api/ansible/web/galaxy/v3/artifacts/collections/", err)
	var b v3Body
	if json.Unmarshal(w.Body.Bytes(), &b) != nil || w.Code != 405 || b.Errors[0].Detail != detailRemotePublish || w.Header().Get("Allow") != "GET" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestR_ANS_007_BelowV1(t *testing.T) {
	for p, want := range map[string]bool{
		"/api/ansible/p/r/v1": true, "/api/ansible/p/r/v1/": true, "/api/ansible/p/r/v1/roles/": true, "/api/ansible/p/r/v3/collections/": false,
		"/api/ansible/p/r/": false, "/api/ansible/p/r/v10/x": false, "/api/ansible/p/r": false, "/api/npm/p/r/v1/x": false, "/v1/x": false,
	} {
		if got := belowV1(httptest.NewRequest("GET", p, nil)); got != want {
			t.Errorf("belowV1(%q) = %v, want %v", p, got, want)
		}
	}
}
