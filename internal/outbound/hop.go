package outbound

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// HopPolicy decides which credentials and custom headers a request carries.
// The client calls Decorate on a copy of every request it sends, the first one
// (hop 0) and each redirect hop (hop 1, 2, …), after net/http has copied the
// headers of the previous hop. Decorate sets what the host of req.URL may
// receive and removes what it may not, so the policy, not net/http's own
// cross-domain rule, decides (R-REPO-033, R-REPO-036, R-REPO-039).
type HopPolicy interface {
	Decorate(req *http.Request, hop int)
}

// ClientCertPolicy is implemented by a HopPolicy that also scopes the client
// certificate of WithClientCertificate: the certificate is presented only on
// connections for which AllowClientCertificate reports true.
type ClientCertPolicy interface {
	AllowClientCertificate(req *http.Request, hop int) bool
}

// WithHopPolicy makes p decide the credentials and headers of every hop. A
// client certificate is presented to every host unless p is a
// ClientCertPolicy.
func WithHopPolicy(p HopPolicy) ClientOption { return func(s *clientSettings) { s.hop = p } }

type certKey struct{}

// hopOf is the number of redirects that led to req: net/http links a request
// made after a redirect to the redirect response through req.Response.
func hopOf(req *http.Request) int {
	n := 0
	for r := req; r.Response != nil && r.Response.Request != nil && n < 1024; r = r.Response.Request {
		n++
	}
	return n
}

// certGetter picks the client certificate of one handshake: the first of certs
// the server accepts, and none where the hop policy withholds it.
func certGetter(certs []tls.Certificate) func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return func(info *tls.CertificateRequestInfo) (*tls.Certificate, error) {
		if allowed, ok := info.Context().Value(certKey{}).(bool); ok && !allowed {
			return &tls.Certificate{}, nil
		}
		for i := range certs {
			if info.SupportsCertificate(&certs[i]) == nil {
				return &certs[i], nil
			}
		}
		return &tls.Certificate{}, nil
	}
}

func withCertAllowed(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, certKey{}, allowed)
}

// CredentialPolicy is the HopPolicy of a remote repository.
//
// The upstream origin (scheme, host and port of Origin) and the hosts that match
// a SendCredentials pattern receive the credentials and custom headers on every
// hop. Any other host receives them only on a redirect hop and only with
// ForwardOnRedirect. The first request to a host that is neither the origin nor
// a SendCredentials host (an extra host the format may visit) never carries them.
//
// Credentials travel encrypted: a hop that is not https receives none, unless the
// origin itself is http, where the operator chose a cleartext upstream and every
// hop the rules above admit carries them (R-REPO-033, Q-ARCH-224).
type CredentialPolicy struct {
	// Origin is the upstream URL of the repository.
	Origin *url.URL
	// SendCredentials holds the host patterns (path.Match syntax, for example
	// "*.registry.example") of extraHosts entries with sendCredentials set.
	SendCredentials []string
	// ForwardOnRedirect is remote.forwardAuthOnRedirect.
	ForwardOnRedirect bool
	// Headers returns the credentials (Authorization) and the custom headers to
	// send, read for every request so a rotated secret applies at once. Its keys
	// are also what Decorate removes from a host that must not have them.
	Headers func() http.Header
}

// allowed reports whether the host of u may receive credentials at hop.
func (p *CredentialPolicy) allowed(u *url.URL, hop int) bool {
	if !isHTTPS(u) && (p.Origin == nil || isHTTPS(p.Origin)) {
		return false
	}
	if p.Origin != nil && sameOrigin(p.Origin, u) {
		return true
	}
	host := strings.ToLower(u.Hostname())
	for _, pat := range p.SendCredentials {
		if ok, _ := path.Match(strings.ToLower(pat), host); ok {
			return true
		}
	}
	return hop > 0 && p.ForwardOnRedirect
}

// Decorate implements HopPolicy.
func (p *CredentialPolicy) Decorate(req *http.Request, hop int) {
	var creds http.Header
	if p.Headers != nil {
		creds = p.Headers()
	}
	req.Header.Del("Authorization")
	for k := range creds {
		req.Header.Del(k)
	}
	if withoutCredentials(req) || (hop > 0 && firstHopOnly(req)) || !p.allowed(req.URL, hop) {
		return
	}
	for k, vs := range creds {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	// A token from the upstream's own handshake belongs to the origin that asked for it: no other host receives it, not an
	// extraHosts entry with sendCredentials and not a redirect target, and it replaces the repository's credential there.
	if v, ok := req.Context().Value(authorizationKey{}).(string); ok && p.Origin != nil && sameOrigin(p.Origin, req.URL) {
		req.Header.Set("Authorization", v)
	}
}

// AllowClientCertificate implements ClientCertPolicy.
func (p *CredentialPolicy) AllowClientCertificate(req *http.Request, hop int) bool {
	return !withoutCredentials(req) && (hop == 0 || !firstHopOnly(req)) && p.allowed(req.URL, hop)
}

func isHTTPS(u *url.URL) bool { return strings.EqualFold(u.Scheme, "https") }

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && portOf(a) == portOf(b)
}

func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

type authorizationKey struct{}

type firstHopOnlyKey struct{}

// WithCredentialsOnTheFirstHopOnly marks the requests made with the returned context as ones whose credentials, custom headers and
// client certificate go to the host the request names and to no redirect target, whatever forwardAuthOnRedirect says: the request
// to an upstream's token service, whose redirect is the upstream's to choose (R-REPO-033).
func WithCredentialsOnTheFirstHopOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, firstHopOnlyKey{}, true)
}

func firstHopOnly(req *http.Request) bool {
	v, _ := req.Context().Value(firstHopOnlyKey{}).(bool)
	return v
}

// WithAuthorization makes the requests made with the returned context carry value as their Authorization header on the hops
// that reach the upstream's origin (CredentialPolicy.Origin), in place of the repository's credential: the token a handshake
// with the upstream's token service produced (R-REPO-033). The hop policy still decides: a hop to any other host gets neither the
// token nor the repository's credential from this mark, a hop the policy gives no credential (https to http) gets no token, and
// [WithoutCredentials] outranks it.
func WithAuthorization(ctx context.Context, value string) context.Context {
	return context.WithValue(ctx, authorizationKey{}, value)
}

type noCredentialsKey struct{}

// WithoutCredentials marks the requests made with the returned context as ones that carry no credential, no custom header and no
// client certificate on any hop, whatever the hop policy would allow the host: a mirror's discovery request to a host that is
// neither the upstream nor an extraHosts entry (R-REPO-051, Q-REPO-151). A redirect keeps the context, so the mark holds for
// every hop of the request.
func WithoutCredentials(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCredentialsKey{}, true)
}

// withoutCredentials reports whether the request was marked by [WithoutCredentials].
func withoutCredentials(req *http.Request) bool {
	v, _ := req.Context().Value(noCredentialsKey{}).(bool)
	return v
}
