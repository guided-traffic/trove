package postgres

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// fakeBackend completes the startup of every connection and then answers
// nothing; enough for pool.Acquire to hand out a connection.
func fakeBackend(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				be := pgproto3.NewBackend(c, c)
				for {
					m, err := be.ReceiveStartupMessage()
					if err != nil {
						return
					}
					if _, ok := m.(*pgproto3.SSLRequest); ok {
						_, _ = c.Write([]byte{'N'})
						continue
					}
					break
				}
				be.Send(&pgproto3.AuthenticationOk{})
				be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
				be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				_ = be.Flush()
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestReadPanic(t *testing.T) {
	t.Run("R-ARCH-019 CR2-3 a panic in the function given to Read does not leak the pooled connection", func(t *testing.T) {
		port := fakeBackend(t)
		dir := t.TempDir()
		c := testConfig()
		c.Host, c.Port, c.SSLMode, c.ConnectTimeout, c.TargetSessionAttrs = "127.0.0.1", port, "disable", 2*time.Second, "any"
		c.UsernameFile = filepath.Join(dir, "user")
		tlstest.WriteFile(t, c.UsernameFile, []byte("trove\n"))
		pc, err := newPoolConfig(c, poolSpec{
			kind: connKind{class: classMain}, ep: mainEndpoint(c), instance: "u", maxConns: 2,
			lifetime: time.Minute, idleTime: time.Minute, healthEach: time.Minute,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		pool, err := pgxpool.NewWithConfig(context.Background(), pc)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		actx, acancel := context.WithTimeout(context.Background(), 3*time.Second)
		conn, err := pool.Acquire(actx)
		acancel()
		if err != nil {
			t.Fatalf("setup: acquire: %v", err)
		}
		conn.Release()
		c.Retry.MaxElapsed = time.Second
		db := &DB{main: pool, retry: newRetrier(c.Retry)}
		for range 2 {
			func() {
				defer func() {
					if recover() == nil {
						t.Error("the panic must reach the recovery stage")
					}
				}()
				_ = db.Read(context.Background(), func(q.DBTX) error { panic("handler bug") })
			}()
		}
		if n := pool.Stat().AcquiredConns(); n != 0 {
			t.Errorf("%d connection(s) leaked by panics", n)
		}
	})
}

func TestTLSVerificationIsNotAnOutage(t *testing.T) {
	t.Run("R-HA-015 CR2-6 a server certificate the configured CA does not verify is not an outage", func(t *testing.T) {
		serverCA, otherCA := tlstest.NewCA(t, "srv"), tlstest.NewCA(t, "other")
		port, _ := fakeTLSServer(t, &tls.Config{Certificates: []tls.Certificate{serverCA.Issue(t, tlstest.Options{}).TLS}})
		dir := t.TempDir()
		c := testConfig()
		c.Host, c.Port, c.ConnectTimeout = "127.0.0.1", port, 2*time.Second
		c.UsernameFile = filepath.Join(dir, "user")
		tlstest.WriteFile(t, c.UsernameFile, []byte("trove\n"))
		c.SSLRootCertFile = otherCA.WriteCA(t, dir, "ca.crt")
		tt, err := newDBTLS(c)
		if err != nil {
			t.Fatal(err)
		}
		pc, err := newPoolConfig(c, poolSpec{
			kind: connKind{class: classMain}, ep: mainEndpoint(c), instance: "u", maxConns: 1,
			lifetime: time.Minute, idleTime: time.Minute, healthEach: time.Minute,
		}, tt)
		if err != nil {
			t.Fatal(err)
		}
		pool, err := pgxpool.NewWithConfig(context.Background(), pc)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err = pool.Acquire(ctx)
		if err == nil {
			t.Fatal("setup: the handshake must fail")
		}
		if classify(err) != retryNo || IsOutage(err) {
			t.Errorf("an x509 verification failure is classified %d (outage=%v): %v", classify(err), IsOutage(err), err)
		}
	})
}

// simpleBackend answers every simple query with one boolean column: "t" when
// primary, "f" otherwise. accepted counts the connections it saw.
func simpleBackend(t *testing.T, primary bool) (port int, accepted *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted = new(atomic.Int32)
	val := []byte("f")
	if primary {
		val = []byte("t")
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func(c net.Conn) {
				defer c.Close()
				be := pgproto3.NewBackend(c, c)
				for {
					m, err := be.ReceiveStartupMessage()
					if err != nil {
						return
					}
					if _, ok := m.(*pgproto3.SSLRequest); ok {
						_, _ = c.Write([]byte{'N'})
						continue
					}
					break
				}
				be.Send(&pgproto3.AuthenticationOk{})
				be.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
				be.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
				be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
				be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				_ = be.Flush()
				for {
					m, err := be.Receive()
					if err != nil {
						return
					}
					switch m.(type) {
					case *pgproto3.Query:
						be.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("?column?"), DataTypeOID: 16, DataTypeSize: 1, TypeModifier: -1}}})
						be.Send(&pgproto3.DataRow{Values: [][]byte{val}})
						be.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
						be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
						_ = be.Flush()
					case *pgproto3.Terminate:
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, accepted
}

func TestReadyStandby(t *testing.T) {
	controlPool := func(t *testing.T, port int) *DB {
		dir := t.TempDir()
		c := testConfig()
		c.Host, c.Port, c.SSLMode, c.ConnectTimeout, c.TargetSessionAttrs = "127.0.0.1", port, "disable", 2*time.Second, "any"
		c.UsernameFile = filepath.Join(dir, "user")
		tlstest.WriteFile(t, c.UsernameFile, []byte("trove\n"))
		pc, err := newPoolConfig(c, poolSpec{
			kind: connKind{class: classMain}, ep: controlEndpoint(c), instance: "u", maxConns: 1,
			lifetime: time.Hour, idleTime: time.Hour, healthEach: time.Hour,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		pc.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		pool, err := pgxpool.NewWithConfig(context.Background(), pc)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return &DB{control: pool}
	}

	t.Run("R-OPS-001 CR2-gap-c Ready destroys a control connection that landed on a standby", func(t *testing.T) {
		port, accepted := simpleBackend(t, false)
		db := controlPool(t, port)
		for i := 1; i <= 2; i++ {
			if err := db.Ready(context.Background()); !errors.Is(err, ErrStandby) {
				t.Fatalf("Ready #%d = %v, want ErrStandby", i, err)
			}
			if n := db.control.Stat().TotalConns(); n != 0 {
				t.Fatalf("after Ready #%d the standby connection is still in the pool (%d)", i, n)
			}
		}
		if n := accepted.Load(); n != 2 {
			t.Errorf("connections made = %d, want 2: every check must connect again", n)
		}
	})

	t.Run("R-OPS-001 CR2-gap-c Ready keeps a connection to a primary", func(t *testing.T) {
		port, accepted := simpleBackend(t, true)
		db := controlPool(t, port)
		for range 3 {
			if err := db.Ready(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		if n := accepted.Load(); n != 1 {
			t.Errorf("connections made = %d, want 1", n)
		}
	})
}
