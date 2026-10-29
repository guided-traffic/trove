package httpserver

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func req(remote string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest("GET", "http://internal.invalid/x", nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Add(k, v)
	}
	return r
}

func TestClientAddress(t *testing.T) {
	trusted := []string{"10.42.0.0/16"}
	cases := []struct {
		name    string
		trusted []string
		remote  string
		hdr     map[string]string
		want    string
	}{
		{
			"R-ARCH-006 a trusted peer: right-most untrusted entry of X-Forwarded-For", trusted, "10.42.1.7:5555",
			map[string]string{"X-Forwarded-For": "198.51.100.4, 10.42.0.9"},
			"198.51.100.4",
		},
		{
			"R-ARCH-006 an untrusted peer: the header is ignored", trusted, "203.0.113.5:4444",
			map[string]string{"X-Forwarded-For": "198.51.100.4, 10.42.0.9"},
			"203.0.113.5",
		},
		{
			"R-ARCH-006 without trusted proxies the header is never read", nil, "10.42.1.7:5555",
			map[string]string{"X-Forwarded-For": "198.51.100.4"},
			"10.42.1.7",
		},
		{
			"R-ARCH-006 a forged left-most entry does not win over the right-most untrusted one", trusted, "10.42.1.7:5555",
			map[string]string{"X-Forwarded-For": "1.1.1.1, 198.51.100.4, 10.42.0.9"},
			"198.51.100.4",
		},
		{
			"R-ARCH-006 several X-Forwarded-For lines count as one list", trusted, "10.42.1.7:5555",
			map[string]string{"X-Forwarded-For": "198.51.100.4,10.42.0.8"},
			"198.51.100.4",
		},
		{
			"R-ARCH-006 a chain of only trusted proxies resolves to its left-most address", trusted, "10.42.1.7:5555",
			map[string]string{"X-Forwarded-For": "10.42.3.3, 10.42.0.9"},
			"10.42.3.3",
		},
		{
			"R-ARCH-006 an entry that is not an address leaves the proxy as the client", trusted, "10.42.1.7:5555",
			map[string]string{"X-Forwarded-For": "198.51.100.4, unknown"},
			"10.42.1.7",
		},
		{
			"R-ARCH-006 a bare trusted address (not a CIDR) is trusted",
			[]string{"192.0.2.10"},
			"192.0.2.10:1",
			map[string]string{"X-Forwarded-For": "198.51.100.4"},
			"198.51.100.4",
		},
		{
			"R-ARCH-006 an IPv4-mapped IPv6 peer matches an IPv4 CIDR", trusted, "[::ffff:10.42.1.7]:5555",
			map[string]string{"X-Forwarded-For": "198.51.100.4"},
			"198.51.100.4",
		},
		{"R-ARCH-006 a trusted peer without forwarding headers is the client", trusted, "10.42.1.7:5555", nil, "10.42.1.7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := NewResolver(c.trusted, "https://trove.example.com", nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.Resolve(req(c.remote, c.hdr)).Client.String(); got != c.want {
				t.Fatalf("client = %s, want %s", got, c.want)
			}
		})
	}

	fwd := []struct {
		name   string
		header ForwardedHeader
		hdr    map[string]string
		want   string
	}{
		{
			"R-ARCH-006 forwarded mode: Forwarded for= is read, with port and IPv6 brackets", HeaderForwarded,
			map[string]string{"Forwarded": `for="[2001:db8::7]:4711";proto=https, for=10.42.0.9`},
			"2001:db8::7",
		},
		{
			"R-ARCH-006 x-forwarded-for mode: a client-written Forwarded header is ignored", HeaderXForwardedFor,
			map[string]string{"X-Forwarded-For": "198.51.100.4", "Forwarded": "for=203.0.113.99"},
			"198.51.100.4",
		},
		{
			"R-ARCH-006 forwarded mode: a client-written X-Forwarded-For is ignored", HeaderForwarded,
			map[string]string{"Forwarded": "for=198.51.100.4", "X-Forwarded-For": "203.0.113.99"},
			"198.51.100.4",
		},
		{
			"R-ARCH-006 x-forwarded-for mode never falls back to Forwarded", HeaderXForwardedFor,
			map[string]string{"Forwarded": "for=203.0.113.99"},
			"10.42.1.7",
		},
		{
			"R-ARCH-006 forwarded mode never falls back to X-Forwarded-For", HeaderForwarded,
			map[string]string{"X-Forwarded-For": "203.0.113.99"},
			"10.42.1.7",
		},
	}
	for _, c := range fwd {
		t.Run(c.name, func(t *testing.T) {
			r, err := NewResolver(trusted, "https://trove.example.com", nil, WithForwardedHeader(c.header))
			if err != nil {
				t.Fatal(err)
			}
			if got := r.Resolve(req("10.42.1.7:5555", c.hdr)).Client.String(); got != c.want {
				t.Fatalf("client = %s, want %s", got, c.want)
			}
		})
	}

	t.Run("R-ARCH-006 spoof: a pass-through Forwarded header does not override the edge's X-Forwarded-For", func(t *testing.T) {
		// The edge appended the real client to X-Forwarded-For; the client sent a
		// Forwarded header of its own that the edge passed through.
		r, err := NewResolver([]string{"10.42.0.0/16"}, "https://trove.example.com", nil)
		if err != nil {
			t.Fatal(err)
		}
		q := req("10.42.1.7:5555", map[string]string{"X-Forwarded-For": "198.51.100.4", "Forwarded": "for=203.0.113.99"})
		if got := r.Resolve(q).Client.String(); got != "198.51.100.4" {
			t.Fatalf("client = %s, want 198.51.100.4", got)
		}
	})

	t.Run("R-ARCH-006 a prefix that covers every address is rejected", func(t *testing.T) {
		for _, c := range []string{"0.0.0.0/0", "::/0"} {
			if _, err := NewResolver([]string{c}, "", nil); err == nil {
				t.Fatalf("%s accepted", c)
			}
		}
	})
	t.Run("R-ARCH-006 an unknown forwarded header is rejected", func(t *testing.T) {
		if _, err := NewResolver(nil, "", nil, WithForwardedHeader("x-real-ip")); err == nil {
			t.Fatal("accepted")
		}
	})

	t.Run("R-ARCH-006 a malformed trusted proxy is rejected", func(t *testing.T) {
		if _, err := NewResolver([]string{"10.0.0.0/33"}, "", nil); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestBaseURL(t *testing.T) {
	trusted := []string{"10.42.0.0/16"}
	extra := []string{"https://npm.example.com"}

	t.Run("R-OPS-005 forwarded host from an untrusted peer is ignored", func(t *testing.T) {
		r, _ := NewResolver(nil, "https://trove.example.com", nil)
		got := r.Resolve(req("203.0.113.5:1", map[string]string{"X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "https"}))
		if got.BaseURL != "https://trove.example.com" || got.BaseURLSource != "config" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("R-OPS-005 a trusted peer's forwarded origin counts only when listed", func(t *testing.T) {
		r, _ := NewResolver(trusted, "https://trove.example.com", extra)
		listed := r.Resolve(req("10.42.1.7:1", map[string]string{"X-Forwarded-Host": "npm.example.com", "X-Forwarded-Proto": "https"}))
		if listed.BaseURL != "https://npm.example.com" {
			t.Fatalf("listed: %+v", listed)
		}
		unlisted := r.Resolve(req("10.42.1.7:1", map[string]string{"X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "https"}))
		if unlisted.BaseURL != "https://trove.example.com" {
			t.Fatalf("unlisted: %+v", unlisted)
		}
	})
	t.Run("R-OPS-005 Forwarded host and proto are read in forwarded mode only", func(t *testing.T) {
		r, _ := NewResolver(trusted, "https://trove.example.com", extra, WithForwardedHeader(HeaderForwarded))
		hdr := map[string]string{"Forwarded": "for=198.51.100.4;host=npm.example.com;proto=https"}
		got := r.Resolve(req("10.42.1.7:1", hdr))
		if got.BaseURL != "https://npm.example.com" {
			t.Fatalf("%+v", got)
		}
		x, _ := NewResolver(trusted, "https://trove.example.com", extra)
		if got := x.Resolve(req("10.42.1.7:1", hdr)); got.BaseURL != "https://trove.example.com" {
			t.Fatalf("x-forwarded-for mode read Forwarded: %+v", got)
		}
		fm, _ := NewResolver(trusted, "https://trove.example.com", extra, WithForwardedHeader(HeaderForwarded))
		if got := fm.Resolve(req("10.42.1.7:1", map[string]string{"X-Forwarded-Host": "npm.example.com", "X-Forwarded-Proto": "https"})); got.BaseURL != "https://trove.example.com" {
			t.Fatalf("forwarded mode read X-Forwarded-*: %+v", got)
		}
	})
	t.Run("R-OPS-005 X-Forwarded-Host with X-Forwarded-Port", func(t *testing.T) {
		r, _ := NewResolver(trusted, "https://trove.example.com", extra)
		got := r.Resolve(req("10.42.1.7:1", map[string]string{"X-Forwarded-Host": "npm.example.com", "X-Forwarded-Proto": "https", "X-Forwarded-Port": "443"}))
		if got.BaseURL != "https://npm.example.com" {
			t.Fatalf("with port: %+v", got)
		}
	})
	t.Run("R-OPS-005 unset base_url derives the origin from the request and says so", func(t *testing.T) {
		r, _ := NewResolver(nil, "", nil)
		q := req("192.0.2.1:1", nil)
		q.Host = "localhost:8080"
		if got := r.Resolve(q); got.BaseURL != "http://localhost:8080" || got.BaseURLSource != "request" {
			t.Fatalf("%+v", got)
		}
		q.TLS = &tls.ConnectionState{}
		if got := r.Resolve(q); got.BaseURL != "https://localhost:8080" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("R-OPS-005 a trailing slash and a default port do not change the origin", func(t *testing.T) {
		r, _ := NewResolver(nil, "https://Trove.example.com:443/", nil)
		if got := r.Resolve(req("192.0.2.1:1", nil)); got.BaseURL != "https://trove.example.com" {
			t.Fatalf("%+v", got)
		}
	})
}
