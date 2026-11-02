package config

import (
	"strings"
	"testing"
	"time"
)

func loadCI(t *testing.T, file string, env ...string) (*Loaded, error) {
	t.Helper()
	return Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: minimalEnv(env...), Command: CommandMigrate})
}

func TestR_CI_001_TheCISectionDefaults(t *testing.T) {
	l, err := loadCI(t, "")
	if err != nil {
		t.Fatal(err)
	}
	c := l.Config.CI
	t.Run("R-CI-003 signing key settings", func(t *testing.T) {
		sk := c.SigningKeys
		if sk.DefaultRefreshInterval != time.Hour || sk.MinRefreshInterval != 5*time.Minute || sk.MaxRefreshInterval != 24*time.Hour ||
			sk.UnknownKIDRefreshInterval != time.Minute || sk.MaxStale != 24*time.Hour || sk.FetchTimeout != 5*time.Second ||
			sk.MaxResponseBytes != 1048576 || sk.CacheSafetyTTL != 10*time.Minute {
			t.Fatalf("%+v", sk)
		}
	})
	t.Run("R-CI-007 R-CI-021 exchange settings", func(t *testing.T) {
		e := c.Exchange
		rl := e.RateLimit
		if !e.Enabled || !e.ExposeReason || e.MaxBodyBytes != 65536 || e.MaxSubjectTokenBytes != 16384 ||
			rl.PerClientIPPerMinute != 600 || rl.PerClientIPBurst != 100 || rl.PerSubjectPerMinute != 300 || rl.PerSubjectBurst != 100 || len(rl.ExemptCIDRs) != 0 {
			t.Fatalf("%+v", e)
		}
	})
	t.Run("R-CI-009 R-CI-016 R-CI-018 R-CI-020 the rest", func(t *testing.T) {
		if c.Mappings.MaxPerIssuer != 1000 || c.Mappings.CacheSafetyTTL != 10*time.Minute ||
			c.Token.DefaultTTL != 15*time.Minute || c.Token.MaxTTL != time.Hour || c.Token.MinTTL != 30*time.Second ||
			c.Replay.RetentionAfterExpiry != 15*time.Minute || c.Replay.WarnAfterExchanges != 10 ||
			c.Issuances.Retention != 8760*time.Hour || len(c.Issuers) != 0 || c.JobTokenFallback.Enabled {
			t.Fatalf("%+v", c)
		}
	})
}

func TestR_CI_016_TheLifetimeCeilingIsOne(t *testing.T) {
	t.Run("R-CI-016 ci.token.max_ttl above auth.tokens.cit.max_ttl names both keys", func(t *testing.T) {
		_, err := loadCI(t, "", "TROVE_CI_TOKEN_MAX_TTL=6h", "TROVE_AUTH_TOKENS_CIT_MAX_TTL=2h")
		if err == nil || !strings.Contains(err.Error(), "ci.token.max_ttl") || !strings.Contains(err.Error(), "auth.tokens.cit.max_ttl") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-CI-016 the environment override of the specification", func(t *testing.T) {
		l, err := loadCI(t, "", "TROVE_CI_TOKEN_MAX_TTL=30m")
		if err != nil || l.Config.CI.Token.MaxTTL != 30*time.Minute {
			t.Fatalf("%v %v", l, err)
		}
	})
	t.Run("R-CI-016 a default ttl above the maximum is refused", func(t *testing.T) {
		_, err := loadCI(t, "ci:\n  token:\n    default_ttl: 2h\n")
		if err == nil || !strings.Contains(err.Error(), "ci.token.default_ttl") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_CI_021_ThePerIssuerBucketIsRetired(t *testing.T) {
	t.Run("R-CI-021 the key in the file names its replacement", func(t *testing.T) {
		_, err := loadCI(t, "ci:\n  exchange:\n    rate_limit:\n      per_issuer_per_second: 5\n")
		if err == nil || !strings.Contains(err.Error(), "ci.exchange.rate_limit.per_issuer_per_second") || !strings.Contains(err.Error(), "per_client_ip_per_minute") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-CI-021 the environment variable names its replacement", func(t *testing.T) {
		_, err := loadCI(t, "", "TROVE_CI_EXCHANGE_RATE_LIMIT_PER_ISSUER_PER_SECOND=5")
		if err == nil || !strings.Contains(err.Error(), "per_subject_per_minute") {
			t.Fatalf("err = %v", err)
		}
	})
}

// R-CI-022 (P2) is not built: its switch is refused by name, and the test carries no requirement ID, because a refusal is no
// evidence that the fallback is implemented.
func TestTheJobTokenFallbackIsRefusedByName(t *testing.T) {
	_, err := loadCI(t, "ci:\n  job_token_fallback:\n    enabled: true\n")
	if err == nil || !strings.Contains(err.Error(), "ci.job_token_fallback.enabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestR_CI_001_IssuersFromTheConfiguration(t *testing.T) {
	t.Run("Q-CI-16 the owner's declaration loads and keeps the keys left out unset", func(t *testing.T) {
		l, err := loadCI(t, `ci:
  issuers:
    - name: gitlab-main
      kind: gitlab
      issuer: https://gitlab.example.com
      public_issuer: false
      allowed_audiences: ["https://packages.example.com"]
      ca_bundle_ref: file:/var/run/secrets/gitlab-ca/ca.crt
`)
		if err != nil {
			t.Fatal(err)
		}
		got := l.Config.CI.Issuers
		if len(got) != 1 || got[0].Name != "gitlab-main" || got[0].PublicIssuer == nil || *got[0].PublicIssuer ||
			got[0].ExclusiveAudience != nil || got[0].CABundleRef == nil || len(got[0].AllowedAudiences) != 1 {
			t.Fatalf("%+v", got)
		}
	})
	refusals := []struct{ name, entry, want string }{
		{"a name that breaks the rule", "{name: GitLab, kind: gitlab, issuer: https://g.example.com}", "ci.issuers[0].name"},
		{"an http issuer", "{name: gl, kind: gitlab, issuer: http://g.example.com}", "ci.issuers[0].issuer"},
		{"kind github", "{name: gh, kind: github, issuer: https://token.actions.githubusercontent.com}", "ci.issuers[0].kind"},
		{"an HMAC algorithm", "{name: gl, kind: gitlab, issuer: https://g.example.com, allowed_algorithms: [HS256]}", "ci.issuers[0].allowed_algorithms"},
		{"a clock skew above 300", "{name: gl, kind: gitlab, issuer: https://g.example.com, clock_skew_seconds: 301}", "ci.issuers[0].clock_skew_seconds"},
		{"an empty audience list", "{name: gl, kind: gitlab, issuer: https://g.example.com, allowed_audiences: []}", "ci.issuers[0].allowed_audiences"},
	}
	for _, tc := range refusals {
		t.Run("R-CI-001 "+tc.name+" is refused and names the key", func(t *testing.T) {
			_, err := loadCI(t, "ci:\n  issuers:\n    - "+tc.entry+"\n")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("R-CI-001 an issuer declared twice names both", func(t *testing.T) {
		_, err := loadCI(t, "ci:\n  issuers:\n    - {name: a1, kind: gitlab, issuer: https://g.example.com}\n    - {name: a2, kind: gitlab, issuer: https://g.example.com}\n")
		if err == nil || !strings.Contains(err.Error(), "ci.issuers[1].issuer") || !strings.Contains(err.Error(), "ci.issuers[0]") {
			t.Fatalf("err = %v", err)
		}
	})
}
