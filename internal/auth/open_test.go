package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestR_AUTH_037_TheNpmLoginStartIsAnchored(t *testing.T) {
	for _, c := range []struct {
		path string
		open bool
	}{
		{"/-/v1/login", true},
		{"/api/npm/npm-local/-/v1/login", true},
		{"/files/private/-/v1/login", false},
		{"/api/v1/users/-/v1/login", false},
		{"/api/npm/-/v1/login", false},
		{"/api/npm/a/b/-/v1/login", false},
		{"/api/npm/npm-local/-/v1/login/x", false},
	} {
		t.Run("R-AUTH-037 "+c.path, func(t *testing.T) {
			if got := alwaysOpen(httptest.NewRequest(http.MethodPost, c.path, nil)); got != c.open {
				t.Fatalf("alwaysOpen(%s) = %v, want %v", c.path, got, c.open)
			}
		})
	}
}
