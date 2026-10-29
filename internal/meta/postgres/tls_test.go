package postgres

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// handshake is what the fake server saw of one connection.
type handshake struct {
	peer         string // the client's address
	err          error
	clientSerial int64 // 0 without a client certificate
}

// fakeTLSServer answers PostgreSQL's SSLRequest with 'S', runs the TLS handshake
// with cfg and reports it; then it closes the connection. That is enough to see
// which CA the client verified against and which certificate it presented.
func fakeTLSServer(t *testing.T, cfg *tls.Config) (port int, seen <-chan handshake) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan handshake, 64)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var req [8]byte
				if _, err := io.ReadFull(c, req[:]); err != nil || binary.BigEndian.Uint32(req[4:]) != 80877103 {
					return
				}
				if _, err := c.Write([]byte{'S'}); err != nil {
					return
				}
				tc := tls.Server(c, cfg)
				_ = tc.SetDeadline(time.Now().Add(3 * time.Second))
				err := tc.Handshake()
				h := handshake{err: err}
				if err == nil {
					// TLS 1.3: the client's verdict on the server certificate and its own
					// certificate arrive with its Finished; read once so both are in.
					_, _ = tc.Read(make([]byte, 1))
					if pcs := tc.ConnectionState().PeerCertificates; len(pcs) > 0 {
						h.clientSerial = pcs[0].SerialNumber.Int64()
					}
				}
				h.peer = c.RemoteAddr().String()
				ch <- h
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, ch
}

// dbClient is a pool against a fake server, with the local addresses its dials
// used, so a server record can be matched to the attempt that caused it (pgx
// may open more than one connection per attempt).
type dbClient struct {
	pool  *pgxpool.Pool
	seen  <-chan handshake
	mu    sync.Mutex
	local []string
}

// tryConnect opens one new pool connection, which fails after the handshake
// because the fake server speaks no further protocol, and returns what the
// server saw of the attempt's first connection and the client's error.
func (d *dbClient) tryConnect(t *testing.T) (handshake, error) {
	t.Helper()
	d.mu.Lock()
	d.local = nil
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := d.pool.Acquire(ctx)
	if err == nil {
		conn.Release()
		t.Fatal("the fake server accepted a session")
	}
	d.mu.Lock()
	local := append([]string(nil), d.local...)
	d.mu.Unlock()
	if len(local) == 0 {
		t.Fatalf("no connection was dialed: %v", err)
	}
	timeout := time.After(3 * time.Second)
	for {
		select {
		case h := <-d.seen:
			if h.peer == local[0] {
				return h, err
			}
		case <-timeout:
			t.Fatalf("the server saw no connection from %s; client error %v", local[0], err)
			return handshake{}, err
		}
	}
}

func TestDatabaseTLSReload(t *testing.T) {
	serverCA := tlstest.NewCA(t, "database server ca")
	otherCA := tlstest.NewCA(t, "unrelated ca")
	clientCA := tlstest.NewCA(t, "database client ca")
	srvCfg := func(leaf *tlstest.Leaf) *tls.Config {
		return &tls.Config{
			Certificates: []tls.Certificate{leaf.TLS}, MinVersion: tls.VersionTLS12,
			ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: clientCA.Pool(),
		}
	}
	localhost := srvCfg(serverCA.Issue(t, tlstest.Options{}))

	newClient := func(t *testing.T, server *tls.Config, mutate func(dir string, c *config.Database)) *dbClient {
		t.Helper()
		port, seen := fakeTLSServer(t, server)
		dir := t.TempDir()
		c := testConfig()
		c.Host, c.Port = "127.0.0.1", port
		c.ConnectTimeout = 2 * time.Second
		c.UsernameFile = filepath.Join(dir, "user")
		tlstest.WriteFile(t, c.UsernameFile, []byte("trove\n"))
		mutate(dir, &c)
		tt, err := newDBTLS(c)
		if err != nil {
			t.Fatal(err)
		}
		pc, err := newPoolConfig(c, poolSpec{
			kind: connKind{class: classMain}, ep: mainEndpoint(c), instance: "unit", maxConns: 2,
			lifetime: time.Minute, idleTime: time.Minute, healthEach: time.Minute,
		}, tt)
		if err != nil {
			t.Fatal(err)
		}
		d := &dbClient{seen: seen}
		dial := pc.ConnConfig.DialFunc
		pc.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dial(ctx, network, addr)
			if err == nil {
				d.mu.Lock()
				d.local = append(d.local, conn.LocalAddr().String())
				d.mu.Unlock()
			}
			return conn, err
		}
		if d.pool, err = pgxpool.NewWithConfig(context.Background(), pc); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.pool.Close)
		return d
	}

	t.Run("R-HA-015 R-ARCH-035 a root CA rewritten in place is used by the next new connection", func(t *testing.T) {
		var caFile string
		d := newClient(t, localhost, func(dir string, c *config.Database) {
			caFile = otherCA.WriteCA(t, dir, "ca.crt")
			c.SSLRootCertFile = caFile
		})
		if h, err := d.tryConnect(t); h.err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("an unrelated CA verified the server: server %v, client %v", h.err, err)
		}
		tlstest.WriteFile(t, caFile, serverCA.PEM)
		if h, err := d.tryConnect(t); h.err != nil {
			t.Fatalf("the rewritten CA was not used: server %v, client %v", h.err, err)
		}
		// a broken rewrite keeps the last good CA
		tlstest.WriteFile(t, caFile, []byte("-----BEGIN CERTIFICATE-----\nhalf"))
		if h, err := d.tryConnect(t); h.err != nil {
			t.Fatalf("a broken CA file replaced the good one: server %v, client %v", h.err, err)
		}
	})

	t.Run("R-HA-015 R-ARCH-035 a client certificate rotated in place is presented by the next new connection", func(t *testing.T) {
		var certFile, keyFile string
		d := newClient(t, localhost, func(dir string, c *config.Database) {
			c.SSLRootCertFile = serverCA.WriteCA(t, dir, "ca.crt")
			certFile, keyFile = clientCA.Issue(t, tlstest.Options{Serial: big.NewInt(41)}).Files(t, dir, "client")
			c.SSLCertFile, c.SSLKeyFile = certFile, keyFile
		})
		if h, _ := d.tryConnect(t); h.err != nil || h.clientSerial != 41 {
			t.Fatalf("first connection: %+v", h)
		}
		clientCA.Issue(t, tlstest.Options{Serial: big.NewInt(42)}).Write(t, certFile, keyFile)
		if h, _ := d.tryConnect(t); h.err != nil || h.clientSerial != 42 {
			t.Fatalf("after the rotation: %+v", h)
		}
	})

	// A server whose certificate names another host: verify-full refuses it,
	// verify-ca accepts it once its CA is trusted.
	elsewhere := srvCfg(serverCA.Issue(t, tlstest.Options{DNSNames: []string{"db.elsewhere.example"}}))

	t.Run("R-HA-015 verify-ca verifies the chain against the current CA and not the host name", func(t *testing.T) {
		var caFile string
		d := newClient(t, elsewhere, func(dir string, c *config.Database) {
			caFile = otherCA.WriteCA(t, dir, "ca.crt")
			c.SSLMode, c.SSLRootCertFile = "verify-ca", caFile
		})
		if h, _ := d.tryConnect(t); h.err == nil {
			t.Fatal("verify-ca accepted a server of another CA")
		}
		tlstest.WriteFile(t, caFile, serverCA.PEM)
		if h, err := d.tryConnect(t); h.err != nil {
			t.Fatalf("verify-ca with the rotated CA: server %v, client %v", h.err, err)
		}
	})

	t.Run("R-HA-015 verify-full checks the host name against the current CA", func(t *testing.T) {
		d := newClient(t, elsewhere, func(dir string, c *config.Database) {
			c.SSLRootCertFile = serverCA.WriteCA(t, dir, "ca.crt")
		})
		if h, err := d.tryConnect(t); h.err == nil || err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
			t.Fatalf("verify-full accepted a certificate for another host: server %v, client %v", h.err, err)
		}
	})

	t.Run("R-HA-015 a missing CA or a half pair is a configuration error at start", func(t *testing.T) {
		c := testConfig()
		c.SSLRootCertFile = filepath.Join(t.TempDir(), "missing.crt")
		if _, err := newDBTLS(c); err == nil || !strings.Contains(err.Error(), "database.ssl_root_cert_file") {
			t.Fatalf("err = %v", err)
		}
		c = testConfig()
		c.SSLCertFile = "/x.crt"
		if _, err := newDBTLS(c); err == nil || !strings.Contains(err.Error(), "ssl_key_file") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-HA-015 the connection string names no TLS file", func(t *testing.T) {
		c := testConfig()
		c.SSLRootCertFile, c.SSLCertFile, c.SSLKeyFile = "/ca.crt", "/c.crt", "/c.key"
		if s := connString(c, mainEndpoint(c)); strings.Contains(s, "sslrootcert") || strings.Contains(s, "sslcert") || strings.Contains(s, "sslkey") {
			t.Fatalf("connection string %q: pgx would read the files once", s)
		}
	})
}
