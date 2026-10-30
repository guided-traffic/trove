package config

import (
	"testing"
	"time"
)

func loadM2(t *testing.T, file string) (*Loaded, error) {
	t.Helper()
	return Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: servableEnv(), Command: CommandServe})
}

func TestProxyDefaults(t *testing.T) {
	l := mustLoad(t, Options{Environ: servableEnv(), Command: CommandServe})
	p := l.Config.Proxy
	t.Run("R-REPO-026 proxy.lock_poll_interval_ms and proxy.fetch_wait_timeout_seconds default", func(t *testing.T) {
		if p.LockPollIntervalMs != 1000 || p.LockPollInterval() != time.Second {
			t.Errorf("lock_poll_interval_ms = %d (%s)", p.LockPollIntervalMs, p.LockPollInterval())
		}
		if p.FetchWaitTimeoutSeconds != 120 || p.FetchWaitTimeout() != 2*time.Minute {
			t.Errorf("fetch_wait_timeout_seconds = %d (%s)", p.FetchWaitTimeoutSeconds, p.FetchWaitTimeout())
		}
	})
	t.Run("R-REPO-028 proxy.negative_l1_entries default", func(t *testing.T) {
		if p.NegativeL1Entries != 100000 {
			t.Errorf("negative_l1_entries = %d", p.NegativeL1Entries)
		}
	})
	t.Run("R-REPO-020 proxy.merged_metadata_cache_bytes default", func(t *testing.T) {
		if p.MergedMetadataCacheBytes != 256*MiB {
			t.Errorf("merged_metadata_cache_bytes = %s", p.MergedMetadataCacheBytes)
		}
	})
	t.Run("R-REPO-023 proxy.claims.remote_refusal default", func(t *testing.T) {
		if !p.Claims.RemoteRefusal {
			t.Error("remote_refusal is false")
		}
	})
	t.Run("R-REPO-031 proxy.global_offline default", func(t *testing.T) {
		if p.GlobalOffline {
			t.Error("global_offline is true")
		}
	})
	t.Run("R-REPO-039 proxy.max_redirects default", func(t *testing.T) {
		if p.MaxRedirects != 5 {
			t.Errorf("max_redirects = %d", p.MaxRedirects)
		}
	})
	t.Run("R-REPO-040 proxy limits default", func(t *testing.T) {
		if p.MaxArtifactBytes != 10*GiB || p.MaxMetadataBytes != 256*MiB {
			t.Errorf("max_artifact_bytes = %s, max_metadata_bytes = %s", p.MaxArtifactBytes, p.MaxMetadataBytes)
		}
		if p.MaxConcurrentFetches != 256 || p.QueueTimeoutSeconds != 10 || p.QueueTimeout() != 10*time.Second {
			t.Errorf("max_concurrent_fetches = %d, queue_timeout_seconds = %d", p.MaxConcurrentFetches, p.QueueTimeoutSeconds)
		}
	})
	t.Run("R-REPO-042 proxy.blocked_content_types default", func(t *testing.T) {
		if got := p.BlockedContentTypes; len(got) != 2 || got[0] != "text/html" || got[1] != "application/xhtml+xml" {
			t.Errorf("blocked_content_types = %v", got)
		}
	})
	t.Run("R-REPO-035 proxy.outbound keeps its defaults", func(t *testing.T) {
		if !p.Outbound.TLS.UseSystemRoots || p.Outbound.TLS.MinVersion != "1.2" || p.Outbound.ProxyFromEnvironment {
			t.Errorf("outbound = %+v", p.Outbound)
		}
	})
}

func TestProxyRanges(t *testing.T) {
	cases := []struct {
		req, name, yaml, key string
		bad                  bool
	}{
		{"R-REPO-026", "poll interval below the minimum", "proxy:\n  lock_poll_interval_ms: 10\n", "proxy.lock_poll_interval_ms", true},
		{"R-REPO-026", "poll interval above the maximum", "proxy:\n  lock_poll_interval_ms: 60001\n", "proxy.lock_poll_interval_ms", true},
		{"R-REPO-026", "poll interval at the minimum", "proxy:\n  lock_poll_interval_ms: 50\n", "proxy.lock_poll_interval_ms", false},
		{"R-REPO-026", "wait timeout of 0", "proxy:\n  fetch_wait_timeout_seconds: 0\n", "proxy.fetch_wait_timeout_seconds", true},
		{"R-REPO-026", "wait timeout above the maximum", "proxy:\n  fetch_wait_timeout_seconds: 3601\n", "proxy.fetch_wait_timeout_seconds", true},
		{"R-REPO-026", "wait timeout of 5 s as the acceptance criteria use it", "proxy:\n  fetch_wait_timeout_seconds: 5\n", "proxy.fetch_wait_timeout_seconds", false},
		{"R-REPO-026", "poll interval not below the wait timeout", "proxy:\n  lock_poll_interval_ms: 5000\n  fetch_wait_timeout_seconds: 5\n", "lock_poll_interval_ms", true},
		{"R-REPO-028", "negative L1 of 0", "proxy:\n  negative_l1_entries: 0\n", "proxy.negative_l1_entries", true},
		{"R-REPO-028", "negative L1 above the maximum", "proxy:\n  negative_l1_entries: 10000001\n", "proxy.negative_l1_entries", true},
		{"R-REPO-020", "merged cache of 0 turns it off", "proxy:\n  merged_metadata_cache_bytes: 0\n", "proxy.merged_metadata_cache_bytes", false},
		{"R-REPO-020", "merged cache negative", "proxy:\n  merged_metadata_cache_bytes: -1\n", "proxy.merged_metadata_cache_bytes", true},
		{"R-REPO-039", "no redirects", "proxy:\n  max_redirects: 0\n", "proxy.max_redirects", false},
		{"R-REPO-039", "negative redirects", "proxy:\n  max_redirects: -1\n", "proxy.max_redirects", true},
		{"R-REPO-039", "more than 20 redirects", "proxy:\n  max_redirects: 21\n", "proxy.max_redirects", true},
		{"R-REPO-040", "artifact limit as a plain integer", "proxy:\n  max_artifact_bytes: 1048576\n", "proxy.max_artifact_bytes", false},
		{"R-REPO-040", "artifact limit below 1 KiB", "proxy:\n  max_artifact_bytes: 512\n", "proxy.max_artifact_bytes", true},
		{"R-REPO-040", "metadata limit below 1 KiB", "proxy:\n  max_metadata_bytes: 1\n", "proxy.max_metadata_bytes", true},
		{"R-REPO-040", "no fetch slot", "proxy:\n  max_concurrent_fetches: 0\n", "proxy.max_concurrent_fetches", true},
		{"R-REPO-040", "queue timeout of 0 refuses at once", "proxy:\n  queue_timeout_seconds: 0\n", "proxy.queue_timeout_seconds", false},
		{"R-REPO-040", "queue timeout above the maximum", "proxy:\n  queue_timeout_seconds: 601\n", "proxy.queue_timeout_seconds", true},
		{"R-REPO-042", "a media type", "proxy:\n  blocked_content_types: [text/html, application/x-msdownload]\n", "blocked_content_types", false},
		{"R-REPO-042", "a media type with a parameter", "proxy:\n  blocked_content_types: [\"text/html; charset=utf-8\"]\n", "blocked_content_types", true},
		{"R-REPO-042", "an upper-case media type", "proxy:\n  blocked_content_types: [Text/HTML]\n", "blocked_content_types", true},
		{"R-REPO-042", "a type without a subtype", "proxy:\n  blocked_content_types: [text]\n", "blocked_content_types", true},
		{"R-REPO-042", "an empty list turns the block off", "proxy:\n  blocked_content_types: []\n", "blocked_content_types", false},
	}
	for _, c := range cases {
		t.Run(c.req+" "+c.name, func(t *testing.T) {
			_, err := loadM2(t, c.yaml)
			if got := hasIssue(err, c.key); got != c.bad {
				t.Fatalf("issue on %s = %v, want %v (%v)", c.key, got, c.bad, err)
			}
			if !c.bad && err != nil {
				t.Fatalf("a valid value is refused: %v", err)
			}
		})
	}
}

func TestNPMDefaults(t *testing.T) {
	n := mustLoad(t, Options{Environ: servableEnv(), Command: CommandServe}).Config.NPM
	t.Run("R-NPM-007 npm.publish defaults", func(t *testing.T) {
		p := n.Publish
		if p.MaxTarballSize != 256*MiB || p.MaxUnpackedSize != 2*GiB || p.MaxTarEntries != 100000 ||
			p.ManifestConsistency != "reject" || p.RetryWindow != 10*time.Minute {
			t.Errorf("publish = %+v", p)
		}
	})
	t.Run("R-NPM-011 npm.abbreviated_time default", func(t *testing.T) {
		if !n.AbbreviatedTime {
			t.Error("abbreviated_time is false")
		}
	})
	t.Run("R-NPM-013 npm.document_max_age default", func(t *testing.T) {
		if n.DocumentMaxAge != 0 {
			t.Errorf("document_max_age = %s", n.DocumentMaxAge)
		}
	})
}

func TestNPMRanges(t *testing.T) {
	cases := []struct {
		req, name, yaml, key string
		bad                  bool
	}{
		{"R-NPM-007", "tarball limit below 1 KiB", "npm:\n  publish:\n    max_tarball_size: 100\n", "npm.publish.max_tarball_size", true},
		{"R-NPM-007", "tarball limit above the upload limit", "npm:\n  publish:\n    max_tarball_size: 11GiB\n", "npm.publish.max_tarball_size", true},
		{"R-NPM-007", "tarball limit at the upload limit", "npm:\n  publish:\n    max_tarball_size: 10GiB\n", "npm.publish.max_tarball_size", false},
		{"R-NPM-007", "unpacked limit below 1 KiB", "npm:\n  publish:\n    max_unpacked_size: 1\n", "npm.publish.max_unpacked_size", true},
		{"R-NPM-007", "no tar entries", "npm:\n  publish:\n    max_tar_entries: 0\n", "npm.publish.max_tar_entries", true},
		{"R-NPM-007", "manifest consistency warn", "npm:\n  publish:\n    manifest_consistency: warn\n", "npm.publish.manifest_consistency", false},
		{"R-NPM-007", "manifest consistency of another value", "npm:\n  publish:\n    manifest_consistency: ignore\n", "npm.publish.manifest_consistency", true},
		{"R-NPM-007", "retry window 0s turns the acknowledgement off", "npm:\n  publish:\n    retry_window: 0s\n", "npm.publish.retry_window", false},
		{"R-NPM-007", "retry window above 24 h", "npm:\n  publish:\n    retry_window: 25h\n", "npm.publish.retry_window", true},
		{"R-NPM-013", "document max age of 5 minutes", "npm:\n  document_max_age: 5m\n", "npm.document_max_age", false},
		{"R-NPM-013", "document max age above 24 h", "npm:\n  document_max_age: 48h\n", "npm.document_max_age", true},
		{"R-NPM-011", "abbreviated time off", "npm:\n  abbreviated_time: false\n", "npm.abbreviated_time", false},
	}
	for _, c := range cases {
		t.Run(c.req+" "+c.name, func(t *testing.T) {
			_, err := loadM2(t, c.yaml)
			if got := hasIssue(err, c.key); got != c.bad {
				t.Fatalf("issue on %s = %v, want %v (%v)", c.key, got, c.bad, err)
			}
			if !c.bad && err != nil {
				t.Fatalf("a valid value is refused: %v", err)
			}
		})
	}
}

func TestM2KeysHaveEnvironmentNames(t *testing.T) {
	t.Run("R-ARCH-020 an M2 key is set by its environment variable", func(t *testing.T) {
		l := mustLoad(t, Options{Environ: servableEnv("TROVE_PROXY_MAX_REDIRECTS=7", "TROVE_PROXY_CLAIMS_REMOTE_REFUSAL=false", "TROVE_NPM_PUBLISH_MAX_TAR_ENTRIES=5"), Command: CommandServe})
		if l.Config.Proxy.MaxRedirects != 7 || l.Config.Proxy.Claims.RemoteRefusal || l.Config.NPM.Publish.MaxTarEntries != 5 {
			t.Errorf("proxy = %+v, npm = %+v", l.Config.Proxy, l.Config.NPM)
		}
		if src := l.Sources["proxy.max_redirects"]; src.Kind != "env" {
			t.Errorf("source of proxy.max_redirects = %+v", src)
		}
	})
}
