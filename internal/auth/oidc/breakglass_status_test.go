package oidc

import (
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

func TestR_AUTH_014_BreakGlassPasswordStatus(t *testing.T) {
	mk := func(cfg config.AuthBreakGlass, pw *password) *Service {
		s := &Service{o: Options{Auth: config.Auth{BreakGlass: cfg}}}
		s.bg = &BreakGlass{cfg: cfg, pw: pw}
		return s
	}
	for _, tc := range []struct {
		name string
		s    *Service
		want PasswordStatus
	}{
		{"disabled", mk(config.AuthBreakGlass{}, nil), PasswordNotApplicable},
		{"enabled without a password file", mk(config.AuthBreakGlass{Enabled: true}, nil), PasswordNotConfigured},
		{"a file whose rotated content is not usable", mk(config.AuthBreakGlass{Enabled: true, PasswordFile: "/x"}, nil), PasswordUnusable},
		{"a usable password", mk(config.AuthBreakGlass{Enabled: true, PasswordFile: "/x"}, &password{}), PasswordOK},
	} {
		t.Run("R-AUTH-014 "+tc.name, func(t *testing.T) {
			if got := tc.s.BreakGlassPassword(); got != tc.want {
				t.Fatalf("BreakGlassPassword() = %q, want %q", got, tc.want)
			}
		})
	}
}
