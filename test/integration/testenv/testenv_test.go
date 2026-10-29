package testenv

import (
	"os"
	"strings"
	"testing"
)

// recorder captures Fatalf of the accessor under test.
type recorder struct {
	testing.TB
	msg string
}

func (r *recorder) Helper() {}
func (r *recorder) Fatalf(format string, args ...any) {
	r.msg = strings.TrimSpace(format)
	panic(r)
}

func TestGetFailsWithAClearMessage(t *testing.T) {
	t.Run("a missing variable fails and names the variable and the fix", func(t *testing.T) {
		t.Setenv("TROVE_TEST_UNSET_FOR_THIS_TEST", "")
		r := &recorder{TB: t}
		func() {
			defer func() { _ = recover() }()
			Get(r, "TROVE_TEST_UNSET_FOR_THIS_TEST")
		}()
		if !strings.Contains(r.msg, "is not set") || !strings.Contains(r.msg, "integration") {
			t.Fatalf("message %q is not clear", r.msg)
		}
	})
	t.Run("a set variable is returned", func(t *testing.T) {
		t.Setenv("TROVE_TEST_SET_FOR_THIS_TEST", "x")
		if got := Get(t, "TROVE_TEST_SET_FOR_THIS_TEST"); got != "x" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("lists are split and trimmed", func(t *testing.T) {
		got := split(" a:1, b:2 ,,c:3")
		if len(got) != 3 || got[1] != "b:2" {
			t.Fatalf("got %v", got)
		}
	})
}

func TestCheckTLSRefusesPlaintextEndpoints(t *testing.T) {
	ca := t.TempDir() + "/ca.crt"
	if err := os.WriteFile(ca, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := map[string]string{
		"TROVE_TEST_CA_FILE":          ca,
		"TROVE_TEST_PG_DSN":           "postgres://u:p@127.0.0.1:15432/trove?sslmode=verify-full&sslrootcert=" + ca,
		"TROVE_TEST_PG_PGBOUNCER_DSN": "postgres://u:p@127.0.0.1:16432/trove?sslmode=verify-full&sslrootcert=" + ca,
		"TROVE_TEST_S3_ENDPOINT":      "https://127.0.0.1:18333",
		"TROVE_TEST_VALKEY_CA_FILE":   ca,
	}
	with := func(k, v string) func(string) string {
		return func(name string) string {
			if name == k {
				return v
			}
			return good[name]
		}
	}
	if err := checkTLS(with("", "")); err != nil {
		t.Fatalf("a TLS environment is refused: %v", err)
	}
	for name, tc := range map[string]struct{ key, val, want string }{
		"plaintext S3 endpoint":    {"TROVE_TEST_S3_ENDPOINT", "http://127.0.0.1:18333", "plaintext"},
		"sslmode disable":          {"TROVE_TEST_PG_DSN", "postgres://u:p@127.0.0.1:15432/trove?sslmode=disable", "plaintext or unverified"},
		"sslmode require":          {"TROVE_TEST_PG_PGBOUNCER_DSN", "postgres://u:p@127.0.0.1:16432/trove?sslmode=require", "plaintext or unverified"},
		"verify-full without root": {"TROVE_TEST_PG_DSN", "postgres://u:p@127.0.0.1:15432/trove?sslmode=verify-full", "plaintext or unverified"},
		"no DSN":                   {"TROVE_TEST_PG_DSN", "", "is not set"},
		"no CA":                    {"TROVE_TEST_CA_FILE", "", "is not set"},
		"CA file missing":          {"TROVE_TEST_CA_FILE", "/nonexistent/ca.crt", "not a readable file"},
		"unknown scope":            {"TROVE_TEST_SCOPE", "everything", "is not stack or s3"},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkTLS(with(tc.key, tc.val))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestCheckTLSScopeS3(t *testing.T) {
	only := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	t.Run("R-STOR-035 the conformance scope needs only an https S3 endpoint", func(t *testing.T) {
		if err := checkTLS(only(map[string]string{"TROVE_TEST_SCOPE": "s3", "TROVE_TEST_S3_ENDPOINT": "https://s3.example.test"})); err != nil {
			t.Fatalf("an S3-only environment is refused: %v", err)
		}
	})
	t.Run("R-STOR-035 the conformance scope refuses a plaintext S3 endpoint and an unreadable CA file", func(t *testing.T) {
		if err := checkTLS(only(map[string]string{"TROVE_TEST_SCOPE": "s3", "TROVE_TEST_S3_ENDPOINT": "http://127.0.0.1:9000"})); err == nil || !strings.Contains(err.Error(), "plaintext") {
			t.Fatalf("err = %v, want plaintext", err)
		}
		if err := checkTLS(only(map[string]string{"TROVE_TEST_SCOPE": "s3", "TROVE_TEST_S3_ENDPOINT": "https://s3.example.test", "TROVE_TEST_CA_FILE": "/nonexistent/ca.crt"})); err == nil || !strings.Contains(err.Error(), "not a readable file") {
			t.Fatalf("err = %v, want not a readable file", err)
		}
	})
	t.Run("R-HA-051 without a scope the full stack is still required", func(t *testing.T) {
		if err := checkTLS(only(map[string]string{"TROVE_TEST_S3_ENDPOINT": "https://s3.example.test"})); err == nil || !strings.Contains(err.Error(), "is not set") {
			t.Fatalf("err = %v, want is not set", err)
		}
	})
}
