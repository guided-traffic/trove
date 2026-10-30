//go:build integration

package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// valkeyAdmin is a direct connection to the primary as the administrator, for the
// tests that inspect or write keys the way another process would.
func (e *env) valkeyAdmin(r *replica) valkey.Client {
	e.t.Helper()
	v := testenv.ValkeyConfig(e.t)
	pem, err := os.ReadFile(v.CAFile)
	if err != nil {
		e.t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{r.vk.Status().Primary}, Username: v.AdminUsername, Password: v.AdminPassword,
		TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DisableCache: true, ForceSingleClient: true, DisableRetry: true,
	})
	if err != nil {
		e.t.Fatalf("connect to the primary: %v", err)
	}
	e.t.Cleanup(c.Close)
	return c
}

// keyBase is the prefix of the keys of this installation: the configured prefix and the
// keyspace id.
func (e *env) keyBase() string { return e.prefix + e.ks[:12] + ":" }

// lockKey is the fetch lock of a flight (R-ARCH-030).
func (e *env) lockKey(rem proxy.Remote, path, variant string) string {
	return e.keyBase() + "lock:fetch:v1:" + cluster.NewFlightKey(rem.ID(), path, variant).String()
}

// negativeKey is the negative entry of a path (R-REPO-028).
func (e *env) negativeKey(rem proxy.Remote, path, variant string) string {
	return e.keyBase() + "neg:v1:" + strconv.FormatInt(rem.ID(), 10) + ":" + cluster.NewFlightKey(rem.ID(), path, variant).String()
}

func getKey(t *testing.T, c valkey.Client, key string) (string, bool) {
	t.Helper()
	s, err := c.Do(context.Background(), c.B().Get().Key(key).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("GET %s: %v", key, err)
	}
	return s, true
}

// setForeign writes a lock key as another process would; ttl 0 writes it without an
// expiry.
func setForeign(t *testing.T, c valkey.Client, key string, ttl time.Duration) {
	t.Helper()
	cmd := c.B().Set().Key(key).Value("foreign-holder")
	var err error
	if ttl > 0 {
		err = c.Do(context.Background(), cmd.Px(ttl).Build()).Error()
	} else {
		err = c.Do(context.Background(), cmd.Build()).Error()
	}
	if err != nil {
		t.Fatalf("SET %s: %v", key, err)
	}
}

func delKey(t *testing.T, c valkey.Client, key string) {
	t.Helper()
	if err := c.Do(context.Background(), c.B().Del().Key(key).Build()).Error(); err != nil {
		t.Fatalf("DEL %s: %v", key, err)
	}
}
