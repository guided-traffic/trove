package openpgp

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func TestR_DEB_015_GeneratedKeyShape(t *testing.T) {
	created := base.Add(-time.Hour)
	secret, public, fp, err := Generate("trove proj/repo", created)
	if err != nil {
		t.Fatal(err)
	}
	if len(fp) != 40 || fp != strings.ToUpper(fp) {
		t.Fatalf("fingerprint %q is not 40 upper-case hex digits", fp)
	}
	for _, armored := range [][]byte{secret, public} {
		ents, err := pgp.ReadArmoredKeyRing(bytes.NewReader(armored))
		if err != nil || len(ents) != 1 {
			t.Fatalf("read back: %v, %d entities", err, len(ents))
		}
		e := ents[0]
		if got := uint8(e.PrimaryKey.PubKeyAlgo); got != 22 {
			t.Errorf("public-key algorithm octet = %d, want 22 (EdDSALegacy)", got)
		}
		if e.PrimaryKey.Version != 4 {
			t.Errorf("key version = %d, want 4", e.PrimaryKey.Version)
		}
		if curve, err := e.PrimaryKey.Curve(); err != nil || curve != packet.Curve25519 {
			t.Errorf("curve = %q, %v, want Ed25519", curve, err)
		}
		if !e.PrimaryKey.CreationTime.Equal(created) {
			t.Errorf("creation = %s, want %s", e.PrimaryKey.CreationTime, created)
		}
		if len(e.Subkeys) != 0 {
			t.Errorf("%d subkeys, want none", len(e.Subkeys))
		}
		if len(e.Identities) != 1 || e.Identities["trove proj/repo"] == nil {
			t.Fatalf("identities = %v, want the one user ID", e.Identities)
		}
		self := e.Identities["trove proj/repo"].SelfSignature
		if !self.CreationTime.Equal(created) {
			t.Errorf("binding self-signature time = %s, want the key's creation %s", self.CreationTime, created)
		}
		if self.KeyLifetimeSecs != nil && *self.KeyLifetimeSecs != 0 {
			t.Errorf("the key has an expiry of %d s", *self.KeyLifetimeSecs)
		}
		if !self.FlagsValid || !self.FlagSign {
			t.Errorf("the self-signature does not mark the key as a signing key")
		}
	}
	if !bytes.Contains(secret, []byte("PRIVATE KEY BLOCK")) || bytes.Contains(public, []byte("PRIVATE KEY")) {
		t.Errorf("the armor types are wrong")
	}

	k, err := ParseSecret(secret, nil, base, Limits{})
	if err != nil {
		t.Fatalf("a generated key does not import: %v", err)
	}
	if k.Algorithm != AlgorithmEdDSALegacy || k.Bits != 256 || !k.Created.Equal(created) || !k.BoundSince.Equal(created) {
		t.Errorf("Key = algorithm %d, %d bits, created %s, bound %s; want 22, 256, %s twice", k.Algorithm, k.Bits, k.Created, k.BoundSince, created)
	}
	c := k.Certificate()
	if c.Fingerprint != fp || !c.BoundSince.Equal(created) {
		t.Errorf("Certificate = %s, bound %s", c.Fingerprint, c.BoundSince)
	}
}

func TestR_DEB_015_GenerateTwiceGivesTwoKeys(t *testing.T) {
	_, _, a, _ := Generate("trove p/r", base)
	_, _, b, _ := Generate("trove p/r", base)
	if a == b {
		t.Fatal("two generated keys share a fingerprint")
	}
}

func TestR_DEB_015_SecretRoundTrip(t *testing.T) {
	k, _ := mustGenerate(t, "trove p/r", base.Add(-time.Hour))
	again, err := k.SecretArmored()
	if err != nil {
		t.Fatal(err)
	}
	k2, err := ParseSecret(again, nil, base, Limits{})
	if err != nil || k2.Fingerprint != k.Fingerprint {
		t.Fatalf("round trip: %v", err)
	}
}

func TestR_DEB_015_KeyNeverPrints(t *testing.T) {
	k, _ := mustGenerate(t, "trove p/r", base.Add(-time.Hour))
	secret, _ := k.SecretArmored()
	for _, s := range []string{k.String(), k.GoString(), sprintAll(k), sprintAll(*k)} {
		if strings.Contains(s, "PRIVATE") || !strings.Contains(s, k.Fingerprint) {
			t.Errorf("rendering %q leaks or lacks the fingerprint", s)
		}
	}
	if bytes.Contains(secret, []byte(k.Fingerprint)) {
		t.Log("armored text is opaque, as expected")
	}
}

func sprintAll(v any) string {
	return strings.Join([]string{fmtV("%v", v), fmtV("%+v", v), fmtV("%#v", v), fmtV("%s", v)}, " ")
}

func TestR_DEB_015_ImportRules(t *testing.T) {
	now := base
	created := now.Add(-48 * time.Hour)

	rsa := func(bits int) *pgp.Entity {
		return newEntity(t, "rsa", created, func(c *packet.Config) { c.Algorithm = packet.PubKeyAlgoRSA; c.RSABits = bits })
	}
	withExpiry := newEntity(t, "expiry", created, func(c *packet.Config) { c.KeyLifetimeSecs = 365 * 24 * 3600 })
	alg27 := newEntity(t, "alg27", created, func(c *packet.Config) { c.Algorithm = packet.PubKeyAlgoEd25519 })
	v6 := newEntity(t, "v6", created, func(c *packet.Config) { c.V6Keys = true; c.Algorithm = packet.PubKeyAlgoEd25519 })
	future := newEntity(t, "future", now.Add(time.Hour), nil)
	withSigningSubkey := newEntity(t, "subkey", created, nil)
	if err := withSigningSubkey.AddSigningSubkey(&packet.Config{Algorithm: packet.PubKeyAlgoEdDSA, Time: func() time.Time { return created }}); err != nil {
		t.Fatal(err)
	}
	// An encryption subkey is what GnuPG adds by default and is accepted.
	withEncryptionSubkey := newEntity(t, "encryption subkey", created, nil)
	futureBinding := newEntity(t, "future binding", created, nil)
	rebind(t, futureBinding, now.Add(time.Hour))

	tests := []struct {
		name   string
		armor  []byte
		wantIs error
		reason string
	}{
		{"RSA 2048", secretBlock(t, rsa(2048)), ErrUnsupportedKey, "at least 3072"},
		{"RSA 3072", secretBlock(t, rsa(3072)), nil, ""},
		{"algorithm 27", secretBlock(t, alg27), ErrUnsupportedKey, "algorithm 27"},
		{"version 6", secretBlock(t, v6), ErrUnsupportedKey, "version 6"},
		{"an expiry", secretBlock(t, withExpiry), ErrUnsupportedKey, "expiry"},
		{"created in the future", secretBlock(t, future), ErrUnsupportedKey, "future"},
		{"binding self-signature in the future", secretBlock(t, futureBinding), ErrUnsupportedKey, "binding self-signature"},
		{"a signing subkey", secretBlock(t, withSigningSubkey), ErrUnsupportedKey, "signing subkey"},
		{"EdDSALegacy with an encryption subkey", secretBlock(t, withEncryptionSubkey), nil, ""},
		{"a public key block", publicBlock(t, newEntity(t, "pub", created, nil)), ErrInvalid, "PGP PUBLIC KEY BLOCK"},
		{"no armor", []byte("not a key"), ErrInvalid, "outside"},
		{"empty", nil, ErrInvalid, ""},
		{"two blocks", append(secretBlock(t, withExpiry), secretBlock(t, withExpiry)...), ErrInvalid, "one armored"},
		{"too large", bytes.Repeat([]byte("A"), 64<<10+1), ErrInvalid, "at most"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, err := ParseSecret(tc.armor, nil, now, Limits{})
			if tc.wantIs == nil {
				if err != nil {
					t.Fatalf("ParseSecret: %v", err)
				}
				if len(k.entity.Subkeys) != 0 {
					t.Errorf("the key kept %d subkeys", len(k.entity.Subkeys))
				}
				return
			}
			if !errors.Is(err, tc.wantIs) {
				t.Fatalf("err = %v, want %v", err, tc.wantIs)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("err = %q does not give the reason %q", err, tc.reason)
			}
		})
	}
}

// rebind replaces the identity self-signatures of an entity by ones made at at: an operator who removed an expiry with GnuPG
// just before the import has such a key.
func rebind(t *testing.T, e *pgp.Entity, at time.Time) {
	t.Helper()
	for _, id := range e.Identities {
		sig := *id.SelfSignature
		sig.CreationTime = at
		if err := sig.SignUserId(id.Name, e.PrimaryKey, e.PrivateKey, &packet.Config{DefaultHash: id.SelfSignature.Hash, Time: func() time.Time { return at }}); err != nil {
			t.Fatal(err)
		}
		id.SelfSignature = &sig
		id.Signatures = []*packet.Signature{&sig}
	}
}

func TestR_DEB_015_RevokedKeyIsRefused(t *testing.T) {
	e := newEntity(t, "revoked", base.Add(-48*time.Hour), nil)
	if err := e.RevokeKey(packet.NoReason, "test", &packet.Config{Time: func() time.Time { return base.Add(-time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSecret(secretBlock(t, e), nil, base, Limits{}); !errors.Is(err, ErrUnsupportedKey) || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("err = %v, want a revoked-key refusal", err)
	}
}

func TestR_DEB_015_PassphraseProtectedKey(t *testing.T) {
	e := newEntity(t, "protected", base.Add(-48*time.Hour), nil)
	if err := e.EncryptPrivateKeys([]byte("hunter2"), nil); err != nil {
		t.Fatal(err)
	}
	armored := secretBlock(t, e)
	if _, err := ParseSecret(armored, nil, base, Limits{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("without the passphrase: %v", err)
	}
	if _, err := ParseSecret(armored, []byte("wrong"), base, Limits{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("with a wrong passphrase: %v", err)
	}
	k, err := ParseSecret(armored, []byte("hunter2"), base, Limits{})
	if err != nil {
		t.Fatalf("with the passphrase: %v", err)
	}
	plain, err := k.SecretArmored()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSecret(plain, nil, base, Limits{}); err != nil {
		t.Errorf("the key stored without its passphrase does not import: %v", err)
	}
}

// An imported key whose only self-signature is fresh: the key is a day old, the binding is 30 s old (Q-DEB-40).
func TestR_DEB_016_BoundSinceOfAFreshSelfSignature(t *testing.T) {
	created := base.Add(-24 * time.Hour)
	bound := base.Add(-30 * time.Second)
	e := newEntity(t, "rebound", created, nil)
	rebind(t, e, bound)
	k, err := ParseSecret(secretBlock(t, e), nil, base, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !k.Created.Equal(created) || !k.BoundSince.Equal(bound) {
		t.Fatalf("Created = %s, BoundSince = %s, want %s and %s", k.Created, k.BoundSince, created, bound)
	}
	if want := bound.Add(time.Second); !k.EarliestSignature().Equal(want) {
		t.Fatalf("EarliestSignature = %s, want %s", k.EarliestSignature(), want)
	}
	for _, at := range []time.Time{base.Add(-60 * time.Second), bound, bound.Add(500 * time.Millisecond)} {
		if _, err := ClearSign([]byte("x\n"), []*Key{k}, at); !errors.Is(err, ErrTooEarly) {
			t.Errorf("ClearSign at %s: %v, want ErrTooEarly", at, err)
		}
		if _, err := DetachSign([]byte("x\n"), []*Key{k}, at); !errors.Is(err, ErrTooEarly) {
			t.Errorf("DetachSign at %s: %v, want ErrTooEarly", at, err)
		}
	}
	for _, at := range []time.Time{bound.Add(time.Second), base} {
		if _, err := ClearSign([]byte("x\n"), []*Key{k}, at); err != nil {
			t.Errorf("ClearSign at %s: %v", at, err)
		}
	}
}

func TestR_DEB_016_SigningWaitsForTheKeyToExist(t *testing.T) {
	created := base.Add(-time.Hour)
	k, _ := mustGenerate(t, "trove p/r", created)
	if _, err := ClearSign([]byte("x\n"), []*Key{k}, created); !errors.Is(err, ErrTooEarly) {
		t.Errorf("signing at the creation time: %v, want ErrTooEarly", err)
	}
	if _, err := ClearSign([]byte("x\n"), []*Key{k}, created.Add(time.Second)); err != nil {
		t.Errorf("signing one second after the creation: %v", err)
	}
	if _, err := ClearSign([]byte("x\n"), nil, base); err == nil {
		t.Error("signing with no key succeeded")
	}
}

func TestR_DEB_034_CertificateBounds(t *testing.T) {
	var certs []Certificate
	for i := 0; i < 33; i++ {
		_, c := mustGenerate(t, "trove p/r", base.Add(-time.Hour))
		certs = append(certs, c)
	}
	if _, err := ParseCertificates(ArmorCertificates(certs[:32]...), base, Limits{}); err != nil {
		t.Errorf("32 certificates: %v", err)
	}
	if _, err := ParseCertificates(ArmorCertificates(certs...), base, Limits{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "at most 32") {
		t.Errorf("33 certificates: %v", err)
	}
	// Several blocks count together.
	var two []byte
	for _, c := range certs[:20] {
		two = append(two, ArmorCertificates(c)...)
	}
	if _, err := ParseCertificates(two, base, Limits{MaxCertificates: 16}); !errors.Is(err, ErrInvalid) {
		t.Errorf("20 certificates in 20 blocks with a bound of 16: %v", err)
	}
	if got, err := ParseCertificates(two, base, Limits{}); err != nil || len(got) != 20 {
		t.Errorf("20 certificates in 20 blocks: %v, %d", err, len(got))
	}
	big := bytes.Repeat([]byte("A"), 256<<10+1)
	if _, err := ParseCertificates(big, base, Limits{}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "262144") {
		t.Errorf("257 KiB: %v", err)
	}
	if _, err := ParseCertificates(nil, base, Limits{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("no input: %v", err)
	}
}

func TestR_DEB_020_CertificateRules(t *testing.T) {
	now := base
	created := now.Add(-48 * time.Hour)
	expired := newEntity(t, "expired", now.Add(-4*365*24*time.Hour), func(c *packet.Config) { c.KeyLifetimeSecs = 365 * 24 * 3600 })
	revoked := newEntity(t, "revoked", created, nil)
	if err := revoked.RevokeKey(packet.NoReason, "", &packet.Config{Time: func() time.Time { return now.Add(-time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	rsa1536 := newEntity(t, "rsa", created, func(c *packet.Config) { c.Algorithm = packet.PubKeyAlgoRSA; c.RSABits = 1536 })
	rsa2048 := newEntity(t, "rsa", created, func(c *packet.Config) { c.Algorithm = packet.PubKeyAlgoRSA; c.RSABits = 2048 })
	notYet := newEntity(t, "future", now.Add(time.Hour), nil)
	alg27 := newEntity(t, "alg27", created, func(c *packet.Config) { c.Algorithm = packet.PubKeyAlgoEd25519 })
	secret := newEntity(t, "secret", created, nil)

	tests := []struct {
		name   string
		armor  []byte
		reason string
	}{
		{"expired", publicBlock(t, expired), "expired"},
		{"revoked", publicBlock(t, revoked), "revoked"},
		{"RSA below 2048", publicBlock(t, rsa1536), "RSA key below 2048"},
		{"not yet valid", publicBlock(t, notYet), "not yet valid"},
		{"a secret key block", secretBlock(t, secret), "secret key material"},
		{"junk between blocks", append(append(publicBlock(t, rsa2048), "junk\n"...), publicBlock(t, rsa2048)...), "outside"},
		{"junk after an end line", append(bytes.TrimRight(publicBlock(t, rsa2048), "\n"), " junk\n"...), "after the end"},
		{"unterminated", []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nAAAA\n"), "not closed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCertificates(tc.armor, now, Limits{})
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("err = %v, want ErrInvalid with %q", err, tc.reason)
			}
		})
	}
	for name, e := range map[string]*pgp.Entity{"RSA 2048": rsa2048, "algorithm 27": alg27} {
		got, err := ParseCertificates(publicBlock(t, e), now, Limits{})
		if err != nil || len(got) != 1 {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A certificate listed twice is kept once.
	got, err := ParseCertificates(append(publicBlock(t, rsa2048), publicBlock(t, rsa2048)...), now, Limits{})
	if err != nil || len(got) != 1 {
		t.Errorf("a certificate twice: %v, %d", err, len(got))
	}
}

func TestR_DEB_015_ArmorCertificatesRoundTrip(t *testing.T) {
	_, a := mustGenerate(t, "trove a/a", base.Add(-time.Hour))
	_, b := mustGenerate(t, "trove b/b", base.Add(-time.Hour))
	armored := ArmorCertificates(a, b, Certificate{})
	if !bytes.HasPrefix(armored, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) || bytes.Contains(armored, []byte("PRIVATE")) {
		t.Fatalf("armor = %q", armored[:60])
	}
	got, err := ParseCertificates(armored, base, Limits{})
	if err != nil || len(got) != 2 || got[0].Fingerprint != a.Fingerprint || got[1].Fingerprint != b.Fingerprint {
		t.Fatalf("round trip: %v, %v", err, got)
	}
	if blk, err := armor.Decode(bytes.NewReader(armored)); err != nil || blk.Type != pgp.PublicKeyType {
		t.Errorf("armor type: %v", err)
	}
}
