package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const bastionMinimal = `
server:
  base_url: https://packages.example.com
bastion:
  main:
    url: https://trove.example.com
    token_file: /run/secrets/token/token
`

func loadBastion(t *testing.T, file string, env ...string) (*BastionLoaded, error) {
	t.Helper()
	return LoadBastion(Options{
		File: "/etc/trove/trove.yaml", Environ: env, Command: CommandServe,
		ReadFile: func(name string) ([]byte, error) {
			if name == "/etc/trove/trove.yaml" {
				return []byte(file), nil
			}
			return nil, os.ErrNotExist
		},
	})
}

func mustLoadBastion(t *testing.T, file string, env ...string) *BastionConfig {
	t.Helper()
	l, err := loadBastion(t, file, env...)
	if err != nil {
		t.Fatalf("LoadBastion: %v", err)
	}
	return l.Config
}

func bastionIssues(t *testing.T, file string, env ...string) string {
	t.Helper()
	_, err := loadBastion(t, file, env...)
	if err == nil {
		t.Fatal("LoadBastion succeeded, want a configuration error")
	}
	return err.Error()
}

// TestR_BAS_001_TheRoleLoadsWithTheMinimalKeysAndTheSpecDefaults: with the keys the acceptance names, the role
// starts, and the listeners, periods and limits are the defaults of the specification.
func TestR_BAS_001_TheRoleLoadsWithTheMinimalKeysAndTheSpecDefaults(t *testing.T) {
	c := mustLoadBastion(t, bastionMinimal)
	b := c.Bastion
	checks := []struct {
		name      string
		got, want any
	}{
		{"server.listen", c.Server.Listen, ":8443"},
		{"server.ops_listen", c.Server.OpsListen, ":8081"},
		{"server.max_in_flight", c.Server.MaxInFlight, 1024},
		{"server.forwarded_header", c.Server.ForwardedHeader, "x-forwarded-for"},
		{"answers.period", b.Answers.Period, 5 * time.Minute},
		{"answers.grace_period", b.Answers.GracePeriod, 15 * time.Minute},
		{"answers.max_entries", b.Answers.MaxEntries, 200000},
		{"feed.poll_interval", b.Feed.PollInterval, 2 * time.Second},
		{"main.max_concurrent", b.Main.MaxConcurrent, 64},
		{"main.anonymous_share", b.Main.AnonymousShare, 0.25},
		{"main.queue_timeout", b.Main.QueueTimeout, 2 * time.Second},
		{"main.max_forwarded_body", b.Main.MaxForwardedBody, 8 * MiB},
		{"main.unreachable_after", b.Main.UnreachableAfter, 10 * time.Second},
		{"main.token_reload_interval", b.Main.TokenReloadInterval, 10 * time.Second},
		{"cache.backend", b.Cache.Backend, "volume"},
		{"cache.max_bytes", b.Cache.MaxBytes, 20 * GiB},
		{"cache.documents_max_bytes", b.Cache.DocumentsMaxBytes, 2 * GiB},
		{"cache.metadata_revalidate_after", b.Cache.MetadataRevalidateAfter, time.Minute},
		{"cache.artifact_revalidate_after", b.Cache.ArtifactRevalidateAfter, 10 * time.Minute},
		{"cache.volume.dir", b.Cache.Volume.Dir, "/var/cache/trove-bastion"},
		{"cache.bucket.prefix", b.Cache.Bucket.Prefix, "trove-bastion"},
		{"cache.bucket.credentials.source", b.Cache.Bucket.Credentials.Source, "static"},
		{"limits.authenticated.rps", b.Limits.Authenticated.RequestsPerSecond, 50},
		{"limits.authenticated.burst", b.Limits.Authenticated.Burst, 200},
		{"limits.anonymous.rps", b.Limits.Anonymous.RequestsPerSecond, 10},
		{"limits.anonymous.burst", b.Limits.Anonymous.Burst, 40},
		{"limits.misses.authenticated", b.Limits.Misses.AuthenticatedPerMinute, 300},
		{"limits.misses.anonymous", b.Limits.Misses.AnonymousPerMinute, 30},
		{"failed_auth.max_failures", b.FailedAuth.MaxFailures, 20},
		{"blocking.threshold", b.Blocking.Threshold, 300},
		{"blocking.max_duration", b.Blocking.MaxDuration, 24 * time.Hour},
		{"audit.buffer_events", b.Audit.BufferEvents, 100000},
		{"audit.spool_max_events", b.Audit.SpoolMaxEvents, 1000000},
		{"token.warn_before", b.Token.WarnBefore, 720 * time.Hour},
		{"replicas", b.Replicas, 1},
		{"valkey.mode", c.Valkey.Mode, "none"},
		{"runtime.memory_limit_ratio", c.Runtime.MemoryLimitRatio, 0.85},
		{"tracing.enabled", c.Tracing.Enabled, false},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
}

// TestR_BAS_001_AKeyOfAnotherSectionStopsStartupNamingKeyAndRole: the acceptance of R-BAS-001.
func TestR_BAS_001_AKeyOfAnotherSectionStopsStartupNamingKeyAndRole(t *testing.T) {
	cases := []struct {
		name, extra, want string
	}{
		{"database.host", "database:\n  host: db\n", "database.host"},
		{"storage", "storage:\n  s3:\n    bucket: b\n", "storage.s3.bucket"},
		{"auth", "auth:\n  anonymous:\n    allowed: true\n", "auth.anonymous.allowed"},
		{"server.endpoints", "server:\n  base_url: https://packages.example.com\n  endpoints:\n    x:\n      listen: ':9000'\n", "server.endpoints.x.listen"},
		{"server.additional_base_urls", "server:\n  base_url: https://packages.example.com\n  additional_base_urls: [https://a.example]\n", "server.additional_base_urls"},
		{"process.role", "process:\n  role: bastion\n", "process.role"},
		{"a flat unknown key", "audit: {}\nci:\n  token:\n    max_ttl: 1h\n", "ci.token.max_ttl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := bastionMinimal
			if strings.HasPrefix(c.extra, "server:") {
				file = strings.Replace(file, "server:\n  base_url: https://packages.example.com\n", "", 1)
			}
			msg := bastionIssues(t, file+c.extra)
			if !strings.Contains(msg, c.want) || !strings.Contains(msg, "the role bastion") {
				t.Fatalf("error does not name %q and the role:\n%s", c.want, msg)
			}
		})
	}
	msg := bastionIssues(t, bastionMinimal, "TROVE_DATABASE_HOST=db")
	if !strings.Contains(msg, "TROVE_DATABASE_HOST") || !strings.Contains(msg, "role bastion") {
		t.Fatalf("an environment variable of another section: %s", msg)
	}
}

// TestR_BAS_044_AnHTTPMainURLStopsStartupNamingTheKey: the connection to the main installation is https only.
func TestR_BAS_044_AnHTTPMainURLStopsStartupNamingTheKey(t *testing.T) {
	msg := bastionIssues(t, strings.Replace(bastionMinimal, "https://trove.example.com", "http://trove.example.com", 1))
	if !strings.Contains(msg, "bastion.main.url") || !strings.Contains(msg, "https") {
		t.Fatalf("error:\n%s", msg)
	}
	msg = bastionIssues(t, strings.Replace(bastionMinimal, "https://trove.example.com", "https://trove.example.com/api", 1))
	if !strings.Contains(msg, "bastion.main.url") {
		t.Fatalf("a URL with a path: %s", msg)
	}
	msg = bastionIssues(t, "bastion:\n  main:\n    token_file: /t\n")
	if !strings.Contains(msg, "server.base_url") || !strings.Contains(msg, "bastion.main.url") {
		t.Fatalf("the required keys: %s", msg)
	}
}

// TestR_BAS_046_TheRangesOfTheSpecificationAreEnforced: the ranges R-BAS-020, R-BAS-026 and R-BAS-027 state.
func TestR_BAS_046_TheRangesOfTheSpecificationAreEnforced(t *testing.T) {
	cases := []struct {
		name, patch, want string
	}{
		{"answers.period below 30s", "answers:\n    period: 29s\n", "bastion.answers.period"},
		{"answers.period above 1h", "answers:\n    period: 61m\n", "bastion.answers.period"},
		{"answers.grace_period above 24h", "answers:\n    grace_period: 25h\n", "bastion.answers.grace_period"},
		{"feed.poll_interval below 1s", "feed:\n    poll_interval: 500ms\n", "bastion.feed.poll_interval"},
		{"feed.poll_interval above 10s", "feed:\n    poll_interval: 11s\n", "bastion.feed.poll_interval"},
		{"unreachable_after under 3 polls", "feed:\n    poll_interval: 5s\n  main:\n    unreachable_after: 10s\n", "bastion.main.unreachable_after"},
		{"anonymous_share above 1", "main:\n    anonymous_share: 1.5\n", "bastion.main.anonymous_share"},
		{"an unknown backend", "cache:\n    backend: disk\n", "bastion.cache.backend"},
		{"documents above the cache", "cache:\n    max_bytes: 1GiB\n", "bastion.cache.documents_max_bytes"},
		{"the bucket backend without a bucket", "cache:\n    backend: bucket\n", "bastion.cache.bucket.bucket"},
		{"a plain http bucket endpoint", "cache:\n    backend: bucket\n    bucket:\n      bucket: b\n      endpoint: http://s3.example.com\n", "bastion.cache.bucket.endpoint"},
		{"anonymous limit above the authenticated one", "limits:\n    anonymous:\n      requests_per_second: 80\n", "bastion.limits.anonymous.requests_per_second"},
		{"an exempt entry that is no address", "blocking:\n    exempt_cidrs: [nope]\n", "bastion.blocking.exempt_cidrs"},
		{"a batch above the buffer", "audit:\n    buffer_events: 100\n", "bastion.audit.batch_size"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file := strings.Replace(bastionMinimal, "bastion:\n", "bastion:\n  "+c.patch, 1)
			if strings.Contains(c.patch, "\n  main:") {
				// the patch carries its own main block: merge it with the minimal one
				file = strings.Replace(bastionMinimal, "bastion:\n  main:\n", "bastion:\n  "+c.patch+"    url: https://trove.example.com\n    token_file: /t\n", 1)
				file = strings.Replace(file, "    url: https://trove.example.com\n    token_file: /run/secrets/token/token\n", "", 1)
			}
			msg := bastionIssues(t, file)
			if !strings.Contains(msg, c.want) {
				t.Fatalf("error does not name %s:\n%s", c.want, msg)
			}
		})
	}
}

// TestR_BAS_046_TheChartManagedKeysAreMarkedInTheSchema: the keys the chart renders carry the marker, and no
// other key does (the chart refuses a value under config for exactly these).
func TestR_BAS_046_TheChartManagedKeysAreMarkedInTheSchema(t *testing.T) {
	want := map[string]bool{
		"server.tls.certificates": true, "server.tls.client_ca_file": true, "server.tls.client_auth": true,
		"server.tls.min_version": true, "server.tls.reload_interval": true,
		"server.ops_tls.inherit": true, "server.ops_tls.certificates": true,
		"bastion.main.token_file": true, "bastion.main.ca_file": true, "bastion.signing_key_file": true,
		"bastion.cache.volume.dir": true, "bastion.cache.bucket.ca_file": true, "bastion.replicas": true,
		"bastion.cache.bucket.credentials.access_key_id_file": true, "bastion.cache.bucket.credentials.secret_access_key_file": true,
		"valkey.username_file": true, "valkey.password_file": true, "valkey.tls.ca_file": true, "tracing.ca_file": true,
	}
	managed := map[string]bool{}
	for _, k := range BastionDescribe().Keys {
		if k.ChartManaged {
			managed[k.Path] = true
		}
	}
	for path := range want {
		if !managed[path] {
			t.Errorf("%s is not marked chart-managed", path)
		}
	}
	for path := range managed {
		if !want[path] && !strings.HasPrefix(path, "server.tls.") && !strings.HasPrefix(path, "server.ops_tls.") {
			t.Errorf("%s is marked chart-managed but is not on the list of R-BAS-046", path)
		}
	}
}

// TestR_BAS_046_TheGeneratedSchemaHoldsExactlyTheKeysOfTheRole: bastion.schema.json is the committed
// rendering of the tree: no section of another role is in it, and the marker is in the JSON.
func TestR_BAS_046_TheGeneratedSchemaHoldsExactlyTheKeysOfTheRole(t *testing.T) {
	raw, err := os.ReadFile("bastion.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	props := doc["properties"].(map[string]any)
	if len(props) != 6 {
		t.Errorf("sections = %v, want server, bastion, valkey, log, tracing, runtime", props)
	}
	for _, s := range []string{"server", "bastion", "valkey", "log", "tracing", "runtime"} {
		if _, ok := props[s]; !ok {
			t.Errorf("section %s is missing", s)
		}
	}
	if doc["$id"] != BastionSchemaID {
		t.Errorf("$id = %v", doc["$id"])
	}
	fresh, err := BastionDescribe().BastionJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != strings.TrimSpace(string(fresh)) {
		t.Error("bastion.schema.json is stale: run go generate ./internal/config")
	}
}

// TestR_BAS_001_ValkeyIsOffByDefaultAndNeedsTheSigningKeyWhenOn: the mode none is the default; Valkey or the
// bucket backend without a signing key stops startup (R-BAS-032, R-BAS-034).
func TestR_BAS_001_ValkeyIsOffByDefaultAndNeedsTheSigningKeyWhenOn(t *testing.T) {
	c := mustLoadBastion(t, bastionMinimal+"valkey:\n  mode: none\n")
	if c.Valkey.Mode != "none" {
		t.Fatalf("mode = %s", c.Valkey.Mode)
	}
	msg := bastionIssues(t, bastionMinimal+"valkey:\n  mode: sentinel\n  sentinel:\n    addresses: [s:26379]\n    primary_name: p\n")
	if !strings.Contains(msg, "bastion.signing_key_file") {
		t.Fatalf("Valkey without a signing key: %s", msg)
	}
	c = mustLoadBastion(t, bastionMinimal+"bastion:\n  signing_key_file: /k\nvalkey:\n  mode: sentinel\n  sentinel:\n    addresses: [s:26379]\n    primary_name: p\n")
	if got := c.Valkey.Valkey().KeyPrefix; got != "trove-bastion:packages.example.com:" {
		t.Fatalf("derived key prefix = %q", got)
	}
	msg = bastionIssues(t, bastionMinimal+"valkey:\n  mode: sentinel\n")
	if !strings.Contains(msg, "valkey.sentinel.addresses") {
		t.Fatalf("a sentinel mode without addresses: %s", msg)
	}
}

// TestR_BAS_042_TracingNeverContinuesACallersContext: the role has no incoming_context key; the equivalent
// main section is fixed to none and propagates.
func TestR_BAS_042_TracingNeverContinuesACallersContext(t *testing.T) {
	c := mustLoadBastion(t, bastionMinimal)
	tr := c.Tracing.Tracing()
	if tr.IncomingContext != "none" || !tr.PropagateToUpstreams || len(tr.TrustedPeers) != 0 {
		t.Fatalf("tracing = %+v", tr)
	}
	if msg := bastionIssues(t, bastionMinimal+"tracing:\n  incoming_context: all\n"); !strings.Contains(msg, "tracing.incoming_context") {
		t.Fatalf("incoming_context: %s", msg)
	}
	if msg := bastionIssues(t, bastionMinimal+"tracing:\n  enabled: true\n"); !strings.Contains(msg, "tracing.endpoint") {
		t.Fatalf("enabled without endpoint: %s", msg)
	}
}

// TestR_BAS_002_TheRoleTakesNoFlagsButLogLevel: every setting is a value of the chart; --log-level is the one
// flag of the serve command that the role honours.
func TestR_BAS_002_TheRoleTakesNoFlagsButLogLevel(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte(bastionMinimal), nil }
	_, err := LoadBastion(Options{Flags: map[string]string{"cache.backend": "bucket"}, File: "/x", ReadFile: read, Command: CommandServe})
	if err == nil || !strings.Contains(err.Error(), "role bastion") {
		t.Fatalf("a configuration flag: %v", err)
	}
	l, err := LoadBastion(Options{Flags: map[string]string{"log.level": "debug"}, File: "/x", ReadFile: read, Command: CommandServe})
	if err != nil || l.Config.Log.Level != "debug" {
		t.Fatalf("--log-level: %v", err)
	}
}

// TestR_BAS_005_BastionsKeysOfTheMainInstallationHaveTheirDefaults: the new keys of the main tree.
func TestR_BAS_005_BastionsKeysOfTheMainInstallationHaveTheirDefaults(t *testing.T) {
	var c Config
	if _, err := loadInto(&c, Options{Environ: minimalEnv(), ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }, Command: CommandMigrate}); err != nil {
		t.Fatal(err)
	}
	if c.Bastions.MaxRegistrations != 16 || c.Bastions.Check.MaxRepositories != 16 || c.Bastions.Feed.Lookback != 5*time.Minute ||
		c.Bastions.Feed.MaxPage != 1000 || c.Bastions.Downloads.MaxBatch != 1000 || c.Bastions.Downloads.MaxAge != 720*time.Hour {
		t.Fatalf("bastions = %+v", c.Bastions)
	}
	if t0 := c.Auth.Tokens.BST; t0.DefaultTTL != 8760*time.Hour || t0.MaxTTL != 9480*time.Hour || c.Auth.Tokens.MaxActive.BST != 3 {
		t.Fatalf("bst tokens = %+v, max active %d", t0, c.Auth.Tokens.MaxActive.BST)
	}
}

// TestR_BAS_005_ABastionTokenIsNoEndpointCredential: bst in auth.token_types stops startup naming the key.
func TestR_BAS_005_ABastionTokenIsNoEndpointCredential(t *testing.T) {
	file := "server:\n  endpoints:\n    default:\n      auth:\n        token_types: [pat, bst]\n"
	_, err := loadInto(&Config{}, Options{
		Environ: minimalEnv(), Command: CommandMigrate, File: "/c.yaml",
		ReadFile: func(string) ([]byte, error) { return []byte(file), nil },
	})
	if err == nil || !strings.Contains(err.Error(), "server.endpoints.default.auth.token_types") || !strings.Contains(err.Error(), "bst") {
		t.Fatalf("error: %v", err)
	}
}
