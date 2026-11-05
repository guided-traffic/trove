package formathost

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/proxy"
)

// TestMapFill is the table of docs/developer/mirror.md "What the host maps": which error of the engine's fill is a permanent
// refusal (*mirror.RefusedError), which is the shutdown, and which stays a failure of the item the engine can read by its code.
func TestMapFill(t *testing.T) {
	e := func(k errs.Kind, code string) error { return errs.E(k, code, "x") }
	for name, tc := range map[string]struct {
		err    error
		reason string // "" for no RefusedError
	}{
		"a negative entry, 404 and 410 (not found)":     {errs.E(errs.NotFound, errs.CodeNotFound, "x", errs.WithAbsent()), mirror.RefusedNotFound},
		"a package marked gone":                         {e(errs.NotFound, errs.CodeNotFound), mirror.RefusedNotFound},
		"a version blocked by the scanning":             {e(errs.Forbidden, errs.CodeVulnerabilityBlocked), mirror.RefusedBlocked},
		"advisory data missing under block":             {e(errs.Forbidden, errs.CodeVulnerabilityDataMissing), mirror.RefusedBlocked},
		"a refused populate check":                      {e(errs.Forbidden, errs.CodePopulateNotPermitted), mirror.RefusedPopulate},
		"offline is a failure the engine reads by code": {e(errs.NotFound, errs.CodeNotCachedOffline), ""},
		"an upstream error is a failure of the item":    {e(errs.UpstreamFailed, errs.CodeUpstreamError), ""},
		"an integrity mismatch is a failure":            {e(errs.UpstreamFailed, errs.CodeUpstreamIntegrityMismatch), ""},
	} {
		t.Run("R-REPO-053 "+name, func(t *testing.T) {
			got := mapFill(tc.err)
			var r *mirror.RefusedError
			if tc.reason == "" {
				if errors.As(got, &r) {
					t.Fatalf("mapped to %v", r)
				}
				if got != tc.err { //nolint:errorlint // identity: nothing is mapped
					t.Fatalf("changed to %v", got)
				}
				return
			}
			if !errors.As(got, &r) || r.Reason != tc.reason || !errors.Is(got, tc.err) {
				t.Fatalf("%v, want refused %s with the cause kept", got, tc.reason)
			}
		})
	}
	t.Run("R-REPO-053 a refusal does not hide a pre-emption, a wait or the shutdown", func(t *testing.T) {
		if !errors.Is(mapFill(proxy.ErrPreempted), mirror.ErrPreempted) {
			t.Fatal("pre-emption")
		}
		var w *mirror.WaitError
		if !errors.As(mapFill(&proxy.BackgroundWait{Reason: proxy.WaitSpoolBusy}), &w) {
			t.Fatal("wait")
		}
	})
}

func TestMapGate(t *testing.T) {
	t.Run("R-REPO-053 a refusal of the format's gate is gate_refused", func(t *testing.T) {
		for _, k := range []errs.Kind{errs.NotFound, errs.Forbidden} {
			var r *mirror.RefusedError
			if got := mapGate(errs.E(k, errs.CodeNotFound, "removed")); !errors.As(got, &r) || r.Reason != mirror.RefusedGate {
				t.Fatalf("%v: %v", k, got)
			}
		}
	})
	t.Run("R-REPO-053 the gate's own failed request is a failure of the item, and offline is not a refusal", func(t *testing.T) {
		for _, err := range []error{errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "x"), errs.E(errs.NotFound, errs.CodeNotCachedOffline, "x")} {
			var r *mirror.RefusedError
			if got := mapGate(err); errors.As(got, &r) {
				t.Fatalf("%v mapped to a refusal", err)
			}
		}
	})
}

func TestMapShuttingDown(t *testing.T) {
	t.Run("R-REPO-053 R-ARCH-024 a request the shutdown cancelled wraps mirror.ErrShuttingDown and keeps the engine's error", func(t *testing.T) {
		src := errs.E(errs.Unavailable, errs.CodeShuttingDown, "The replica is shutting down; retry.")
		got := mapBackground(src)
		if !errors.Is(got, mirror.ErrShuttingDown) || !errors.Is(got, src) {
			t.Fatalf("%v", got)
		}
		if !errors.Is(mapFill(src), mirror.ErrShuttingDown) || !errors.Is(mapGate(src), mirror.ErrShuttingDown) {
			t.Fatal("not mapped on every road")
		}
	})
}

type drainingEngine struct {
	MirrorEngine
	draining bool
}

func (d drainingEngine) Draining() bool { return d.draining }

func TestMirrorHostReady(t *testing.T) {
	h := newTestHost(t, &recordingDecider{})
	t.Run("R-ARCH-024 a host over a draining engine is not ready: no pass starts in a process that shuts down", func(t *testing.T) {
		m, _ := NewMirrorHost(h, drainingEngine{draining: true})
		if err := m.Ready(context.Background()); !errors.Is(err, mirror.ErrShuttingDown) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-REPO-053 a host over a running engine is ready", func(t *testing.T) {
		m, _ := NewMirrorHost(h, drainingEngine{})
		if err := m.Ready(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
