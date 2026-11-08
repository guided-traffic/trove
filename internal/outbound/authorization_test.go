package outbound

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

// TestR_REPO_033_HandshakeTokenGoesToTheOriginOnly: the token of an upstream handshake replaces the repository's credential on the
// origin and is sent to no other host: not to an extraHosts entry with sendCredentials (which keeps the repository's credential),
// not to a redirect target, not over a downgrade to http, and never when the request is marked without credentials.
func TestR_REPO_033_HandshakeTokenGoesToTheOriginOnly(t *testing.T) {
	origin, _ := url.Parse("https://registry.example.com")
	p := &CredentialPolicy{
		Origin: origin, SendCredentials: []string{"auth.example.com"}, ForwardOnRedirect: true,
		Headers: func() http.Header { return http.Header{"Authorization": {"Basic cmVwbw=="}} },
	}
	const token = "Bearer handshake-token"
	for _, tc := range []struct {
		name, target string
		hop          int
		without      bool
		want         string
	}{
		{"the origin gets the token instead of the credential", "https://registry.example.com/v2/x", 0, false, token},
		{"the origin on a redirect hop gets the token", "https://registry.example.com/v2/y", 1, false, token},
		{"a credential host keeps the repository's credential", "https://auth.example.com/token", 0, false, "Basic cmVwbw=="},
		{"a host that forwardAuthOnRedirect admits gets the credential, not the token", "https://cdn.example.net/blob", 1, false, "Basic cmVwbw=="},
		{"another host on the first hop gets nothing", "https://cdn.example.net/blob", 0, false, ""},
		{"the origin over http gets nothing", "http://registry.example.com/v2/x", 0, false, ""},
		{"a request marked without credentials gets nothing", "https://registry.example.com/v2/x", 0, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithAuthorization(context.Background(), token)
			if tc.without {
				ctx = WithoutCredentials(ctx)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer smuggled")
			p.Decorate(req, tc.hop)
			if got := req.Header.Get("Authorization"); got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestR_REPO_033_WithoutTheMarkTheCredentialRuleIsUnchanged: a request that carries no handshake token gets what the hop policy
// gave it before.
func TestR_REPO_033_WithoutTheMarkTheCredentialRuleIsUnchanged(t *testing.T) {
	origin, _ := url.Parse("https://registry.example.com")
	p := &CredentialPolicy{Origin: origin, Headers: func() http.Header { return http.Header{"Authorization": {"Basic cmVwbw=="}} }}
	req, _ := http.NewRequest(http.MethodGet, "https://registry.example.com/v2/x", nil)
	p.Decorate(req, 0)
	if got := req.Header.Get("Authorization"); got != "Basic cmVwbw==" {
		t.Errorf("Authorization = %q", got)
	}
}

// TestR_REPO_033_FirstHopOnlyMarkKeepsTheCredentialFromARedirectTarget: a request marked for the first hop only carries the credential to
// the host it names and to no redirect target, whatever the policy forwards.
func TestR_REPO_033_FirstHopOnlyMarkKeepsTheCredentialFromARedirectTarget(t *testing.T) {
	origin, _ := url.Parse("https://registry.example.com")
	p := &CredentialPolicy{
		Origin: origin, SendCredentials: []string{"auth.example.com"}, ForwardOnRedirect: true,
		Headers: func() http.Header { return http.Header{"Authorization": {"Basic cmVwbw=="}} },
	}
	for _, tc := range []struct {
		name, target string
		hop          int
		marked       bool
		want         string
	}{
		{"the first hop keeps the credential", "https://auth.example.com/token", 0, true, "Basic cmVwbw=="},
		{"a redirect to a credential host gets none", "https://auth.example.com/other", 1, true, ""},
		{"a redirect to the origin gets none", "https://registry.example.com/x", 1, true, ""},
		{"a redirect forwardAuthOnRedirect admits gets none", "https://cdn.example.net/x", 1, true, ""},
		{"without the mark a redirect to a credential host keeps it", "https://auth.example.com/other", 1, false, "Basic cmVwbw=="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.marked {
				ctx = WithCredentialsOnTheFirstHopOnly(ctx)
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, tc.target, nil)
			p.Decorate(req, tc.hop)
			if got := req.Header.Get("Authorization"); got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
			if allowed := p.AllowClientCertificate(req, tc.hop); allowed != (tc.want != "") {
				t.Errorf("client certificate allowed = %v for a request whose credential is %q", allowed, tc.want)
			}
		})
	}
}
