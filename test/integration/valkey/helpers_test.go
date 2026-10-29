//go:build integration

package valkey

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// runPrefix is the key prefix of this run. It starts with the stack's ACL
// pattern (trove:*), and separates this run's keys from those of tests that
// run in parallel on the same Valkey.
var runPrefix = "trove:it" + randHex(3) + ":"

// writeFile stores a credential or key file in a fresh directory.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// stackConfig is the configuration of a replica that talks to the stack the way
// the chart configures it: Sentinel discovery, TLS with certificate
// verification, the least-privilege ACL user. refresh is
// valkey.sentinel.refresh_interval.
func stackConfig(t *testing.T, refresh time.Duration) config.Valkey {
	t.Helper()
	v := testenv.ValkeyConfig(t)
	return config.Valkey{
		Mode:         "sentinel",
		KeyPrefix:    runPrefix,
		UsernameFile: writeFile(t, "username", v.Username+"\n"),
		PasswordFile: writeFile(t, "password", v.Password+"\n"),
		Sentinel: config.ValkeySentinel{
			Addresses:       v.Sentinels,
			PrimaryName:     v.PrimaryName,
			PasswordFile:    writeFile(t, "sentinel-password", v.SentinelPassword+"\n"),
			TLS:             true,
			RefreshInterval: refresh,
		},
		TLS:                  config.ValkeyTLS{Mode: "verify-full", CAFile: v.CAFile},
		ConnectTimeout:       2 * time.Second,
		CommandTimeout:       500 * time.Millisecond,
		SentinelQueryTimeout: 300 * time.Millisecond,
		MaxConnLifetime:      30 * time.Minute,
		HealthCheckInterval:  500 * time.Millisecond,
		Reconnect:            config.ValkeyReconnect{InitialBackoff: 100 * time.Millisecond, MaxBackoff: time.Second},
		CircuitBreaker:       config.ValkeyBreaker{FailureThreshold: 5, OpenDuration: 2 * time.Second},
	}
}

// syncBuffer collects log output from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// client is a cluster.Valkey under test with its log, registry and instance
// name. The raw commands come from cluster.Raw, which only the integration
// build tag compiles: outside internal/cluster no code can read or write a
// Valkey key (Q-ARCH-16).
type client struct {
	*cluster.Valkey
	cluster.IntegrationRaw
	Instance string
	Log      *syncBuffer
	Reg      *prometheus.Registry
}

func newClient(t *testing.T, cfg config.Valkey) *client { return newClientIn(t, cfg, randHex(8)) }

// newClientIn creates a client of the installation whose keyspace ID is ks:
// replicas of one installation share it.
func newClientIn(t *testing.T, cfg config.Valkey, ks string) *client {
	t.Helper()
	c := &client{Instance: "it" + randHex(3), Log: &syncBuffer{}, Reg: prometheus.NewRegistry()}
	log := slog.New(slog.NewJSONHandler(c.Log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var err error
	c.Valkey, err = cluster.NewValkey(t.Context(), cfg, c.Instance, "all", log, c.Reg, cluster.WithKeyspaceID(ks))
	if err != nil {
		t.Fatalf("NewValkey: %v", err)
	}
	c.IntegrationRaw = cluster.Raw(c.Valkey)
	t.Cleanup(c.Close)
	return c
}

func eventually(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

func waitAvailable(t *testing.T, c *client) {
	t.Helper()
	eventually(t, "the client becoming available (status "+fmt.Sprint(c.Status().DegradedReason)+")", 15*time.Second, c.Available)
}

// metric reads one series of a family; label is the first label value, empty
// for an unlabelled family.
func metric(t *testing.T, reg *prometheus.Registry, name, label string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if label != "" && (len(m.GetLabel()) == 0 || m.GetLabel()[0].GetValue() != label) {
				continue
			}
			if m.GetGauge() != nil {
				return m.GetGauge().GetValue()
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// --- direct access for inspection and failover orchestration -------------------

func caPool(t *testing.T, file string) *x509.CertPool {
	t.Helper()
	pem, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatalf("no certificate in %s", file)
	}
	return pool
}

// direct connects to one node as the given user; tlsCfg is nil for a plain port.
func direct(t *testing.T, addr, user, pass string, tlsCfg *tls.Config) valkey.Client {
	t.Helper()
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{addr}, Username: user, Password: pass, TLSConfig: tlsCfg,
		DisableCache: true, ForceSingleClient: true, DisableRetry: true,
	})
	if err != nil {
		t.Fatalf("connect to %s as %q: %v", addr, user, err)
	}
	t.Cleanup(c.Close)
	return c
}

func stackTLS(t *testing.T, v testenv.Valkey) *tls.Config {
	return &tls.Config{RootCAs: caPool(t, v.CAFile), MinVersion: tls.VersionTLS12}
}

func adminAt(t *testing.T, v testenv.Valkey, addr string) valkey.Client {
	return direct(t, addr, v.AdminUsername, v.AdminPassword, stackTLS(t, v))
}

func sentinelAdmin(t *testing.T, v testenv.Valkey, addr string) valkey.Client {
	return direct(t, addr, "", v.SentinelPassword, stackTLS(t, v))
}

// sentinelPrimary asks the Sentinels, in order, where the primary is.
func sentinelPrimary(ctx context.Context, t *testing.T, v testenv.Valkey) string {
	t.Helper()
	var last error
	for _, s := range v.Sentinels {
		c := sentinelAdmin(t, v, s)
		arr, err := c.Do(ctx, c.B().SentinelGetMasterAddrByName().Master(v.PrimaryName).Build()).AsStrSlice()
		if err != nil || len(arr) != 2 {
			last = err
			continue
		}
		return net.JoinHostPort(arr[0], arr[1])
	}
	t.Fatalf("no Sentinel answered get-master-addr-by-name: %v", last)
	return ""
}

// clientList returns the CLIENT LIST entries of a node as field maps.
func clientList(ctx context.Context, t *testing.T, c valkey.Client, extra ...string) []map[string]string {
	t.Helper()
	args := append([]string{"CLIENT", "LIST"}, extra...)
	text, err := c.Do(ctx, c.B().Arbitrary(args...).Build()).ToString()
	if err != nil {
		t.Fatalf("CLIENT LIST: %v", err)
	}
	var out []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := map[string]string{}
		for _, kv := range strings.Fields(line) {
			k, v, _ := strings.Cut(kv, "=")
			m[k] = v
		}
		out = append(out, m)
	}
	return out
}

// named returns the entries whose client name starts with trove:<instance>:.
func named(list []map[string]string, instance string) []map[string]string {
	var out []map[string]string
	for _, m := range list {
		if strings.HasPrefix(m["name"], "trove:"+instance+":") {
			out = append(out, m)
		}
	}
	return out
}

func waitForReplica(ctx context.Context, t *testing.T, v testenv.Valkey, primary string) {
	t.Helper()
	admin := adminAt(t, v, primary)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		info, err := admin.Do(ctx, admin.B().Info().Section("replication").Build()).ToString()
		if err == nil && strings.Contains(info, "connected_slaves:1") && strings.Contains(info, "state=online") {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s has no online replica after 90s", primary)
}

// failover forces a Sentinel failover and returns the old and new primary and
// the time at which a poll of the Sentinels first saw the new one.
func failover(ctx context.Context, t *testing.T, v testenv.Valkey) (oldAddr, newAddr string, announced time.Time) {
	t.Helper()
	oldAddr = sentinelPrimary(ctx, t, v)
	waitForReplica(ctx, t, v, oldAddr)
	s := sentinelAdmin(t, v, v.Sentinels[0])
	if err := s.Do(ctx, s.B().SentinelFailover().Master(v.PrimaryName).Build()).Error(); err != nil {
		t.Fatalf("SENTINEL FAILOVER: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		cur := sentinelPrimary(ctx, t, v)
		if cur != oldAddr {
			return oldAddr, cur, time.Now()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the primary is still %s after 60s", oldAddr)
	return "", "", time.Time{}
}

// restoreLayout fails over until the original primary (the first data node)
// serves again, and waits for the replica to be online.
func restoreLayout(t *testing.T, v testenv.Valkey) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		cur := sentinelPrimary(ctx, t, v)
		if cur == v.Nodes[0] {
			waitForReplica(ctx, t, v, cur)
			return
		}
		failover(ctx, t, v)
	}
	cur := sentinelPrimary(ctx, t, v)
	waitForReplica(ctx, t, v, cur)
	if cur != v.Nodes[0] {
		t.Errorf("the stack could not be returned to its initial layout: primary is %s, was %s", cur, v.Nodes[0])
	}
}
