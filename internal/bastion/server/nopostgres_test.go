package server

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestR_BAS_001_TheRoleOpensNoConnectionToPostgreSQL proves at runtime what the import rule proves for the
// source (Q-BAS-129): the role is compiled with packages of the main installation behind its platform
// packages, and enables none of them. A trap listens where a PostgreSQL client of this process would look
// (the environment of libpq and of the main installation's database section names it); the role starts,
// becomes ready, serves a read and a miss, and the trap never accepts a connection. The role's metrics carry
// no database collector.
func TestR_BAS_001_TheRoleOpensNoConnectionToPostgreSQL(t *testing.T) {
	trap, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trap.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			c, err := trap.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	host, port, _ := net.SplitHostPort(trap.Addr().String())
	for k, v := range map[string]string{
		"PGHOST": host, "PGPORT": port, "PGDATABASE": "trove", "PGUSER": "trove", "PGPASSWORD": "x",
		"DATABASE_URL": "postgres://trove:x@" + trap.Addr().String() + "/trove",
	} {
		t.Setenv(k, v)
	}
	environ := []string{
		"TROVE_POD_NAME=trove-bastion-test", "TROVE_DATABASE_HOST=" + host, "TROVE_DATABASE_PORT=" + port,
		"TROVE_DATABASE_NAME=trove", "TROVE_DATABASE_USER=trove",
	}

	// The configuration loader refuses a TROVE_DATABASE_* setting for the role (R-BAS-002); the process
	// environment and the server's own environment still carry the address, so a pool opened by mistake
	// would find it.
	loaded, err := load(t, minimal(writeTokenFile(t, t.TempDir()), ""))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	logs := &syncBuffer{}
	srv, err := New(Options{Loaded: loaded, Environ: environ, LogWriter: logs, DrainDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		srv.abort()
		t.Fatalf("Start: %v\n%s", err, logs.String())
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	waitReady(t, &harness{srv: srv, logs: logs})

	_, _, _ = get(t, "http://"+srv.ClientAddr().String()+"/api/npm/web/npm-all/lodash")
	_, _, _ = get(t, "http://"+srv.ClientAddr().String()+"/api/v1/system/ping")
	time.Sleep(300 * time.Millisecond) // a connection attempt made in the background would show by now

	if n := accepted.Load(); n != 0 {
		t.Fatalf("the role opened %d connections to the PostgreSQL address", n)
	}
	body := metricsBody(t, srv)
	for _, banned := range []string{"trove_db_", "pgx", "trove_postgres"} {
		if strings.Contains(body, banned) {
			t.Errorf("the role's metrics carry %q:\n%s", banned, grepLines(body, banned))
		}
	}
}
