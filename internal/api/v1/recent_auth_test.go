package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// staleSessions resolves every cookie to a session without a recent
// authentication: the zero Info was never authenticated on this replica.
type staleSessions struct{ SessionService }

func (staleSessions) Validate(context.Context, string) (session.Info, error) {
	return session.Info{}, nil
}

func sessionCaller(*http.Request) (httpserver.Principal, error) {
	return httpserver.Principal{
		Kind: httpserver.KindUser, ID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10", Name: "alice",
		Credential: httpserver.Credential{Method: httpserver.MethodSession},
	}, nil
}

func tokenCaller(*http.Request) (httpserver.Principal, error) {
	return httpserver.Principal{
		Kind: httpserver.KindUser, ID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10", Name: "alice",
		Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a11"},
	}, nil
}

// recentAuthOperations is the table of R-API-007 as far as the operations exist
// in this build. The command-line login, CI identity and configuration-as-code
// rows join when those operations do.
var recentAuthOperations = []string{
	"bindRole", "createExposureRule", "createPermissionPolicy", "createServiceAccountToken", "createToken",
	"deleteExposureRule", "deletePermissionPolicy", "deleteRepository", "patchExposureRule", "patchPermissionPolicy",
	"replaceExposureRule", "replacePermissionPolicy", "rotateToken", "unbindRole", "updateAuthSettings",
}

func TestR_API_007_DocumentDeclaresRecentAuth(t *testing.T) {
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("R-API-007 exactly the operations of the table declare x-trove-recent-auth", func(t *testing.T) {
		var got []string
		for _, op := range doc.Operations() {
			if op.RecentAuth {
				got = append(got, op.ID)
			}
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(recentAuthOperations, ",") {
			t.Fatalf("declared:\n%v\nwant:\n%v", got, recentAuthOperations)
		}
	})
	t.Run("R-API-007 x-trove-recent-auth: false is refused at load", func(t *testing.T) {
		raw := strings.Replace(string(EmbeddedYAML()), "x-trove-recent-auth: true", "x-trove-recent-auth: false", 1)
		if _, err := LoadDocument([]byte(raw)); err == nil || !strings.Contains(err.Error(), "x-trove-recent-auth") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_API_007_SharedStepEnforcesRecentAuth(t *testing.T) {
	withSessions := func(o *Options) { o.Auth.Sessions = staleSessions{} }
	call := func(s setup, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.handler.ServeHTTP(w, req)
		return w
	}
	problemCode := func(t *testing.T, w *httptest.ResponseRecorder) string {
		t.Helper()
		var p struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatalf("body %q: %v", w.Body.String(), err)
		}
		return p.Code
	}
	cases := []struct{ name, method, path string }{
		{"creating a token", "POST", "/api/v1/tokens"},
		{"changing authentication settings", "PATCH", "/api/v1/settings/auth"},
		{"deleting a permission policy", "DELETE", "/api/v1/permission-policies/p"},
		{"binding a role", "PUT", "/api/v1/role-bindings/admin/user:alice"},
		{"deleting a repository", "DELETE", "/api/v1/repositories/npm-old"},
		{"deleting an exposure rule", "DELETE", "/api/v1/endpoints/default/exposure-rules/r"},
	}
	for _, tc := range cases {
		t.Run("R-API-007 a session without a recent sign-in is refused on "+tc.name, func(t *testing.T) {
			s := newSetup(t, withSessions, sessionCaller)
			w := call(s, tc.method, tc.path, "{}")
			if w.Code != 401 || problemCode(t, w) != "reauthentication_required" {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
		t.Run("R-API-007 a token is not asked on "+tc.name, func(t *testing.T) {
			s := newSetup(t, withSessions, tokenCaller)
			if w := call(s, tc.method, tc.path, "{}"); w.Code == 401 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	t.Run("R-API-007 the permission is decided first: a caller who may not act gets 403", func(t *testing.T) {
		s := newSetup(t, func(o *Options) {
			withSessions(o)
			o.Access = accessFunc(func(context.Context, string) error { return errs.E(errs.Forbidden, errs.CodeForbidden, "no") })
		}, sessionCaller)
		if w := call(s, "DELETE", "/api/v1/repositories/npm-old", ""); w.Code != 403 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("R-API-007 an operation that does not declare it is not asked", func(t *testing.T) {
		s := newSetup(t, withSessions, sessionCaller)
		if w := call(s, "GET", "/api/v1/tokens", ""); w.Code == 401 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("R-API-007 a replica without a session store refuses with 503, not an unchecked pass", func(t *testing.T) {
		s := newSetup(t, nil, sessionCaller)
		if w := call(s, "POST", "/api/v1/tokens", "{}"); w.Code != 503 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
}
