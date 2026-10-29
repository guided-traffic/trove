package cluster

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
)

// fakeServer is a minimal RESP server for tests that need no real Valkey. The
// handler gets the command words and returns the raw RESP reply.
type fakeServer struct {
	ln   net.Listener
	mu   sync.Mutex
	cmds [][]string
}

func newFakeServer(t *testing.T, handler func(args []string) string) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, handler)
		}
	}()
	return s
}

func (s *fakeServer) addr() string { return s.ln.Addr().String() }

func (s *fakeServer) seen() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.cmds...)
}

func (s *fakeServer) serve(conn net.Conn, handler func([]string) string) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.cmds = append(s.cmds, args)
		s.mu.Unlock()
		if _, err := io.WriteString(conn, handler(args)); err != nil {
			return
		}
	}
}

func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("unexpected line %q", line)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
	args := make([]string, 0, n)
	for range n {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, _ := strconv.Atoi(strings.TrimSpace(hdr[1:]))
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

// helloReply is a HELLO 3 reply of a server of the given name and version.
func helloReplyFor(server, version string) string {
	return "%4\r\n" +
		bulk("server") + bulk(server) +
		bulk("version") + bulk(version) +
		bulk("proto") + ":3\r\n" +
		bulk("role") + bulk("master")
}

func bulk(s string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }

// standaloneServer answers like a Valkey of the given version.
func standaloneServer(t *testing.T, server, version string) *fakeServer {
	return newFakeServer(t, func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "HELLO":
			return helloReplyFor(server, version)
		case "PING":
			return "+PONG\r\n"
		case "SET":
			return "+OK\r\n"
		case "CLIENT":
			return "+OK\r\n"
		}
		return "-ERR unknown command\r\n"
	})
}

// errorServer answers every command with the error reply msg.
func errorServer(t *testing.T, msg string) *fakeServer {
	return newFakeServer(t, func([]string) string { return "-" + msg + "\r\n" })
}

// realReplyError returns the error valkey-go produces for the server reply msg.
func realReplyError(t *testing.T, msg string) error {
	t.Helper()
	srv := errorServer(t, msg)
	c, err := valkey.NewClient(valkey.ClientOption{
		InitAddress: []string{srv.addr()}, ForceSingleClient: true, DisableCache: true, DisableRetry: true,
		Dialer: net.Dialer{Timeout: time.Second},
	})
	if err == nil {
		c.Close()
		t.Fatal("expected a connection error")
	}
	return err
}
