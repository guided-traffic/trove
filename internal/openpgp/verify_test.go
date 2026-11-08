package openpgp

import (
	"bytes"
	"crypto"
	"errors"
	"strings"
	"testing"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

const release = "Origin: trove\nSuite: stable\nDate: Sun, 08 Nov 2026 11:59:00 UTC\nSHA256:\n e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 0 main/binary-amd64/Packages\n"

func TestR_DEB_016_ClearSignVerifiesWithTheModuleAndWithItself(t *testing.T) {
	created := base.Add(-time.Hour)
	k, c := mustGenerate(t, "trove p/r", created)
	at := base.Add(-time.Minute)

	signed, err := ClearSign([]byte(release), []*Key{k}, at)
	if err != nil {
		t.Fatal(err)
	}
	// The module's own reader: the signature verifies and the signed text is the Release text, trailing newline included.
	block, rest := clearsign.Decode(signed)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatal("not a cleartext message")
	}
	if string(block.Plaintext) != release {
		t.Errorf("signed text = %q, want the Release text", block.Plaintext)
	}
	ring, err := pgp.ReadArmoredKeyRing(bytes.NewReader(ArmorCertificates(c)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := block.VerifySignature(ring, &packet.Config{Time: func() time.Time { return base }}); err != nil {
		t.Errorf("the module does not verify the signature: %v", err)
	}
	if got := block.Headers["Hash"]; len(got) != 1 || got[0] != "SHA512" {
		t.Errorf("Hash header = %v, want SHA512", got)
	}
	again, _ := clearsign.Decode(signed)
	sigPacket := firstSignature(t, again.ArmoredSignature.Body)
	if !sigPacket.CreationTime.Equal(at) || sigPacket.Hash != crypto.SHA512 || sigPacket.Version != 4 || sigPacket.SigType != packet.SigTypeText {
		t.Errorf("signature = time %s, hash %v, version %d, type %#x", sigPacket.CreationTime, sigPacket.Hash, sigPacket.Version, sigPacket.SigType)
	}

	text, signer, err := VerifyClear(signed, []Certificate{c}, base, Limits{})
	if err != nil || signer != c.Fingerprint || string(text) != release {
		t.Fatalf("VerifyClear = %q, %q, %v", text, signer, err)
	}
}

func firstSignature(t *testing.T, r interface{ Read([]byte) (int, error) }) *packet.Signature {
	t.Helper()
	p, err := packet.Read(r)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := p.(*packet.Signature)
	if !ok {
		t.Fatalf("packet %T is no signature", p)
	}
	return s
}

func TestR_DEB_016_DetachSignVerifiesWithTheModuleAndWithItself(t *testing.T) {
	k, c := mustGenerate(t, "trove p/r", base.Add(-time.Hour))
	at := base.Add(-time.Minute)
	sig, err := DetachSign([]byte(release), []*Key{k}, at)
	if err != nil {
		t.Fatal(err)
	}
	ring, err := pgp.ReadArmoredKeyRing(bytes.NewReader(ArmorCertificates(c)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgp.CheckArmoredDetachedSignature(ring, strings.NewReader(release), bytes.NewReader(sig), &packet.Config{Time: func() time.Time { return base }}); err != nil {
		t.Errorf("the module does not verify the detached signature: %v", err)
	}
	signer, err := VerifyDetached([]byte(release), sig, []Certificate{c}, base, Limits{})
	if err != nil || signer != c.Fingerprint {
		t.Fatalf("VerifyDetached = %q, %v", signer, err)
	}
	if _, err := VerifyDetached([]byte(release+"x"), sig, []Certificate{c}, base, Limits{}); Reason(err) != ReasonBadSignature {
		t.Errorf("changed data: %v", err)
	}
}

// While a next key exists each file carries two signatures, and either key alone verifies it (R-DEB-017).
func TestR_DEB_016_CoSignedFilesVerifyWithEitherKey(t *testing.T) {
	created := base.Add(-time.Hour)
	k1, c1 := mustGenerate(t, "trove p/r", created)
	k2, c2 := mustGenerate(t, "trove p/r", created)
	at := base.Add(-time.Minute)
	clear, err := ClearSign([]byte(release), []*Key{k1, k2}, at)
	if err != nil {
		t.Fatal(err)
	}
	detached, err := DetachSign([]byte(release), []*Key{k1, k2}, at)
	if err != nil {
		t.Fatal(err)
	}
	for name, certs := range map[string][]Certificate{"k1": {c1}, "k2": {c2}, "both": {c1, c2}} {
		if _, signer, err := VerifyClear(clear, certs, base, Limits{}); err != nil || signer == "" {
			t.Errorf("InRelease with %s: %q, %v", name, signer, err)
		}
		if signer, err := VerifyDetached([]byte(release), detached, certs, base, Limits{}); err != nil || signer == "" {
			t.Errorf("Release.gpg with %s: %q, %v", name, signer, err)
		}
	}
	_, other := mustGenerate(t, "trove x/x", created)
	_, _, err = VerifyClear(clear, []Certificate{other}, base, Limits{})
	if !errors.Is(err, ErrVerify) || Reason(err) != ReasonUnknownKey {
		t.Errorf("a foreign keyring: %v", err)
	}
	if signer, _ := VerifyDetached([]byte(release), detached, []Certificate{c2}, base, Limits{}); signer != c2.Fingerprint {
		t.Errorf("signer = %q, want the second key", signer)
	}
}

// The acceptance criteria of R-DEB-020 and the rules of Q-DEB-35, with signers the module builds.
func TestR_DEB_020_VerificationRules(t *testing.T) {
	now := base
	good := newEntity(t, "A", now.Add(-48*time.Hour), nil)
	goodCert := entityCert(t, good, now)
	unknown := newEntity(t, "C", now.Add(-48*time.Hour), nil)
	other := newEntity(t, "B", now.Add(-48*time.Hour), nil)
	otherCert := entityCert(t, other, now)

	// A key that expired a year ago, with a signature made while it was valid.
	oldCreated := now.Add(-3 * 365 * 24 * time.Hour)
	expired := newEntity(t, "D", oldCreated, func(c *packet.Config) { c.KeyLifetimeSecs = 365 * 24 * 3600 })
	expiredCert := entityCertNoCheck(t, expired)
	// A key created after the signature it made.
	late := newEntity(t, "late", now.Add(-time.Hour), nil)
	lateCert := entityCert(t, late, now)

	at := now.Add(-time.Minute)
	tests := []struct {
		name   string
		msg    func() []byte
		certs  []Certificate
		reason string // empty: must verify
	}{
		{"signed by A, keys [A]", func() []byte { return clearSignWith(t, release, at, crypto.SHA512, good) }, []Certificate{goodCert}, ""},
		{"signed by A, keys [B]", func() []byte { return clearSignWith(t, release, at, crypto.SHA512, good) }, []Certificate{otherCert}, ReasonUnknownKey},
		{"signed by A and unknown C, keys [A]", func() []byte { return clearSignWith(t, release, at, crypto.SHA512, unknown, good) }, []Certificate{goodCert}, ""},
		{"signed by unknown C, keys [A]", func() []byte { return clearSignWith(t, release, at, crypto.SHA512, unknown) }, []Certificate{goodCert}, ReasonUnknownKey},
		{"SHA-1 only", func() []byte { return manualClear(t, good, release, at, crypto.SHA1, "SHA1") }, []Certificate{goodCert}, ReasonWeakHash},
		{"SHA-224", func() []byte { return manualClear(t, good, release, at, crypto.SHA224, "SHA224") }, []Certificate{goodCert}, ReasonWeakHash},
		{"SHA-256 passes", func() []byte { return clearSignWith(t, release, at, crypto.SHA256, good) }, []Certificate{goodCert}, ""},
		{"expired key D, backdated signature", func() []byte {
			return clearSignWith(t, release, oldCreated.Add(30*24*time.Hour), crypto.SHA512, expired)
		}, []Certificate{expiredCert}, ReasonKeyInvalid},
		{"expired key D left beside A, signed by D", func() []byte {
			return clearSignWith(t, release, oldCreated.Add(30*24*time.Hour), crypto.SHA512, expired)
		}, []Certificate{goodCert, expiredCert}, ReasonKeyInvalid},
		{"key created after its signature", func() []byte { return clearSignWith(t, release, now.Add(-2*time.Hour), crypto.SHA512, late) }, []Certificate{lateCert}, ReasonKeyInvalid},
		{"signature 9 minutes ahead", func() []byte { return clearSignWith(t, release, now.Add(9*time.Minute), crypto.SHA512, good) }, []Certificate{goodCert}, ""},
		{"signature 11 minutes ahead", func() []byte { return clearSignWith(t, release, now.Add(11*time.Minute), crypto.SHA512, good) }, []Certificate{goodCert}, ReasonFuture},
		{"changed text", func() []byte {
			return bytes.Replace(clearSignWith(t, release, at, crypto.SHA512, good), []byte("stable"), []byte("sid"), 1)
		}, []Certificate{goodCert}, ReasonBadSignature},
		{"Hash header absent", func() []byte {
			return bytes.Replace(clearSignWith(t, release, at, crypto.SHA512, good), []byte("Hash: SHA512\n"), nil, 1)
		}, []Certificate{goodCert}, ""},
		{"Hash header names another hash", func() []byte {
			return bytes.Replace(clearSignWith(t, release, at, crypto.SHA512, good), []byte("Hash: SHA512\n"), []byte("Hash: SHA256\n"), 1)
		}, []Certificate{goodCert}, ReasonHashHeader},
		{"Hash header lists two hashes", func() []byte {
			return bytes.Replace(clearSignWith(t, release, at, crypto.SHA512, good), []byte("Hash: SHA512\n"), []byte("Hash: SHA256, SHA512\n"), 1)
		}, []Certificate{goodCert}, ""},
		{"text after the message", func() []byte {
			return append(clearSignWith(t, release, at, crypto.SHA512, good), "More text\n"...)
		}, []Certificate{goodCert}, ReasonMalformed},
		{"text before the message", func() []byte {
			return append([]byte("Preface\n"), clearSignWith(t, release, at, crypto.SHA512, good)...)
		}, []Certificate{goodCert}, ReasonMalformed},
		{"not a message", func() []byte { return []byte("Origin: x\n") }, []Certificate{goodCert}, ReasonMalformed},
		{"no certificate", func() []byte { return clearSignWith(t, release, at, crypto.SHA512, good) }, nil, ReasonUnknownKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, signer, err := VerifyClear(tc.msg(), tc.certs, now, Limits{})
			if tc.reason == "" {
				if err != nil || signer != goodCert.Fingerprint || string(text) != release {
					t.Fatalf("VerifyClear = %q, %q, %v", text, signer, err)
				}
				return
			}
			if !errors.Is(err, ErrVerify) || Reason(err) != tc.reason {
				t.Fatalf("err = %v (reason %q), want reason %q", err, Reason(err), tc.reason)
			}
			if text != nil || signer != "" {
				t.Errorf("a failed verification returned text %q and signer %q", text, signer)
			}
		})
	}
}

// entityCertNoCheck is the certificate of an entity ParseCertificates would refuse (an expired one): the verifier decides on the
// certificate it is handed.
func entityCertNoCheck(t *testing.T, e *pgp.Entity) Certificate {
	t.Helper()
	return newCertificate(e)
}

func TestR_DEB_020_DetachedRules(t *testing.T) {
	now := base
	good := newEntity(t, "A", now.Add(-48*time.Hour), nil)
	goodCert := entityCert(t, good, now)
	data := []byte(release)
	at := now.Add(-time.Minute)

	sig := detachSignWith(t, data, at, crypto.SHA512, good)
	if signer, err := VerifyDetached(data, sig, []Certificate{goodCert}, now, Limits{}); err != nil || signer != goodCert.Fingerprint {
		t.Fatalf("armored: %q, %v", signer, err)
	}
	// A binary signature is read as well.
	body := dearmor(t, sig)
	if _, err := VerifyDetached(data, body, []Certificate{goodCert}, now, Limits{}); err != nil {
		t.Errorf("binary: %v", err)
	}
	if _, err := VerifyDetached(data, detachSignWith(t, data, at, crypto.SHA1, good), []Certificate{goodCert}, now, Limits{}); Reason(err) != ReasonWeakHash {
		t.Errorf("SHA-1: %v", err)
	}
	if _, err := VerifyDetached(data, detachSignWith(t, data, now.Add(11*time.Minute), crypto.SHA512, good), []Certificate{goodCert}, now, Limits{}); Reason(err) != ReasonFuture {
		t.Errorf("11 minutes ahead: %v", err)
	}
	if _, err := VerifyDetached(data, []byte("-----BEGIN PGP SIGNATURE-----\n\nAAAA\n-----END PGP SIGNATURE-----\n"), []Certificate{goodCert}, now, Limits{}); !errors.Is(err, ErrVerify) {
		t.Errorf("garbage signature: %v", err)
	}
	if _, err := VerifyDetached(data, nil, []Certificate{goodCert}, now, Limits{}); Reason(err) != ReasonNoSignature {
		t.Errorf("empty signature: %v", err)
	}
	// A text-mode signature is no Release.gpg.
	text := manualClear(t, good, release, at, crypto.SHA512, "SHA512")
	if _, err := VerifyDetached(data, text, []Certificate{goodCert}, now, Limits{}); !errors.Is(err, ErrVerify) {
		t.Errorf("a cleartext message as signature: %v", err)
	}
}

func dearmor(t *testing.T, armored []byte) []byte {
	t.Helper()
	blocks, err := splitArmor(armored)
	if err != nil || len(blocks) != 1 {
		t.Fatal(err)
	}
	body, err := armorDecode(blocks[0], typeSignature, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestR_DEB_034_SignatureAndMessageBounds(t *testing.T) {
	created := base.Add(-time.Hour)
	var keys []*Key
	var certs []Certificate
	for i := 0; i < 33; i++ {
		k, c := mustGenerate(t, "trove p/r", created)
		keys = append(keys, k)
		certs = append(certs, c)
	}
	at := base.Add(-time.Minute)
	ok, err := ClearSign([]byte(release), keys[:32], at)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyClear(ok, certs[31:32], base, Limits{}); err != nil {
		t.Errorf("32 signatures: %v", err)
	}
	many, err := ClearSign([]byte(release), keys, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyClear(many, certs[:1], base, Limits{}); Reason(err) != ReasonTooMany {
		t.Errorf("33 signatures: %v", err)
	}
	detached, err := DetachSign([]byte(release), keys, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDetached([]byte(release), detached, certs[:1], base, Limits{}); Reason(err) != ReasonTooMany {
		t.Errorf("33 detached signatures: %v", err)
	}
	if _, _, err := VerifyClear(ok, certs, base, Limits{MaxMessageBytes: 100}); Reason(err) != ReasonTooMany {
		t.Errorf("a message over the bound: %v", err)
	}
	if _, err := VerifyDetached(bytes.Repeat([]byte("x"), 101), detached, certs, base, Limits{MaxMessageBytes: 100}); Reason(err) != ReasonTooMany {
		t.Errorf("data over the bound: %v", err)
	}
}

func TestR_DEB_020_SigningSubkeyVerifies(t *testing.T) {
	created := base.Add(-48 * time.Hour)
	e := newEntity(t, "with signing subkey", created, nil)
	if err := e.AddSigningSubkey(&packet.Config{Algorithm: packet.PubKeyAlgoEdDSA, DefaultHash: crypto.SHA512, Time: func() time.Time { return created }}); err != nil {
		t.Fatal(err)
	}
	c := entityCert(t, e, base)
	at := base.Add(-time.Minute)
	var buf bytes.Buffer
	w, err := clearsign.Encode(&buf, e.Subkeys[len(e.Subkeys)-1].PrivateKey, &packet.Config{DefaultHash: crypto.SHA512, Time: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(release))
	_ = w.Close()
	_, signer, err := VerifyClear(buf.Bytes(), []Certificate{c}, base, Limits{})
	if err != nil || signer != c.Fingerprint {
		t.Fatalf("signed by a signing subkey: %q, %v", signer, err)
	}
}

func TestR_DEB_020_DebianArchiveKeyringIsAccepted(t *testing.T) {
	armored := armoredDebianKeyring(t)
	certs, err := ParseCertificates(armored, base, Limits{})
	if err != nil {
		t.Fatalf("Debian's archive keyring is refused: %v", err)
	}
	t.Logf("%d certificates in %d armored bytes", len(certs), len(armored))
	if len(certs) < 9 || len(armored) < 70_000 || len(armored) > 256<<10 {
		t.Errorf("%d certificates in %d bytes; the keyring of debian:13 holds 9 in about 75,000", len(certs), len(armored))
	}
	for _, c := range certs {
		if c.Fingerprint == "" || c.Created.IsZero() || c.BoundSince.IsZero() {
			t.Errorf("incomplete certificate %+v", c)
		}
	}
	// The same keyring one byte over the bound is refused.
	if _, err := ParseCertificates(append(armored, bytes.Repeat([]byte("\n"), 256<<10)...), base, Limits{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an oversized keyring: %v", err)
	}
}
