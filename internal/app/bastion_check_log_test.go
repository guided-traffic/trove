package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/observability"
)

// callerStage is an authentication stage that accepts every credential as the user it names.
type callerStage struct{}

func (callerStage) Authenticate(*http.Request, auth.EndpointRules) (auth.Result, error) {
	return auth.Result{
		Principal:  principal.Principal{ID: "u-caller", Kind: principal.KindUser, Name: "checked-caller", Status: principal.StatusActive},
		Credential: auth.Credential{Method: auth.MethodBearer, TokenID: "t-caller"},
	}, nil
}

// TestR_BAS_008_TheCheckLeavesTheRequestLogLineOfTheRegistration: the check runs the caller's credential through
// the endpoint's authentication stage (Q-BAS-66) on a clone of the Bastion's request. The request log line of
// POST /api/v1/bastion/check names the registration that called it, not the caller it checked (Q-BAS-119): the
// stage the check uses records nothing, the chain's authentication does.
func TestR_BAS_008_TheCheckLeavesTheRequestLogLineOfTheRegistration(t *testing.T) {
	a := &App{cfg: &config.Config{}, parts: &wiring{}}
	a.parts.put(reflect.ValueOf(authArea{stage: callerStage{}}))
	rt := &endpointRuntime{cfg: config.ResolvedEndpoint{Name: "default", Default: true}}
	check := a.credentialStage(rt) // what bastionServices hands the check as BastionServices.Authenticate

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	router := httpserver.NewClientRouter()
	router.HandleFunc("POST /api/v1/bastion/check", func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		clone.Header.Set("Authorization", "Bearer trv_pat_caller")
		if p, err := check(clone); err != nil || p.Name != "checked-caller" {
			t.Errorf("the check's stage: %+v %v", p, err)
		}
		w.WriteHeader(http.StatusOK)
	})
	recognize := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := bastionmain.Recognition{Bastion: "partners", BastionID: "b-1", TokenID: "t-bst", Route: true}
			next.ServeHTTP(w, r.WithContext(bastionmain.WithRecognition(r.Context(), rec)))
		})
	}
	chain := httpserver.Chain{
		Router: router, Log: log, AccessLog: observability.RequestLog(log, observability.NewMetrics("web")),
		Authenticate: a.authenticate(rt), Bastion: recognize,
	}
	chain.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/bastion/check", nil))

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(bytes.SplitN(buf.Bytes(), []byte("\n"), 2)[0]), &rec); err != nil {
		t.Fatalf("log %q: %v", buf.String(), err)
	}
	for k, want := range map[string]any{"principal": "partners", "principal_type": "bastion", "principal_id": "b-1", "token_id": "t-bst"} {
		if rec[k] != want {
			t.Errorf("%s = %v, want %v (record %v)", k, rec[k], want, rec)
		}
	}
}
