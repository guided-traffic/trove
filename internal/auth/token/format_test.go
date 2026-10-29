package token

import (
	"encoding/hex"
	"regexp"
	"testing"
)

var scannerPattern = regexp.MustCompile(`\btrv_(?:pat|sat|cit)_[0-9A-Za-z]{49}\b`)

func TestR_AUTH_022_TestVector(t *testing.T) {
	var zero [32]byte
	got := FromRandom(TypePAT, zero)
	if want := "trv_pat_000000000000000000000000000000000000000000010kJbh"; got != want {
		t.Fatalf("test vector: got %s, want %s", got, want)
	}
	if h := Hash(got); hex.EncodeToString(h[:]) != "e1ba47eddf817cc30ede3aa6f55391378f03840da097aa8c387dca45fb10c703" {
		t.Fatalf("SHA-256 of the test vector: %x", h)
	}
	if typ, err := Check(got); err != nil || typ != TypePAT {
		t.Fatalf("Check(test vector) = %q, %v", typ, err)
	}
}

func TestR_AUTH_022_GeneratedTokens(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20000; i++ {
		for _, typ := range []Type{TypePAT, TypeSAT, TypeCIT} {
			s, err := Generate(typ)
			if err != nil {
				t.Fatal(err)
			}
			if len(s) != 57 || !scannerPattern.MatchString(s) || seen[s] {
				t.Fatalf("token %q: length %d, scanner match %v, duplicate %v", s, len(s), scannerPattern.MatchString(s), seen[s])
			}
			seen[s] = true
			if got, err := Check(s); err != nil || got != typ {
				t.Fatalf("Check(%q) = %q, %v", s, got, err)
			}
		}
	}
}

func TestR_AUTH_022_FlippedCharacterFailsChecksum(t *testing.T) {
	s, _ := Generate(TypePAT)
	for i := 0; i < len(s); i++ {
		for _, repl := range []byte{'0', 'A', 'z', '_', ' '} {
			if s[i] == repl {
				continue
			}
			b := []byte(s)
			b[i] = repl
			if _, err := Check(string(b)); err == nil {
				t.Fatalf("flipping position %d to %q was accepted: %s", i, repl, b)
			}
		}
	}
}

func TestR_AUTH_022_CheckRejectsForeignValues(t *testing.T) {
	for _, v := range []string{
		"", "trv_pat_", "eyJhbGciOiJSUzI1NiJ9.e30.sig", "trv_xyz_000000000000000000000000000000000000000000010kJbh",
		"trv_pat_000000000000000000000000000000000000000000010kJbh0", "hunter2",
	} {
		if _, err := Check(v); err == nil {
			t.Errorf("Check(%q) accepted", v)
		}
	}
}

func TestR_AUTH_022_Hint(t *testing.T) {
	s := "trv_pat_3kQ9" + "000000000000000000000000000000000000000000"
	if got := Hint(s); got != "trv_pat_3kQ9" {
		t.Fatalf("Hint = %q", got)
	}
}
