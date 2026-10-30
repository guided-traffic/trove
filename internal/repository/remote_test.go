package repository

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

func intp(n int) *int { return &n }

// remoteCatalog is a catalog with a remote-capable format that proposes what the
// npm format proposes: a well-known upstream, an extra host and the cache periods.
func remoteCatalog() *catalog.Registry {
	r := testCatalog()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "npmlike", Classes: []Class{Local, Remote, Virtual}, Layout: "npmlike", ProtocolBasePath: "/api/npmlike/{key}/",
		PatternSubject: SubjectName, PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
		DefaultUpstreamURL: "https://registry.example.org/", DefaultExtraHosts: []string{"*.cdn.example.org"},
		DefaultMetadataTTLSeconds: intp(7200), DefaultNotFoundTTLSeconds: intp(1800), ProbePath: "-/ping",
	}, Names: lower{}})
	return r
}

// validateRemoteDoc decodes and validates a remote repository document of the npmlike format.
func validateRemoteDoc(t *testing.T, remote string) (Document, *validation) {
	t.Helper()
	body := `{"key":"npm-remote","class":"remote","format":"npmlike"`
	if remote != "" {
		body += `,"remote":` + remote
	}
	d, err := DecodeDocument([]byte(body + `}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, v := validate(remoteCatalog(), &d, true)
	return d, v
}

func settingsOf(t *testing.T, d Document) RemoteSettings {
	t.Helper()
	s, err := parseStoredRemote(d.Remote)
	if err != nil {
		t.Fatalf("the canonical section does not load: %v (%s)", err, d.Remote)
	}
	return *s
}

func TestRemoteDefaults(t *testing.T) {
	t.Run("R-REPO-024 a remote created with only url reads back every member with its default", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{"url":"https://registry.example.com/"}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		want := RemoteSettings{
			URL: "https://registry.example.com/", ExtraHosts: []ExtraHost{}, AuthMode: "none", Headers: []Header{},
			OutboundProxy: OutboundProxy{Mode: "default"}, ConnectTimeoutMs: 10000, ReadTimeoutMs: 15000, CacheFillAction: "read",
			Cache:        RemoteCache{StoreArtifacts: true, MetadataTTLSeconds: 7200, MetadataRefreshTimeoutSeconds: 60, NotFoundTTLSeconds: 1800},
			Integrity:    RemoteIntegrity{ChecksumPolicy: "verify-if-available", BlockUnexpectedContentTypes: true, AllowedContentTypes: []string{}},
			Availability: Availability{Mode: "online", FailureThreshold: 5, CircuitOpenSeconds: 300},
			Limits:       RemoteLimits{MaxConcurrentFetches: 64},
		}
		if got := settingsOf(t, d); !reflect.DeepEqual(got, want) {
			t.Fatalf("settings =\n%+v\nwant\n%+v", got, want)
		}
		// Every member of the table is in the stored form, also the ones that are null.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(d.Remote, &raw); err != nil {
			t.Fatal(err)
		}
		for _, m := range []string{
			"url", "extraHosts", "authMode", "username", "password", "clientCertificate", "headers",
			"forwardAuthOnRedirect", "outboundProxy", "connectTimeoutMs", "readTimeoutMs", "sourceNetwork", "cacheFillAction",
			"cache", "integrity", "availability", "limits",
		} {
			if _, ok := raw[m]; !ok {
				t.Errorf("member %s is missing from %s", m, d.Remote)
			}
		}
	})

	t.Run("R-REPO-024 an omitted url takes the upstream of the format, with its extra hosts and periods", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		s := settingsOf(t, d)
		if s.URL != "https://registry.example.org/" || !reflect.DeepEqual(s.ExtraHosts, []ExtraHost{{Pattern: "*.cdn.example.org"}}) {
			t.Errorf("url = %q, extraHosts = %+v", s.URL, s.ExtraHosts)
		}
		if s.Cache.MetadataTTLSeconds != 7200 || s.Cache.NotFoundTTLSeconds != 1800 {
			t.Errorf("cache = %+v", s.Cache)
		}
	})

	t.Run("R-REPO-024 the extra hosts of the format are proposed for its upstream only", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{"url":"https://npm.partner.example/"}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		if got := settingsOf(t, d).ExtraHosts; len(got) != 0 {
			t.Errorf("extraHosts = %+v for another upstream", got)
		}
		d, v = validateRemoteDoc(t, `{"extraHosts":[]}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		if got := settingsOf(t, d).ExtraHosts; len(got) != 0 {
			t.Errorf("an explicit empty list gave %+v", got)
		}
	})

	t.Run("R-REPO-024 a format without a url default needs one", func(t *testing.T) {
		d, err := DecodeDocument([]byte(`{"key":"pk-remote","class":"remote","format":"pkgfmt"}`))
		if err != nil {
			t.Fatal(err)
		}
		_, v := validate(remoteCatalog(), &d, true)
		if got := fieldsOf(v.err())["/remote/url"]; got != "invalid_upstream_url" {
			t.Fatalf("code = %q", got)
		}
	})

	t.Run("R-REPO-024 the stored section is canonical: a second pass changes nothing", func(t *testing.T) {
		d, _ := validateRemoteDoc(t, `{"url":"HTTPS://Registry.Example.COM:443","cache":{"metadataTtlSeconds":60}}`)
		first := string(d.Remote)
		d2, err := DecodeDocument([]byte(`{"key":"npm-remote","class":"remote","format":"npmlike","remote":` + first + `}`))
		if err != nil {
			t.Fatal(err)
		}
		_, v := validate(remoteCatalog(), &d2, true)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		if string(d2.Remote) != first {
			t.Fatalf("second pass:\n%s\nfirst:\n%s", d2.Remote, first)
		}
	})
}

func TestRemoteURL(t *testing.T) {
	good := map[string]string{
		"HTTPS://Registry.Example.COM:443":          "https://registry.example.com/",
		"http://registry.example.com:80/npm/":       "http://registry.example.com/npm/",
		"https://registry.example.com:8443/a/b?c=d": "https://registry.example.com:8443/a/b?c=d",
		"https://[::1]:443/":                        "https://[::1]/",
		"https://nuget.example.com/v3/index.json":   "https://nuget.example.com/v3/index.json",
	}
	for in, want := range good {
		t.Run("R-REPO-024 normalizes "+in, func(t *testing.T) {
			got, why := normalizeUpstreamURL(in)
			if why != "" || got != want {
				t.Fatalf("got %q (%s), want %q", got, why, want)
			}
		})
	}
	for _, in := range []string{
		"https://user:pw@registry.example.com/", "https://user@registry.example.com/", "https://registry.example.com/#frag",
		"ftp://registry.example.com/", "registry.example.com", "/relative", "https:///nohost", "", "http://exa mple.com/",
		"https://registry.example.com/" + strings.Repeat("a", 2100),
	} {
		t.Run("R-REPO-008 refuses "+in[:min(len(in), 40)], func(t *testing.T) {
			if _, why := normalizeUpstreamURL(in); why == "" {
				t.Fatal("accepted")
			}
		})
	}
	t.Run("R-REPO-008 a url with user info answers invalid_upstream_url", func(t *testing.T) {
		_, v := validateRemoteDoc(t, `{"url":"https://user:pw@registry.example.com/"}`)
		if got := fieldsOf(v.err())["/remote/url"]; got != "invalid_upstream_url" {
			t.Fatalf("code = %q", got)
		}
		if strings.Contains(v.err().Error(), "pw@") {
			t.Fatal("the error text repeats the credential")
		}
	})
}

func TestRemoteRanges(t *testing.T) {
	cases := []struct {
		pointer, remote string
	}{
		{"/remote/connectTimeoutMs", `{"connectTimeoutMs":50}`},
		{"/remote/connectTimeoutMs", `{"connectTimeoutMs":120001}`},
		{"/remote/readTimeoutMs", `{"readTimeoutMs":999}`},
		{"/remote/readTimeoutMs", `{"readTimeoutMs":600001}`},
		{"/remote/cache/metadataTtlSeconds", `{"cache":{"metadataTtlSeconds":2592001}}`},
		{"/remote/cache/metadataTtlSeconds", `{"cache":{"metadataTtlSeconds":-1}}`},
		{"/remote/cache/metadataRefreshTimeoutSeconds", `{"cache":{"metadataRefreshTimeoutSeconds":0}}`},
		{"/remote/cache/metadataRefreshTimeoutSeconds", `{"cache":{"metadataRefreshTimeoutSeconds":601}}`},
		{"/remote/cache/notFoundTtlSeconds", `{"cache":{"notFoundTtlSeconds":86401}}`},
		{"/remote/cache/maxStaleSeconds", `{"cache":{"maxStaleSeconds":31536001}}`},
		{"/remote/cache/unusedRetentionHours", `{"cache":{"unusedRetentionHours":87601}}`},
		{"/remote/availability/failureThreshold", `{"availability":{"failureThreshold":0}}`},
		{"/remote/availability/failureThreshold", `{"availability":{"failureThreshold":101}}`},
		{"/remote/availability/circuitOpenSeconds", `{"availability":{"circuitOpenSeconds":86401}}`},
		{"/remote/limits/maxConcurrentFetches", `{"limits":{"maxConcurrentFetches":0}}`},
		{"/remote/limits/maxConcurrentFetches", `{"limits":{"maxConcurrentFetches":4097}}`},
		{"/remote/limits/maxArtifactBytes", `{"limits":{"maxArtifactBytes":0}}`},
		{"/remote/limits/maxMetadataBytes", `{"limits":{"maxMetadataBytes":-5}}`},
	}
	for _, c := range cases {
		t.Run("R-REPO-024 R-REPO-008 "+c.pointer+" out of range in "+c.remote, func(t *testing.T) {
			_, v := validateRemoteDoc(t, c.remote)
			if got := fieldsOf(v.err())[c.pointer]; got != "out_of_range" {
				t.Fatalf("code = %q (%v)", got, v.err())
			}
		})
	}
	t.Run("R-REPO-024 the bounds themselves are accepted", func(t *testing.T) {
		for _, remote := range []string{
			`{"connectTimeoutMs":1000,"readTimeoutMs":600000}`, `{"connectTimeoutMs":120000,"readTimeoutMs":1000}`,
			`{"cache":{"metadataTtlSeconds":0,"metadataRefreshTimeoutSeconds":1,"notFoundTtlSeconds":86400,"maxStaleSeconds":31536000}}`,
			`{"availability":{"failureThreshold":100,"circuitOpenSeconds":0},"limits":{"maxConcurrentFetches":4096,"maxArtifactBytes":1}}`,
		} {
			if _, v := validateRemoteDoc(t, remote); v.err() != nil {
				t.Errorf("%s: %v", remote, v.err())
			}
		}
	})
	t.Run("R-REPO-008 one pass reports every violation", func(t *testing.T) {
		_, v := validateRemoteDoc(t, `{"connectTimeoutMs":1,"readTimeoutMs":1,"authMode":"digest","cacheFillAction":"write"}`)
		got := fieldsOf(v.err())
		if len(got) != 4 || got["/remote/authMode"] != "invalid_value" || got["/remote/cacheFillAction"] != "invalid_value" {
			t.Fatalf("fields = %v", got)
		}
	})
}

func TestRemoteEnumerations(t *testing.T) {
	for _, c := range []struct{ pointer, remote string }{
		{"/remote/integrity/checksumPolicy", `{"integrity":{"checksumPolicy":"trust"}}`},
		{"/remote/availability/mode", `{"availability":{"mode":"sleeping"}}`},
		{"/remote/integrity/allowedContentTypes/0", `{"integrity":{"allowedContentTypes":["Text/HTML"]}}`},
		{"/remote/integrity/allowedContentTypes/1", `{"integrity":{"allowedContentTypes":["text/plain","text/html; charset=utf-8"]}}`},
		{"/remote/extraHosts/0/pattern", `{"extraHosts":[{"pattern":"https://cdn.example.org"}]}`},
		{"/remote/extraHosts/1/pattern", `{"extraHosts":[{"pattern":"a.example"},{"pattern":"[a"}]}`},
		{"/remote/outboundProxy/mode", `{"outboundProxy":{"mode":"pac"}}`},
		{"/remote/outboundProxy/name", `{"outboundProxy":{"mode":"direct","name":"x"}}`},
		{"/remote/username", `{"authMode":"basic","username":"a:b","password":{"value":"x"}}`},
	} {
		t.Run("R-REPO-008 "+c.pointer, func(t *testing.T) {
			_, v := validateRemoteDoc(t, c.remote)
			if got := fieldsOf(v.err())[c.pointer]; got == "" {
				t.Fatalf("no violation at %s: %v", c.pointer, fieldsOf(v.err()))
			}
		})
	}
	t.Run("R-REPO-024 max 20 extra hosts, 20 content types", func(t *testing.T) {
		hosts := make([]string, 21)
		types := make([]string, 21)
		for i := range hosts {
			hosts[i] = `{"pattern":"h` + string(rune('a'+i)) + `.example"}`
			types[i] = `"text/x` + string(rune('a'+i)) + `"`
		}
		_, v := validateRemoteDoc(t, `{"extraHosts":[`+strings.Join(hosts, ",")+`],"integrity":{"allowedContentTypes":[`+strings.Join(types, ",")+`]}}`)
		got := fieldsOf(v.err())
		if got["/remote/extraHosts"] != "out_of_range" || got["/remote/integrity/allowedContentTypes"] != "out_of_range" {
			t.Fatalf("fields = %v", got)
		}
	})
	t.Run("R-REPO-024 extra host patterns are stored in lower case", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{"extraHosts":[{"pattern":"*.CDN.Example.org","sendCredentials":true}]}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		if got := settingsOf(t, d).ExtraHosts; !reflect.DeepEqual(got, []ExtraHost{{Pattern: "*.cdn.example.org", SendCredentials: true}}) {
			t.Fatalf("extraHosts = %+v", got)
		}
	})
}

func TestRemoteWarnings(t *testing.T) {
	warnings := func(v *validation) map[string]string {
		out := map[string]string{}
		for _, w := range v.warnings {
			out[w.Code] = w.Pointer
		}
		return out
	}
	t.Run("R-REPO-008 R-REPO-024 a plain http upstream is saved with plain_http_upstream", func(t *testing.T) {
		_, v := validateRemoteDoc(t, `{"url":"http://registry.internal.example/"}`)
		if v.err() != nil || warnings(v)["plain_http_upstream"] != "/remote/url" {
			t.Fatalf("err = %v, warnings = %+v", v.err(), v.warnings)
		}
		if _, v := validateRemoteDoc(t, `{"url":"https://registry.internal.example/"}`); len(v.warnings) != 0 {
			t.Fatalf("an https upstream warned: %+v", v.warnings)
		}
	})
	t.Run("R-REPO-024 R-REPO-008 a prefix of one address is stored with source_network_single_address", func(t *testing.T) {
		for _, p := range []string{"10.99.0.11/32", "fd00::11/128"} {
			d, v := validateRemoteDoc(t, `{"sourceNetwork":"`+p+`"}`)
			if v.err() != nil || warnings(v)["source_network_single_address"] != "/remote/sourceNetwork" {
				t.Fatalf("%s: err = %v, warnings = %+v", p, v.err(), v.warnings)
			}
			if got := settingsOf(t, d).SourceNetwork; got == nil || got.String() != p {
				t.Fatalf("%s stored as %v", p, got)
			}
		}
		if _, v := validateRemoteDoc(t, `{"sourceNetwork":"10.99.0.0/24"}`); len(v.warnings) != 0 {
			t.Fatalf("a /24 warned: %+v", v.warnings)
		}
	})
	t.Run("R-REPO-024 a source network is stored as its network", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{"sourceNetwork":"10.99.0.77/24"}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		if got := settingsOf(t, d).SourceNetwork; got == nil || *got != netip.MustParsePrefix("10.99.0.0/24") {
			t.Fatalf("sourceNetwork = %v", got)
		}
	})
	t.Run("R-REPO-024 R-REPO-008 an address without a length answers invalid_source_network", func(t *testing.T) {
		for _, p := range []string{"10.99.0.11", "10.99.0.0/33", "nonsense", "10.99.0.0/24%eth0"} {
			_, v := validateRemoteDoc(t, `{"sourceNetwork":"`+p+`"}`)
			if got := fieldsOf(v.err())["/remote/sourceNetwork"]; got != "invalid_source_network" {
				t.Errorf("%s: code = %q", p, got)
			}
		}
	})
}

func TestRemoteNotApplicable(t *testing.T) {
	for _, c := range []struct{ pointer, remote string }{
		{"/remote/headers", `{"headers":[{"name":"X-Api-Key","value":{"value":"k"},"sensitive":true}]}`},
		{"/remote/clientCertificate", `{"clientCertificate":"partner-mtls"}`},
		{"/remote/outboundProxy", `{"outboundProxy":{"mode":"named","name":"corp"}}`},
		{"/remote/cache/storeArtifacts", `{"cache":{"storeArtifacts":false}}`},
		{"/remote/cache/unusedRetentionHours", `{"cache":{"unusedRetentionHours":24}}`},
	} {
		t.Run("R-REPO-008 "+c.pointer+" is refused until it is built", func(t *testing.T) {
			_, v := validateRemoteDoc(t, c.remote)
			if got := fieldsOf(v.err())[c.pointer]; got != errs.CodeNotApplicable {
				t.Fatalf("code = %q (%v)", got, fieldsOf(v.err()))
			}
		})
	}
	t.Run("R-REPO-037 the direct proxy mode is built", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{"outboundProxy":{"mode":"direct"}}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		if got := settingsOf(t, d).OutboundProxy; got.Mode != "direct" {
			t.Fatalf("outboundProxy = %+v", got)
		}
	})
	t.Run("R-REPO-008 the remote section of a local repository is not applicable", func(t *testing.T) {
		d, err := DecodeDocument([]byte(`{"key":"pk-local","class":"local","format":"pkgfmt","remote":{"url":"https://x.example/"}}`))
		if err != nil {
			t.Fatal(err)
		}
		_, v := validate(remoteCatalog(), &d, true)
		if got := fieldsOf(v.err())["/remote"]; got != errs.CodeNotApplicable {
			t.Fatalf("code = %q", got)
		}
	})
}

func TestRemoteShapeIsStrict(t *testing.T) {
	for name, body := range map[string]string{
		"a misspelt member":         `{"key":"npm-remote","class":"remote","format":"npmlike","remote":{"url":"https://x.example/","connectTimeout":5}}`,
		"a misspelt nested member":  `{"key":"npm-remote","class":"remote","format":"npmlike","remote":{"cache":{"ttl":5}}}`,
		"a duplicate member":        `{"key":"npm-remote","class":"remote","format":"npmlike","remote":{"url":"https://x.example/","url":"https://y.example/"}}`,
		"a wrong type":              `{"key":"npm-remote","class":"remote","format":"npmlike","remote":{"connectTimeoutMs":"fast"}}`,
		"a member of a secret":      `{"key":"npm-remote","class":"remote","format":"npmlike","remote":{"password":{"valu":"x"}}}`,
		"a member of an extra host": `{"key":"npm-remote","class":"remote","format":"npmlike","remote":{"extraHosts":[{"host":"x"}]}}`,
		"a fractional number":       `{"key":"npm-remote","class":"remote","format":"npmlike","remote":{"connectTimeoutMs":1500.5}}`,
	} {
		t.Run("R-REPO-008 "+name+" answers 400 malformed_request", func(t *testing.T) {
			_, err := DecodeDocument([]byte(body))
			e, ok := errs.As(err)
			if !ok || e.Status() != 400 || e.Code != errs.CodeInvalid {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestRemoteSecrets(t *testing.T) {
	t.Run("R-REPO-033 the stored document holds the marker, never the value", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{"authMode":"bearer","password":{"value":"s3cr3t-token"}}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		cfg, err := d.marshalConfig()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(cfg), "s3cr3t-token") || strings.Contains(string(d.Remote), "s3cr3t-token") {
			t.Fatalf("the plaintext is in the stored document: %s", cfg)
		}
		if !strings.Contains(string(d.Remote), `"password":{"set":true}`) {
			t.Fatalf("remote = %s", d.Remote)
		}
		if len(d.pending) != 1 || d.pending[0].op != secretSeal || d.pending[0].field != "/remote/password" || d.pending[0].value.Reveal() != "s3cr3t-token" {
			t.Fatalf("pending = %+v", d.pending)
		}
		if strings.Contains(strings.Join([]string{d.pending[0].value.String()}, ""), "s3cr3t") {
			t.Fatal("the pending value prints its plaintext")
		}
	})
	t.Run("R-REPO-033 a document that was read can be sent back: the marker keeps the stored value", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{"authMode":"bearer","password":{"set":true}}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		if len(d.pending) != 1 || d.pending[0].op != secretKeep {
			t.Fatalf("pending = %+v", d.pending)
		}
		if hasSecretWrites(d.pending) {
			t.Fatal("the marker counts as a write")
		}
	})
	t.Run("R-REPO-033 a new value next to the marker of a merge patch replaces the stored one", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{"authMode":"bearer","password":{"set":true,"value":"rotated"}}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		if len(d.pending) != 1 || d.pending[0].op != secretSeal {
			t.Fatalf("pending = %+v", d.pending)
		}
	})
	t.Run("R-REPO-033 a reference is stored as written and read back as itself", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{"authMode":"basic","username":"ci","password":{"secretRef":{"file":"npm-partner-token"}}}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(d.Remote), `"password":{"secretRef":{"file":"npm-partner-token"}}`) {
			t.Fatalf("remote = %s", d.Remote)
		}
		if s := settingsOf(t, d); s.Password == nil || s.Password.Ref == nil || s.Password.Ref.File != "npm-partner-token" || s.Password.Set {
			t.Fatalf("password = %+v", s.Password)
		}
		if len(d.pending) != 1 || d.pending[0].op != secretReference {
			t.Fatalf("pending = %+v", d.pending)
		}
	})
	t.Run("R-REPO-033 an unset password is stored as null", func(t *testing.T) {
		d, _ := validateRemoteDoc(t, `{}`)
		if !strings.Contains(string(d.Remote), `"password":null`) || len(d.pending) != 0 {
			t.Fatalf("remote = %s, pending = %+v", d.Remote, d.pending)
		}
	})

	invalid := map[string]string{
		"both value and secretRef":      `{"authMode":"bearer","password":{"value":"x","secretRef":{"file":"t"}}}`,
		"a trove variable":              `{"authMode":"bearer","password":{"secretRef":{"env":"TROVE_DATABASE_PASSWORD"}}}`,
		"a variable without the prefix": `{"authMode":"bearer","password":{"secretRef":{"env":"HOME"}}}`,
		"a path in the file name":       `{"authMode":"bearer","password":{"secretRef":{"file":"../db-password"}}}`,
		"a nested path":                 `{"authMode":"bearer","password":{"secretRef":{"file":"a/b"}}}`,
		"an absolute path":              `{"authMode":"bearer","password":{"secretRef":{"file":"/etc/passwd"}}}`,
		"a hidden file":                 `{"authMode":"bearer","password":{"secretRef":{"file":".data"}}}`,
		"a reference naming both":       `{"authMode":"bearer","password":{"secretRef":{"file":"a","env":"TROVE_CAC_A"}}}`,
		"a reference naming neither":    `{"authMode":"bearer","password":{"secretRef":{}}}`,
		"an empty value":                `{"authMode":"bearer","password":{"value":""}}`,
		"a value with a line break":     `{"authMode":"bearer","password":{"value":"a\nb"}}`,
		"set false":                     `{"authMode":"bearer","password":{"set":false}}`,
		"an empty object":               `{"authMode":"bearer","password":{}}`,
		"bearer without a password":     `{"authMode":"bearer"}`,
		"basic without anything":        `{"authMode":"basic"}`,
	}
	for name, remote := range invalid {
		t.Run("R-REPO-033 R-REPO-008 "+name+" answers invalid_secret", func(t *testing.T) {
			_, v := validateRemoteDoc(t, remote)
			for p, c := range fieldsOf(v.err()) {
				if strings.HasPrefix(p, "/remote/password") && c == "invalid_secret" {
					return
				}
			}
			t.Fatalf("fields = %v", fieldsOf(v.err()))
		})
	}
	t.Run("R-REPO-033 a TROVE_CAC_ variable and a plain file name are accepted", func(t *testing.T) {
		for _, ref := range []string{`{"env":"TROVE_CAC_NPM_TOKEN"}`, `{"file":"npm-partner-token"}`, `{"file":"token.v2"}`} {
			if _, v := validateRemoteDoc(t, `{"authMode":"bearer","password":{"secretRef":`+ref+`}}`); v.err() != nil {
				t.Errorf("%s: %v", ref, v.err())
			}
		}
	})
	t.Run("R-REPO-033 a value over 8 KiB is out of range", func(t *testing.T) {
		_, v := validateRemoteDoc(t, `{"authMode":"bearer","password":{"value":"`+strings.Repeat("a", 8193)+`"}}`)
		if got := fieldsOf(v.err())["/remote/password/value"]; got != "out_of_range" {
			t.Fatalf("code = %q", got)
		}
	})
	t.Run("R-REPO-033 the plaintext is in no error text", func(t *testing.T) {
		_, v := validateRemoteDoc(t, `{"authMode":"bearer","password":{"value":"hunter2\n","secretRef":{"file":"x"}},"connectTimeoutMs":1}`)
		if err := v.err(); err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("err = %v", err)
		}
		_, err := DecodeDocument([]byte(`{"key":"npm-remote","class":"remote","format":"npmlike","remote":{"password":{"value":123456789}}}`))
		if err == nil || strings.Contains(err.Error(), "123456789") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-REPO-033 the JSON form of a secret field never carries the value", func(t *testing.T) {
		var f SecretField
		if err := json.Unmarshal([]byte(`{"value":"topsecret"}`), &f); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(f)
		if err != nil || strings.Contains(string(b), "topsecret") {
			t.Fatalf("%s %v", b, err)
		}
	})
}

func TestRepositoryRemote(t *testing.T) {
	t.Run("R-REPO-024 the snapshot entry carries the parsed settings of a remote repository", func(t *testing.T) {
		d, v := validateRemoteDoc(t, `{"url":"https://registry.example.com/","authMode":"bearer","password":{"secretRef":{"env":"TROVE_CAC_T"}},"sourceNetwork":"10.99.0.0/24","extraHosts":[{"pattern":"*.cdn.example.com","sendCredentials":true}]}`)
		if err := v.err(); err != nil {
			t.Fatal(err)
		}
		cfg, err := d.marshalConfig()
		if err != nil {
			t.Fatal(err)
		}
		repo, err := toRepository(q.Repository{ID: 7, Key: "npm-remote", Class: "remote", Format: "npmlike", ConfigVersion: 3, Config: cfg}, remoteCatalog())
		if err != nil {
			t.Fatal(err)
		}
		s, ok := repo.Remote()
		if !ok || s.URL != "https://registry.example.com/" || s.AuthMode != "bearer" || s.Password == nil || s.Password.Ref.Env != "TROVE_CAC_T" ||
			s.SourceNetwork == nil || s.SourceNetwork.String() != "10.99.0.0/24" || !s.ExtraHosts[0].SendCredentials {
			t.Fatalf("settings = %+v", s)
		}
	})
	t.Run("R-REPO-024 a repository of another class has no remote settings", func(t *testing.T) {
		d, err := DecodeDocument([]byte(`{"key":"pk-local","class":"local","format":"pkgfmt"}`))
		if err != nil {
			t.Fatal(err)
		}
		cfg, _ := d.marshalConfig()
		repo, err := toRepository(q.Repository{ID: 1, Key: "pk-local", Class: "local", Format: "pkgfmt", Config: cfg}, remoteCatalog())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := repo.Remote(); ok {
			t.Fatal("a local repository has remote settings")
		}
	})
	t.Run("R-REPO-024 a stored section that does not load disables the repository", func(t *testing.T) {
		repo, err := toRepository(q.Repository{
			ID: 1, Key: "npm-remote", Class: "remote", Format: "npmlike",
			Config: []byte(`{"remote":{"url":"https://x.example/","connectTimeoutMs":"fast"},"hideExistence":"inherit"}`),
		}, remoteCatalog())
		if err == nil || !repo.Disabled {
			t.Fatalf("err = %v, disabled = %v", err, repo.Disabled)
		}
	})
}
