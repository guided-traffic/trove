//go:build integration

package bastionshared

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The Bastion's Valkey user of the stack (test/integration/conf/users.acl): the commands of the least-privilege
// rule, on the keys and channels below trove-bastion: only.
const (
	bastionUser = "bastion"
	bastionPass = "bastion-valkey-it"
	aclRules    = "on >" + bastionPass + " resetkeys ~trove-bastion:* resetchannels &trove-bastion:* -@all +get +set +delifeq +incr +expire +multi +exec +publish +subscribe +unsubscribe +sunsubscribe +punsubscribe +discard +ping +hello +role +client|setname +client|setinfo"
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func tlsConfig(t *testing.T, v testenv.Valkey) *tls.Config {
	t.Helper()
	pem, err := os.ReadFile(v.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatalf("no certificate in %s", v.CAFile)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

var aclOnce sync.Once

// ensureBastionUser makes sure both data nodes know the user of users.acl. A stack started before that line
// existed does not; ACL SETUSER with the same rules is idempotent and touches nothing else. ACL changes are
// not replicated, so every node is asked.
func ensureBastionUser(t *testing.T) {
	t.Helper()
	v := testenv.ValkeyConfig(t)
	aclOnce.Do(func() {
		for _, node := range v.Nodes {
			c, err := valkey.NewClient(valkey.ClientOption{
				InitAddress: []string{node}, Username: v.AdminUsername, Password: v.AdminPassword, TLSConfig: tlsConfig(t, v),
				DisableCache: true, ForceSingleClient: true, DisableRetry: true,
			})
			if err != nil {
				t.Fatalf("connect to %s as the administrative user: %v", node, err)
			}
			args := append([]string{"ACL", "SETUSER", bastionUser}, splitFields(aclRules)...)
			if err := c.Do(t.Context(), c.B().Arbitrary(args[0], args[1]).Args(args[2:]...).Build()).Error(); err != nil {
				c.Close()
				t.Fatalf("ACL SETUSER on %s: %v", node, err)
			}
			c.Close()
		}
	})
}

func splitFields(s string) []string {
	var out []string
	start := -1
	for i, r := range s + " " {
		if r == ' ' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	return out
}

// admin is a client of the primary as the administrative user, for what the Bastion's own user may not see:
// the raw records and their expiry.
func admin(t *testing.T) valkey.Client {
	t.Helper()
	v := testenv.ValkeyConfig(t)
	tc := tlsConfig(t, v)
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: v.Sentinels, Username: v.AdminUsername, Password: v.AdminPassword, TLSConfig: tc, DisableCache: true,
		Sentinel: valkey.SentinelOption{MasterSet: v.PrimaryName, Password: v.SentinelPassword, TLSConfig: tc},
	})
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// valkeySection is the valkey section of a Bastion that talks to the stack the way the chart configures it:
// Sentinel discovery, TLS with verification, the least-privilege user, and a key prefix of its own. With
// unreachable it points at a Sentinel nothing listens on: the state "Valkey unavailable" (R-BAS-034).
func valkeySection(t *testing.T, prefix string, unreachable bool) config.BastionValkey {
	t.Helper()
	v := testenv.ValkeyConfig(t)
	sentinels := v.Sentinels
	if unreachable {
		sentinels = []string{"127.0.0.1:1"}
	}
	return config.BastionValkey{
		Mode:         "sentinel",
		KeyPrefix:    prefix,
		UsernameFile: writeFile(t, "username", bastionUser+"\n"),
		PasswordFile: writeFile(t, "password", bastionPass+"\n"),
		Sentinel: config.ValkeySentinel{
			Addresses: sentinels, PrimaryName: v.PrimaryName, PasswordFile: writeFile(t, "sentinel-password", v.SentinelPassword+"\n"),
			TLS: true, RefreshInterval: 2 * time.Second,
		},
		TLS:                  config.BastionValkeyTLS{Mode: "verify-full", CAFile: v.CAFile},
		ConnectTimeout:       2 * time.Second,
		CommandTimeout:       500 * time.Millisecond,
		SentinelQueryTimeout: 300 * time.Millisecond,
		MaxConnLifetime:      30 * time.Minute,
		HealthCheckInterval:  500 * time.Millisecond,
		Reconnect:            config.ValkeyReconnect{InitialBackoff: 100 * time.Millisecond, MaxBackoff: time.Second},
		CircuitBreaker:       config.ValkeyBreaker{FailureThreshold: 5, OpenDuration: 2 * time.Second},
	}
}

// clock is the injectable clock the replicas of a test share: the limits are bounded by what the requirement
// bounds, and nothing waits for a minute or a quarter of an hour.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Now().Truncate(time.Hour)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// replica is one Bastion replica's shared state and guard.
type replica struct {
	name string
	st   *shared.State
	g    *guard.Instance
	reg  *prometheus.Registry
	m    *metrics.Metrics
	valk config.BastionValkey
}

type setup struct {
	prefix      string
	replicas    int
	unreachable bool
	clk         *clock
	signer      *cache.Signer
	mutate      func(*guard.Options)
}

// newSetup is a Bastion of two replicas on its own key prefix.
func newSetup(t *testing.T) *setup {
	t.Helper()
	ensureBastionUser(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	s, err := cache.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	return &setup{prefix: "it-" + randHex(4) + ".example.com", replicas: 2, clk: newClock(), signer: s}
}

// defaults are the defaults of R-BAS-035 to R-BAS-037.
func defaults() guard.Options {
	return guard.Options{
		Limits: config.BastionLimits{
			Authenticated: config.BastionLimitAuthenticated{RequestsPerSecond: 50, Burst: 200},
			Anonymous:     config.BastionLimitAnonymous{RequestsPerSecond: 10, Burst: 40},
			Misses:        config.BastionMisses{AuthenticatedPerMinute: 300, AnonymousPerMinute: 30},
		},
		FailedAuth: config.BastionFailedAuth{MaxFailures: 20, Window: 10 * time.Minute, Timeout: 15 * time.Minute},
		Blocking:   config.BastionBlocking{Threshold: 300, Window: time.Minute, Duration: 15 * time.Minute, MaxDuration: 24 * time.Hour},
	}
}

// replica starts replica n of the Bastion.
func (s *setup) replica(t *testing.T, n int) *replica {
	t.Helper()
	r := &replica{name: fmt.Sprintf("bastion-%d", n), reg: prometheus.NewRegistry(), valk: valkeySection(t, s.prefix, s.unreachable)}
	var err error
	if r.m, err = metrics.New(r.reg); err != nil {
		t.Fatal(err)
	}
	st, closer, err := shared.Open(t.Context(), shared.Options{
		Valkey: r.valk, Replicas: s.replicas, Instance: r.name + "-" + randHex(3), Signer: s.signer,
		Registry: r.reg, Metrics: r.m, Now: s.clk.now,
	})
	if err != nil {
		t.Fatalf("shared.Open: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	r.st = st
	o := defaults()
	if s.mutate != nil {
		s.mutate(&o)
	}
	o.State, o.Metrics, o.Now = st, r.m, s.clk.now
	if r.g, err = guard.New(o); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.g.Close() })
	return r
}

// pair starts the two replicas and, unless the Valkey is meant to be unreachable, waits until both reach it.
func (s *setup) pair(t *testing.T) (*replica, *replica) {
	t.Helper()
	a, b := s.replica(t, 0), s.replica(t, 1)
	if !s.unreachable {
		for _, r := range []*replica{a, b} {
			waitShared(t, r)
		}
	}
	return a, b
}

func waitShared(t *testing.T, r *replica) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if r.st.Shared() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: Valkey is not available after 20 s", r.name)
}

// address returns a client address no other test uses.
func address() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("10.%d.%d.%d", b[0], b[1], b[2])
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// gauge reads a gauge or counter of a registry; the series with the labels, or 0.
func metricValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
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
			if matches(m, labels) {
				if m.Counter != nil {
					return m.GetCounter().GetValue()
				}
				return m.GetGauge().GetValue()
			}
		}
	}
	return 0
}

func matches(m *dto.Metric, labels map[string]string) bool {
	for k, want := range labels {
		found := false
		for _, l := range m.GetLabel() {
			if l.GetName() == k && l.GetValue() == want {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

var ctx = context.Background()
