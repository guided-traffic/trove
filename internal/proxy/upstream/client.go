package upstream

import (
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// Client is the HTTP client of one remote repository in one version of its
// configuration. It is safe for concurrent use and keeps one connection pool.
//
// A request made with it carries no Authorization header of the caller's making:
// the hop policy removes any and adds the repository's credential for the hosts
// that may have it (R-REPO-033, R-REPO-039). Code that builds a request for an
// upstream therefore never sets that header.
type Client struct {
	key      string
	settings repository.RemoteSettings
	origin   *url.URL
	http     *http.Client
	cred     *credential
}

// Key is the key of the repository.
func (c *Client) Key() string { return c.key }

// Settings returns the settings the client was built from.
func (c *Client) Settings() repository.RemoteSettings { return c.settings }

// Origin is the parsed upstream URL.
func (c *Client) Origin() *url.URL {
	u := *c.origin
	return &u
}

// HTTP returns the client to send requests with. Its transport fails a request
// whose credential cannot be resolved with 502 upstream_secret_unavailable, and
// applies the egress rules, the TLS verification and the redirect rules.
func (c *Client) HTTP() *http.Client { return c.http }

// Close drops the idle connections of the client's pool.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Allowed reports whether a request to u may be sent for this repository: its
// host is the upstream's, or matches an entry of extraHosts (R-REPO-043). The
// answer is 502 foreign_host_denied otherwise, and no request goes out. It
// guards the URLs that come out of upstream metadata; redirects have their own
// rule (R-REPO-039) in the client.
func (c *Client) Allowed(u *url.URL) error {
	if sameHost(c.origin, u) {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range c.settings.ExtraHosts {
		if ok, _ := path.Match(h.Pattern, host); ok {
			return nil
		}
	}
	return errs.E(errs.UpstreamFailed, errs.CodeForeignHostDenied,
		"The upstream document points to the host "+host+", which is neither the upstream of the repository "+c.key+" nor one of its extraHosts.")
}

// sameHost compares the host names and the effective ports. A port that is the
// default of its scheme equals the other default, so that an upstream document may
// move from http to https on the same host.
func sameHost(a, b *url.URL) bool {
	if !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	pa, pb := a.Port(), b.Port()
	return pa == pb || (isDefaultPort(a.Scheme, pa) && isDefaultPort(b.Scheme, pb))
}

func isDefaultPort(scheme, port string) bool {
	switch {
	case port == "":
		return true
	case strings.EqualFold(scheme, "https"):
		return port == "443"
	}
	return port == "80"
}

// guard fails a request before it is sent when the credential cannot be
// resolved, so a vanished secret file or a copied sealed value is 502
// upstream_secret_unavailable and never an anonymous request that the upstream
// would answer with 401.
type guard struct {
	next http.RoundTripper
	repo string
	cred *credential
}

func (g *guard) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := g.cred.check(g.repo); err != nil {
		return nil, err
	}
	return g.next.RoundTrip(r)
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the pool.
func (g *guard) CloseIdleConnections() {
	if c, ok := g.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
