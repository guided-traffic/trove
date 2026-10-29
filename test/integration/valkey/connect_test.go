//go:build integration

package valkey

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

var connFamily = cluster.RegisterTestKeyFamily("itest:conn", 1, 60*time.Second)

func TestConnect(t *testing.T) {
	v := testenv.ValkeyConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("R-ARCH-031 connects through Sentinel over TLS as the ACL user", func(t *testing.T) {
		c := newClient(t, stackConfig(t, 2*time.Second))
		waitAvailable(t, c)
		st := c.Status()
		if want := sentinelPrimary(ctx, t, v); st.Primary != want {
			t.Fatalf("client is bound to %s, Sentinel announces %s", st.Primary, want)
		}
		if !strings.HasPrefix(st.Version, testenv.ValkeyLine(t)+".") {
			t.Fatalf("version %q, stack line %s", st.Version, testenv.ValkeyLine(t))
		}
		if st.Mode != "sentinel" || st.SentinelsConfigured != 3 {
			t.Fatalf("status = %+v", st)
		}
		if err := c.StartupError(); err != nil {
			t.Fatalf("StartupError = %v", err)
		}
		// the least-privilege ACL user, not the administrator
		admin := adminAt(t, v, st.Primary)
		mine := named(clientList(ctx, t, admin), c.Instance)
		if len(mine) == 0 {
			t.Fatal("no connection of this client on the primary")
		}
		for _, m := range mine {
			if m["user"] != v.Username {
				t.Errorf("connection %s runs as %q, want %q", m["name"], m["user"], v.Username)
			}
		}
	})

	t.Run("R-HA-056 CLIENT LIST names every connection trove:<instance>:... and shows at most six", func(t *testing.T) {
		c := newClient(t, stackConfig(t, 2*time.Second))
		waitAvailable(t, c)
		ch, _ := c.Channel(chanFamily, "conns")
		stop, err := c.Subscribe(ctx, ch, func(cluster.Message) {})
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		eventually(t, "the Pub/Sub connection", 10*time.Second, func() bool { return c.Status().Subscribed })

		admin := adminAt(t, v, c.Status().Primary)
		check := func(when string) {
			mine := named(clientList(ctx, t, admin), c.Instance)
			if len(mine) == 0 || len(mine) > 6 {
				t.Fatalf("%s: %d connections of trove:%s:..., want 1 to 6: %v", when, len(mine), c.Instance, mine)
			}
			var main, pubsub int
			for _, m := range mine {
				switch m["name"] {
				case "trove:" + c.Instance + ":main":
					main++
				case "trove:" + c.Instance + ":pubsub":
					pubsub++
				default:
					t.Errorf("%s: connection named %q", when, m["name"])
				}
			}
			t.Logf("%s: %d main and %d pubsub connections", when, main, pubsub)
			if main < 1 || main > 4 || pubsub != 1 {
				t.Errorf("%s: %d main and %d pubsub connections, want 1 to 4 and 1", when, main, pubsub)
			}
		}
		check("idle")

		// 64 concurrent writers: the count must not follow the request rate
		var wg sync.WaitGroup
		for w := 0; w < 64; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					k, err := c.Key(connFamily, "load", randHex(4))
					if err != nil {
						t.Error(err)
						return
					}
					if err := c.Set(ctx, k, "x"); err != nil {
						t.Errorf("Set under load: %v", err)
						return
					}
				}
			}()
		}
		wg.Wait()
		check("after 3200 writes from 64 goroutines")

		// the Sentinel side: one connection to the current Sentinel, named, plus
		// at most the short probe connections that are closed again
		var sentinelConns int
		for _, s := range v.Sentinels {
			sc := sentinelAdmin(t, v, s)
			for _, m := range named(clientList(ctx, t, sc), c.Instance) {
				if m["name"] == "trove:"+c.Instance+":sentinel" {
					sentinelConns++
				}
			}
		}
		if sentinelConns != 1 {
			t.Errorf("%d connections named trove:%s:sentinel on the Sentinels, want 1", sentinelConns, c.Instance)
		}
	})

	t.Run("R-HA-056 the data node accepts TLS only", func(t *testing.T) {
		conn, err := net.DialTimeout("tcp", v.Nodes[0], 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = conn.Write([]byte("PING\r\n"))
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		if strings.Contains(string(buf[:n]), "PONG") {
			t.Fatal("a plaintext PING was answered")
		}
		// and a client configured without TLS never gets in
		cfg := stackConfig(t, 2*time.Second)
		cfg.TLS = cfgTLSDisabled()
		cfg.AllowInsecure = true
		cfg.Sentinel.TLS = false
		c := newClient(t, cfg)
		time.Sleep(time.Second)
		if c.Available() {
			t.Fatal("a client without TLS reached the TLS-only Sentinels or data nodes")
		}
	})

	t.Run("R-ARCH-031 the example ACL rule needs no more: ACL LOG has no denial for this client", func(t *testing.T) {
		c := newClient(t, stackConfig(t, 2*time.Second))
		waitAvailable(t, c)
		exerciseAPI(ctx, t, c)
		admin := adminAt(t, v, c.Status().Primary)
		log, err := admin.Do(ctx, admin.B().Arbitrary("ACL", "LOG", "100").Build()).ToArray()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range log {
			m, err := e.AsMap()
			if err != nil {
				continue
			}
			msg := m["client-info"]
			info, _ := msg.ToString()
			if strings.Contains(info, "name=trove:"+c.Instance+":") {
				t.Errorf("ACL denial for trove:%s: %s", c.Instance, info)
			}
		}
	})
}
