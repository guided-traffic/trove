// Command fwd is the TCP forwarder of the clients pod (test/e2e/clients, R-NPM-045). It listens on
// the loopback address of the registry's base URL (127.0.0.1:8080) and relays every connection,
// byte for byte, to the target: the edge or one web pod. The clients therefore use the base URL of
// the setup snippets (R-NPM-041) unchanged and verify the server certificate against `localhost`,
// while the bytes reach the cluster. The forwarder does not terminate TLS and reads nothing of the
// stream.
//
//	fwd -listen 127.0.0.1:8080 -to trove-edge.trove.svc:443
//
// The target is resolved for every connection, so a Service or pod that is replaced during a run is
// followed. It is the process the pod runs as PID 1 (the image's entrypoint).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const dialTimeout = 10 * time.Second

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "address the clients connect to")
	to := flag.String("to", "", "host:port every connection is relayed to")
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
	slog.Info("forwarding", "listen", ln.Addr().String(), "to", *to)
	if err := serve(ctx, ln, *to); err != nil {
		fmt.Fprintln(os.Stderr, "fwd:", err)
		os.Exit(1)
	}
}

// serve relays every accepted connection to target until ctx ends or the listener fails.
func serve(ctx context.Context, ln net.Listener, target string) error {
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
			relay(ctx, c, target)
		}()
	}
}

// relay copies both directions until each side has finished. A half-close is passed on, so a client
// that finishes its request and waits for the answer is not cut off.
func relay(ctx context.Context, client net.Conn, target string) {
	defer client.Close()
	d := net.Dialer{Timeout: dialTimeout}
	up, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		slog.Warn("dial failed", "to", target, "error", err)
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(up, client)
	go pipe(client, up)
	<-done
	<-done
}
