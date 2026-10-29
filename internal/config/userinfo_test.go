package config

import (
	"bytes"
	"strings"
	"testing"
)

func loadEdge(t *testing.T, file string, env ...string) (*Loaded, error) {
	t.Helper()
	return Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: minimalEnv(env...), Command: CommandAdmin})
}

func TestURLUserinfo(t *testing.T) {
	t.Run("R-OPS-013 tracing.endpoint with user information is refused and the message hides the password", func(t *testing.T) {
		_, err := loadEdge(t, "tracing:\n  endpoint: https://otel:s3cr3t-pw@collector.example:4318\n")
		if err == nil || !strings.Contains(err.Error(), "tracing.endpoint") || !strings.Contains(err.Error(), "tracing.headers_file") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "s3cr3t-pw") {
			t.Fatalf("the message prints the password: %v", err)
		}
		if _, err := loadEdge(t, "tracing:\n  endpoint: https://collector.example:4318\n"); err != nil {
			t.Fatalf("plain endpoint refused: %v", err)
		}
	})

	t.Run("R-OPS-013 user information that url.Parse refuses is found too", func(t *testing.T) {
		for _, raw := range []string{"https://u:p@ss@h.example/", `https://u:p"w@h.example`, "https://tokenonly@h.example"} {
			var issues []Issue
			rejectUserinfo(&Report{prefix: "x.", issues: &issues}, "url", raw, "")
			if len(issues) != 1 {
				t.Errorf("%s: %v", raw, issues)
				continue
			}
			for _, secret := range []string{"p@ss", `p"w`, "tokenonly"} {
				if strings.Contains(raw, secret) && strings.Contains(issues[0].Message, secret) {
					t.Errorf("%s: message prints %q: %s", raw, secret, issues[0].Message)
				}
			}
		}
	})

	t.Run("R-OPS-028 admin config validate redacts user information of any value", func(t *testing.T) {
		// log.request.redact_query_params is no URL key and has no userinfo
		// check; the effective view must still not print the password.
		got := redactDisplay([]string{"https://ops:hunter2@a.example", "https://b.example"}).([]string)
		if got[0] != "https://ops:"+Redacted+"@a.example" || got[1] != "https://b.example" {
			t.Fatalf("redacted list = %v", got)
		}
		if s := redactDisplay("s3://key:secret@bucket/prefix").(string); strings.Contains(s, "secret") {
			t.Fatalf("redacted = %q", s)
		}
		if s := redactDisplay("not a url, mail ops@example.org").(string); s != "not a url, mail ops@example.org" {
			t.Fatalf("plain text changed: %q", s)
		}
		l, err := loadEdge(t, "", `TROVE_LOG_REQUEST_REDACT_QUERY_PARAMS=["https://u:leak-me@x.example"]`)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := l.WriteTable(&buf); err != nil {
			t.Fatal(err)
		}
		js, _ := l.EffectiveJSON()
		if strings.Contains(buf.String(), "leak-me") || strings.Contains(string(js), "leak-me") || !strings.Contains(buf.String(), "u:"+Redacted+"@x.example") {
			t.Fatalf("table or JSON prints the password:\n%s\n%s", buf.String(), js)
		}
	})
}

func TestEdgeSectionRules(t *testing.T) {
	t.Run("R-OPS-018 tracing.trusted_peers defaults to empty and refuses a catch-all or a non-address", func(t *testing.T) {
		l, err := loadEdge(t, "")
		if err != nil {
			t.Fatal(err)
		}
		if tr := l.Config.Tracing; len(tr.TrustedPeers) != 0 || tr.IncomingContext != "trusted_peers" {
			t.Fatalf("defaults = %+v", tr)
		}
		for _, bad := range []string{`["0.0.0.0/0"]`, `["::/0"]`, `["edge.example"]`} {
			if _, err := loadEdge(t, "", "TROVE_TRACING_TRUSTED_PEERS="+bad); err == nil || !strings.Contains(err.Error(), "tracing.trusted_peers") {
				t.Errorf("%s: err = %v", bad, err)
			}
		}
		if _, err := loadEdge(t, "", `TROVE_TRACING_TRUSTED_PEERS=["10.0.5.7","fd00:1::/64"]`); err != nil {
			t.Errorf("valid peers refused: %v", err)
		}
		if _, err := loadEdge(t, "tracing:\n  incoming_context: trusted_proxies\n"); err == nil {
			t.Error("the old value trusted_proxies is accepted")
		}
	})

	t.Run("R-OPS-013 the URL keys of storage.s3 refuse user information and hide the password", func(t *testing.T) {
		for _, c := range []struct{ yaml, key, hint string }{
			{"endpoint: https://ak:s3cr3t-pw@s3.example", "storage.s3.endpoint", "storage.s3.credentials.access_key_id_file"},
			{"credentials:\n      sts_endpoint: https://ak:s3cr3t-pw@sts.example", "storage.s3.credentials.sts_endpoint", ""},
			{"presign:\n      endpoint: https://ak:s3cr3t-pw@blobs.example", "storage.s3.presign.endpoint", ""},
			{"http:\n      proxy_url: http://ak:s3cr3t-pw@proxy.example:3128", "storage.s3.http.proxy_url", "storage.s3.http.proxy_credentials_file"},
		} {
			_, err := loadEdge(t, "storage:\n  s3:\n    "+c.yaml+"\n")
			if err == nil || !strings.Contains(err.Error(), c.key) || !strings.Contains(err.Error(), "user information") {
				t.Errorf("%s: err = %v", c.key, err)
				continue
			}
			if strings.Contains(err.Error(), "s3cr3t-pw") {
				t.Errorf("%s: the message prints the password: %v", c.key, err)
			}
			if c.hint != "" && !strings.Contains(err.Error(), c.hint) {
				t.Errorf("%s: no hint %s in %v", c.key, c.hint, err)
			}
		}
	})

	t.Run("R-OPS-005 a base_url with user information is refused and the message hides the password", func(t *testing.T) {
		_, err := loadEdge(t, "server:\n  base_url: https://u:s3cr3t-pw@trove.example\n")
		if err == nil || !strings.Contains(err.Error(), "server.base_url") || strings.Contains(err.Error(), "s3cr3t-pw") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-HA-056 valkey.tls.mode verify-ca without a CA file is refused", func(t *testing.T) {
		if _, err := loadEdge(t, "valkey:\n  tls:\n    mode: verify-ca\n"); err == nil || !strings.Contains(err.Error(), "valkey.tls.ca_file") {
			t.Fatalf("err = %v", err)
		}
		if _, err := loadEdge(t, "valkey:\n  tls:\n    mode: verify-ca\n    ca_file: /etc/trove/valkey-ca.pem\n"); err != nil {
			t.Fatalf("verify-ca with a CA file: %v", err)
		}
	})

	t.Run("R-OPS-009 log.ring_buffer_bytes has a default and accepts sizes", func(t *testing.T) {
		l, err := loadEdge(t, "log:\n  ring_buffer_bytes: 2MiB\n")
		if err != nil || l.Config.Log.RingBufferBytes != 2*MiB {
			t.Fatalf("%v %v", err, l)
		}
		l, _ = loadEdge(t, "")
		if l.Config.Log.RingBufferBytes != 8*MiB {
			t.Fatalf("default = %s", l.Config.Log.RingBufferBytes)
		}
	})

	t.Run("R-ARCH-022 the default deny list holds the metadata and IPv4-compatible ranges", func(t *testing.T) {
		l, _ := loadEdge(t, "")
		deny := strings.Join(l.Config.Proxy.Outbound.AddressPolicy.DenyCIDRs, " ")
		for _, c := range []string{"::/96", "100.100.100.200/32", "168.63.129.16/32", "169.254.0.0/16", "fd00:ec2::254/128"} {
			if !strings.Contains(" "+deny+" ", " "+c+" ") {
				t.Errorf("%s missing from %s", c, deny)
			}
		}
	})
}
