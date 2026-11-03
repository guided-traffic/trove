//go:build integration

package bastion

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func gauge(t *testing.T, r *replica, name string) float64 {
	t.Helper()
	return metricValue(t, r.metrics(t), name)
}

func waitGauge(t *testing.T, r *replica, name string, want float64) {
	t.Helper()
	testenv.WaitFor(t, name+" = "+itoa(int64(want)), 30*time.Second, func() bool { return gauge(t, r, name) == want })
}

// TestR_BAS_027_TheMainInstallationUnreachable: the link is cut. Inside the period an answer lives on; inside the
// grace period a caller the replica knows is served from the cache; a token it has not seen and a package it has
// not cached get 503 main_unreachable with Retry-After; past the period and the grace period nothing is served;
// no request is sent to the main installation while it is down; when it is back, service resumes.
func TestR_BAS_027_TheMainInstallationUnreachable(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	b := w.newBastion(t, bastionOptions{Settings: "  answers:\n    period: 30s\n    grace_period: 60s\n"}).wait()
	r := b.reps[0]
	known := callOpts{token: w.full.token, addr: "198.51.100.51"}
	tgz := tarballPath(npmAll, "@acme/sdk", "1.0.0")
	cold := tarballPath(npmAll, "@acme/sdk", "2.0.0")
	if got := r.do(t, "GET", tgz, known); got.Status != http.StatusOK {
		t.Fatalf("warm: %d", got.Status)
	}
	unseen := w.newReader(npmAll, npmLocal, npmRemote)

	w.front.SetDown(true)
	waitGauge(t, r, "trove_bastion_main_reachable", 0)
	mark := w.front.Mark()

	b.clk.Advance(45 * time.Second) // past the period, inside the grace period
	if got := r.do(t, "GET", tgz, known); got.Status != http.StatusOK {
		t.Fatalf("a known caller inside the grace period: %d %s", got.Status, got.Body)
	}
	for name, c := range map[string]struct {
		path string
		o    callOpts
	}{"unseen token": {tgz, callOpts{token: unseen.token, addr: "198.51.100.52"}}, "cold package": {cold, known}} {
		got := r.do(t, "GET", c.path, c.o)
		if got.Status != http.StatusServiceUnavailable || got.Code() != "main_unreachable" || got.Header.Get("Retry-After") != "30" {
			t.Fatalf("%s: %d %s, Retry-After %q", name, got.Status, got.Code(), got.Header.Get("Retry-After"))
		}
	}
	b.clk.Advance(60 * time.Second) // past the period and the grace period
	if got := r.do(t, "GET", tgz, known); got.Status != http.StatusServiceUnavailable {
		t.Fatalf("past period and grace period: %d, want 503", got.Status)
	}
	if n := len(w.front.Since(mark)); n != 0 {
		t.Fatalf("%d requests were recorded at the main installation while the link was cut", n)
	}

	// The main installation is back: the replica reconnects and serves again.
	w.front.SetDown(false)
	waitGauge(t, r, "trove_bastion_main_reachable", 1)
	testenv.WaitFor(t, "service after the outage", 30*time.Second, func() bool {
		return r.do(t, "GET", tgz, known).Status == http.StatusOK
	})
	if got := r.do(t, "GET", cold, known); got.Status != http.StatusOK {
		t.Fatalf("cold package after the outage: %d", got.Status)
	}
}

// TestR_BAS_009_R_BAS_028_InterfaceMismatch: a main installation of another interface version is the main
// installation unreachable, with the mismatch shown in the metrics, in both directions. The main installation
// here is the real one; the front stands for the other version on the wire (Q-BAS-105): in one run it rewrites
// the version the Bastion's request carries (so the real main installation refuses it with its real 400
// bastion_interface_mismatch), in the other the version the answers claim.
func TestR_BAS_009_R_BAS_028_InterfaceMismatch(t *testing.T) {
	for _, dir := range []string{"the main installation refuses the Bastion's version", "the Bastion refuses the main installation's version"} {
		t.Run(dir, func(t *testing.T) {
			w := newWorld(t, worldOptions{})
			w.content()
			b := w.newBastion(t, bastionOptions{Settings: "  answers:\n    period: 30s\n    grace_period: 60s\n"}).wait()
			r := b.reps[0]
			known := callOpts{token: w.full.token, addr: "198.51.100.61"}
			tgz := tarballPath(npmAll, "@acme/sdk", "1.0.0")
			if got := r.do(t, "GET", tgz, known); got.Status != http.StatusOK {
				t.Fatalf("warm: %d", got.Status)
			}
			if gauge(t, r, "trove_bastion_interface_mismatch") != 0 {
				t.Fatal("a mismatch is shown before there is one")
			}
			other := int64(iface.InterfaceVersion + 1)
			wantMain := float64(iface.InterfaceVersion)
			if strings.Contains(dir, "refuses the main") {
				w.front.resVersion.Store(other)
				wantMain = float64(other)
			} else {
				w.front.reqVersion.Store(other)
			}
			waitGauge(t, r, "trove_bastion_interface_mismatch", 1)
			waitGauge(t, r, "trove_bastion_main_reachable", 0)
			if got := gauge(t, r, `trove_bastion_interface_version{side="main"}`); got != wantMain {
				t.Fatalf("the main interface version metric = %v, want %v", got, wantMain)
			}
			// Inside the grace period the known caller is served from the cache, a cold package gets 503.
			b.clk.Advance(45 * time.Second)
			if got := r.do(t, "GET", tgz, known); got.Status != http.StatusOK {
				t.Fatalf("a known caller during the mismatch: %d %s", got.Status, got.Body)
			}
			if got := r.do(t, "GET", tarballPath(npmAll, "@acme/sdk", "2.0.0"), known); got.Status != http.StatusServiceUnavailable {
				t.Fatalf("a cold package during the mismatch: %d, want 503", got.Status)
			}
			b.clk.Advance(60 * time.Second)
			if got := r.do(t, "GET", tgz, known); got.Status != http.StatusServiceUnavailable {
				t.Fatalf("past the grace period: %d, want 503", got.Status)
			}
			// The upgrade of the other side clears it.
			w.front.reqVersion.Store(0)
			w.front.resVersion.Store(0)
			waitGauge(t, r, "trove_bastion_interface_mismatch", 0)
			waitGauge(t, r, "trove_bastion_main_reachable", 1)
		})
	}
}

// TestR_BAS_010_R_BAS_017_BaseURLMismatch: a Bastion whose base URL the main installation does not list in
// server.additional_base_urls would hand out documents with the main installation's URLs; the handshake proves
// it and the Bastion treats the main installation as unreachable, so no document is ever served.
func TestR_BAS_010_R_BAS_017_BaseURLMismatch(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	const other = "https://other.example.com"
	b := w.newBastion(t, bastionOptions{BaseURL: other})
	r := b.reps[0]
	waitGauge(t, r, "trove_bastion_base_url_mismatch", 1)
	got := r.do(t, "GET", "/api/npm/"+npmAll+"/"+escapeNPM("@acme/sdk"), callOpts{token: w.full.token, addr: "198.51.100.71", host: "other.example.com"})
	if got.Status != http.StatusServiceUnavailable || got.Code() != "main_unreachable" {
		t.Fatalf("a cold document: %d %s", got.Status, got.Body)
	}
	if strings.Contains(string(got.Body), mainHost) {
		t.Fatalf("the refusal names the main installation's host: %s", got.Body)
	}
	if !strings.Contains(r.logs.String(), "base URL not honoured") {
		t.Fatal("the Bastion did not log that the main installation does not honour its base URL")
	}
}

// TestR_BAS_005_R_BAS_006_ARevokedBastionToken: the Bastion's token is revoked at the main installation. The
// replica takes the main installation as unreachable (the 401 bastion_token_invalid concerns the Bastion and is
// never passed to a caller), keeps serving what the grace period allows, and recovers when the token file holds
// a new token, which it reads without a restart.
func TestR_BAS_005_R_BAS_006_ARevokedBastionToken(t *testing.T) {
	w := newWorld(t, worldOptions{})
	w.content()
	second := w.issueBastionToken("partners")
	b := w.newBastion(t, bastionOptions{Token: second.token, Settings: "  answers:\n    period: 30s\n    grace_period: 60s\n"}).wait()
	r := b.reps[0]
	known := callOpts{token: w.full.token, addr: "198.51.100.81"}
	tgz := tarballPath(npmAll, "@acme/sdk", "1.0.0")
	if got := r.do(t, "GET", tgz, known); got.Status != http.StatusOK {
		t.Fatalf("warm: %d", got.Status)
	}

	w.mustAPI("DELETE", "/api/v1/bastions/partners/tokens/"+second.id, 204, nil)
	waitGauge(t, r, "trove_bastion_main_token_refused", 1)
	waitGauge(t, r, "trove_bastion_main_reachable", 0)
	got := r.do(t, "GET", tarballPath(npmAll, "@acme/sdk", "2.0.0"), known)
	if got.Status != http.StatusServiceUnavailable || got.Code() != "main_unreachable" {
		t.Fatalf("a cold package with the Bastion's token revoked: %d %s (the caller must never see the Bastion's 401)", got.Status, got.Body)
	}
	b.clk.Advance(45 * time.Second)
	if got := r.do(t, "GET", tgz, known); got.Status != http.StatusOK {
		t.Fatalf("a known caller inside the grace period: %d", got.Status)
	}

	fresh := w.issueBastionToken("partners")
	if err := os.WriteFile(b.tokenFil, []byte(fresh.token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitGauge(t, r, "trove_bastion_main_token_refused", 0)
	waitGauge(t, r, "trove_bastion_main_reachable", 1)
	if got := r.do(t, "GET", tarballPath(npmAll, "@acme/sdk", "2.0.0"), known); got.Status != http.StatusOK {
		t.Fatalf("after the new token: %d %s", got.Status, got.Body)
	}
}
