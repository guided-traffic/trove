//go:build integration

package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup/platform"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_HA_023_PlatformCheckAgainstTheStack: the checks read the real server's archive_mode and the real object store's bucket
// settings, and the report round-trips through system_state, which is how every replica exports the gauge. The stack's
// PostgreSQL does not archive WAL, so a declared recovery window is reported as failed; whether the bucket is versioned is the
// stack's state, so the test asserts only that the read answers or reports "unknown" and never fails the run.
func TestR_HA_023_PlatformCheckAgainstTheStack(t *testing.T) {
	stack := testenv.NewStack(t, testenv.StackOptions{Name: "platformcheck"})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, stack.DatabaseConfig(), "platform-check-it", meta.RoleWeb, quietLog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	o := testenv.S3Config(t)
	cfg := blobtest.Storage(t, t.TempDir()).S3
	cfg.Endpoint, cfg.Region, cfg.Bucket, cfg.ForcePathStyle, cfg.CAFile = o.Endpoint, o.Region, o.BlobsBucket, true, testenv.CAFile(t)
	dir := t.TempDir()
	cfg.Credentials = config.StorageS3Credentials{
		Source: "static", AccessKeyIDFile: testenv.WriteFile(t, dir, "s3-id", o.AccessKey), SecretAccessKeyFile: testenv.WriteFile(t, dir, "s3-secret", o.SecretKey),
	}
	client, err := s3.NewFromFactory(ctx, cfg, outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}}))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the bucket reads answer or say unknown", func(t *testing.T) {
		if _, err := client.VersioningStatus(ctx); err != nil && !errors.Is(err, s3.ErrProtectionUnknown) {
			t.Errorf("VersioningStatus: %v", err)
		}
		if _, _, err := client.NoncurrentExpiryDays(ctx); err != nil && !errors.Is(err, s3.ErrProtectionUnknown) {
			t.Errorf("NoncurrentExpiryDays: %v", err)
		}
	})

	var rep platform.Report
	if err := db.Read(ctx, func(c q.DBTX) error {
		var rerr error
		rep, rerr = platform.Checker{
			ArchiveMode: platform.ArchiveModeOf(c), Bucket: client,
			Settings: platform.Settings{RecoveryWindow: 168 * time.Hour, ObjectVersioning: true, GracePeriod: 24 * time.Hour},
		}.Run(ctx)
		return rerr
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("a declared window on a server that archives nothing fails the archive_mode check", func(t *testing.T) {
		var mode string
		if err := db.Read(ctx, func(c q.DBTX) error { return c.QueryRow(ctx, "SHOW archive_mode").Scan(&mode) }); err != nil {
			t.Fatal(err)
		}
		want := platform.StatusFailed
		if mode == "on" || mode == "always" {
			want = platform.StatusOK
		}
		var got platform.Status
		for _, r := range rep.Results {
			if r.Check == platform.CheckArchiveMode {
				got = r.Status
				if !strings.Contains(r.Detail, mode) {
					t.Errorf("detail %q does not name the mode %q", r.Detail, mode)
				}
			}
		}
		if got != want {
			t.Errorf("archive_mode %q: status %q, want %q", mode, got, want)
		}
	})

	t.Run("the grace period of 24h does not cover a window of 168h, versioning being asserted it holds", func(t *testing.T) {
		for _, r := range rep.Results {
			if r.Check == platform.CheckGracePeriod && r.Status != platform.StatusOK {
				t.Errorf("grace_period = %s: %s", r.Status, r.Detail)
			}
		}
	})

	t.Run("the report round-trips through system_state", func(t *testing.T) {
		if _, ok, err := loadReport(ctx, db); err != nil || ok {
			t.Fatalf("before the first run: ok=%v err=%v", ok, err)
		}
		if err := db.Read(ctx, func(c q.DBTX) error { return platform.Store(ctx, c, rep) }); err != nil {
			t.Fatal(err)
		}
		got, ok, err := loadReport(ctx, db)
		if err != nil || !ok {
			t.Fatalf("after the run: ok=%v err=%v", ok, err)
		}
		if len(got.Results) != len(rep.Results) || !got.At.Equal(rep.At) {
			t.Errorf("loaded %+v, stored %+v", got, rep)
		}
		// A second run replaces it, also with an empty one (the declaration was removed).
		if err := db.Read(ctx, func(c q.DBTX) error {
			return platform.Store(ctx, c, platform.Report{At: time.Now().UTC(), Results: []platform.Result{}})
		}); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := loadReport(ctx, db); len(got.Results) != 0 {
			t.Errorf("results after an empty run = %v", got.Results)
		}
	})
}

func loadReport(ctx context.Context, db *postgres.DB) (rep platform.Report, ok bool, err error) {
	err = db.Read(ctx, func(c q.DBTX) error {
		var lerr error
		rep, ok, lerr = platform.Load(ctx, c)
		return lerr
	})
	return rep, ok, err
}
