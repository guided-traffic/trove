// Package openpgp is trove's only door to the OpenPGP module (Q-ARCH-383, Q-DEB-24). It signs the Release documents of local
// and virtual Debian repositories with keys trove generates or imports, and it verifies the signatures of the documents a remote
// repository's upstream signs. Nothing outside this package imports the module.
//
// The package is a leaf: it reads no clock (every function takes the time it decides against), writes no log line and has no
// state. Every input is untrusted and bounded by Limits (R-DEB-034).
//
// Keys are version 4 and use public-key algorithm 22 (EdDSALegacy) over Ed25519, or RSA of at least 3072 bits when imported;
// algorithm 27 and version 6 keys are refused because the verifiers of Debian 12 and of Ubuntu 22.04 and 24.04 cannot read them
// (Q-DEB-10). A generated key's binding self-signature carries the key's creation time, and a signature is never made earlier
// than one second after the later of the key's creation and its earliest binding self-signature (Q-DEB-40).
package openpgp

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// The OpenPGP public-key algorithm numbers of RFC 9580 that matter here.
const (
	// AlgorithmRSA is RSA (encrypt or sign).
	AlgorithmRSA uint8 = 1
	// AlgorithmEdDSALegacy is EdDSA over Ed25519 as GnuPG 2.1 and later write it (algorithm 22).
	AlgorithmEdDSALegacy uint8 = 22
	// AlgorithmEd25519 is the Ed25519 of RFC 9580 (algorithm 27), which trove never generates or imports.
	AlgorithmEd25519 uint8 = 27
)

// The least RSA modulus an imported signing key may have, and the least a configured upstream certificate may have.
const (
	MinImportRSABits   = 3072
	MinUpstreamRSABits = 2048
)

// ClockSkew is how far after the verifier's now a signature's creation time may lie (Q-DEB-35).
const ClockSkew = 10 * time.Minute

// Limits are the bounds on untrusted OpenPGP input (R-DEB-034). A zero field means the bound of DefaultLimits.
type Limits struct {
	// MaxCertificateBytes bounds the armored text of ParseCertificates.
	MaxCertificateBytes int
	// MaxCertificates bounds the number of certificates in it.
	MaxCertificates int
	// MaxSecretBytes bounds the armored text of ParseSecret.
	MaxSecretBytes int
	// MaxSignatures bounds the signature packets of one cleartext message or detached signature.
	MaxSignatures int
	// MaxMessageBytes bounds a signed document: the cleartext message of VerifyClear or the data of VerifyDetached.
	MaxMessageBytes int
}

// DefaultLimits are the bounds of R-DEB-034: 256 KiB and 32 certificates for the keys of a remote, 64 KiB for an imported secret
// key, 32 signatures and 16 MiB for a release document.
func DefaultLimits() Limits {
	return Limits{
		MaxCertificateBytes: 256 << 10,
		MaxCertificates:     32,
		MaxSecretBytes:      64 << 10,
		MaxSignatures:       32,
		MaxMessageBytes:     16 << 20,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxCertificateBytes <= 0 {
		l.MaxCertificateBytes = d.MaxCertificateBytes
	}
	if l.MaxCertificates <= 0 {
		l.MaxCertificates = d.MaxCertificates
	}
	if l.MaxSecretBytes <= 0 {
		l.MaxSecretBytes = d.MaxSecretBytes
	}
	if l.MaxSignatures <= 0 {
		l.MaxSignatures = d.MaxSignatures
	}
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = d.MaxMessageBytes
	}
	return l
}

// The classes of error. A caller tests with errors.Is; the wrapped text names the reason and is safe to show to the person who
// sent the input (it never holds key material).
var (
	// ErrInvalid reports input that does not parse as what it should be, is too large, holds secret material where only public
	// material is allowed, or needs a passphrase that was not given.
	ErrInvalid = errors.New("openpgp: invalid input")
	// ErrUnsupportedKey reports a secret key that parses but breaks the import rules of R-DEB-015.
	ErrUnsupportedKey = errors.New("openpgp: unsupported key")
	// ErrTooEarly reports a signature time before the keys can sign (Q-DEB-40).
	ErrTooEarly = errors.New("openpgp: signature time is before the key can sign")
	// ErrVerify reports a document no signature of a configured certificate verifies (R-DEB-020).
	ErrVerify = errors.New("openpgp: signature verification failed")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupportedKey, fmt.Sprintf(format, args...))
}

// Key is an opened secret key: the primary key of one certificate, its private part decrypted, in memory for the signing call.
// It prints and logs as its fingerprint; the private part is never rendered.
type Key struct {
	// Fingerprint is the 40 upper-case hexadecimal digits of the primary key's fingerprint.
	Fingerprint string
	// Algorithm is the public-key algorithm number of the primary key.
	Algorithm uint8
	// Bits is the key size: 256 for Ed25519, the modulus size for RSA.
	Bits int
	// Created is the key's creation time.
	Created time.Time
	// BoundSince is the creation time of the earliest valid binding self-signature (Q-DEB-40).
	BoundSince time.Time

	entity *pgp.Entity
}

// String renders the key as its fingerprint.
func (k Key) String() string { return "openpgp.Key{" + k.Fingerprint + "}" }

// GoString renders the key as its fingerprint, for %#v.
func (k Key) GoString() string { return k.String() }

// LogValue logs the key as its fingerprint.
func (k Key) LogValue() slog.Value { return slog.StringValue(k.Fingerprint) }

// Certificate is the public part of a key: what clients and remotes trust. The zero value holds no key.
type Certificate struct {
	// Fingerprint is the upper-case hexadecimal fingerprint of the primary key (40 digits for version 4).
	Fingerprint string
	// Algorithm is the public-key algorithm number of the primary key.
	Algorithm uint8
	// Bits is the key size: 256 for the elliptic curves of the 22 and 27 algorithms, the modulus size for RSA.
	Bits int
	// Created is the primary key's creation time.
	Created time.Time
	// BoundSince is the creation time of the earliest valid binding self-signature (Q-DEB-40).
	BoundSince time.Time

	entity *pgp.Entity
}

// Certificate returns the public part of the key.
func (k Key) Certificate() Certificate { return newCertificate(k.entity) }

func newCertificate(e *pgp.Entity) Certificate {
	pub := publicCopy(e)
	since, _ := boundSince(pub)
	return Certificate{
		Fingerprint: fingerprint(pub.PrimaryKey),
		Algorithm:   uint8(pub.PrimaryKey.PubKeyAlgo),
		Bits:        keyBits(pub.PrimaryKey),
		Created:     pub.PrimaryKey.CreationTime.UTC(),
		BoundSince:  since.UTC(),
		entity:      pub,
	}
}

// publicCopy is the entity without any private material, sharing the public packets.
func publicCopy(e *pgp.Entity) *pgp.Entity {
	out := *e
	out.PrivateKey = nil
	out.Subkeys = make([]pgp.Subkey, len(e.Subkeys))
	for i, s := range e.Subkeys {
		s.PrivateKey = nil
		out.Subkeys[i] = s
	}
	return &out
}

func fingerprint(pk *packet.PublicKey) string {
	const hexDigits = "0123456789ABCDEF"
	out := make([]byte, 0, 2*len(pk.Fingerprint))
	for _, b := range pk.Fingerprint {
		out = append(out, hexDigits[b>>4], hexDigits[b&15])
	}
	return string(out)
}

// bindingSignatures are the self-signatures that bind the primary key to its identities, and its direct-key signatures. The
// module verified each of them while reading the entity.
func bindingSignatures(e *pgp.Entity) []*packet.Signature {
	var out []*packet.Signature
	for _, id := range e.Identities {
		for _, s := range id.Signatures {
			if s.SigType == packet.SigTypeCertificationRevocation || !s.CheckKeyIdOrFingerprint(e.PrimaryKey) {
				continue
			}
			out = append(out, s)
		}
	}
	for _, s := range e.Signatures {
		if s.SigType == packet.SigTypeDirectSignature && s.CheckKeyIdOrFingerprint(e.PrimaryKey) {
			out = append(out, s)
		}
	}
	return out
}

// boundSince is the creation time of the earliest binding self-signature; false when there is none.
func boundSince(e *pgp.Entity) (time.Time, bool) {
	var since time.Time
	for _, s := range bindingSignatures(e) {
		if since.IsZero() || s.CreationTime.Before(since) {
			since = s.CreationTime
		}
	}
	return since, !since.IsZero()
}

// keyBits is the size of a key: the curve's 256 bits for the algorithms 22 and 27, the modulus size for RSA.
func keyBits(pk *packet.PublicKey) int {
	switch uint8(pk.PubKeyAlgo) {
	case AlgorithmEdDSALegacy, AlgorithmEd25519:
		return 256
	}
	bits, err := pk.BitLength()
	if err != nil {
		return 0
	}
	return int(bits)
}
