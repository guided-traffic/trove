package cluster

import (
	"strings"
	"testing"
)

func TestVersionCheck(t *testing.T) {
	cases := []struct {
		server, version string
		wantOK          bool
	}{
		{"valkey", "9.0.0", true},
		{"valkey", "9.0.7", true},
		{"valkey", "9.1.3", true},
		{"valkey", "9.2.0-rc1", true},
		{"valkey", "10.0.1", true},
		{"valkey", "8.1.5", false},
		{"valkey", "8.0.0", false},
		{"valkey", "7.2.4", false},
		{"redis", "9.0.0", false},
		{"valkey", "", false},
		{"valkey", "nine", false},
	}
	for _, c := range cases {
		t.Run("R-ARCH-031 "+c.server+" "+c.version, func(t *testing.T) {
			reason := checkServer(c.server, c.version)
			if (reason == "") != c.wantOK {
				t.Fatalf("checkServer(%q, %q) = %q, want supported=%v", c.server, c.version, reason, c.wantOK)
			}
		})
	}
	t.Run("R-ARCH-031 the refusal names the version and the floor", func(t *testing.T) {
		want := "valkey 8.1.5 is not supported; trove requires 9.0 or later"
		if got := checkServer("valkey", "8.1.5"); got != want {
			t.Fatalf("reason = %q, want %q", got, want)
		}
	})
	t.Run("R-ARCH-031 HELLO fields", func(t *testing.T) {
		s, v, err := helloFields(map[string]string{"server": "valkey", "version": "8.1.5", "proto": "3"})
		if err != nil || s != "valkey" || v != "8.1.5" {
			t.Fatalf("helloFields = %q, %q, %v", s, v, err)
		}
		if _, _, err := helloFields(map[string]string{"server": "valkey"}); err == nil || !strings.Contains(err.Error(), "HELLO") {
			t.Fatalf("missing version: %v", err)
		}
	})
}
