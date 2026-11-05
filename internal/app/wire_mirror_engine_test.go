package app

import (
	"errors"
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/mirror"
)

func TestMirrorEngineArea(t *testing.T) {
	t.Run("R-ARCH-001 the worker and all build the area, no other role does", func(t *testing.T) {
		for role, want := range map[string]bool{"worker": true, "all": true, "web": false, "packages": false, "api": false} {
			sub, err := SubsystemsFor(role)
			if err != nil {
				t.Fatal(err)
			}
			if sub.Has(AreaMirrorEngine) != want {
				t.Errorf("%s: area %v, want %v", role, sub.Has(AreaMirrorEngine), want)
			}
		}
	})
	t.Run("Q-ARCH-317 the caches of the worker's engine are sized for background use", func(t *testing.T) {
		got := backgroundProxyConfig(config.Proxy{MetadataCacheBytes: 128 << 20, NegativeL1Entries: 100000, QueryCacheBytes: 8 << 20})
		if got.MetadataCacheBytes != backgroundMetadataCacheBytes || got.NegativeL1Entries != backgroundNegativeL1Entries || got.QueryCacheBytes != 0 {
			t.Fatalf("%+v", got)
		}
		small := backgroundProxyConfig(config.Proxy{MetadataCacheBytes: 1 << 20, NegativeL1Entries: 50})
		if small.MetadataCacheBytes != 1<<20 || small.NegativeL1Entries != 50 {
			t.Fatalf("an operator's smaller value was raised: %+v", small)
		}
	})
	t.Run("Q-ARCH-312 a host that cannot be built answers every Ready with its error, and a later call tries again without panicking", func(t *testing.T) {
		env := baseEnv(t, nil)
		a, err := New(Options{Loaded: loadCfg(t, "worker", env), Role: "worker", Environ: env, SkipInfrastructure: true})
		if err != nil {
			t.Fatal(err)
		}
		// A builder with nothing wired fails the same way twice: no state of a failed attempt is kept that would make the next one
		// build a second engine (a cache registered twice panics) or answer with a stale success.
		a.parts.put(reflect.ValueOf(mirrorEngineArea{b: &mirrorHostBuilder{a: a}}))
		h := a.mirrorHost()
		e1, e2 := h.Ready(t.Context()), h.Ready(t.Context())
		if e1 == nil || e2 == nil || errors.Is(e1, mirror.ErrShuttingDown) {
			t.Fatalf("%v %v", e1, e2)
		}
	})
}
