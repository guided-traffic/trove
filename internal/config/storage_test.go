package config

import (
	"strings"
	"testing"
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
		{"endpoint without a scheme", "storage:\n  s3:\n    endpoint: \"s3.example.com\"\n", "storage.s3.endpoint", true},
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
