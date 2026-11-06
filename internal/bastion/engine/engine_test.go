package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

func TestR_BAS_001_NewRefusesMissingDependenciesAndBuildsTheFlowFromRealOnes(t *testing.T) {
	h := newHarness(t, options{})
	full := Deps{
		Config: h.cfg, Metrics: h.m, Main: h.main, Cache: h.cache.Cache, Guard: h.guard, Downloads: h.dl, Link: h.link,
	}
	e, err := New(full)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.(*engine); !ok {
		t.Fatalf("New returned %T, want the flow", e)
	}
	for name, strip := range map[string]func(*Deps){
		"config": func(d *Deps) { d.Config = nil }, "metrics": func(d *Deps) { d.Metrics = nil }, "main": func(d *Deps) { d.Main = nil },
		"cache": func(d *Deps) { d.Cache = nil }, "guard": func(d *Deps) { d.Guard = nil }, "downloads": func(d *Deps) { d.Downloads = nil },
		"link": func(d *Deps) { d.Link = nil },
	} {
		d := full
		strip(&d)
		if _, err := New(d); err == nil {
			t.Errorf("New accepted dependencies without the %s", name)
		}
	}
	if _, err := New(Deps{}); err == nil {
		t.Error("New accepted empty dependencies")
	}
}

// TestR_BAS_037_ARefusalTheGuardCallsQuietIsMarkedForTheRequestLog: Verdict.Quiet keeps a blocked address's
// requests out of the request log; the engine marks the slot the server's request log reads, and counts the
// refusal.
func TestR_BAS_037_ARefusalTheGuardCallsQuietIsMarkedForTheRequestLog(t *testing.T) {
	h := newHarness(t, options{})
	h.guard.blocked = guard.Verdict{Status: 429, Code: iface.CodeAddressBlocked, RetryAfter: 60e9, Reason: metrics.RefusedBlocked, Quiet: true}
	for _, quiet := range []bool{true, false} {
		h.guard.blocked.Quiet = quiet
		r := httptest.NewRequest(http.MethodGet, "https://packages.example.com/api/npm/web/npm-all/lodash", nil)
		r = r.WithContext(WithQuietSlot(r.Context()))
		w := httptest.NewRecorder()
		h.e.ServeHTTP(w, r)
		if w.Code != http.StatusTooManyRequests || problemCode(w) != iface.CodeAddressBlocked {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if got := Quiet(r.Context()); got != quiet {
			t.Errorf("Quiet = %v, want %v", got, quiet)
		}
	}
	// Without a slot (no request log in front) marking is harmless.
	h.guard.blocked.Quiet = true
	w := httptest.NewRecorder()
	h.e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://packages.example.com/api/npm/web/npm-all/lodash", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("%d", w.Code)
	}
	if counterValue(t, h, "refused", metrics.RefusedBlocked) != 3 {
		t.Error("the refusals are not counted")
	}
}
