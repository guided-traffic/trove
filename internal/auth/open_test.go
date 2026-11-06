package auth

import (
	"net/http"
	"testing"
)

func TestR_AUTH_037_TheNpmLoginStartIsAnchored(t *testing.T) {
	for _, c := range []struct {
		path string
		open bool
	}{
		{"/-/v1/login", true},
		{"/api/npm/it/npm-local/-/v1/login", true},
		{"/api/npm/it/npm-local~cache/-/v1/login", true},
		{"/files/it/private/-/v1/login", false},
		{"/api/v1/users/-/v1/login", false},
		{"/api/npm/-/v1/login", false},
		// One segment is no address (R-REPO-001), and neither is a "%" in a named segment.
		{"/api/npm/npm-local/-/v1/login", false},
		{"/api/npm/it%2Fnpm-local/-/v1/login", false},
		{"/api/npm/i%74/npm-local/-/v1/login", false},
		{"/api/npm/it/npm-loca%6C/-/v1/login", false},
		{"/api/nuget/it/npm-local/-/v1/login", false},
		{"/api/npm/it/a/b/-/v1/login", false},
		{"/api/npm/it/npm-local/-/v1/login/x", false},
	} {
		t.Run("R-AUTH-037 "+c.path, func(t *testing.T) {
			if got := alwaysOpen(http.MethodPost, c.path); got != c.open {
				t.Fatalf("alwaysOpen(%s) = %v, want %v", c.path, got, c.open)
			}
		})
	}
}

func TestR_AUTH_001_TheNpmExchangeRouteBelowTheRegistryIsAnAddress(t *testing.T) {
	const tail = "-/npm/v1/oidc/token/exchange/package/@acme%2Fui"
	for path, want := range map[string]bool{
		"/-/npm/v1/oidc/token/exchange/package/@acme%2Fui": true,
		"/api/npm/it/npm-all/" + tail:                      true,
		"/api/npm/npm-all/" + tail:                         false, // one segment
		"/api/npm/it%2Fnpm-all/" + tail:                    false,
		"/api/nuget/it/npm-all/" + tail:                    false,
	} {
		if got := isExchangeRoute(http.MethodPost, path); got != want {
			t.Errorf("isExchangeRoute(POST %s) = %v, want %v", path, got, want)
		}
	}
	if isExchangeRoute(http.MethodGet, "/-/npm/v1/oidc/token/exchange/package/x") {
		t.Error("a GET is no exchange")
	}
}
