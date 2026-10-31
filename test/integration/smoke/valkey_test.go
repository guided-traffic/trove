//go:build integration

package smoke

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

func tlsConfig(t *testing.T, caFile string) *tls.Config {
	t.Helper()
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatalf("read CA %s: %v", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatalf("no certificate in %s", caFile)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// sentinelClient is a client built the way trove builds its own: Sentinel
// discovery, TLS with certificate verification, the least-privilege ACL user.
func sentinelClient(t *testing.T, v testenv.Valkey) valkey.Client {
	t.Helper()
	tc := tlsConfig(t, v.CAFile)
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:  v.Sentinels,
		Username:     v.Username,
		Password:     v.Password,
		ClientName:   "trove:smoke:main",
		TLSConfig:    tc,
		DisableCache: true,
		Sentinel: valkey.SentinelOption{
			MasterSet:               v.PrimaryName,
			Password:                v.SentinelPassword,
			TLSConfig:               tc,
			ClientName:              "trove:smoke:sentinel",
			TopologyRefreshInterval: 2 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("connect through Sentinel: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// direct connects to one address as the administrative user.
func direct(t *testing.T, v testenv.Valkey, addr, user, pass string) valkey.Client {
	t.Helper()
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:       []string{addr},
		Username:          user,
		Password:          pass,
		TLSConfig:         tlsConfig(t, v.CAFile),
		DisableCache:      true,
		ForceSingleClient: true,
	})
	if err != nil {
		t.Fatalf("connect to %s: %v", addr, err)
	}
	t.Cleanup(c.Close)
	return c
}

// sentinelPrimary asks the first answering Sentinel where the primary is.
func sentinelPrimary(ctx context.Context, t *testing.T, v testenv.Valkey) string {
	t.Helper()
	var last error
	for _, s := range v.Sentinels {
		c := direct(t, v, s, "", v.SentinelPassword)
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

func TestValkeyDiscoversThePrimaryThroughSentinel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v := testenv.ValkeyConfig(t)
	c := sentinelClient(t, v)

	if err := c.Do(ctx, c.B().Ping().Build()).Error(); err != nil {
		t.Fatalf("PING: %v", err)
	}
	role, err := c.Do(ctx, c.B().Role().Build()).ToArray()
	if err != nil || len(role) == 0 {
		t.Fatalf("ROLE: %v", err)
	}
	if name, _ := role[0].ToString(); name != "master" {
		t.Fatalf("the client is connected to a %q, want the master", name)
	}

	want := sentinelPrimary(ctx, t, v)
	found := false
	for _, n := range v.Nodes {
		if n == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("Sentinel announces %s, which is none of the data nodes %v: the announced address is not the host-mapped one", want, v.Nodes)
	}
}

func TestValkeyServerLineAndTLSOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v := testenv.ValkeyConfig(t)
	line := testenv.ValkeyLine(t)
	admin := direct(t, v, v.Nodes[0], v.AdminUsername, v.AdminPassword)

	info, err := admin.Do(ctx, admin.B().Info().Section("server").Build()).ToString()
	if err != nil {
		t.Fatalf("INFO server: %v", err)
	}
	if !strings.Contains(info, "valkey_version:"+line+".") {
		t.Fatalf("server is not Valkey %s:\n%s", line, info)
	}

	// port 0: a plaintext client gets no answer from the TLS port.
	conn, err := net.DialTimeout("tcp", v.Nodes[0], 3*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", v.Nodes[0], err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Write([]byte("PING\r\n"))
	buf := make([]byte, 64)
	n, _ := conn.Read(buf)
	if strings.Contains(string(buf[:n]), "PONG") {
		t.Fatalf("the data node answers a plaintext PING: TLS is not enforced")
	}
}

// TestValkeyExampleACLRuleHasNoDenial runs every command trove needs as the
// least-privilege user and asserts that ACL LOG stays empty (R-ARCH-031's example
// rule), and that a command outside the rule is denied.
func TestValkeyExampleACLRuleHasNoDenial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	v := testenv.ValkeyConfig(t)
	c := sentinelClient(t, v)
	primary := sentinelPrimary(ctx, t, v)
	admin := direct(t, v, primary, v.AdminUsername, v.AdminPassword)

	if err := admin.Do(ctx, admin.B().Arbitrary("ACL", "LOG", "RESET").Build()).Error(); err != nil {
		t.Fatalf("ACL LOG RESET: %v", err)
	}

	key := v.KeyPrefix + "smoke:acl:" + fmt.Sprint(time.Now().UnixNano())
	counter := key + ":n"
	channel := v.KeyPrefix + "smoke:chan"

	steps := []struct {
		name string
		cmd  valkey.Completed
	}{
		{"SET", c.B().Set().Key(key).Value("v1").Build()},
		{"GET", c.B().Get().Key(key).Build()},
		{"INCR", c.B().Incr().Key(counter).Build()},
		{"EXPIRE", c.B().Expire().Key(counter).Seconds(60).Build()},
		{"EXPIRE on the value", c.B().Expire().Key(key).Seconds(60).Build()},
		{"SET IFEQ", c.B().Arbitrary("SET", key, "v2", "IFEQ", "v1").Build()},
		{"DELIFEQ", c.B().Arbitrary("DELIFEQ", key, "v2").Build()},
		{"PUBLISH", c.B().Publish().Channel(channel).Message("hello").Build()},
	}
	for _, s := range steps {
		if err := c.Do(ctx, s.cmd).Error(); err != nil {
			t.Errorf("%s: %v", s.name, err)
		}
	}
	for i, r := range c.DoMulti(ctx, c.B().Multi().Build(), c.B().Incr().Key(counter).Build(), c.B().Exec().Build()) {
		if err := r.Error(); err != nil {
			t.Errorf("MULTI/EXEC step %d: %v", i, err)
		}
	}

	received := make(chan string, 1)
	subCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = c.Receive(subCtx, c.B().Subscribe().Channel(channel).Build(), func(m valkey.PubSubMessage) {
			select {
			case received <- m.Message:
			default:
			}
		})
	}()
	deadline := time.After(10 * time.Second)
	for got := false; !got; {
		_ = c.Do(ctx, c.B().Publish().Channel(channel).Message("ping").Build()).Error()
		select {
		case <-received:
			got = true
		case <-time.After(200 * time.Millisecond):
		case <-deadline:
			t.Fatal("no Pub/Sub message arrived on the subscribed channel")
		}
	}

	log, err := admin.Do(ctx, admin.B().Arbitrary("ACL", "LOG").Build()).ToArray()
	if err != nil {
		t.Fatalf("ACL LOG: %v", err)
	}
	if len(log) != 0 {
		t.Errorf("ACL LOG holds %d denials after trove's commands; the example rule is too narrow: %v", len(log), log)
	}

	// The rule is a real restriction: a command outside it is refused.
	if err := c.Do(ctx, c.B().Del().Key(key).Build()).Error(); err == nil {
		t.Error("DEL succeeded: the ACL user is not restricted to the commands of the example rule")
	}
	if err := c.Do(ctx, c.B().Set().Key("other:"+key).Value("x").Build()).Error(); err == nil {
		t.Error("SET outside the key prefix succeeded: the ACL user is not restricted to its prefix")
	}
}

// TestValkeyClientFollowsASentinelFailover forces a failover, as the stack's
// Sentinels announce host-reachable addresses, and asserts that a client on the
// host writes to the new primary. A second failover returns the stack to its
// initial layout.
func TestValkeyClientFollowsASentinelFailover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	v := testenv.ValkeyConfig(t)
	c := sentinelClient(t, v)

	for round := 1; round <= 2; round++ {
		before := sentinelPrimary(ctx, t, v)
		key := fmt.Sprintf("%ssmoke:failover:%d:%d", v.KeyPrefix, round, time.Now().UnixNano())
		if err := c.Do(ctx, c.B().Set().Key(key).Value("before").Ex(5*time.Minute).Build()).Error(); err != nil {
			t.Fatalf("round %d: SET before failover: %v", round, err)
		}

		sentinel := direct(t, v, v.Sentinels[0], "", v.SentinelPassword)
		if err := sentinel.Do(ctx, sentinel.B().SentinelFailover().Master(v.PrimaryName).Build()).Error(); err != nil {
			t.Fatalf("round %d: SENTINEL FAILOVER: %v", round, err)
		}
		after := waitForNewPrimary(ctx, t, v, before)

		// The client, which holds no knowledge of the change, writes again.
		newKey := key + ":after"
		deadline := time.Now().Add(40 * time.Second)
		var werr error
		for time.Now().Before(deadline) {
			if werr = c.Do(ctx, c.B().Set().Key(newKey).Value("after").Ex(5*time.Minute).Build()).Error(); werr == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if werr != nil {
			t.Fatalf("round %d: the client never reached the new primary %s: %v", round, after, werr)
		}

		node := direct(t, v, after, v.AdminUsername, v.AdminPassword)
		got, err := node.Do(ctx, node.B().Get().Key(newKey).Build()).ToString()
		if err != nil || got != "after" {
			t.Fatalf("round %d: the write is not on the new primary %s: %q, %v", round, after, got, err)
		}
		t.Logf("round %d: primary moved %s -> %s and the client followed", round, before, after)
	}
}

func waitForNewPrimary(ctx context.Context, t *testing.T, v testenv.Valkey, old string) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		cur := sentinelPrimary(ctx, t, v)
		if cur != old {
			// Wait for the old primary to rejoin as a replica so the next round
			// has a replica to promote.
			waitForReplica(ctx, t, v, cur)
			waitForSentinelReplica(ctx, t, v)
			return cur
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("the primary is still %s after 60s", old)
	return ""
}

func waitForReplica(ctx context.Context, t *testing.T, v testenv.Valkey, primary string) {
	t.Helper()
	admin := direct(t, v, primary, v.AdminUsername, v.AdminPassword)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		info, err := admin.Do(ctx, admin.B().Info().Section("replication").Build()).ToString()
		if err == nil && strings.Contains(info, "connected_slaves:1") && strings.Contains(info, "state=online") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s has no online replica after 60s", primary)
}

// waitForSentinelReplica waits until the Sentinel that the next round asks to
// fail over lists a replica it would promote. The primary reports the replica as
// online before that Sentinel's own INFO of it (refreshed every 10 s) does, and a
// SENTINEL FAILOVER in between answers NOGOODSLAVE.
func waitForSentinelReplica(ctx context.Context, t *testing.T, v testenv.Valkey) {
	t.Helper()
	sentinel := direct(t, v, v.Sentinels[0], "", v.SentinelPassword)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		replicas, err := sentinel.Do(ctx, sentinel.B().SentinelReplicas().Master(v.PrimaryName).Build()).ToArray()
		for _, r := range replicas {
			if m, merr := r.AsStrMap(); err == nil && merr == nil && m["flags"] == "slave" && m["master-link-status"] == "ok" {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("Sentinel %s lists no replica it would promote after 60s", v.Sentinels[0])
}
