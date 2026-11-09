package app

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// stalledOpsPeer opens a connection to the ops listener, pipelines many requests for the metrics and never reads a byte, with a
// receive buffer of a few KiB: the responses fill the server's send queue and the connection stays busy or, once the handler is
// through, holds unacknowledged bytes.
func stalledOpsPeer(t *testing.T, addr string) net.Conn {
	t.Helper()
	d := net.Dialer{Control: nil}
	raw, err := d.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	tc := raw.(*net.TCPConn)
	if err := tc.SetReadBuffer(2048); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tc.Close() })
	go func() {
		_ = tc.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, _ = tc.Write([]byte(strings.Repeat("GET /metrics HTTP/1.1\r\nHost: x\r\n\r\n", 2000)))
	}()
	// One byte of the first response proves the connection is active and the server is writing, on a slow runner too; nothing is read
	// after it, so the responses fill the socket buffers and the server's write stalls.
	_ = tc.SetReadDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.ReadFull(tc, make([]byte, 1)); err != nil {
		t.Fatalf("the ops listener did not start to answer: %v", err)
	}
	return tc
}

func TestShutdownOpsListener(t *testing.T) {
	t.Run("R-ARCH-024 R-HA-011 a peer on the ops listener that never reads its responses is reset at the ops bound and does not hold the exit to the end of the grace period", func(t *testing.T) {
		// drain 0, timeout 1 s: the plan's exit deadline is 1 s + 0.8 s - 0.1 s = 1.7 s after the signal.
		h := start(t, "web", map[string]string{"TROVE_HEALTH_SHUTDOWN_DRAIN_DELAY": "0s", "TROVE_SERVER_SHUTDOWN_TIMEOUT": "1s"})
		h.app.budget = scaledBudget()
		stalledOpsPeer(t, h.app.OpsAddr().String())
		time.Sleep(300 * time.Millisecond) // the server keeps writing until the socket buffers are full

		begin := time.Now()
		_ = h.app.Shutdown(context.Background())
		took := time.Since(begin)
		// The server phase ends at once (no client peer) and the close phase has nothing to wait for: what is left is the ops
		// listener's own bound, OpsReserve (0.1 s here). Before the fix it waited for the stalled peer until the exit
		// deadline, 1.7 s here and 119.5 s of a grace period of 120 s in a pod.
		if took > 800*time.Millisecond {
			t.Errorf("Shutdown took %s with a stalled peer on the ops listener, want about the ops bound of 0.1 s (the exit deadline is 1.7 s)", took)
		}
		// Which of the two ends the connection takes depends on where its writes stand at the deadline: inside a response (cut and reset) or
		// between two pipelined requests (closed as idle with a send queue, held until the bound). Both are bounded and both are logged.
		logs := h.logs.String()
		if !strings.Contains(logs, "ops listener: connections still open at the ops deadline were reset") &&
			!strings.Contains(logs, "connections held open until the peer acknowledged their last bytes") {
			t.Errorf("the end of the stalled ops connection is not logged:\n%s", logs)
		}
	})
}
