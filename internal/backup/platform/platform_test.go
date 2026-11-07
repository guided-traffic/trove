package platform_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/backup/platform"
	"github.com/guided-traffic/trove/internal/blob/s3"
)

type fakeBucket struct {
	status     string
	statusErr  error
	days       int
	limited    bool
	lifecycleE error
}

func (f fakeBucket) VersioningStatus(context.Context) (string, error) { return f.status, f.statusErr }

func (f fakeBucket) NoncurrentExpiryDays(context.Context) (int, bool, error) {
	return f.days, f.limited, f.lifecycleE
}

func mode(m string, err error) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return m, err }
}

func run(t *testing.T, c platform.Checker) map[string]platform.Status {
	t.Helper()
	c.Now = func() time.Time { return time.Unix(1000, 0) }
	rep, err := c.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]platform.Status{}
	for _, r := range rep.Results {
		if r.Detail == "" {
			t.Errorf("%s has no detail", r.Check)
		}
		out[r.Check] = r.Status
	}
	return out
}

const day = 24 * time.Hour

// TestR_HA_023_PlatformChecks (R-HA-023): the checks report what the operator declared and nothing else.
func TestR_HA_023_PlatformChecks(t *testing.T) {
	t.Run("nothing declared, nothing checked: the gauge has no series", func(t *testing.T) {
		got := run(t, platform.Checker{ArchiveMode: mode("off", nil), Bucket: fakeBucket{}, Settings: platform.Settings{GracePeriod: day}})
		if len(got) != 0 {
			t.Errorf("results = %v", got)
		}
	})

	t.Run("archive_mode on passes, off fails, always passes", func(t *testing.T) {
		s := platform.Settings{RecoveryWindow: day, GracePeriod: day}
		for m, want := range map[string]platform.Status{"on": platform.StatusOK, "always": platform.StatusOK, "off": platform.StatusFailed} {
			if got := run(t, platform.Checker{ArchiveMode: mode(m, nil), Settings: s})[platform.CheckArchiveMode]; got != want {
				t.Errorf("archive_mode %s = %s, want %s", m, got, want)
			}
		}
	})

	t.Run("the grace period must cover the window, or the bucket must be versioned", func(t *testing.T) {
		short := platform.Settings{RecoveryWindow: 7 * day, GracePeriod: day}
		if got := run(t, platform.Checker{ArchiveMode: mode("on", nil), Settings: short})[platform.CheckGracePeriod]; got != platform.StatusFailed {
			t.Errorf("grace 1d, window 7d = %s, want failed", got)
		}
		short.ObjectVersioning = true
		got := run(t, platform.Checker{ArchiveMode: mode("on", nil), Bucket: fakeBucket{status: "Enabled"}, Settings: short})
		if got[platform.CheckGracePeriod] != platform.StatusOK {
			t.Errorf("with versioning = %s, want ok", got[platform.CheckGracePeriod])
		}
		if got := run(t, platform.Checker{ArchiveMode: mode("on", nil), Settings: platform.Settings{RecoveryWindow: day, GracePeriod: day}})[platform.CheckGracePeriod]; got != platform.StatusOK {
			t.Errorf("grace equal to window = %s, want ok", got)
		}
	})

	t.Run("object_versioning: Enabled passes, Suspended and never-enabled fail, a denied read is unknown", func(t *testing.T) {
		s := platform.Settings{ObjectVersioning: true}
		for status, want := range map[string]platform.Status{"Enabled": platform.StatusOK, "Suspended": platform.StatusFailed, "": platform.StatusFailed} {
			if got := run(t, platform.Checker{Bucket: fakeBucket{status: status}, Settings: s})[platform.CheckObjectVersioning]; got != want {
				t.Errorf("versioning %q = %s, want %s", status, got, want)
			}
		}
		denied := fakeBucket{statusErr: fmt.Errorf("GetBucketVersioning: %w", s3.ErrProtectionUnknown)}
		if got := run(t, platform.Checker{Bucket: denied, Settings: s})[platform.CheckObjectVersioning]; got != platform.StatusUnknown {
			t.Errorf("denied = %s, want unknown", got)
		}
	})

	t.Run("noncurrent expiry must reach the window; no rule keeps everything", func(t *testing.T) {
		s := platform.Settings{ObjectVersioning: true, RecoveryWindow: 7 * day, GracePeriod: day}
		cases := []struct {
			b    fakeBucket
			want platform.Status
		}{
			{fakeBucket{status: "Enabled", limited: false}, platform.StatusOK},
			{fakeBucket{status: "Enabled", limited: true, days: 7}, platform.StatusOK},
			{fakeBucket{status: "Enabled", limited: true, days: 30}, platform.StatusOK},
			{fakeBucket{status: "Enabled", limited: true, days: 3}, platform.StatusFailed},
			{fakeBucket{status: "Enabled", lifecycleE: s3.ErrProtectionUnknown}, platform.StatusUnknown},
		}
		for i, c := range cases {
			if got := run(t, platform.Checker{ArchiveMode: mode("on", nil), Bucket: c.b, Settings: s})[platform.CheckNoncurrentExpiry]; got != c.want {
				t.Errorf("case %d = %s, want %s", i, got, c.want)
			}
		}
	})

	t.Run("a read that fails for another reason is an error and stores nothing", func(t *testing.T) {
		boom := errors.New("connection reset")
		if _, err := (platform.Checker{ArchiveMode: mode("", boom), Settings: platform.Settings{RecoveryWindow: day}}).Run(t.Context()); err == nil {
			t.Error("archive_mode failure was swallowed")
		}
		if _, err := (platform.Checker{Bucket: fakeBucket{statusErr: boom}, Settings: platform.Settings{ObjectVersioning: true}}).Run(t.Context()); err == nil {
			t.Error("versioning failure was swallowed")
		}
	})
}

// gather returns the value of every series of g by its check label.
func gather(t *testing.T, g *prometheus.GaugeVec) map[string]float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(g)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			out[m.GetLabel()[0].GetValue()] = m.GetGauge().GetValue()
		}
	}
	return out
}

// TestR_HA_023_ExportsTheGauge (R-HA-023, R-OPS-008): trove_backup_check_ok{check} is 1, 0, or absent.
func TestR_HA_023_ExportsTheGauge(t *testing.T) {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "trove_backup_check_ok"}, []string{"check"})
	platform.Export(g, platform.Report{Results: []platform.Result{
		{Check: platform.CheckArchiveMode, Status: platform.StatusOK},
		{Check: platform.CheckObjectVersioning, Status: platform.StatusFailed},
		{Check: platform.CheckNoncurrentExpiry, Status: platform.StatusUnknown},
	}})
	got := gather(t, g)
	if len(got) != 2 || got[platform.CheckArchiveMode] != 1 || got[platform.CheckObjectVersioning] != 0 {
		t.Errorf("series = %v, want archive_mode 1 and object_versioning 0 and none for the unknown check", got)
	}
	platform.Export(g, platform.Report{})
	if got := gather(t, g); len(got) != 0 {
		t.Errorf("series = %v after an empty report: a check that is no longer declared must leave the gauge", got)
	}
}
