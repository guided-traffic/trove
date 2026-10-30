package config

import (
	"strings"
	"testing"
	"time"
)

func TestUIDefaults(t *testing.T) {
	u := mustLoad(t, Options{Environ: servableEnv(), Command: CommandServe}).Config.UI
	t.Run("R-UI-008 ui.readme defaults", func(t *testing.T) {
		if u.Readme.MaxSourceBytes != 512*KiB || len(u.Readme.ExternalImageOrigins) != 0 {
			t.Errorf("max_source_bytes = %s, external_image_origins = %v", u.Readme.MaxSourceBytes, u.Readme.ExternalImageOrigins)
		}
	})
	t.Run("R-API-030 ui.client_setup defaults", func(t *testing.T) {
		if u.ClientSetup.TokenEnvVar != "TROVE_TOKEN" || u.ClientSetup.TokenTTL != 720*time.Hour {
			t.Errorf("token_env_var = %q, token_ttl = %s", u.ClientSetup.TokenEnvVar, u.ClientSetup.TokenTTL)
		}
	})
}

func TestUIValidation(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"R-UI-008 an origin with a path":                        {"ui:\n  readme:\n    external_image_origins: [\"https://img.example/path\"]\n", "external_image_origins"},
		"R-UI-008 an http origin":                               {"ui:\n  readme:\n    external_image_origins: [\"http://img.example\"]\n", "https origin"},
		"R-UI-008 a wildcard origin":                            {"ui:\n  readme:\n    external_image_origins: [\"https://*.example.com\"]\n", "not allowed in an origin"},
		"R-UI-008 an origin with a directive":                   {"ui:\n  readme:\n    external_image_origins: [\"https://img.example; script-src *\"]\n", "not allowed in an origin"},
		"R-UI-008 an origin with credentials":                   {"ui:\n  readme:\n    external_image_origins: [\"https://u:p@img.example\"]\n", "https origin"},
		"R-API-030 a variable name that is no name":             {"ui:\n  client_setup:\n    token_env_var: \"A-B\"\n", "token_env_var"},
		"R-API-030 a lifetime above the personal token ceiling": {"ui:\n  client_setup:\n    token_ttl: 9000h\n", "auth.tokens.pat.max_ttl"},
		"R-UI-008 a source limit below 1 KiB":                   {"ui:\n  readme:\n    max_source_bytes: 100\n", "max_source_bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadM2(t, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want an error naming %q", err, tc.want)
			}
		})
	}
	t.Run("R-UI-008 an origin of this installation is refused, whatever its spelling", func(t *testing.T) {
		for _, o := range []string{"https://trove.example.com", "https://TROVE.example.com:443", "https://partners.example.com"} {
			yaml := "server:\n  base_url: https://trove.example.com\n  additional_base_urls: [\"https://partners.example.com\"]\nui:\n  readme:\n    external_image_origins: [\"" + o + "\"]\n"
			if _, err := loadM2(t, yaml); err == nil || !strings.Contains(err.Error(), "origin of this installation") {
				t.Errorf("%s: %v", o, err)
			}
		}
	})
	t.Run("R-UI-008 exact https origins are accepted", func(t *testing.T) {
		l, err := loadM2(t, "ui:\n  readme:\n    external_image_origins: [\"https://img.shields.io\", \"https://gitlab.example.com:8443\"]\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Config.UI.Readme.ExternalImageOrigins; len(got) != 2 {
			t.Fatalf("%v", got)
		}
	})
}
