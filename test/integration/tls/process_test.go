//go:build integration

package tls

import (
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// TestListenerRotation runs `trove serve --role all` with TLS on both listeners
// and rotates the certificate files in place under a constant load (E0.15 at
// process level).
func TestListenerRotation(t *testing.T) {
	in := newInstall(t)
	in.run(t, "migrate", "up")

	ca := tlstest.NewCA(t, "trove listener ca")
	certFile, keyFile := ca.Issue(t, tlstest.Options{Serial: big.NewInt(1001)}).Files(t, in.dir, "tls")
	const reload = time.Second
	r := in.serve(t, "all", certificatesEnv("TROVE_SERVER_TLS_CERTIFICATES", certFile, keyFile),
		"TROVE_SERVER_TLS_RELOAD_INTERVAL="+reload.String())
	fresh := httpsClient(ca, false)
	r.waitReady(t, fresh, 60*time.Second)
	pid := r.cmd.Process.Pid

	t.Run("R-ARCH-035 the client listener serves the API over TLS and the ops listener serves the probes over TLS", func(t *testing.T) {
		if code, body, serial, err := fetch(fresh, "https://"+r.client+"/api/v1/system/ping"); err != nil || code != 200 || body != "OK" || serial != 1001 {
			t.Fatalf("ping: %d %q serial %d %v", code, body, serial, err)
		}
		if code, _, serial, err := fetch(fresh, "https://"+r.ops+"/readyz"); err != nil || code != 200 || serial != 1001 {
			t.Fatalf("readyz: %d serial %d %v", code, serial, err)
		}
	})

	t.Run("R-ARCH-035 a plaintext request to either TLS listener fails", func(t *testing.T) {
		plain := &http.Client{Timeout: 3 * time.Second}
		for _, u := range []string{"http://" + r.client + "/api/v1/system/ping", "http://" + r.ops + "/readyz"} {
			resp, err := plain.Get(u)
			if err != nil {
				continue // refused at the transport: fine
			}
			resp.Body.Close()
			if resp.StatusCode == 200 || resp.Header.Get("X-Request-Id") != "" {
				t.Errorf("plaintext %s was served by trove: %d", u, resp.StatusCode)
			}
		}
	})

	t.Run("E0.15 R-ARCH-035 rotating the certificate under load: no failed request, the new serial within reload_interval + 1 s, no restart", func(t *testing.T) {
		var (
			stop     atomic.Bool
			wg       sync.WaitGroup
			ok, fail atomic.Int64
			mu       sync.Mutex
			failures []string
		)
		record := func(code int, err error) {
			if err == nil && code == 200 {
				ok.Add(1)
				return
			}
			fail.Add(1)
			mu.Lock()
			if len(failures) < 10 {
				failures = append(failures, fmt.Sprintf("%d %v", code, err))
			}
			mu.Unlock()
		}
		// New handshakes on both listeners.
		for i := range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := httpsClient(ca, false)
				url := "https://" + r.client + "/api/v1/system/ping"
				if i%3 == 2 {
					url = "https://" + r.ops + "/readyz"
				}
				for !stop.Load() {
					code, _, _, err := fetch(c, url)
					record(code, err)
					time.Sleep(5 * time.Millisecond)
				}
			}()
		}
		// One kept-alive connection: it was opened before the rotation and keeps
		// the certificate it negotiated (Q-ARCH-31).
		kept := httpsClient(ca, true)
		var keptSerials sync.Map
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				code, _, serial, err := fetch(kept, "https://"+r.client+"/api/v1/system/ping")
				record(code, err)
				keptSerials.Store(serial, true)
				time.Sleep(20 * time.Millisecond)
			}
		}()
		time.Sleep(time.Second)

		// Rotate in place: the files keep their names and are overwritten one after
		// the other, so a reader can catch the certificate and the key half-way.
		next := ca.Issue(t, tlstest.Options{Serial: big.NewInt(1002)})
		next.Write(t, certFile, keyFile)
		rotated := time.Now()

		var took time.Duration
		for {
			_, _, serial, err := fetch(fresh, "https://"+r.client+"/api/v1/system/ping")
			if err == nil && serial == 1002 {
				took = time.Since(rotated)
				break
			}
			if time.Since(rotated) > reload+time.Second {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if took == 0 || took > reload+time.Second {
			t.Errorf("the new certificate was not served within reload_interval + 1 s (%s)", reload+time.Second)
		} else {
			t.Logf("new certificate served %s after the rotation", took.Round(time.Millisecond))
		}
		deadline := time.Now().Add(reload + time.Second)
		for {
			if _, _, serial, err := fetch(fresh, "https://"+r.ops+"/readyz"); err == nil && serial == 1002 {
				break
			}
			if time.Now().After(deadline) {
				t.Error("the ops listener does not serve the rotated certificate")
				break
			}
			time.Sleep(20 * time.Millisecond)
		}

		time.Sleep(time.Second) // load continues after the rotation
		stop.Store(true)
		wg.Wait()
		if n := fail.Load(); n > 0 {
			t.Errorf("%d of %d requests failed during the rotation: %v", n, n+ok.Load(), failures)
		}
		if ok.Load() < 100 {
			t.Errorf("only %d requests ran: the load did not run", ok.Load())
		}
		if _, old := keptSerials.Load(int64(1001)); !old {
			t.Error("the kept-alive connection never saw the old certificate")
		}
		if _, newer := keptSerials.Load(int64(1002)); newer {
			t.Error("the connection opened before the rotation switched certificates without a new handshake")
		}
		r.running(t)
		if r.cmd.Process.Pid != pid {
			t.Fatal("the process changed")
		}
		t.Logf("%d requests, 0 failed", ok.Load())
	})

	t.Run("R-ARCH-035 the rotation shows in the reload counter and the expiry gauge", func(t *testing.T) {
		_, body, _, err := fetch(fresh, "https://"+r.ops+"/metrics")
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range []string{"client", "ops"} {
			series := fmt.Sprintf(`trove_tls_reloads_total{kind="listener",listener=%q,name=%q}`, l, certFile)
			line := metricLine(body, series)
			n, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, series)), 64)
			if n < 1 {
				t.Errorf("%s = %q, want at least 1", series, line)
			}
			if metricLine(body, fmt.Sprintf(`trove_tls_certificate_expiry_timestamp_seconds{kind="listener",listener=%q,name=%q}`, l, certFile)) == "" {
				t.Errorf("no expiry gauge for the %s listener", l)
			}
		}
		for _, kind := range []string{"database", "valkey", "objectstore"} {
			if !strings.Contains(body, `trove_tls_certificate_expiry_timestamp_seconds{kind="`+kind+`"`) {
				t.Errorf("no expiry gauge for the %s CA", kind)
			}
		}
	})
}
