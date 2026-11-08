package openpgp

import (
	"bytes"
	"crypto"
	"fmt"
	"strings"
	"testing"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// The tests build their keys and odd signatures with the module directly, so the package under test is checked against input it
// did not produce itself.

var base = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// newEntity makes an EdDSALegacy entity created at created; mutate adjusts the configuration before it is used.
func newEntity(t *testing.T, uid string, created time.Time, mutate func(*packet.Config)) *pgp.Entity {
	t.Helper()
	cfg := &packet.Config{
		Algorithm:   packet.PubKeyAlgoEdDSA,
		Curve:       packet.Curve25519,
		DefaultHash: crypto.SHA512,
		Time:        func() time.Time { return created },
	}
	if mutate != nil {
		mutate(cfg)
	}
	e, err := pgp.NewEntity(uid, "", "", cfg)
	if err != nil {
		t.Fatalf("NewEntity: %v", err)
	}
	return e
}

func secretBlock(t *testing.T, e *pgp.Entity) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, pgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SerializePrivateWithoutSigning(w, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return append(buf.Bytes(), '\n')
}

func publicBlock(t *testing.T, e *pgp.Entity) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, pgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return append(buf.Bytes(), '\n')
}

func mustGenerate(t *testing.T, uid string, created time.Time) (*Key, Certificate) {
	t.Helper()
	secret, public, fp, err := Generate(uid, created)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	k, err := ParseSecret(secret, nil, created.Add(time.Hour), Limits{})
	if err != nil {
		t.Fatalf("ParseSecret: %v", err)
	}
	certs, err := ParseCertificates(public, created.Add(time.Hour), Limits{})
	if err != nil || len(certs) != 1 {
		t.Fatalf("ParseCertificates: %v, %d certificates", err, len(certs))
	}
	if k.Fingerprint != fp || certs[0].Fingerprint != fp {
		t.Fatalf("fingerprints differ: %s, %s, %s", k.Fingerprint, certs[0].Fingerprint, fp)
	}
	return k, certs[0]
}

// entityCert is the Certificate of an entity the test built.
func entityCert(t *testing.T, e *pgp.Entity, now time.Time) Certificate {
	t.Helper()
	certs, err := ParseCertificates(publicBlock(t, e), now, Limits{})
	if err != nil {
		t.Fatalf("ParseCertificates: %v", err)
	}
	return certs[0]
}

// clearSignWith signs text with the module's own encoder at at.
func clearSignWith(t *testing.T, text string, at time.Time, hash crypto.Hash, es ...*pgp.Entity) []byte {
	t.Helper()
	var keys []*packet.PrivateKey
	for _, e := range es {
		keys = append(keys, e.PrivateKey)
	}
	var out bytes.Buffer
	w, err := clearsign.EncodeMulti(&out, keys, &packet.Config{DefaultHash: hash, Time: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// manualClear frames and signs by hand, for hashes the encoder does not offer. header is the Hash header's value, empty for none.
func manualClear(t *testing.T, e *pgp.Entity, text string, at time.Time, hash crypto.Hash, header string) []byte {
	t.Helper()
	sig := newSignature(e.PrivateKey, packet.SigTypeText, at, hash)
	if err := signData(sig, e.PrivateKey, []byte(strings.TrimSuffix(text, "\n")), plainConfig(at)); err != nil {
		t.Fatal(err)
	}
	var sigBuf bytes.Buffer
	w, err := armor.Encode(&sigBuf, "PGP SIGNATURE", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sig.Serialize(w); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	out.WriteString("-----BEGIN PGP SIGNED MESSAGE-----\n")
	if header != "" {
		out.WriteString("Hash: " + header + "\n")
	}
	out.WriteString("\n" + strings.TrimSuffix(text, "\n") + "\n" + sigBuf.String())
	return out.Bytes()
}

func detachSignWith(t *testing.T, data []byte, at time.Time, hash crypto.Hash, es ...*pgp.Entity) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, "PGP SIGNATURE", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		sig := newSignature(e.PrivateKey, packet.SigTypeBinary, at, hash)
		if err := signData(sig, e.PrivateKey, data, plainConfig(at)); err != nil {
			t.Fatal(err)
		}
		if err := sig.Serialize(w); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fmtV(format string, v any) string { return fmt.Sprintf(format, v) }

// plainConfig signs without the random salt notation, which the module cannot make for SHA-1.
func plainConfig(at time.Time) *packet.Config {
	off := false
	cfg := signConfig(at)
	cfg.NonDeterministicSignaturesViaNotation = &off
	return cfg
}
