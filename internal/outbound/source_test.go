package outbound

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func addrs(ss ...string) func() ([]netip.Addr, error) {
	return func() ([]netip.Addr, error) {
		out := make([]netip.Addr, 0, len(ss))
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

func TestLocalPrefix(t *testing.T) {
	t.Run("R-REPO-024 the first interface address inside the prefix is taken", func(t *testing.T) {
		b := newLocalBinder(netip.MustParsePrefix("10.2.0.0/16"), addrs("127.0.0.1", "10.3.0.4", "10.2.0.9", "10.2.0.7"), time.Now)
		la, err := b.address()
		if err != nil || la.IP.String() != "10.2.0.9" {
			t.Errorf("address = %v, %v", la, err)
		}
	})

	t.Run("R-REPO-024 an IPv6 prefix that is not masked", func(t *testing.T) {
		b := newLocalBinder(netip.MustParsePrefix("fd00:1:2::5/64"), addrs("10.0.0.1", "fd00:1:2::77"), time.Now)
		la, err := b.address()
		if err != nil || la.IP.String() != "fd00:1:2::77" {
			t.Errorf("address = %v, %v", la, err)
		}
	})

	t.Run("R-REPO-024 an IPv4-mapped interface address is matched as IPv4", func(t *testing.T) {
		b := newLocalBinder(netip.MustParsePrefix("10.2.0.0/16"), addrs("::ffff:10.2.0.9"), time.Now)
		la, err := b.address()
		if err != nil || la.IP.String() != "10.2.0.9" {
			t.Errorf("address = %v, %v", la, err)
		}
	})

	t.Run("R-REPO-024 no address in the prefix is source_network_unavailable", func(t *testing.T) {
		b := newLocalBinder(netip.MustParsePrefix("10.2.0.0/16"), addrs("127.0.0.1", "192.168.1.5"), time.Now)
		if _, err := b.address(); !errors.Is(err, ErrSourceNetworkUnavailable) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-REPO-024 the addresses are enumerated again after 60 seconds, not before", func(t *testing.T) {
		now := time.Unix(1_000_000, 0)
		calls := 0
		cur := []string{"192.168.1.5"}
		list := func() ([]netip.Addr, error) { calls++; return addrs(cur...)() }
		b := newLocalBinder(netip.MustParsePrefix("10.2.0.0/16"), list, func() time.Time { return now })
		cur = []string{"10.2.0.9"}
		now = now.Add(LocalRefresh - time.Second)
		if _, err := b.address(); !errors.Is(err, ErrSourceNetworkUnavailable) || calls != 1 {
			t.Fatalf("before the interval: err = %v, calls = %d", err, calls)
		}
		now = now.Add(time.Second)
		la, err := b.address()
		if err != nil || la.IP.String() != "10.2.0.9" || calls != 2 {
			t.Errorf("after the interval: %v, %v, calls = %d", la, err, calls)
		}
		cur = nil
		now = now.Add(LocalRefresh)
		if _, err := b.address(); !errors.Is(err, ErrSourceNetworkUnavailable) {
			t.Errorf("an address that vanished is still used: %v", err)
		}
	})

	t.Run("R-REPO-024 a failing lister keeps the last good address and never invents one", func(t *testing.T) {
		now := time.Unix(1_000_000, 0)
		fail := false
		list := func() ([]netip.Addr, error) {
			if fail {
				return nil, errors.New("netlink busy")
			}
			return addrs("10.2.0.9")()
		}
		b := newLocalBinder(netip.MustParsePrefix("10.2.0.0/16"), list, func() time.Time { return now })
		fail = true
		now = now.Add(LocalRefresh)
		if la, err := b.address(); err != nil || la.IP.String() != "10.2.0.9" {
			t.Errorf("address = %v, %v", la, err)
		}
		first := newLocalBinder(netip.MustParsePrefix("10.2.0.0/16"), list, func() time.Time { return now })
		if _, err := first.address(); !errors.Is(err, ErrSourceNetworkUnavailable) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("R-REPO-024 a request without an address in the prefix fails and no connection is made", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
		defer srv.Close()
		c := New(allowLoopback()).Client(PurposeRemote, WithLocalPrefix(netip.MustParsePrefix("10.2.0.0/16"), addrs("127.0.0.1")))
		if err := getErr(t, c, srv.URL); !errors.Is(err, ErrSourceNetworkUnavailable) || hits.Load() != 0 {
			t.Errorf("err = %v, hits = %d", err, hits.Load())
		}
	})

	t.Run("R-REPO-024 connections leave from the bound address", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			_, _ = io.WriteString(w, host)
		}))
		defer srv.Close()
		c := New(allowLoopback()).Client(PurposeRemote, WithLocalPrefix(netip.MustParsePrefix("127.0.0.0/8"), addrs("10.9.9.9", "127.0.0.1")))
		resp, err := get(t, c, srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if b, _ := io.ReadAll(resp.Body); string(b) != "127.0.0.1" {
			t.Errorf("remote address = %q", b)
		}
	})

	t.Run("R-REPO-024 the kernel is not asked to choose: an address of no interface fails the dial", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer srv.Close()
		// 192.0.2.0/24 is TEST-NET-1: the lister claims an address this host does not have.
		c := New(allowLoopback()).Client(PurposeRemote, WithLocalPrefix(netip.MustParsePrefix("192.0.2.0/24"), addrs("192.0.2.1")))
		err := getErr(t, c, srv.URL)
		if err == nil || errors.Is(err, ErrSourceNetworkUnavailable) {
			t.Errorf("err = %v, want the dial to fail on the bind", err)
		}
	})

	t.Run("R-REPO-038 the egress policy still applies to a bound client", func(t *testing.T) {
		c := New(defaultCfg()).Client(PurposeRemote, WithLocalPrefix(netip.MustParsePrefix("127.0.0.0/8"), addrs("127.0.0.1")))
		if err := getErr(t, c, "http://127.0.0.1:9/"); !errors.Is(err, ErrEgressDenied) {
			t.Errorf("err = %v", err)
		}
	})
}
