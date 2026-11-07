package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The target file replaces the default while it holds an address; the default returns when the file
// is missing or empty.
func TestFileTarget(t *testing.T) {
	file := filepath.Join(t.TempDir(), "target")
	target := fileTarget("edge:443", file)
	if got := target(); got != "edge:443" {
		t.Fatalf("no file: %q, want the default", got)
	}
	if err := os.WriteFile(file, []byte("10.0.0.7:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := target(); got != "10.0.0.7:8080" {
		t.Fatalf("file: %q, want the pinned address", got)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := target(); got != "edge:443" {
		t.Fatalf("empty file: %q, want the default", got)
	}
	if got := fileTarget("edge:443", "")(); got != "edge:443" {
		t.Fatalf("no file configured: %q, want the default", got)
	}
}

// The forwarder relays bytes in both directions and passes a half-close on: the echo server answers
// only after the client has finished writing.
func TestServeRelaysBothDirections(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b, _ := io.ReadAll(c)
				_, _ = c.Write(append([]byte("echo:"), b...))
			}()
		}
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, ln, echo.Addr().String()) }()

	c, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "echo:ping" {
		t.Fatalf("relayed %q, want %q", got, "echo:ping")
	}
	_ = c.Close()

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve returned %v after cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
}

// A target that refuses the connection closes the client's connection instead of hanging it.
func TestServeClosesClientWhenTargetIsDown(t *testing.T) {
	// Port 1 is privileged, so no test can listen on it, and nothing does: the dial is refused at once. A port taken
	// with :0 and closed again was handed to a parallel test on a busy runner once, and the dial then reached a server
	// that never answered.
	const addr = "127.0.0.1:1"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = serve(ctx, ln, addr) }()

	c, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(c); err != nil {
		t.Fatalf("read after refused target: %v, want a clean close", err)
	}
}

// A keep-alive connection that the target closes while it is idle, and that the client then writes to, is
// the case behind F-70 (the edge's idle timeout against npm's retry delay): the forwarder says so in
// its log, with the idle time and whether the client's bytes came after the close.
func TestRelayLogsTargetCloseBeforeClientBytes(t *testing.T) {
	var buf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		c, err := target.Accept()
		if err != nil {
			return
		}
		b := make([]byte, 4)
		_, _ = io.ReadFull(c, b)
		_, _ = c.Write([]byte("pong"))
		time.Sleep(200 * time.Millisecond) // the idle timeout of the target
		_ = c.Close()
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = serve(ctx, ln, target.Addr().String()) }()

	c, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // the target has closed by now
	_, _ = c.Write([]byte("late"))
	_, _ = io.ReadAll(c) // the forwarder ends its side towards the client when the target has closed
	_ = c.Close()        // a client that sees the close ends its side too; the closing line follows

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "target closed the connection first") {
		if time.Now().After(deadline) {
			t.Fatalf("no closing line in the log:\n%s", buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := buf.String()
	if !strings.Contains(got, "client_bytes_met_closed_connection=true") {
		t.Errorf("the closing line does not say that the client wrote after the close:\n%s", got)
	}
	if !strings.Contains(got, "idle_at_close=") {
		t.Errorf("the closing line has no idle time:\n%s", got)
	}
}

// A connection that the client closes first leaves no closing line.
func TestRelayLogsNothingWhenClientClosesFirst(t *testing.T) {
	var buf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	closed := make(chan struct{})
	go func() {
		c, err := target.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(io.Discard, c)
		close(closed)
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = serve(ctx, ln, target.Addr().String()) }()

	c, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("ping"))
	_ = c.Close()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the target did not see the client's close")
	}
	time.Sleep(100 * time.Millisecond)
	if got := buf.String(); strings.Contains(got, "target closed") {
		t.Errorf("a client-first close was logged as a target close:\n%s", got)
	}
}

// syncBuffer is a log sink that the test reads while the forwarder writes.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
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
