package observability

import (
	"strings"
	"testing"
)

func TestR_OPS_013_LoginSecretsInURLsAreScrubbed(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{
			"R-OPS-013 Q-OPS-91 the query of an OIDC callback",
			"/auth/oidc/corp/callback?code=SplxlOBeZQQYbYS6WxSbIA&state=af0ifjsldkj&session_state=x.y",
			"/auth/oidc/corp/callback?[REDACTED]",
		},
		{
			"R-OPS-013 Q-OPS-91 a callback inside a logged URL keeps its fragment and what follows",
			`redirect to "https://trove.test/auth/oidc/corp/callback?state=s3cr3t&code=c0de" failed`,
			`redirect to "https://trove.test/auth/oidc/corp/callback?[REDACTED]" failed`,
		},
		{
			"R-OPS-013 Q-OPS-91 an ID token hint",
			"https://idp.test/logout?id_token_hint=eyJhbGciOi.eyJzdWIi.sig&post_logout_redirect_uri=https%3A%2F%2Ftrove.test%2F",
			"https://idp.test/logout?id_token_hint=[REDACTED]&post_logout_redirect_uri=https%3A%2F%2Ftrove.test%2F",
		},
		{
			"R-OPS-010 the state filter of the token listing stays readable",
			"/api/v1/tokens?state=active&limit=10",
			"/api/v1/tokens?state=active&limit=10",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Scrub(c.in); got != c.want {
				t.Fatalf("Scrub(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
	t.Run("R-OPS-013 the request log's path field is scrubbed", func(t *testing.T) {
		got := clipField("/auth/oidc/corp/callback?code=abc&state=def", MaxPath, defaultScrubber)
		if strings.Contains(got, "abc") || strings.Contains(got, "def") {
			t.Fatalf("%q", got)
		}
	})
}
