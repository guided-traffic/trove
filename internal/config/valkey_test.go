package config

import (
	"strings"
	"testing"
)

func TestValkeyRules(t *testing.T) {
	load := func(file string) error {
		_, err := Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: servableEnv(), Command: CommandServe})
		return err
	}
	t.Run("R-ARCH-030 key_prefix must end in a colon", func(t *testing.T) {
		err := load("valkey:\n  key_prefix: \"trove\"\n")
		if err == nil || !strings.Contains(err.Error(), "valkey.key_prefix") {
			t.Fatalf("want an error naming valkey.key_prefix, got %v", err)
		}
		if err := load("valkey:\n  key_prefix: \"tenant-a:\"\n"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-HA-056 plaintext Sentinels need allow_insecure", func(t *testing.T) {
		err := load("valkey:\n  sentinel:\n    tls: false\n")
		if err == nil || !strings.Contains(err.Error(), "valkey.sentinel.tls") {
			t.Fatalf("want an error naming valkey.sentinel.tls, got %v", err)
		}
		if err := load("valkey:\n  allow_insecure: true\n  sentinel:\n    tls: false\n"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-HA-056 sentinel mode requires addresses and primary_name", func(t *testing.T) {
		_, err := Load(Options{ReadFile: fakeFS(nil), Environ: minimalEnv("TROVE_PROCESS_ROLE=web"), Command: CommandServe})
		if err == nil || !strings.Contains(err.Error(), "valkey.sentinel.addresses") || !strings.Contains(err.Error(), "valkey.sentinel.primary_name") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-HA-056 reconnect backoff is ordered", func(t *testing.T) {
		err := load("valkey:\n  reconnect:\n    initial_backoff: 10s\n    max_backoff: 1s\n")
		if err == nil || !strings.Contains(err.Error(), "valkey.reconnect.initial_backoff") {
			t.Fatalf("got %v", err)
		}
	})
}
