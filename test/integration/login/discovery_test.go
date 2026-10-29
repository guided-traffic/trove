//go:build integration

package login

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

func TestR_AUTH_004_Discovery(t *testing.T) {
	e := newEnv(t)
	auth := e.defaultAuth()
	auth.OIDC.Providers["down"] = e.provider(func(p *config.AuthOIDCProvider) {
		p.DisplayName, p.Issuer = "Unreachable", "https://127.0.0.1:1"
	})
	// The replica is built without waiting for discovery: a provider that cannot
	// be reached must not hold start-up back.
	start := time.Now()
	rep := e.addReplicaNoWait("web-0", auth)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("start-up waited %s for discovery", took)
	}

	t.Run("R-AUTH-004 an unreachable issuer does not block start-up; its start route answers 503 provider_unavailable and the reachable one logs in", func(t *testing.T) {
		resp, body := e.browser().get(e.baseURL + "/auth/oidc/down/start")
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "provider_unavailable") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		if !eventually(t, 10*time.Second, func() bool { p, _ := rep.login.Provider("corp"); return p.Status().Available }) {
			t.Fatal("corp was never discovered")
		}
		e.browser().mustLogin("corp")
		for _, p := range rep.login.LoginOptions(false).Providers {
			if p.ID == "down" && p.Available {
				t.Fatal("the unreachable provider is listed as available")
			}
		}
	})

	t.Run("R-AUTH-004 an issuer whose discovery document names another issuer is unusable and the status names the mismatch", func(t *testing.T) {
		e.idp.set(func(f *fakeIDP) { f.issuerInDoc = "https://other.example.com" })
		defer e.idp.set(func(f *fakeIDP) { f.issuerInDoc = "" })
		p, _ := rep.login.Provider("corp")
		_ = p.Refresh(context.Background())
		st := p.Status()
		if st.Available || !strings.Contains(st.LastError, "names issuer") {
			t.Fatalf("%+v", st)
		}
		resp, _ := e.browser().get(e.baseURL + "/auth/oidc/corp/start")
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("start of an unusable provider: %d", resp.StatusCode)
		}
	})
}
