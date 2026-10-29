package config

import (
	"strings"
	"testing"
)

func TestTracingTransport(t *testing.T) {
	refused := func(t *testing.T, file, want string) {
		t.Helper()
		_, err := loadEdge(t, file)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want a message containing %q", err, want)
		}
	}

	t.Run("R-OPS-018 SA2 a plaintext endpoint is refused unless tracing.allow_insecure is true", func(t *testing.T) {
		refused(t, "tracing:\n  enabled: true\n  endpoint: http://otel-collector:4318\n", "tracing.endpoint")
		if _, err := loadEdge(t, "tracing:\n  enabled: true\n  endpoint: https://otel-collector:4318\n"); err != nil {
			t.Fatalf("https endpoint refused: %v", err)
		}
		if _, err := loadEdge(t, "tracing:\n  enabled: true\n  allow_insecure: true\n  endpoint: http://127.0.0.1:4318\n"); err != nil {
			t.Fatalf("allow_insecure refused: %v", err)
		}
		if _, err := loadEdge(t, "tracing:\n  enabled: false\n  endpoint: http://otel-collector:4318\n"); err != nil {
			t.Fatalf("a disabled exporter has nothing to refuse: %v", err)
		}
	})

	t.Run("R-OPS-018 SA2 a ca_file next to an http endpoint is refused, even with allow_insecure", func(t *testing.T) {
		refused(t, "tracing:\n  enabled: true\n  allow_insecure: true\n  ca_file: /etc/ca.pem\n  endpoint: http://127.0.0.1:4318\n", "tracing.ca_file")
	})

	t.Run("R-OPS-018 SA2 the endpoint of the standard variable is held to the same rule", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4318")
		refused(t, "tracing:\n  enabled: true\n", "tracing.endpoint")
		if _, err := loadEdge(t, "tracing:\n  enabled: true\n  allow_insecure: true\n"); err != nil {
			t.Fatalf("allow_insecure refused: %v", err)
		}
	})

	t.Run("R-OPS-018 SA2 OTEL_EXPORTER_OTLP_INSECURE=true is refused unless tracing.allow_insecure is true", func(t *testing.T) {
		t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")
		refused(t, "tracing:\n  enabled: true\n  endpoint: https://otel-collector:4318\n", "OTEL_EXPORTER_OTLP_INSECURE")
		if _, err := loadEdge(t, "tracing:\n  enabled: true\n  allow_insecure: true\n  endpoint: https://otel-collector:4318\n"); err != nil {
			t.Fatalf("allow_insecure refused: %v", err)
		}
	})
}

func TestOutboundTrustStore(t *testing.T) {
	t.Run("R-ARCH-022 CR2-8 use_system_roots false with no ca_files is refused at load", func(t *testing.T) {
		_, err := loadEdge(t, "proxy:\n  outbound:\n    tls:\n      use_system_roots: false\n")
		if err == nil || !strings.Contains(err.Error(), "use_system_roots") {
			t.Fatalf("err = %v", err)
		}
		if _, err := loadEdge(t, "proxy:\n  outbound:\n    tls:\n      use_system_roots: false\n      ca_files: [/etc/ca.pem]\n"); err != nil {
			t.Fatalf("a configured CA file is enough: %v", err)
		}
	})
}

func TestBootstrapAnonymousReadEndpoints(t *testing.T) {
	t.Run("R-OPS-002 SA3 the guard looks at the base_url of every endpoint", func(t *testing.T) {
		_, err := loadEdge(t, "system:\n  bootstrap_anonymous_read: true\nserver:\n  endpoints:\n    customers:\n      listen: \":8443\"\n      base_url: https://packages.example.com\n")
		if !hasIssue(err, "system.bootstrap_anonymous_read") || !strings.Contains(err.Error(), "customers") {
			t.Fatalf("err = %v", err)
		}
		if _, err := loadEdge(t, "system:\n  bootstrap_anonymous_read: true\nserver:\n  endpoints:\n    customers:\n      listen: \":8443\"\n      base_url: http://packages.example.com\n"); err != nil {
			t.Fatalf("plain http endpoint refused: %v", err)
		}
	})
}

func TestBootstrapAnonymousReadLocalhostNames(t *testing.T) {
	t.Run("R-OPS-002 a name below .localhost (RFC 6761) counts as loopback for the guard", func(t *testing.T) {
		for _, u := range []string{"https://packages.trove.localhost:8080", "https://trove.localhost"} {
			file := "system:\n  bootstrap_anonymous_read: true\nserver:\n  endpoints:\n    customers:\n      listen: \":8443\"\n      base_url: " + u + "\n"
			if _, err := loadEdge(t, file); err != nil {
				t.Errorf("%s: %v", u, err)
			}
		}
		if !localOrigin("https://x.localhost") || localOrigin("https://localhost.example.com") || localOrigin("https://notlocalhost") {
			t.Error("localOrigin matches the wrong names")
		}
	})
}

func TestEndpointHostRules(t *testing.T) {
	t.Run("R-EP-003 SA4 a wildcard matches one DNS label and nothing else", func(t *testing.T) {
		for _, h := range []string{"a_b.example.com", "-a.example.com", "a b.example.com", "é.example.com", ".example.com", "a.b.example.com", "example.com"} {
			if l, ok := WildcardLabel("*.example.com", h); ok {
				t.Errorf("%q matched with label %q", h, l)
			}
		}
		if l, ok := WildcardLabel("*.example.com", "acme-1.example.com"); !ok || l != "acme-1" {
			t.Errorf("a DNS label was refused: %q %v", l, ok)
		}
	})

	baseURLs := func(base string) string {
		return "server:\n  endpoints:\n    customers:\n      listen: \":8443\"\n      base_url: " + base + "\n"
	}
	t.Run("R-EP-003 SA7 a host-checking endpoint's base_url host is a DNS name", func(t *testing.T) {
		for _, b := range []string{"https://10.0.0.5", "https://[2001:db8::1]:8443", "https://packages.example.com."} {
			if _, err := loadEdge(t, baseURLs(b)); err == nil || !strings.Contains(err.Error(), "base_url") {
				t.Errorf("%s: err = %v", b, err)
			}
		}
		if _, err := loadEdge(t, baseURLs("https://packages.example.com:8443")); err != nil {
			t.Fatalf("DNS name refused: %v", err)
		}
	})
	t.Run("R-EP-003 SA7 the default endpoint checks the host of base_url only when it checks hosts", func(t *testing.T) {
		if _, err := loadEdge(t, "server:\n  base_url: http://10.0.0.5:8080\n"); err != nil {
			t.Fatalf("a development base_url without host checking: %v", err)
		}
		_, err := loadEdge(t, "server:\n  base_url: http://10.0.0.5:8080\n  endpoints:\n    default:\n      hosts: [trove.example.com]\n")
		if err == nil || !strings.Contains(err.Error(), "base_url") {
			t.Fatalf("err = %v", err)
		}
	})
}
