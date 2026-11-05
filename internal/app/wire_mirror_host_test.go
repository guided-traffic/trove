package app

import (
	"io"
	"testing"

	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestMirrorHostOfAProcessWithoutTheAreas: the host is built on first use, and a process whose areas are not wired (the shell
// profile has none) answers every call with the error that says so, never a panic and never a half-built host (Q-ARCH-308).
func TestMirrorHostOfAProcessWithoutTheAreas(t *testing.T) {
	env := baseEnv(t, nil)
	a, err := New(Options{Loaded: loadCfg(t, "worker", env), Role: "worker", Environ: env, LogWriter: io.Discard, SkipInfrastructure: true})
	if err != nil {
		t.Fatal(err)
	}
	h := a.mirrorHost()
	if h == nil {
		t.Fatal("no host")
	}
	t.Run("R-REPO-055 the calls of a process that cannot build the host fail with the same error", func(t *testing.T) {
		if l, ok := h.Lister("npm"); ok || l != nil {
			t.Fatal("a lister without a host")
		}
		if _, err := h.Admits(t.Context(), repository.Repository{}); err == nil {
			t.Fatal("Admits answered")
		}
		if _, err := h.Upstream(repository.Repository{}, mirror.Pass{}); err == nil {
			t.Fatal("Upstream answered")
		}
		if _, err := h.Fill(t.Context(), repository.Repository{}, mirror.Pass{}, discover.Item{}); err == nil {
			t.Fatal("Fill answered")
		}
	})
}
