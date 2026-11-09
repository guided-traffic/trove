package postgres

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// blackHole accepts connections and never answers: a database whose host is up and whose PostgreSQL is stopped behind a
// proxy, or a route that drops packets. It returns the address.
func blackHole(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})
	return l.Addr().String()
}

// TestRetryBudgetBoundsTheWaitForAConnection the finding of the first E5.10 runs: with a database that does not answer, a request waits for a pooled
// connection (the pool is one dial deep, the dial times out after connect_timeout) and used to wait for its own deadline,
// 45 s at a client's timeout. It now fails with the 503 of R-ARCH-023 within retry.max_elapsed.
func TestRetryBudgetBoundsTheWaitForAConnection(t *testing.T) {
	pc, err := pgxpool.ParseConfig("postgres://trove@" + blackHole(t) + "/trove?sslmode=disable&connect_timeout=60")
	if err != nil {
		t.Fatal(err)
	}
	pc.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	r := newRetrier(config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: time.Second})

	t.Run("R-HA-007 R-HA-009 R-HA-052 every request that waits for a connection of a database that does not answer ends in the 503 within retry.max_elapsed", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // the request's deadline, far beyond the budget
		defer cancel()
		var wg sync.WaitGroup
		took := make([]time.Duration, 4)
		errsGot := make([]error, 4)
		for i := range took {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start := time.Now()
				errsGot[i] = r.doCtx(ctx, func(actx context.Context) error {
					conn, err := acquire(actx, pool)
					if err != nil {
						return err
					}
					conn.Release()
					return nil
				})
				took[i] = time.Since(start)
			}()
		}
		wg.Wait()
		for i := range took {
			var e *errs.Error
			if !errors.As(errsGot[i], &e) || e.Kind != errs.Unavailable {
				t.Errorf("request %d: %v, want the 503 (unavailable)", i, errsGot[i])
			}
			if took[i] > 2*time.Second {
				t.Errorf("request %d took %s, want within retry.max_elapsed (1s) plus one backoff", i, took[i])
			}
		}
	})
}
