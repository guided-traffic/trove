package config

import (
	"reflect"
	"strings"
	"testing"
)

const customersYAML = `server:
  base_url: https://trove.example.com
  endpoints:
    customers:
      listen: ":8443"
      base_url: https://packages.example.com
`

func loadEndpoints(t *testing.T, file string, env ...string) (*Loaded, error) {
	t.Helper()
	return Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: minimalEnv(env...), Command: CommandAdmin})
}

func TestEndpoints(t *testing.T) {
	t.Run("R-EP-001 without endpoints only the default endpoint exists", func(t *testing.T) {
		l, err := loadEndpoints(t, "")
		if err != nil {
			t.Fatal(err)
		}
		eps := l.Config.Server.ResolveEndpoints()
		if len(eps) != 1 || !eps[0].Default || eps[0].Name != "default" || eps[0].CheckHost {
			t.Fatalf("endpoints = %+v", eps)
		}
		if got := eps[0].Surface; !reflect.DeepEqual(got, Surfaces()) {
			t.Errorf("default surface = %v", got)
		}
		if !eps[0].Anonymous || eps[0].BoundTokensOnly || eps[0].Exposure != ExposureAll || !eps[0].CacheFill {
			t.Errorf("default endpoint policy = %+v", eps[0])
		}
	})

	t.Run("R-EP-001 a further endpoint takes the defaults of an outside audience", func(t *testing.T) {
		l, err := loadEndpoints(t, customersYAML)
		if err != nil {
			t.Fatal(err)
		}
		eps := l.Config.Server.ResolveEndpoints()
		if len(eps) != 2 || eps[1].Name != "customers" {
			t.Fatalf("endpoints = %+v", eps)
		}
		c := eps[1]
		if !reflect.DeepEqual(c.Surface, []string{"packages"}) || !reflect.DeepEqual(c.TokenTypes, []string{"sat"}) ||
			!c.BoundTokensOnly || c.Anonymous || c.Exposure != ExposureRules || c.CacheFill ||
			c.RateLimitClass != "external" || c.ReplicaHeader != "never" || c.MaxInFlight != 1024 || !c.CheckHost {
			t.Errorf("defaults = %+v", c)
		}
		if !reflect.DeepEqual(c.Hosts, []string{"packages.example.com"}) {
			t.Errorf("hosts = %v", c.Hosts)
		}
		// Finalize wrote the defaults into the entry, so the effective view shows them.
		got := l.Config.Server.Endpoints["customers"]
		if got.MaxInFlight != 1024 || got.Exposure != "rules" || got.Auth.Anonymous == nil || *got.Auth.Anonymous {
			t.Errorf("effective entry = %+v", got)
		}
	})

	t.Run("R-EP-001 the list arrives through the environment as JSON", func(t *testing.T) {
		l, err := loadEndpoints(t, "", `TROVE_SERVER_ENDPOINTS={"customers":{"listen":":8443","base_url":"https://packages.example.com"}}`)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := l.Config.Server.Endpoints["customers"]; !ok {
			t.Fatalf("endpoints = %+v", l.Config.Server.Endpoints)
		}
	})

	t.Run("R-EP-001 the default entry sets only the keys it may", func(t *testing.T) {
		_, err := loadEndpoints(t, "server:\n  endpoints:\n    default:\n      listen: \":9000\"\n      base_url: https://x.example.com\n      pool: ep-x\n")
		if err == nil {
			t.Fatal("no error")
		}
		for _, want := range []string{
			"server.endpoints.default.listen is not allowed on the default endpoint; it is owned by server.listen",
			"server.endpoints.default.base_url", "owned by server.base_url", "server.endpoints.default.pool",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("missing %q in %v", want, err)
			}
		}
		l, err := loadEndpoints(t, "server:\n  endpoints:\n    default:\n      hosts: [trove.example.com]\n      replica_header: always\n")
		if err != nil {
			t.Fatal(err)
		}
		d := l.Config.Server.ResolveEndpoints()[0]
		if !d.CheckHost || d.ReplicaHeader != "always" || !d.Anonymous {
			t.Errorf("default = %+v", d)
		}
	})

	t.Run("R-EP-001 a further endpoint needs listen and base_url", func(t *testing.T) {
		_, err := loadEndpoints(t, "server:\n  endpoints:\n    customers: {}\n")
		if err == nil || !strings.Contains(err.Error(), "server.endpoints.customers.listen is required") ||
			!strings.Contains(err.Error(), "server.endpoints.customers.base_url is required") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-EP-001 a port shared with a listener names both keys", func(t *testing.T) {
		_, err := loadEndpoints(t, "server:\n  endpoints:\n    customers:\n      listen: \":8080\"\n      base_url: https://p.example.com\n    other:\n      listen: \":8081\"\n      base_url: https://o.example.com\n")
		if err == nil || !strings.Contains(err.Error(), "server.endpoints.customers.listen uses port 8080, which server.listen already uses") ||
			!strings.Contains(err.Error(), "server.endpoints.other.listen uses port 8081, which server.ops_listen already uses") {
			t.Fatalf("err = %v", err)
		}
		_, err = loadEndpoints(t, "server:\n  endpoints:\n    a:\n      listen: \":9000\"\n      base_url: https://a.example.com\n    b:\n      listen: \"127.0.0.1:9000\"\n      base_url: https://b.example.com\n")
		if err == nil || !strings.Contains(err.Error(), "server.endpoints.b.listen uses port 9000, which server.endpoints.a.listen already uses") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-EP-001 names are bounded in syntax and count", func(t *testing.T) {
		_, err := loadEndpoints(t, "server:\n  endpoints:\n    Customers:\n      listen: \":8443\"\n      base_url: https://p.example.com\n")
		if err == nil || !strings.Contains(err.Error(), "server.endpoints.Customers name must match") {
			t.Fatalf("err = %v", err)
		}
		var b strings.Builder
		b.WriteString("server:\n  endpoints:\n")
		for i := 0; i < 17; i++ {
			n := string(rune('a'+i)) + "x"
			b.WriteString("    " + n + ":\n      listen: \":" + strings.Repeat("9", 1) + string(rune('0'+i/10)) + string(rune('0'+i%10)) + "00\"\n      base_url: https://" + n + ".example.com\n")
		}
		_, err = loadEndpoints(t, b.String())
		if err == nil || !strings.Contains(err.Error(), "server.endpoints has 17 further endpoints; at most 16") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-EP-001 hosts may not be shared between endpoints", func(t *testing.T) {
		_, err := loadEndpoints(t, customersYAML+`    partners:
      listen: ":8444"
      base_url: https://partners.example.com
      hosts: [packages.example.com]
`)
		if err == nil || !strings.Contains(err.Error(), `host "packages.example.com" is also a host of endpoint customers`) {
			t.Fatalf("err = %v", err)
		}
		_, err = loadEndpoints(t, customersYAML+`    partners:
      listen: ":8444"
      base_url: https://partners.example.com
      wildcard_host: "*.example.com"
`)
		if err == nil || !strings.Contains(err.Error(), `"*.example.com" matches host`) {
			t.Fatalf("err = %v", err)
		}
		_, err = loadEndpoints(t, `server:
  endpoints:
    a:
      listen: ":8443"
      base_url: https://a.example.com
      wildcard_host: "*.p.example.com"
    b:
      listen: ":8444"
      base_url: https://b.example.com
      wildcard_host: "*.p.example.com"
`)
		if err == nil || !strings.Contains(err.Error(), `is also the wildcard host of endpoint a`) {
			t.Fatalf("err = %v", err)
		}
		_, err = loadEndpoints(t, "server:\n  base_url: https://trove.example.com\n  content_base_url: https://content.example.com\n  endpoints:\n    c:\n      listen: \":8443\"\n      base_url: https://content.example.com\n")
		if err == nil || !strings.Contains(err.Error(), "server.content_base_url") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-EP-003 the wildcard host is one label under a domain of two labels", func(t *testing.T) {
		for _, tc := range []struct {
			wild, host, label string
			ok                bool
		}{
			{"*.registry.example.com", "docker-all.registry.example.com", "docker-all", true},
			{"*.registry.example.com", "registry.example.com", "", false},
			{"*.registry.example.com", "a.b.registry.example.com", "", false},
			{"*.registry.example.com", "evilregistry.example.com", "", false},
			{"", "x.example.com", "", false},
		} {
			label, ok := WildcardLabel(tc.wild, tc.host)
			if label != tc.label || ok != tc.ok {
				t.Errorf("WildcardLabel(%q,%q) = %q,%v", tc.wild, tc.host, label, ok)
			}
		}
		for _, bad := range []string{"*.com", "registry.example.com", "*registry.example.com", "*.*.example.com"} {
			_, err := loadEndpoints(t, "server:\n  endpoints:\n    c:\n      listen: \":8443\"\n      base_url: https://p.example.org\n      wildcard_host: \""+bad+"\"\n")
			if err == nil || !strings.Contains(err.Error(), "server.endpoints.c.wildcard_host") {
				t.Errorf("%q: err = %v", bad, err)
			}
		}
	})

	t.Run("R-EP-003 hosts are DNS names without a port", func(t *testing.T) {
		for _, bad := range []string{"10.0.0.5", "host:8443", "pkgs.example.org/x", "-bad.example.com"} {
			_, err := loadEndpoints(t, customersYAML+"      hosts: ["+`"`+bad+`"`+"]\n")
			if err == nil || !strings.Contains(err.Error(), "server.endpoints.customers.hosts") {
				t.Errorf("%q: err = %v", bad, err)
			}
		}
		l, err := loadEndpoints(t, customersYAML+"      hosts: [PKGS.Example.org]\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Config.Server.ResolveEndpoints()[1].Hosts; !reflect.DeepEqual(got, []string{"packages.example.com", "pkgs.example.org"}) {
			t.Errorf("hosts = %v", got)
		}
	})

	t.Run("R-EP-003 the default endpoint host set holds the additional base URLs", func(t *testing.T) {
		l, err := loadEndpoints(t, "server:\n  base_url: https://trove.example.com\n  additional_base_urls: [https://trove.internal:8443]\n  endpoints:\n    default:\n      hosts: [alias.example.com]\n")
		if err != nil {
			t.Fatal(err)
		}
		d := l.Config.Server.ResolveEndpoints()[0]
		if !reflect.DeepEqual(d.Hosts, []string{"trove.example.com", "alias.example.com", "trove.internal"}) {
			t.Errorf("hosts = %v", d.Hosts)
		}
	})

	t.Run("R-EP-005 surface rules", func(t *testing.T) {
		_, err := loadEndpoints(t, customersYAML+"      surface: [ui, packages]\n")
		if err == nil || !strings.Contains(err.Error(), "server.endpoints.customers.surface") || !strings.Contains(err.Error(), "surface_not_supported") {
			t.Errorf("ui: %v", err)
		}
		_, err = loadEndpoints(t, customersYAML+"      surface: []\n")
		if err == nil || !strings.Contains(err.Error(), "server.endpoints.customers.surface is empty") {
			t.Errorf("empty: %v", err)
		}
		_, err = loadEndpoints(t, customersYAML+"      surface: [packages, nope]\n")
		if err == nil || !strings.Contains(err.Error(), `"nope" is not a route family`) {
			t.Errorf("unknown: %v", err)
		}
		l, err := loadEndpoints(t, customersYAML+"      surface: [packages, api, files]\n")
		if err != nil {
			t.Fatal(err)
		}
		if c := l.Config.Server.ResolveEndpoints()[1]; !c.Serves("api") || c.Serves("ci_exchange") {
			t.Errorf("surface = %v", c.Surface)
		}
	})

	t.Run("R-EP-001 a rate limit class must exist", func(t *testing.T) {
		_, err := loadEndpoints(t, customersYAML+"      rate_limit_class: nope\n")
		if err == nil || !strings.Contains(err.Error(), `server.endpoints.customers.rate_limit_class "nope" names no entry of server.endpoint_rate_limits`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-EP-001 a pool name follows the endpoint name syntax", func(t *testing.T) {
		_, err := loadEndpoints(t, customersYAML+"      pool: Bad_Pool\n")
		if err == nil || !strings.Contains(err.Error(), "server.endpoints.customers.pool") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-EP-006 host-root bindings are validated and reserved", func(t *testing.T) {
		l, err := loadEndpoints(t, customersYAML+"      hosts: [mirror.example.com]\n      host_roots: {mirror.example.com: docker-hub}\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Config.Server.ResolveEndpoints()[1].HostRoots; got["mirror.example.com"] != "docker-hub" {
			t.Errorf("host_roots = %v", got)
		}
		_, err = loadEndpoints(t, customersYAML+"      host_roots: {other.example.com: docker-hub}\n")
		if err == nil || !strings.Contains(err.Error(), `server.endpoints.customers.host_roots "other.example.com" is not an exact host`) {
			t.Errorf("foreign host: %v", err)
		}
		_, err = loadEndpoints(t, customersYAML+"      host_roots: {packages.example.com: Bad_Key}\n")
		if err == nil || !strings.Contains(err.Error(), "not a repository key") {
			t.Errorf("bad key: %v", err)
		}
		_, err = loadEndpoints(t, customersYAML+"      wildcard_root: label\n")
		if err == nil || !strings.Contains(err.Error(), "server.endpoints.customers.wildcard_root is label, which needs a wildcard_host") {
			t.Errorf("label without wildcard: %v", err)
		}
		if _, err = loadEndpoints(t, customersYAML+"      wildcard_host: \"*.registry.example.com\"\n      wildcard_root: label\n"); err != nil {
			t.Errorf("label with wildcard: %v", err)
		}
	})

	t.Run("R-EP-004 the TLS block of a further endpoint", func(t *testing.T) {
		l, err := loadEndpoints(t, "server:\n  tls:\n    certificates: [{cert_file: /d.crt, key_file: /d.key}]\n    client_ca_file: /proxy-ca.pem\n    client_auth: require\n  ops_tls: {inherit: true}\n"+
			"  endpoints:\n    customers:\n      listen: \":8443\"\n      base_url: https://packages.example.com\n")
		if err != nil {
			t.Fatal(err)
		}
		tl := l.Config.Server.ResolveEndpoints()[1].TLS
		if len(tl.Certificates) != 1 || tl.Certificates[0].CertFile != "/d.crt" || tl.ClientCAFile != "" || tl.ClientAuth != ClientAuthNone {
			t.Errorf("inherited block = %+v", tl)
		}
		l, err = loadEndpoints(t, "server:\n  tls:\n    certificates: [{cert_file: /d.crt, key_file: /d.key}]\n  endpoints:\n    customers:\n      listen: \":8443\"\n      base_url: https://packages.example.com\n      tls:\n        certificates:\n          - {cert_file: /a.crt, key_file: /a.key}\n          - {cert_file: /w.crt, key_file: /w.key}\n        client_ca_file: /ca.pem\n")
		if err != nil {
			t.Fatal(err)
		}
		tl = l.Config.Server.ResolveEndpoints()[1].TLS
		if len(tl.Certificates) != 2 || tl.ClientCAFile != "/ca.pem" || tl.ClientAuth != ClientAuthRequire {
			t.Errorf("own block = %+v", tl)
		}
		_, err = loadEndpoints(t, customersYAML+"      tls:\n        client_ca_file: /ca.pem\n")
		if err == nil || !strings.Contains(err.Error(), "server.endpoints.customers.tls.certificates") {
			t.Errorf("client CA without TLS: %v", err)
		}
	})

	t.Run("R-EP-001 every violation is reported in one pass", func(t *testing.T) {
		_, err := loadEndpoints(t, "server:\n  endpoints:\n    a:\n      listen: \":8080\"\n    b:\n      surface: [ui]\n      listen: \"x\"\n      base_url: https://b.example.com/path\n")
		if err == nil {
			t.Fatal("no error")
		}
		for _, want := range []string{"endpoints.a.listen uses port 8080", "endpoints.a.base_url is required", "endpoints.b.surface", "endpoints.b.listen", "endpoints.b.base_url"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("missing %q in %v", want, err)
			}
		}
	})
}
