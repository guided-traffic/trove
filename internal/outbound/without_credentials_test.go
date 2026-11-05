package outbound

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

// TestWithoutCredentials: a request marked by WithoutCredentials carries no credential, no custom header and no client certificate on
// any hop, also toward a host that the hop policy would give them (R-REPO-051, Q-REPO-151: a mirror's discovery host).
func TestWithoutCredentials(t *testing.T) {
	origin, _ := url.Parse("https://registry.example.org/")
	p := &CredentialPolicy{Origin: origin, SendCredentials: []string{"replicate.example.org"}, ForwardOnRedirect: true, Headers: creds}
	for _, tc := range []struct {
		name   string
		target string
		hop    int
	}{
		{"the upstream's own host", "https://registry.example.org/x", 0},
		{"an extraHosts entry with sendCredentials", "https://replicate.example.org/x", 0},
		{"a redirect hop that is forwarded", "https://blobs.example.com/x", 1},
	} {
		t.Run("R-REPO-051 "+tc.name, func(t *testing.T) {
			plain, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, tc.target, nil)
			plain.Header.Set("Authorization", "Bearer from-the-caller")
			p.Decorate(plain, tc.hop)
			if plain.Header.Get("Authorization") != "Bearer s3cret" || !p.AllowClientCertificate(plain, tc.hop) {
				t.Fatalf("the policy no longer gives %s the credential: %v", tc.name, plain.Header)
			}
			marked, _ := http.NewRequestWithContext(WithoutCredentials(context.Background()), http.MethodGet, tc.target, nil)
			marked.Header.Set("Authorization", "Bearer from-the-caller")
			marked.Header.Set("X-Api-Key", "k-123")
			p.Decorate(marked, tc.hop)
			if marked.Header.Get("Authorization") != "" || marked.Header.Get("X-Api-Key") != "" {
				t.Fatalf("a marked request carries credentials: %v", marked.Header)
			}
			if p.AllowClientCertificate(marked, tc.hop) {
				t.Fatal("a marked request presents the client certificate")
			}
		})
	}
	t.Run("R-REPO-051 the mark holds for a redirect: the next hop keeps the context", func(t *testing.T) {
		origin, _, atOrigin, atOther := redirectPair(t)
		pol := policyFor(t, origin, true)
		c := New(allowLoopback()).Client(PurposeRemote, WithHopPolicy(pol))
		req, _ := http.NewRequestWithContext(WithoutCredentials(context.Background()), http.MethodGet, origin.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		for name, got := range map[string]*seen{"origin": atOrigin, "other": atOther} {
			if h := got.last(t); h.Get("Authorization") != "" || h.Get("X-Api-Key") != "" {
				t.Errorf("%s got %v", name, h)
			}
		}
	})
}
