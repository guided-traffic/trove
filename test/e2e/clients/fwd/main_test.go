package main

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

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
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := dead.Addr().String()
	_ = dead.Close()

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
