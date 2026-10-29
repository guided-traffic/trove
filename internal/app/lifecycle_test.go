package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// openStream sends a request on a fresh connection and returns the reader once
// the first body line arrived, so the request is provably in flight.
func openStream(t *testing.T, addr, path string) (io.Reader, net.Conn) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: x\r\n\r\n", path)
	r := bufio.NewReader(c)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp.Body, c
}

func TestGracefulShutdown(t *testing.T) {
	t.Run("R-ARCH-024 R-OPS-002 readiness turns 503 first, ping keeps answering 200 through the drain delay, then the listener refuses, and an in-flight request finishes", func(t *testing.T) {
		h := start(t, "web", map[string]string{
			"TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "600ms", "TROVE_SERVER_SHUTDOWN_TIMEOUT": "10s",
		})
		clientAddr, opsAddr := h.app.ClientAddr().String(), h.app.OpsAddr().String()

		// An in-flight request, started before the signal: it holds for 1.5 s.
		inflight := make(chan string, 1)
		go func() {
			resp, err := http.Get("http://" + clientAddr + "/test/hold?for=1500ms")
			if err != nil {
				inflight <- "error: " + err.Error()
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			inflight <- string(b)
		}()
		time.Sleep(200 * time.Millisecond)

		if code, _, _ := get(t, h.ops+"/readyz"); code != 200 {
			t.Fatalf("setup: readyz = %d", code)
		}
		down := make(chan error, 1)
		go func() { down <- h.app.Shutdown(context.Background()) }()

		// 1. Readiness is off at once, liveness stays 200, and the ops listener still answers.
		deadline := time.Now().Add(300 * time.Millisecond)
		for {
			code, _, body := get(t, h.ops+"/readyz")
			if code == 503 && strings.Contains(body, "draining") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("readyz did not turn 503 draining within 300 ms (last %d %s)", code, body)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if code, _, _ := get(t, h.ops+"/healthz"); code != 200 {
			t.Fatalf("healthz = %d while draining", code)
		}

		// 2. During the drain delay the client listener still serves, and says Connection: close.
		resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Get(h.client + "/api/v1/system/ping")
		if err != nil {
			t.Fatalf("the drain delay must keep serving: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("ping during the drain delay = %d, want 200: the replica still serves (R-OPS-002)", resp.StatusCode)
		}
		if !resp.Close {
			t.Fatal("a response completed during the drain delay must carry Connection: close")
		}

		// 3. After the drain delay the listener refuses new connections.
		refused := false
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			c, err := net.DialTimeout("tcp", clientAddr, 200*time.Millisecond)
			if err != nil {
				refused = true
				break
			}
			c.Close()
		}
		if !refused {
			t.Fatal("the client listener still accepts connections after the drain delay")
		}

		// 4. The in-flight request completes.
		select {
		case got := <-inflight:
			if !strings.Contains(got, "finished") {
				t.Fatalf("in-flight request = %q", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("in-flight request did not finish")
		}
		if err := <-down; err != nil {
			t.Fatalf("Shutdown = %v", err)
		}
		// The ops listener went last.
		if c, err := net.DialTimeout("tcp", opsAddr, 200*time.Millisecond); err == nil {
			c.Close()
			t.Fatal("ops listener still open after Shutdown")
		}
	})

	t.Run("Q-ARCH-18 a transfer that outlasts shutdown_timeout is cut and counted", func(t *testing.T) {
		h := start(t, "web", map[string]string{
			"TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "0s", "TROVE_SERVER_SHUTDOWN_TIMEOUT": "1s",
		})
		r, c := openStream(t, h.app.ClientAddr().String(), "/test/stream")
		defer c.Close()
		_, _ = io.ReadFull(r, make([]byte, 1))
		start := time.Now()
		if err := h.app.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown = %v", err)
		}
		if took := time.Since(start); took > 8*time.Second {
			t.Fatalf("Shutdown took %v", took)
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.Copy(io.Discard, r); err == nil || !strings.Contains(err.Error(), "unexpected EOF") && !strings.Contains(err.Error(), "reset") {
			t.Fatalf("the cut transfer ended cleanly: %v", err)
		}
		fams, _ := h.app.tele.metrics.Registry().Gather()
		var cut float64
		for _, f := range fams {
			if f.GetName() == "trove_http_transfers_cut_total" {
				for _, m := range f.GetMetric() {
					cut += m.GetCounter().GetValue()
				}
			}
		}
		if cut != 1 {
			t.Fatalf("trove_http_transfers_cut_total = %v, want 1", cut)
		}
	})

	t.Run("R-ARCH-024 a worker shuts down without a drain delay", func(t *testing.T) {
		h := start(t, "worker", map[string]string{"TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "10s"})
		begin := time.Now()
		if err := h.app.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if time.Since(begin) > 3*time.Second {
			t.Fatalf("worker waited %v", time.Since(begin))
		}
	})

	t.Run("R-ARCH-024 components stop in reverse start order after flush, and the shutdown phases run in order", func(t *testing.T) {
		h := start(t, "web", map[string]string{"TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "0s"})
		var order []string
		a, b := &recComp{name: "a", log: &order}, &recComp{name: "b", log: &order}
		h.app.comps = append(h.app.comps, a, b)
		if err := h.app.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(order, ",")
		if got != "drain a,drain b,settle a,settle b,flush b,flush a,stop b,stop a" && got != "drain a,drain b,settle b,settle a,flush b,flush a,stop b,stop a" {
			t.Fatalf("order = %s", got)
		}
	})
}

type recComp struct {
	name string
	log  *[]string
}

var recMu sync.Mutex

func (c *recComp) add(s string) { recMu.Lock(); *c.log = append(*c.log, s+" "+c.name); recMu.Unlock() }

func (c *recComp) Name() string                { return c.name }
func (c *recComp) Start(context.Context) error { return nil }
func (c *recComp) Stop(context.Context) error  { c.add("stop"); return nil }
func (c *recComp) Drain(context.Context)       { c.add("drain") }
func (c *recComp) Settle(context.Context) error {
	c.add("settle")
	return nil
}
func (c *recComp) Flush(context.Context) error { c.add("flush"); return nil }
func (c *recComp) Quiesce(context.Context)     {}
