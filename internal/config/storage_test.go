package config

import (
	"strings"
	"testing"
	"time"
)

func loadStorage(t *testing.T, file, role string, cmd Command) error {
	t.Helper()
	env := minimalEnv("TROVE_STORAGE_S3_BUCKET=blobs")
	if role != "" {
		env = append(env, "TROVE_PROCESS_ROLE="+role)
	}
	_, err := Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: env, Command: cmd})
	return err
}

func hasIssue(err error, key string) bool {
	return err != nil && strings.Contains(err.Error(), key)
}

func TestStorageSpoolRule(t *testing.T) {
	small := "storage:\n  spool:\n    max_bytes: 1GiB\n"
	t.Run("R-STOR-004 the maximum upload must fit the spool of web and all", func(t *testing.T) {
		for _, role := range []string{"web", "all"} {
			if err := loadStorage(t, small, role, CommandServe); !hasIssue(err, "storage.upload.max_size") {
				t.Errorf("role %s: want an issue naming storage.upload.max_size, got %v", role, err)
			}
		}
	})
	t.Run("R-HA-029 a worker accepts no uploads and its spool need not hold the maximum", func(t *testing.T) {
		if err := loadStorage(t, small, "worker", CommandServe); hasIssue(err, "storage.upload.max_size") {
			t.Fatalf("worker: %v", err)
		}
	})
	t.Run("R-STOR-004 config validate without a role cannot know the role and checks no spool rule", func(t *testing.T) {
		if err := loadStorage(t, small, "", CommandValidate); hasIssue(err, "storage.upload.max_size") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-STOR-004 a spool that holds the maximum passes on every role", func(t *testing.T) {
		ok := "storage:\n  spool:\n    max_bytes: 12GiB\n"
		for _, role := range []string{"web", "worker", "all"} {
			if err := loadStorage(t, ok, role, CommandServe); hasIssue(err, "storage.upload.max_size") {
				t.Errorf("role %s: %v", role, err)
			}
		}
	})
}

func TestStorageConnectionRules(t *testing.T) {
	cases := []struct {
		name, yaml, key string
		bad             bool
	}{
		{"prefix of a slash", "storage:\n  s3:\n    prefix: \"/\"\n", "storage.s3.prefix", true},
		{"prefix with a double slash", "storage:\n  s3:\n    prefix: \"a//b\"\n", "storage.s3.prefix", true},
		{"prefix with a trailing slash", "storage:\n  s3:\n    prefix: \"a/\"\n", "storage.s3.prefix", true},
		{"prefix with a space", "storage:\n  s3:\n    prefix: \"a b\"\n", "storage.s3.prefix", true},
		{"nested prefix", "storage:\n  s3:\n    prefix: \"team-a/trove\"\n", "storage.s3.prefix", false},
		{"empty prefix", "storage:\n  s3:\n    prefix: \"\"\n", "storage.s3.prefix", false},
		{"http endpoint", "storage:\n  s3:\n    endpoint: \"http://s3.example.com\"\n", "storage.s3.endpoint", true},
		{"http endpoint allowed", "storage:\n  s3:\n    endpoint: \"http://s3.example.com\"\n    allow_insecure_endpoint: true\n", "storage.s3.endpoint", false},
		{"endpoint without a scheme is a host and means https", "storage:\n  s3:\n    endpoint: \"s3.example.com:9000\"\n", "storage.s3.endpoint", false},
		{"endpoint of an unsupported scheme", "storage:\n  s3:\n    endpoint: \"ftp://s3.example.com\"\n", "storage.s3.endpoint", true},
		{"https endpoint", "storage:\n  s3:\n    endpoint: \"https://s3.example.com:9000\"\n", "storage.s3.endpoint", false},
	}
	for _, c := range cases {
		t.Run("R-STOR-030 "+c.name, func(t *testing.T) {
			err := loadStorage(t, c.yaml, "web", CommandServe)
			if got := hasIssue(err, c.key); got != c.bad {
				t.Fatalf("issue on %s = %v, want %v (%v)", c.key, got, c.bad, err)
			}
		})
	}
}

func TestStorageEndpointScheme(t *testing.T) {
	load := func(t *testing.T, y string) *Loaded {
		t.Helper()
		l, err := Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": y}), Environ: servableEnv("TROVE_STORAGE_S3_BUCKET=blobs", "TROVE_PROCESS_ROLE=web"), Command: CommandServe})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	t.Run("R-STOR-030 an endpoint without a scheme becomes https:// in the effective configuration, with its source", func(t *testing.T) {
		l := load(t, "storage:\n  s3:\n    endpoint: \"s3.eu01.example.cloud:443\"\n    presign:\n      endpoint: \"blobs.example.com\"\n")
		if got := l.Config.Storage.S3.Endpoint; got != "https://s3.eu01.example.cloud:443" {
			t.Errorf("endpoint = %q", got)
		}
		if got := l.Config.Storage.S3.Presign.Endpoint; got != "https://blobs.example.com" {
			t.Errorf("presign endpoint = %q", got)
		}
		if k := l.Sources["storage.s3.endpoint"].Kind; k != "derived" {
			t.Errorf("source kind = %q, want derived", k)
		}
	})
	t.Run("R-STOR-030 allow_insecure_endpoint does not turn a scheme-less host into http", func(t *testing.T) {
		l := load(t, "storage:\n  s3:\n    endpoint: \"minio.local:9000\"\n    allow_insecure_endpoint: true\n")
		if got := l.Config.Storage.S3.Endpoint; got != "https://minio.local:9000" {
			t.Errorf("endpoint = %q", got)
		}
	})
	t.Run("R-STOR-030 an endpoint with a scheme and the empty endpoint are left alone", func(t *testing.T) {
		for in, want := range map[string]string{"": "", "http://s3.local:9000": "http://s3.local:9000", "https://s3.example.com": "https://s3.example.com"} {
			if got := NormalizeEndpoint(in); got != want {
				t.Errorf("NormalizeEndpoint(%q) = %q, want %q", in, got, want)
			}
		}
	})
}

func TestStorageHoldBackAndSettle(t *testing.T) {
	t.Run("R-STOR-013 a download hold-back below 1 KiB is refused", func(t *testing.T) {
		for _, v := range []string{"0", "1023B"} {
			if err := loadStorage(t, "storage:\n  download:\n    hold_back: "+v+"\n", "web", CommandServe); !hasIssue(err, "storage.download.hold_back") {
				t.Errorf("hold_back %s: %v", v, err)
			}
		}
		if err := loadStorage(t, "storage:\n  download:\n    hold_back: 1KiB\n", "web", CommandServe); hasIssue(err, "storage.download.hold_back") {
			t.Errorf("1KiB: %v", err)
		}
	})
	t.Run("R-STOR-024 delete_settle below one second is refused", func(t *testing.T) {
		if err := loadStorage(t, "storage:\n  gc:\n    delete_settle: 0s\n", "worker", CommandServe); !hasIssue(err, "storage.gc.delete_settle") {
			t.Errorf("0s: %v", err)
		}
	})
}

func TestStorageCoupling(t *testing.T) {
	check := func(lease, settle, handover time.Duration) []string {
		var c Config
		c.Jobs.LeaseDuration = lease
		c.Storage.GC.DeleteSettle, c.Storage.GC.HandoverDelay = settle, handover
		var issues []Issue
		c.validateStorageCoupling(&Report{issues: &issues})
		out := make([]string, len(issues))
		for i, is := range issues {
			out[i] = is.Message
		}
		return out
	}
	t.Run("R-STOR-024 the shipped defaults are accepted", func(t *testing.T) {
		if got := check(60*time.Second, 30*time.Second, 10*time.Minute); len(got) != 0 {
			t.Fatalf("issues: %v", got)
		}
	})
	t.Run("R-STOR-024 handover_delay not above lease_duration plus twice delete_settle is refused naming the keys", func(t *testing.T) {
		for _, handover := range []time.Duration{2 * time.Minute, 90 * time.Second} { // equal to and below 60 s + 2 x 30 s ... 120 s
			got := check(60*time.Second, 30*time.Second, handover)
			if len(got) != 1 || !strings.Contains(got[0], "storage.gc.handover_delay") || !strings.Contains(got[0], "jobs.lease_duration") {
				t.Errorf("handover %s: issues = %v", handover, got)
			}
		}
		if got := check(60*time.Second, 30*time.Second, 121*time.Second); len(got) != 0 {
			t.Errorf("one second above the bound: %v", got)
		}
	})
}
