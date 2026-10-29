package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestServerTLS(t *testing.T) {
	load := func(file string, env ...string) (*Loaded, error) {
		return Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: minimalEnv(env...), Command: CommandAdmin})
	}
	pair := "      - cert_file: /tls/tls.crt\n        key_file: /tls/tls.key\n"

	t.Run("R-ARCH-035 without certificates both listeners are plain HTTP", func(t *testing.T) {
		l, err := load("")
		if err != nil {
			t.Fatal(err)
		}
		s := l.Config.Server
		if s.TLS.Enabled() || s.OpsListenerTLS().Enabled() || !s.OpsTLS.Inherit {
			t.Fatalf("defaults: %+v %+v", s.TLS, s.OpsTLS)
		}
		if s.TLS.ClientAuth != ClientAuthNone || s.TLS.MinVersion != "1.2" || s.TLS.ReloadInterval != 10*time.Second {
			t.Errorf("block defaults: %+v", s.TLS)
		}
	})

	t.Run("R-ARCH-035 certificates are a list of pairs, from the file and from the environment", func(t *testing.T) {
		l, err := load("server:\n  tls:\n    certificates:\n" + pair + "      - cert_file: /tls/wild.crt\n        key_file: /tls/wild.key\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Config.Server.TLS.Certificates; len(got) != 2 || got[1].CertFile != "/tls/wild.crt" {
			t.Fatalf("certificates = %+v", got)
		}
		l, err = load("", `TROVE_SERVER_TLS_CERTIFICATES=[{"cert_file":"/e/tls.crt","key_file":"/e/tls.key"}]`, "TROVE_SERVER_TLS_RELOAD_INTERVAL=2s")
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Config.Server.TLS; len(got.Certificates) != 1 || got.Certificates[0].KeyFile != "/e/tls.key" || got.ReloadInterval != 2*time.Second {
			t.Fatalf("from the environment: %+v", got)
		}
	})

	t.Run("R-ARCH-035 an unknown field in a certificate entry is refused", func(t *testing.T) {
		_, err := load("server:\n  tls:\n    certificates:\n      - cert_file: /a\n        key_file: /b\n        cert: /c\n")
		if err == nil || !strings.Contains(err.Error(), "server.tls.certificates") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-035 a pair needs both files", func(t *testing.T) {
		_, err := load("server:\n  tls:\n    certificates:\n      - cert_file: /a\n")
		if err == nil || !strings.Contains(err.Error(), "server.tls.certificates entry 0 needs both cert_file and key_file") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-035 client_auth other than none needs client_ca_file", func(t *testing.T) {
		_, err := load("server:\n  tls:\n    client_auth: require\n    certificates:\n" + pair)
		if err == nil || !strings.Contains(err.Error(), "server.tls.client_ca_file is required with client_auth require") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-035 a client CA without client_auth is refused, not ignored", func(t *testing.T) {
		_, err := load("server:\n  tls:\n    client_ca_file: /ca.crt\n    certificates:\n" + pair)
		if err == nil || !strings.Contains(err.Error(), "server.tls.client_auth is none while client_ca_file is set") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-035 client certificates need TLS on the listener", func(t *testing.T) {
		_, err := load("server:\n  tls:\n    client_ca_file: /ca.crt\n    client_auth: require\n")
		if err == nil || !strings.Contains(err.Error(), "server.tls.certificates is empty") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-035 client_auth and min_version take only their listed values", func(t *testing.T) {
		_, err := load("server:\n  tls:\n    client_auth: optional\n    min_version: \"1.1\"\n")
		if err == nil || !strings.Contains(err.Error(), "server.tls.client_auth must be one of") || !strings.Contains(err.Error(), "server.tls.min_version must be one of") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-035 ops_tls inherit serves server.tls's pairs without client certificates", func(t *testing.T) {
		l, err := load("server:\n  tls:\n    client_ca_file: /ca.crt\n    client_auth: require\n    min_version: \"1.3\"\n    certificates:\n" + pair)
		if err != nil {
			t.Fatal(err)
		}
		ops := l.Config.Server.OpsListenerTLS()
		if !reflect.DeepEqual(ops.Certificates, l.Config.Server.TLS.Certificates) || ops.MinVersion != "1.3" {
			t.Errorf("ops block %+v does not carry the client listener's pairs", ops)
		}
		if ops.ClientAuth != ClientAuthNone || ops.ClientCAFile != "" {
			t.Errorf("ops block inherits client certificates: %+v; probes cannot present one", ops)
		}
	})

	t.Run("R-ARCH-035 ops_tls with inherit false is its own block", func(t *testing.T) {
		l, err := load("server:\n  tls:\n    certificates:\n" + pair + "  ops_tls:\n    inherit: false\n    certificates:\n      - cert_file: /ops/tls.crt\n        key_file: /ops/tls.key\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Config.Server.OpsListenerTLS().Certificates; len(got) != 1 || got[0].CertFile != "/ops/tls.crt" {
			t.Fatalf("ops certificates = %+v", got)
		}
	})

	t.Run("R-ARCH-035 ops_tls keys set while inherit is true are refused, not ignored", func(t *testing.T) {
		_, err := load("server:\n  ops_tls:\n    min_version: \"1.3\"\n")
		if err == nil || !strings.Contains(err.Error(), "server.ops_tls.inherit is true") || !strings.Contains(err.Error(), "min_version") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-035 a plaintext ops listener beside a TLS client listener is refused", func(t *testing.T) {
		_, err := load("server:\n  tls:\n    certificates:\n" + pair + "  ops_tls:\n    inherit: false\n")
		if err == nil || !strings.Contains(err.Error(), "server.ops_tls.certificates is empty") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-020 the TLS defaults in the tags equal the exported constants", func(t *testing.T) {
		for _, path := range []string{"server.tls.min_version", "server.ops_tls.min_version"} {
			if k, _ := MustDescribe().ByPath(path); k.Default != DefaultTLSMinVersion {
				t.Errorf("%s default %q, want %q", path, k.Default, DefaultTLSMinVersion)
			}
		}
		for _, path := range []string{"server.tls.reload_interval", "server.ops_tls.reload_interval"} {
			k, _ := MustDescribe().ByPath(path)
			if d, _ := time.ParseDuration(k.Default); d != DefaultTLSReloadInterval {
				t.Errorf("%s default %q, want %s", path, k.Default, DefaultTLSReloadInterval)
			}
		}
	})

	t.Run("R-ARCH-020 the effective view shows the pairs with their configuration names", func(t *testing.T) {
		l, err := load("server:\n  tls:\n    certificates:\n" + pair)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		if err := l.WriteTable(&b); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), `[{"cert_file":"/tls/tls.crt","key_file":"/tls/tls.key"}]`) {
			t.Errorf("table:\n%s", b.String())
		}
	})
}

func TestStructLists(t *testing.T) {
	t.Run("R-ARCH-020 a list element with a nested type is refused by Describe", func(t *testing.T) {
		type elem struct {
			Name  string   `yaml:"name"`
			Inner []string `yaml:"inner"`
		}
		type root struct {
			Items []elem `yaml:"items" default:"[]"`
		}
		if _, errs := Describe(reflect.TypeOf(root{})); len(errs) == 0 || !strings.Contains(errs[0].Error(), "scalars only") {
			t.Fatalf("errs = %v", errs)
		}
	})
}
