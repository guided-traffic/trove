package edge

import (
	"net/http"
	"strings"
	"testing"
)

func TestReprDigest(t *testing.T) {
	sha256 := "sha-256=:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=:"
	sha512 := "sha-512=:" + strings.Repeat("AAAA", 21) + "AA==:"
	t.Run("R-STOR-007 sha-256 and sha-512 are read", func(t *testing.T) {
		h := http.Header{"Repr-Digest": {sha256 + ", " + sha512}}
		got, err := ParseReprDigest(h)
		if err != nil || len(got.SHA256) != 32 || len(got.SHA512) != 64 {
			t.Fatalf("%+v, %v", got, err)
		}
	})
	t.Run("R-STOR-007 other algorithms are not verified", func(t *testing.T) {
		got, err := ParseReprDigest(http.Header{"Repr-Digest": {"md5=:AAAAAAAAAAAAAAAAAAAAAA==:, " + sha256}})
		if err != nil || len(got.MD5) != 0 || len(got.SHA256) != 32 {
			t.Fatalf("%+v, %v", got, err)
		}
	})
	t.Run("R-STOR-007 a value that is not a dictionary of byte sequences is 400 invalid_header", func(t *testing.T) {
		for _, bad := range []string{"sha-256=abc", "sha-256", "sha-256=:!!!:", "sha-256=:AAAA:", "sha-512=:AAAA:"} {
			if _, err := ParseReprDigest(http.Header{"Repr-Digest": {bad}}); err == nil {
				t.Errorf("%q was accepted", bad)
			}
		}
	})
	t.Run("RFC 9530 the response digest adds sha-512 on request", func(t *testing.T) {
		sum, sum512 := make([]byte, 32), make([]byte, 64)
		if got := reprDigest(http.Header{}, sum, sum512); strings.Contains(got, "sha-512") {
			t.Errorf("without Want-Repr-Digest: %q", got)
		}
		got := reprDigest(http.Header{"Want-Repr-Digest": {"sha-512=10"}}, sum, sum512)
		if !strings.HasPrefix(got, "sha-256=:") || !strings.Contains(got, ", sha-512=:") {
			t.Errorf("with Want-Repr-Digest: %q", got)
		}
		if got := reprDigest(http.Header{"Want-Repr-Digest": {"sha-512=0"}}, sum, sum512); strings.Contains(got, "sha-512") {
			t.Errorf("weight 0: %q", got)
		}
	})
}
