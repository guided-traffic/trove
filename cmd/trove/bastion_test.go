package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestServeBastion(t *testing.T) {
	t.Run("R-BAS-001 a file that holds database.host stops the role bastion naming the key and the role", func(t *testing.T) {
		cfg := writeConfig(t, "server:\n  base_url: https://packages.example.com\nbastion:\n  main:\n    url: https://trove.example.com\n    token_file: /t\ndatabase:\n  host: db.example\n")
		code, _, stderr := runCmd(t, nil, "serve", "--role", "bastion", "--config", cfg)
		if code != 1 || !strings.Contains(stderr, "database.host") || !strings.Contains(stderr, "role bastion") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})
	t.Run("R-BAS-001 an environment variable of another section stops the role too", func(t *testing.T) {
		cfg := writeConfig(t, "server:\n  base_url: https://packages.example.com\nbastion:\n  main:\n    url: https://trove.example.com\n    token_file: /t\n")
		code, _, stderr := runCmd(t, []string{"TROVE_DATABASE_HOST=db.example"}, "serve", "--role", "bastion", "--config", cfg)
		if code != 1 || !strings.Contains(stderr, "TROVE_DATABASE_HOST") || !strings.Contains(stderr, "role bastion") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})
	t.Run("R-BAS-044 an http main URL stops the role naming bastion.main.url", func(t *testing.T) {
		cfg := writeConfig(t, "server:\n  base_url: https://packages.example.com\nbastion:\n  main:\n    url: http://trove.example.com\n    token_file: /t\n")
		code, _, stderr := runCmd(t, nil, "serve", "--role", "bastion", "--config", cfg)
		if code != 1 || !strings.Contains(stderr, "bastion.main.url") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})
	t.Run("R-BAS-006 a missing token file stops the role at start naming the key", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "token")
		cfg := writeConfig(t, "server:\n  listen: 127.0.0.1:0\n  ops_listen: localhost:0\n  base_url: https://packages.example.com\nbastion:\n  main:\n    url: https://trove.example.com\n    token_file: "+missing+"\n")
		code, _, stderr := runCmd(t, nil, "serve", "--role", "bastion", "--config", cfg)
		if code != 1 || !strings.Contains(stderr, "bastion.main.token_file") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})
	t.Run("R-BAS-002 the role takes no configuration flag but --log-level", func(t *testing.T) {
		code, _, stderr := runCmd(t, nil, "serve", "--role", "bastion", "--log-level", "nonsense")
		if code != 1 || strings.Contains(stderr, "process.role") {
			t.Fatalf("code %d stderr %q", code, stderr)
		}
	})
}
