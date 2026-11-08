package openpgp

import (
	"bytes"
	"io"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// ParseCertificates reads the armored public certificates of an upstream archive (deb.upstreamKeys, R-DEB-020): one or more
// armor blocks holding 1 to Limits.MaxCertificates certificates, in at most Limits.MaxCertificateBytes. A block that does not
// parse, holds secret key material, or holds a certificate that is revoked, expired or not yet valid at now, cannot sign, or is
// an RSA key below 2048 bits answers ErrInvalid with the reason. A certificate listed twice is kept once.
func ParseCertificates(armored []byte, now time.Time, lim Limits) ([]Certificate, error) {
	lim = lim.withDefaults()
	if len(armored) > lim.MaxCertificateBytes {
		return nil, invalid("the keys are at most %d bytes", lim.MaxCertificateBytes)
	}
	blocks, err := splitArmor(armored)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, invalid("no armored block found")
	}
	var (
		certs []Certificate
		seen  = map[string]bool{}
		count int
	)
	for _, block := range blocks {
		if bytes.HasPrefix(block, []byte("-----BEGIN PGP PRIVATE KEY BLOCK-----")) {
			return nil, invalid("the block holds secret key material")
		}
		body, err := armorDecode(block, typePublicKey, lim.MaxCertificateBytes)
		if err != nil {
			return nil, err
		}
		ents, err := readEntities(body, lim.MaxCertificates-count)
		if err != nil {
			return nil, err
		}
		count += len(ents)
		for _, e := range ents {
			if err := checkCertificate(e, now); err != nil {
				return nil, err
			}
			c := newCertificate(e)
			if seen[c.Fingerprint] {
				continue
			}
			seen[c.Fingerprint] = true
			certs = append(certs, c)
		}
	}
	return certs, nil
}

func checkCertificate(e *pgp.Entity, now time.Time) error {
	name := fingerprint(e.PrimaryKey)
	if e.PrivateKey != nil {
		return invalid("certificate %s holds secret key material", name)
	}
	for _, s := range e.Subkeys {
		if s.PrivateKey != nil {
			return invalid("certificate %s holds secret key material", name)
		}
	}
	if e.Revoked(now) {
		return invalid("certificate %s is revoked", name)
	}
	self, _ := e.PrimarySelfSignature()
	if self != nil && e.PrimaryKey.KeyExpired(self, now) {
		return invalid("certificate %s is expired or not yet valid", name)
	}
	if len(signingKeys(e)) == 0 {
		return invalid("certificate %s cannot sign", name)
	}
	for _, s := range signingKeys(e) {
		if uint8(s.pub.PubKeyAlgo) == AlgorithmRSA {
			if bits, err := s.pub.BitLength(); err != nil || bits < MinUpstreamRSABits {
				return invalid("certificate %s has an RSA key below %d bits", name, MinUpstreamRSABits)
			}
		}
	}
	return nil
}

// signerKey is a key of a certificate that can make data signatures: the primary key or a signing subkey.
type signerKey struct {
	pub     *packet.PublicKey
	binding *packet.Signature // the self-signature that carries the key's flags and lifetime
	sub     *pgp.Subkey       // nil for the primary key
}

func signingKeys(e *pgp.Entity) []signerKey {
	var out []signerKey
	self, _ := e.PrimarySelfSignature()
	if canSign(e.PrimaryKey, self) {
		out = append(out, signerKey{pub: e.PrimaryKey, binding: self})
	}
	for i := range e.Subkeys {
		s := &e.Subkeys[i]
		if len(s.Revocations) == 0 && s.Sig != nil && canSign(s.PublicKey, s.Sig) {
			out = append(out, signerKey{pub: s.PublicKey, binding: s.Sig, sub: s})
		}
	}
	return out
}

// ArmorCertificates writes the certificates as one ASCII-armored public key block, the signing-key.asc of a repository. A
// certificate without a key is skipped.
func ArmorCertificates(certs ...Certificate) []byte {
	out, err := armorEncode(typePublicKey, func(w io.Writer) error {
		for _, c := range certs {
			if c.entity == nil {
				continue
			}
			if err := c.entity.Serialize(w); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// Serializing public packets to memory does not fail unless a packet is corrupt, which a parsed certificate is not.
		return nil
	}
	return out
}
