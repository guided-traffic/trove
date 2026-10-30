package cluster

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memServer is a small in-memory Valkey for the tests of the mechanisms built
// on keys and channels: GET, SET with NX, PX and IFEQ, DELIFEQ, PUBLISH and
// SUBSCRIBE with RESP3 pushes. Expiry uses the wall clock. The tests reach into
// it to lose a key, drop messages or cut the Pub/Sub connection.
type memServer struct {
	ln net.Listener

	mu    sync.Mutex
	data  map[string]memValue
	subs  map[string]map[*memConn]bool
	conns map[*memConn]bool
	cmds  [][]string

	dropPublish atomic.Bool
}

type memValue struct {
	val string
	exp time.Time
}

type memConn struct {
	c  net.Conn
	mu sync.Mutex
	n  int // channels subscribed
}

func (c *memConn) write(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = io.WriteString(c.c, s)
}

func newMemServer(t *testing.T) *memServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &memServer{ln: ln, data: map[string]memValue{}, subs: map[string]map[*memConn]bool{}, conns: map[*memConn]bool{}}
	t.Cleanup(func() {
		_ = ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			_ = c.c.Close()
		}
		s.mu.Unlock()
	})
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			c := &memConn{c: nc}
			s.mu.Lock()
			s.conns[c] = true
			s.mu.Unlock()
			go s.serve(c)
		}
	}()
	return s
}

func (s *memServer) addr() string { return s.ln.Addr().String() }

func (s *memServer) serve(c *memConn) {
	defer func() {
		_ = c.c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		for _, m := range s.subs {
			delete(m, c)
		}
		s.mu.Unlock()
	}()
	r := bufio.NewReader(c.c)
	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.cmds = append(s.cmds, args)
		s.mu.Unlock()
		if reply := s.handle(c, args); reply != "" {
			c.write(reply)
		}
	}
}

const respNull = "_\r\n"

func (s *memServer) live(key string) (memValue, bool) {
	v, ok := s.data[key]
	if ok && !v.exp.IsZero() && time.Now().After(v.exp) {
		delete(s.data, key)
		return memValue{}, false
	}
	return v, ok
}

func (s *memServer) handle(c *memConn, args []string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch strings.ToUpper(args[0]) {
	case "HELLO":
		return helloReplyFor("valkey", "9.1.3")
	case "PING":
		return "+PONG\r\n"
	case "CLIENT":
		return "+OK\r\n"
	case "GET":
		if v, ok := s.live(args[1]); ok {
			return bulk(v.val)
		}
		return respNull
	case "SET":
		return s.set(args)
	case "DELIFEQ":
		if v, ok := s.live(args[1]); ok && v.val == args[2] {
			delete(s.data, args[1])
			return ":1\r\n"
		}
		return ":0\r\n"
	case "PUBLISH":
		if s.dropPublish.Load() {
			return ":0\r\n"
		}
		n := 0
		for sub := range s.subs[args[1]] {
			sub.write(fmt.Sprintf(">3\r\n%s%s%s", bulk("message"), bulk(args[1]), bulk(args[2])))
			n++
		}
		return ":" + strconv.Itoa(n) + "\r\n"
	case "SUBSCRIBE":
		for _, ch := range args[1:] {
			if s.subs[ch] == nil {
				s.subs[ch] = map[*memConn]bool{}
			}
			if !s.subs[ch][c] {
				s.subs[ch][c] = true
				c.n++
			}
			c.write(fmt.Sprintf(">3\r\n%s%s:%d\r\n", bulk("subscribe"), bulk(ch), c.n))
		}
		return ""
	case "UNSUBSCRIBE":
		for _, ch := range args[1:] {
			if s.subs[ch][c] {
				delete(s.subs[ch], c)
				c.n--
			}
			c.write(fmt.Sprintf(">3\r\n%s%s:%d\r\n", bulk("unsubscribe"), bulk(ch), c.n))
		}
		return ""
	}
	return "-ERR unknown command\r\n"
}

// set implements SET key value [NX] [PX ms] [IFEQ old].
func (s *memServer) set(args []string) string {
	key, val := args[1], args[2]
	var nx bool
	var px time.Duration
	var ifeq *string
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "NX":
			nx = true
		case "PX":
			i++
			ms, _ := strconv.Atoi(args[i])
			px = time.Duration(ms) * time.Millisecond
		case "IFEQ":
			i++
			ifeq = &args[i]
		}
	}
	cur, exists := s.live(key)
	if (nx && exists) || (ifeq != nil && (!exists || cur.val != *ifeq)) {
		return respNull
	}
	nv := memValue{val: val}
	if px > 0 {
		nv.exp = time.Now().Add(px)
	}
	s.data[key] = nv
	return "+OK\r\n"
}

// count is the number of commands seen whose name is name.
func (s *memServer) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	for _, c := range s.cmds {
		if strings.EqualFold(c[0], name) {
			n++
		}
	}
	return n
}

// commands returns the commands seen whose name is name.
func (s *memServer) commands(name string) [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]string
	for _, c := range s.cmds {
		if strings.EqualFold(c[0], name) {
			out = append(out, c)
		}
	}
	return out
}

// value returns the live value of key.
func (s *memServer) value(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.live(key)
	return v.val, ok
}

// lose deletes key without telling anyone, as a failover that lost a write.
func (s *memServer) lose(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}

// plant stores raw under key with an expiry, as another writer would.
func (s *memServer) plant(key, raw string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = memValue{val: raw, exp: time.Now().Add(ttl)}
}

// subscribers is the number of connections subscribed to ch.
func (s *memServer) subscribers(ch string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs[ch])
}

// cutSubscribers closes every connection that is subscribed to something.
func (s *memServer) cutSubscribers() {
	s.mu.Lock()
	var cut []*memConn
	for c := range s.conns {
		if c.n > 0 {
			cut = append(cut, c)
		}
	}
	s.mu.Unlock()
	for _, c := range cut {
		_ = c.c.Close()
	}
}
