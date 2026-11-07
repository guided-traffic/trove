package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

// The forwarded headers of a Bastion (R-BAS-017): exactly one forwarded-for entry in each family, the
// client address the Bastion determined, and the Bastion's own scheme, host and port. The main installation
// lists the Bastion's egress address in server.trusted_proxies and the Bastion's base URL in
// server.additional_base_urls; the client address then resolves to the caller's and every document is
// rendered on the Bastion's host, with either value of server.forwarded_header.

const (
	bastionEgress  = "198.51.100.20"
	bastionIngress = "10.244.3.9"
)

// bastionHeaders returns the headers the Bastion sends for a caller at client: the caller's own forwarded
// headers are replaced, so one entry stands in each family (R-BAS-017). hostPort is the Bastion's base URL
// authority and proto its scheme.
func bastionHeaders(client, proto, host, port string) http.Header {
	node := client
	if a, ok := parseNode(client); ok && a.Is6() {
		node = `"[` + client + `]"`
	}
	h := http.Header{}
	h.Set("X-Forwarded-For", client)
	h.Set("Forwarded", "for="+node+";proto="+proto+";host="+host)
	h.Set("X-Forwarded-Proto", proto)
	h.Set("X-Forwarded-Host", host)
	h.Set("X-Forwarded-Port", port)
	return h
}

func bastionRequest(peer string, h http.Header) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://trove.example.com/api/npm/it/npm-all/@acme/sdk", nil)
	r.RemoteAddr = peer + ":40000"
	for k, vs := range h {
		r.Header[k] = append([]string(nil), vs...)
	}
	return r
}

func TestR_BAS_017_TheMainInstallationResolvesTheRealClientAndTheBastionsHost(t *testing.T) {
	trusted := []string{"10.244.0.0/16", bastionEgress + "/32"}
	additional := []string{"https://packages.example.com", "https://mirror.example.com:8443"}
	server := config.Server{
		Listen: ":8080", OpsListen: ":8081", BaseURL: "https://trove.example.com", MaxInFlight: 4096,
		ReplicaHeader: "authenticated", TrustedProxies: trusted, AdditionalBaseURLs: additional,
	}
	for _, family := range []ForwardedHeader{HeaderXForwardedFor, HeaderForwarded} {
		resolvers := map[string]func(t *testing.T) *Resolver{
			"resolver": func(t *testing.T) *Resolver {
				r, err := NewResolver(trusted, "https://trove.example.com", additional, WithForwardedHeader(family))
				if err != nil {
					t.Fatal(err)
				}
				return r
			},
			"default endpoint": func(t *testing.T) *Resolver {
				r, err := NewResolver(trusted, "https://trove.example.com", additional, WithForwardedHeader(family), ForEndpoint(endpointFixture(t, server, "default")))
				if err != nil {
					t.Fatal(err)
				}
				return r
			},
		}
		for rname, build := range resolvers {
			cases := []struct {
				name    string
				peer    string
				headers http.Header
				client  string
				base    string
			}{
				{
					"the Bastion connects directly: an IPv4 caller on the Bastion's host",
					bastionEgress, bastionHeaders("203.0.113.7", "https", "packages.example.com", "443"),
					"203.0.113.7", "https://packages.example.com",
				},
				{
					"an IPv6 caller, quoted and bracketed in Forwarded",
					bastionEgress, bastionHeaders("2001:db8::7", "https", "packages.example.com", "443"),
					"2001:db8::7", "https://packages.example.com",
				},
				{
					"a Bastion base URL with a port",
					bastionEgress, bastionHeaders("203.0.113.7", "https", "mirror.example.com:8443", "8443"),
					"203.0.113.7", "https://mirror.example.com:8443",
				},
				{
					"the main installation's ingress sits in between and appends the Bastion's address",
					bastionIngress, func() http.Header {
						h := bastionHeaders("203.0.113.7", "https", "packages.example.com", "443")
						h.Set("X-Forwarded-For", "203.0.113.7, "+bastionEgress)
						return h
					}(),
					"203.0.113.7", "https://packages.example.com",
				},
			}
			for _, c := range cases {
				t.Run(string(family)+" "+rname+" "+c.name, func(t *testing.T) {
					info := build(t).Resolve(bastionRequest(c.peer, c.headers))
					if got := info.Client.String(); got != c.client {
						t.Errorf("client = %s, want %s", got, c.client)
					}
					if info.BaseURL != c.base {
						t.Errorf("response base URL = %s, want %s", info.BaseURL, c.base)
					}
				})
			}
		}
	}

	t.Run("a Bastion base URL that is no entry of additional_base_urls renders the main installation's host", func(t *testing.T) {
		for _, family := range []ForwardedHeader{HeaderXForwardedFor, HeaderForwarded} {
			r, err := NewResolver(trusted, "https://trove.example.com", nil, WithForwardedHeader(family))
			if err != nil {
				t.Fatal(err)
			}
			info := r.Resolve(bastionRequest(bastionEgress, bastionHeaders("203.0.113.7", "https", "packages.example.com", "443")))
			if info.BaseURL != "https://trove.example.com" || info.Client.String() != "203.0.113.7" {
				t.Errorf("%s: base %s client %s", family, info.BaseURL, info.Client)
			}
		}
	})

	t.Run("a Bastion whose egress address is not a trusted proxy is the client itself", func(t *testing.T) {
		r, err := NewResolver([]string{"10.244.0.0/16"}, "https://trove.example.com", additional)
		if err != nil {
			t.Fatal(err)
		}
		info := r.Resolve(bastionRequest(bastionEgress, bastionHeaders("203.0.113.7", "https", "packages.example.com", "443")))
		if info.Client.String() != bastionEgress || info.BaseURL != "https://trove.example.com" {
			t.Errorf("client %s base %s: the forwarded headers of an untrusted peer were honoured", info.Client, info.BaseURL)
		}
	})

	t.Run("a caller's own forwarded headers do not survive: the Bastion's one entry is the only one", func(t *testing.T) {
		// The Bastion replaces the headers (R-BAS-017), so a forged entry of the caller never reaches the main
		// installation. Were one to arrive to the left of the Bastion's entry, the right-most untrusted entry
		// still wins.
		for _, family := range []ForwardedHeader{HeaderXForwardedFor, HeaderForwarded} {
			r, err := NewResolver(trusted, "https://trove.example.com", additional, WithForwardedHeader(family))
			if err != nil {
				t.Fatal(err)
			}
			h := bastionHeaders("203.0.113.7", "https", "packages.example.com", "443")
			h.Set("X-Forwarded-For", "192.0.2.1, 203.0.113.7")
			h.Set("Forwarded", "for=192.0.2.1, for=203.0.113.7;proto=https;host=packages.example.com")
			info := r.Resolve(bastionRequest(bastionEgress, h))
			if info.Client.String() != "203.0.113.7" {
				t.Errorf("%s: client %s, want 203.0.113.7", family, info.Client)
			}
		}
	})
}
