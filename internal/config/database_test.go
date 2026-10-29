package config

import (
	"strings"
	"testing"
)

func TestDatabaseTLSProblem(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		rootCert string
		insecure bool
		want     string // a substring of the problem, empty for none
	}{
		{"verify-full needs nothing more", "verify-full", "", false, ""},
		{"require without a CA file", "require", "", false, "ssl_root_cert_file"},
		{"require with a CA file", "require", "/ca.pem", false, ""},
		{"require without a CA file, explicitly insecure", "require", "", true, ""},
		{"verify-ca without a CA file", "verify-ca", "", false, "ssl_root_cert_file"},
		{"verify-ca without a CA file is not rescued by allow_insecure", "verify-ca", "", true, "ssl_root_cert_file"},
		{"verify-ca with a CA file", "verify-ca", "/ca.pem", false, ""},
		{"disable", "disable", "", false, "allow_insecure"},
		{"disable, explicitly insecure", "disable", "", true, ""},
	}
	for _, tc := range cases {
		t.Run("R-HA-015 "+tc.name, func(t *testing.T) {
			d := Database{SSLMode: tc.mode, SSLRootCertFile: tc.rootCert, AllowInsecure: tc.insecure}
			got := d.TLSProblem()
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("TLSProblem() = %q, want one containing %q", got, tc.want)
			}
		})
	}
	t.Run("R-HA-015 the load refuses require without a CA file naming ssl_mode", func(t *testing.T) {
		env := minimalEnv("TROVE_STORAGE_S3_BUCKET=blobs", "TROVE_DATABASE_SSL_MODE=require")
		_, err := Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": ""}), Environ: env, Command: CommandMigrate})
		if !hasIssue(err, "database.ssl_mode") {
			t.Fatalf("got %v", err)
		}
	})
}
