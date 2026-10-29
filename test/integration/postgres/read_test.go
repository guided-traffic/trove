//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

func TestRead(t *testing.T) {
	st := getStack(t)
	cfg, _, admin := migrated(t, st)
	cfg.Retry.MaxElapsed = 3 * time.Second
	db := open(t, cfg, "it-read", meta.RoleWeb)
	ctx := ctxT(t, 90*time.Second)

	t.Run("R-ARCH-019 Read repeats a read that a failover ended (SQLSTATE 57P01) on a new connection", func(t *testing.T) {
		attempts := 0
		err := db.Read(ctx, func(c q.DBTX) error {
			attempts++
			if attempts < 3 {
				_, err := c.Exec(ctx, `DO $$ BEGIN RAISE EXCEPTION 'terminating connection due to administrator command' USING ERRCODE = '57P01'; END $$`)
				return err
			}
			var one int
			return c.QueryRow(ctx, "SELECT 1").Scan(&one)
		})
		if err != nil || attempts != 3 {
			t.Fatalf("attempts = %d, err = %v", attempts, err)
		}
	})

	t.Run("R-ARCH-019 Read returns an error that is not a failover error at once, unmapped", func(t *testing.T) {
		attempts := 0
		err := db.Read(ctx, func(c q.DBTX) error {
			attempts++
			var n int
			return c.QueryRow(ctx, "SELECT 1/0").Scan(&n)
		})
		if attempts != 1 || err == nil || errs.KindOf(err) == errs.Unavailable {
			t.Fatalf("attempts = %d, err = %v", attempts, err)
		}
	})

	t.Run("R-ARCH-019 R-ARCH-023 a read that keeps failing ends as database_unavailable within retry.max_elapsed", func(t *testing.T) {
		start := time.Now()
		err := db.Read(ctx, func(c q.DBTX) error {
			_, err := c.Exec(ctx, `DO $$ BEGIN RAISE EXCEPTION 'down' USING ERRCODE = '57P01'; END $$`)
			return err
		})
		took := time.Since(start)
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.Unavailable || e.Code != errs.CodeDatabaseUnavailable {
			t.Fatalf("err = %v, want database_unavailable", err)
		}
		if took > cfg.Retry.MaxElapsed+time.Second {
			t.Fatalf("took %s, the bound is %s", took, cfg.Retry.MaxElapsed)
		}
	})

	t.Run("R-ARCH-019 reads that run while every backend is terminated all succeed", func(t *testing.T) {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		var kills atomic.Int64
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				case <-time.After(120 * time.Millisecond):
				}
				var n int
				if err := admin.QueryRow(context.Background(),
					`SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
					  WHERE application_name LIKE 'trove:it-read:%' AND pid <> pg_backend_pid()`).Scan(&n); err == nil {
					kills.Add(int64(n))
				}
			}
		}()
		var failures []error
		var attempts atomic.Int64
		rctx := ctxT(t, 40*time.Second)
		for i := 0; i < 150; i++ {
			err := db.Read(rctx, func(c q.DBTX) error {
				attempts.Add(1)
				var n int
				if err := c.QueryRow(ctx, "SELECT 1").Scan(&n); err != nil {
					return err
				}
				return c.QueryRow(ctx, "SELECT pg_sleep(0.005)::text IS NOT NULL, 1").Scan(new(bool), &n)
			})
			if err != nil {
				failures = append(failures, err)
			}
		}
		close(stop)
		wg.Wait()
		if kills.Load() == 0 {
			t.Fatal("no backend was terminated: the test measured nothing")
		}
		if attempts.Load() <= 150 {
			t.Fatalf("%d attempts for 150 reads: no read was ever repeated, the test measured nothing", attempts.Load())
		}
		if len(failures) != 0 {
			t.Fatalf("%d of 150 reads failed after %d terminated backends, first: %v", len(failures), kills.Load(), failures[0])
		}
	})

	t.Run("R-ARCH-019 the system facts reads go through Read", func(t *testing.T) {
		killAll := func() {
			_, _ = admin.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
				WHERE application_name LIKE 'trove:it-read:%' AND pid <> pg_backend_pid()`)
		}
		sctx := ctxT(t, 20*time.Second)
		killAll()
		f, err := db.SystemFacts(sctx)
		if err != nil || f.InstallationID == "" {
			t.Fatalf("SystemFacts after a terminated backend: %+v, %v", f, err)
		}
		killAll()
		if _, err := db.ServerVersion(sctx); err != nil {
			t.Fatalf("ServerVersion after a terminated backend: %v", err)
		}
	})
}
