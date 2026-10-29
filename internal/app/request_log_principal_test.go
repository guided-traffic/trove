package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/observability"
)

// TestR_OPS_010_RequestLogRecordsThePrincipal drives the real chain order: the
// request log is a stage outside the authentication stage (R-ARCH-005), so the
// record can only learn the caller from the request-scoped holder that the
// stage fills (recordPrincipal), and it must read it after the request.
func TestR_OPS_010_RequestLogRecordsThePrincipal(t *testing.T) {
	cases := []struct {
		name      string
		principal httpserver.Principal
		want      map[string]any
	}{
		{
			"R-OPS-010 a user with a bearer token",
			httpserver.Principal{Kind: httpserver.KindUser, ID: "u-1", Name: "alice", Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "t-1"}},
			map[string]any{"principal": "alice", "principal_type": "user", "principal_id": "u-1", "token_id": "t-1", "auth_method": "bearer"},
		},
		{
			"R-OPS-010 a user with a session and no token",
			httpserver.Principal{Kind: httpserver.KindUser, ID: "u-1", Name: "alice", Credential: httpserver.Credential{Method: httpserver.MethodSession}},
			map[string]any{"principal": "alice", "principal_type": "user", "principal_id": "u-1", "token_id": nil, "auth_method": "session"},
		},
		{
			"R-OPS-010 the break-glass administrator is written breakglass",
			httpserver.Principal{Kind: httpserver.KindBreakGlass, ID: "b-1", Name: "breakglass", Credential: httpserver.Credential{Method: httpserver.MethodSession}},
			map[string]any{"principal": "breakglass", "principal_type": "breakglass", "principal_id": "b-1"},
		},
		{
			"R-OPS-010 a service account",
			httpserver.Principal{Kind: httpserver.KindServiceAccount, ID: "s-1", Name: "sa:ci", Credential: httpserver.Credential{Method: httpserver.MethodBasic, TokenID: "t-2"}},
			map[string]any{"principal": "sa:ci", "principal_type": "service_account", "token_id": "t-2", "auth_method": "basic"},
		},
		{
			"R-OPS-010 an anonymous caller keeps the defaults",
			httpserver.AnonymousPrincipal(),
			map[string]any{"principal": "anonymous", "principal_type": "anonymous", "principal_id": nil, "token_id": nil, "auth_method": "none"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&buf, nil))
			router := httpserver.NewClientRouter()
			router.HandleFunc("GET /api/v1/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			chain := httpserver.Chain{
				Router: router, Log: log,
				AccessLog: observability.RequestLog(log, observability.NewMetrics("web")),
				Authenticate: func(r *http.Request) (httpserver.Principal, error) {
					recordPrincipal(r, tc.principal)
					return tc.principal, nil
				},
			}
			chain.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/ping", nil))

			var rec map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(bytes.SplitN(buf.Bytes(), []byte("\n"), 2)[0]), &rec); err != nil {
				t.Fatalf("log %q: %v", buf.String(), err)
			}
			for k, want := range tc.want {
				if rec[k] != want {
					t.Errorf("%s = %v, want %v (record %v)", k, rec[k], want, rec)
				}
			}
		})
	}
}
