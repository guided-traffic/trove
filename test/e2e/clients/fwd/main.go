// Command fwd is the TCP forwarder of the clients pod (test/e2e/clients, R-NPM-045). It listens on
// the loopback address of the registry's base URL (127.0.0.1:8080) and relays every connection,
// byte for byte, to the target: the edge or one web pod. The clients therefore use the base URL of
// the setup snippets (R-NPM-041) unchanged and verify the server certificate against `localhost`,
// while the bytes reach the cluster. The forwarder does not terminate TLS and reads nothing of the
// stream.
//
//	fwd -listen 127.0.0.1:8080 -to trove-edge.trove.svc:443 -target-file /tmp/target
//
// The target is resolved for every connection, so a Service or pod that is replaced during a run is
// followed. With -target-file, a file that holds a host:port overrides -to for every connection made
// while it holds one: a test pins the client to one web pod by writing the pod's address into it and
// returns to the edge by emptying it (docs/developer/e2e.md, "Pinning a client to a replica"). It is
// the process the pod runs as PID 1 (the image's entrypoint).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const dialTimeout = 10 * time.Second

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "address the clients connect to")
	to := flag.String("to", "", "host:port every connection is relayed to")
	targetFile := flag.String("target-file", "", "file whose host:port, while it holds one, replaces -to")
	flag.Parse()
	if *to == "" {
		fmt.Fprintln(os.Stderr, "fwd: -to is required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fwd:", err)
		os.Exit(1)
	}
	slog.Info("forwarding", "listen", ln.Addr().String(), "to", *to, "target-file", *targetFile)
	if err := serveWith(ctx, ln, fileTarget(*to, *targetFile)); err != nil {
		fmt.Fprintln(os.Stderr, "fwd:", err)
		os.Exit(1)
	}
}

// fileTarget is the target of one connection: the content of the file when it exists and is not
// empty, else the default.
func fileTarget(def, file string) func() string {
	if file == "" {
		return func() string { return def }
	}
	return func() string {
		raw, err := os.ReadFile(file) //nolint:gosec // G304: the path is the -target-file flag of the test pod, set by deploy/dev/e2e.sh
		if t := strings.TrimSpace(string(raw)); err == nil && t != "" {
			return t
		}
		return def
	}
}

// serve relays every accepted connection to target until ctx ends or the listener fails.
func serve(ctx context.Context, ln net.Listener, target string) error {
	return serveWith(ctx, ln, func() string { return target })
}

// serveWith is serve with a target that is asked for at every connection.
func serveWith(ctx context.Context, ln net.Listener, target func() string) error {
	go func() { <-ctx.Done(); _ = ln.Close() }()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			relay(ctx, c, target())
		}()
	}
}

// relay copies both directions until each side has finished. A half-close is passed on, so a client
// that finishes its request and waits for the answer is not cut off.
//
// A connection that the target closed before the client did is logged with how long it had been idle
// and whether the client's next bytes met the closed connection. That is the witness for a request
// that a keep-alive connection lost to the target's idle timeout: the client sees a reset, and the
// target's access log has no line for the request (F-70).
func relay(ctx context.Context, client net.Conn, target string) {
	defer client.Close()
	d := net.Dialer{Timeout: dialTimeout}
	up, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		slog.Warn("dial failed", "to", target, "error", err)
		return
	}
	defer up.Close()
	st := &connState{opened: time.Now()}
	st.last.Store(st.opened.UnixNano())
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn, fromUpstream bool) {
		_ = st.copy(dst, src, fromUpstream)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(up, client, false)
	go pipe(client, up, true)
	<-done
	<-done
	st.log(target)
}

// connState is what one relayed connection remembers for its closing line.
type connState struct {
	opened               time.Time
	last                 atomic.Int64 // unix nanoseconds of the last byte relayed in either direction
	idleAtUpstreamClose  atomic.Int64 // nanoseconds the connection had been idle when the target ended its side
	upstreamClosedFirst  atomic.Bool
	clientClosed         atomic.Bool
	clientBytesMetClosed atomic.Bool // the client sent bytes after the target had closed its side
}

// copy relays src to dst and notes which side ended first. It returns the write error, if the copy
// ended because dst could not take the bytes.
func (s *connState) copy(dst, src net.Conn, fromUpstream bool) error {
	buf := make([]byte, 32*1024)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			s.last.Store(time.Now().UnixNano())
			if !fromUpstream && s.upstreamClosedFirst.Load() {
				s.clientBytesMetClosed.Store(true)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if rerr != nil {
			if fromUpstream {
				if !s.clientClosed.Load() && s.upstreamClosedFirst.CompareAndSwap(false, true) {
					s.idleAtUpstreamClose.Store(time.Now().UnixNano() - s.last.Load())
				}
			} else {
				s.clientClosed.Store(true)
			}
			return nil
		}
	}
}

// log writes the closing line of a connection that the target closed before the client did.
func (s *connState) log(target string) {
	if !s.upstreamClosedFirst.Load() {
		return
	}
	slog.Info("target closed the connection first",
		"to", target,
		"age", time.Since(s.opened).Round(time.Millisecond).String(),
		"idle_at_close", time.Duration(s.idleAtUpstreamClose.Load()).Round(time.Millisecond).String(),
		"client_bytes_met_closed_connection", s.clientBytesMetClosed.Load())
}
