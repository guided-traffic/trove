package registrytoken

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/secrets"
)

// keyFile writes a key file <id>.key of 32 bytes filled from fill and returns its path.
func keyFile(t *testing.T, dir, id string, fill byte) string {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill + byte(i)
	}
	p := filepath.Join(dir, id+".key")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(raw)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// signer loads a ring of the given key files with the active key and returns its signer, with the default lifetime of
// oci.token_lifetime.
func signer(t *testing.T, active string, files ...string) *Signer {
	t.Helper()
	return signerFor(t, 5*time.Minute, active, files...)
}

func signerFor(t *testing.T, lifetime time.Duration, active string, files ...string) *Signer {
	t.Helper()
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: files, ActiveKeyID: active})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSigner(ring, lifetime)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var (
	t0      = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	webRepo = qname.Repo{Project: "web", Key: "docker-local"}
)

func patClaims() Claims {
	return Claims{
		PrincipalKind: "user", PrincipalID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10",
		Binding:  Binding{TokenID: "0192f0c4-0000-7000-8000-000000000001", TokenType: "pat", SecretSHA256: sha256.Sum256([]byte("trv_pat_x"))},
		Endpoint: "default", IssuedAt: t0, ExpiresAt: t0.Add(5 * time.Minute),
		Grant: Grant{Entries: []Entry{{Repo: webRepo, Name: "app", Actions: Pull | Push}}},
	}
}

// R-OCI-005: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestRegistryTokenForm(t *testing.T) {
	dir := t.TempDir()
	s := signer(t, "k1", keyFile(t, dir, "k1", 1))
	tok, err := s.Issue(patClaims())
	if err != nil {
		t.Fatal(err)
	}

	t.Run("trv_rgt_, the payload 1:<key id>:<claims>, a dot and the base64url HMAC-SHA-256 of the signed part", func(t *testing.T) {
		if !strings.HasPrefix(tok, Prefix) || len(tok) > MaxLength {
			t.Fatalf("token %q", tok)
		}
		encoded, sig, ok := strings.Cut(tok[len(Prefix):], ".")
		if !ok || len(sig) != 43 {
			t.Fatalf("form of %q", tok)
		}
		payload, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || !strings.HasPrefix(string(payload), "1:k1:{") {
			t.Fatalf("payload %q %v", payload, err)
		}
		for _, secret := range []string{"trv_pat_x", "trv_pat_"} {
			if strings.Contains(string(payload), secret) {
				t.Fatal("the payload carries the trove token")
			}
		}
	})
	t.Run("the token verifies on the endpoint it was issued on and returns the claims", func(t *testing.T) {
		c, err := s.Verify(tok, "default", t0.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		want := patClaims()
		if c.KeyID != "k1" || c.PrincipalID != want.PrincipalID || c.Binding != want.Binding || c.Endpoint != "default" ||
			!c.ExpiresAt.Equal(want.ExpiresAt) || !c.Grant.Allows(webRepo, "app", Push) || c.Grant.Allows(webRepo, "app", Delete) {
			t.Fatalf("claims %+v", c)
		}
	})
	t.Run("flipping any character of the token fails verification", func(t *testing.T) {
		alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
		for i := len(Prefix); i < len(tok); i++ {
			if tok[i] == '.' {
				continue
			}
			c := alphabet[(strings.IndexByte(alphabet, tok[i])+1)%len(alphabet)]
			forged := tok[:i] + string(c) + tok[i+1:]
			if _, err := s.Verify(forged, "default", t0); err == nil {
				t.Fatalf("a token with character %d changed verified", i)
			}
		}
	})
	t.Run("a token after its expiry, or presented on another endpoint, is refused", func(t *testing.T) {
		if _, err := s.Verify(tok, "default", t0.Add(5*time.Minute)); !errors.Is(err, ErrExpired) {
			t.Fatalf("at expiry: %v", err)
		}
		if _, err := s.Verify(tok, "customers", t0); !errors.Is(err, ErrEndpoint) {
			t.Fatalf("other endpoint: %v", err)
		}
	})
	t.Run("a token signed with another key of the same ID is refused", func(t *testing.T) {
		other := signer(t, "k1", keyFile(t, t.TempDir(), "k1", 77))
		if _, err := other.Verify(tok, "default", t0); !errors.Is(err, ErrSignature) {
			t.Fatalf("wrong key: %v", err)
		}
	})
	t.Run("a token the ring has no key for is refused", func(t *testing.T) {
		other := signer(t, "k9", keyFile(t, t.TempDir(), "k9", 1))
		if _, err := other.Verify(tok, "default", t0); !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("unknown key: %v", err)
		}
	})
	t.Run("values that are no registry token are malformed before any key is asked", func(t *testing.T) {
		for _, raw := range []string{
			"", "trv_rgt_", "trv_rgt_abc", "trv_rgt_a.b.c", Prefix + strings.Repeat("A", MaxLength), "trv_pat_" + tok[8:],
			tok + "=", strings.Replace(tok, ".", "..", 1),
			Prefix + base64.RawURLEncoding.EncodeToString([]byte("2:k1:{}")) + "." + strings.Repeat("A", 43),
			Prefix + base64.RawURLEncoding.EncodeToString([]byte("1::{}")) + "." + strings.Repeat("A", 43),
		} {
			if _, err := s.Verify(raw, "default", t0); err == nil {
				t.Fatalf("%q verified", raw)
			}
		}
	})
	t.Run("the scanner pattern and the syntax step of a trove token (R-AUTH-022) do not match a registry token", func(t *testing.T) {
		scanner := regexp.MustCompile(`\btrv_(?:pat|sat|cit|bst)_[0-9A-Za-z]{49}\b`)
		if scanner.MatchString(tok) {
			t.Fatal("the scanner pattern matches")
		}
		if _, err := token.Check(tok); err == nil {
			t.Fatal("the syntax step accepts a registry token")
		}
	})
}

// R-OCI-005: named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestRegistryTokenClaimsRules(t *testing.T) {
	s := signer(t, "k1", keyFile(t, t.TempDir(), "k1", 1))
	mutate := func(f func(*Claims)) Claims {
		c := patClaims()
		f(&c)
		return c
	}
	t.Run("claims a verifier would refuse are not issued", func(t *testing.T) {
		for name, c := range map[string]Claims{
			"no endpoint":                mutate(func(c *Claims) { c.Endpoint = "" }),
			"zero lifetime":              mutate(func(c *Claims) { c.ExpiresAt = c.IssuedAt }),
			"over one hour":              mutate(func(c *Claims) { c.ExpiresAt = c.IssuedAt.Add(MaxLifetime + time.Second) }),
			"over the lifetime":          mutate(func(c *Claims) { c.ExpiresAt = c.IssuedAt.Add(5*time.Minute + time.Second) }),
			"no binding":                 mutate(func(c *Claims) { c.Binding = Binding{} }),
			"a Bastion token":            mutate(func(c *Claims) { c.Binding.TokenType = "bst" }),
			"anonymous with a principal": mutate(func(c *Claims) { c.Anonymous = true }),
			"an entry without actions":   mutate(func(c *Claims) { c.Grant.Entries[0].Actions = 0 }),
			"an entry of no repository":  mutate(func(c *Claims) { c.Grant.Entries[0].Repo = qname.Repo{} }),
			"seventeen entries": mutate(func(c *Claims) {
				for i := 0; i < MaxEntries; i++ {
					c.Grant.Entries = append(c.Grant.Entries, Entry{Repo: webRepo, Name: "n" + string(rune('a'+i)), Actions: Pull})
				}
			}),
		} {
			if _, err := s.Issue(c); err == nil {
				t.Errorf("%s: issued", name)
			}
		}
	})
	t.Run("H-579 claims signed with the ring verify for at most the configured lifetime and are not issued in the future", func(t *testing.T) {
		dir := t.TempDir()
		k := keyFile(t, dir, "k1", 7)
		long, short := signerFor(t, time.Hour, "k1", k), signerFor(t, 5*time.Minute, "k1", k)
		hour := mutate(func(c *Claims) { c.ExpiresAt = c.IssuedAt.Add(time.Hour) })
		tok, err := long.Issue(hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := short.Verify(tok, "default", t0); !errors.Is(err, ErrLifetime) {
			t.Fatalf("a token spanning more than the configured lifetime: %v", err)
		}
		ahead := mutate(func(c *Claims) {
			c.IssuedAt = t0.Add(10 * 365 * 24 * time.Hour)
			c.ExpiresAt = c.IssuedAt.Add(time.Minute)
		})
		if tok, err = short.Issue(ahead); err != nil {
			t.Fatal(err)
		}
		if _, err := short.Verify(tok, "default", t0); !errors.Is(err, ErrLifetime) {
			t.Fatalf("a token issued ten years ahead: %v", err)
		}
		skew := mutate(func(c *Claims) {
			c.IssuedAt = t0.Add(30 * time.Second)
			c.ExpiresAt = c.IssuedAt.Add(time.Minute)
		})
		if tok, err = short.Issue(skew); err != nil {
			t.Fatal(err)
		}
		if _, err := short.Verify(tok, "default", t0); err != nil {
			t.Fatalf("a token issued by a replica whose clock runs 30 s ahead: %v", err)
		}
	})
	t.Run("the lifetime of a signer lies between 1 s and 1 h", func(t *testing.T) {
		ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: []string{keyFile(t, t.TempDir(), "k1", 3)}, ActiveKeyID: "k1"})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range []time.Duration{0, MaxLifetime + time.Second} {
			if _, err := NewSigner(ring, d); err == nil {
				t.Errorf("NewSigner(%s) succeeded", d)
			}
		}
		if _, err := NewSigner(nil, time.Minute); err == nil {
			t.Error("NewSigner without a ring succeeded")
		}
	})
	t.Run("an anonymous token names no principal and no trove token", func(t *testing.T) {
		tok, err := s.Issue(Claims{Anonymous: true, Endpoint: "default", IssuedAt: t0, ExpiresAt: t0.Add(time.Minute), Grant: Grant{Catalog: true}})
		if err != nil {
			t.Fatal(err)
		}
		c, err := s.Verify(tok, "default", t0)
		if err != nil || !c.Anonymous || c.PrincipalID != "" || c.Binding != (Binding{}) || !c.Grant.Catalog {
			t.Fatalf("%+v %v", c, err)
		}
	})
	t.Run("sixteen entries of long names do not fit in 4 KiB: the issue is refused, never cut", func(t *testing.T) {
		c := patClaims()
		c.Grant.Entries = nil
		long := strings.Repeat("a", 230)
		for i := 0; i < MaxEntries; i++ {
			c.Grant.Entries = append(c.Grant.Entries, Entry{Repo: webRepo, Name: long + string(rune('a'+i)), Actions: All})
		}
		if _, err := s.Issue(c); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v", err)
		}
	})
}

// R-OCI-005: the registry token's part is named in the test name by WP3 once /v2/ is mounted and the grant check runs.
func TestR_ARCH_021_RegistryTokenKeyRingRotation(t *testing.T) {
	dir := t.TempDir()
	k1, k2 := keyFile(t, dir, "k1", 1), keyFile(t, dir, "k2", 90)
	before := signer(t, "k1", k1)     // the ring before the rotation
	during := signer(t, "k2", k1, k2) // k2 active, k1 still in the ring
	after := signer(t, "k2", k2)      // k1 removed once nothing sealed under it remains
	old, err := before.Issue(patClaims())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("a token signed before the rotation verifies while its key stays in the ring", func(t *testing.T) {
		if c, err := during.Verify(old, "default", t0); err != nil || c.KeyID != "k1" {
			t.Fatalf("%+v %v", c, err)
		}
	})
	t.Run("new tokens name the new key ID", func(t *testing.T) {
		tok, err := during.Issue(patClaims())
		if err != nil {
			t.Fatal(err)
		}
		c, err := after.Verify(tok, "default", t0)
		if err != nil || c.KeyID != "k2" {
			t.Fatalf("%+v %v", c, err)
		}
	})
	t.Run("once the old key left the ring, the old token is refused", func(t *testing.T) {
		if _, err := after.Verify(old, "default", t0); !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("err = %v", err)
		}
	})
}

// R-OCI-034 (the request log holds no registry token): named in the test name by WP3 once /v2/ is mounted and the grant
// check runs. The scrubber cuts the claims to the prefix and at most four characters; the MAC after the dot stays, worthless
// without the claims it signs.
func TestTheLogScrubberHidesARegistryToken(t *testing.T) {
	s := signer(t, "k1", keyFile(t, t.TempDir(), "k1", 1))
	tok, err := s.Issue(patClaims())
	if err != nil {
		t.Fatal(err)
	}
	scrub := observability.NewScrubber(observability.DefaultQueryParams())
	long := regexp.MustCompile(`trv_rgt_[0-9A-Za-z_\-]{5,}`)
	for _, line := range []string{
		"Authorization: Bearer " + tok,
		"GET /v2/web/docker-local/app/manifests/1?access_token=" + tok,
		"GET /v2/web/docker-local/app/manifests/1?x=" + tok,
		"failed to verify " + tok,
	} {
		if out := scrub.String(line); long.MatchString(out) || strings.Contains(out, tok[len(Prefix):len(Prefix)+16]) {
			t.Errorf("the log keeps the token: %q", out)
		}
	}
}
