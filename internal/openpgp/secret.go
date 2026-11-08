package openpgp

import (
	"bytes"
	"crypto"
	"errors"
	"fmt"
	"io"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// Generate makes an EdDSALegacy (algorithm 22) key over Ed25519, version 4, with no expiry, signing and certifying capability
// and no subkeys, created at created. The binding self-signature over the user ID is made at the same time (Config.Time), so
// the key's BoundSince equals its creation time (Q-DEB-40). The caller passes the PostgreSQL time minus one hour (Q-DEB-30).
// The secret key is returned unprotected: the caller seals it. Generate never makes algorithm 27.
func Generate(uid string, created time.Time) (secretArmored, publicArmored []byte, fp string, err error) {
	created = created.UTC().Truncate(time.Second)
	cfg := &packet.Config{
		Algorithm:   packet.PubKeyAlgoEdDSA,
		Curve:       packet.Curve25519,
		DefaultHash: crypto.SHA512,
		Time:        func() time.Time { return created },
	}
	e, err := pgp.NewEntity(uid, "", "", cfg)
	if err != nil {
		return nil, nil, "", fmt.Errorf("openpgp: generate key: %w", err)
	}
	// NewEntity adds an encryption subkey; a repository key only signs.
	e.Subkeys = nil
	if secretArmored, err = armorEncode(typePrivateKey, func(w io.Writer) error { return e.SerializePrivateWithoutSigning(w, cfg) }); err != nil {
		return nil, nil, "", fmt.Errorf("openpgp: serialize secret key: %w", err)
	}
	if publicArmored, err = armorEncode(typePublicKey, e.Serialize); err != nil {
		return nil, nil, "", fmt.Errorf("openpgp: serialize public key: %w", err)
	}
	return secretArmored, publicArmored, fingerprint(e.PrimaryKey), nil
}

// ParseSecret reads one armored secret key for import (R-DEB-015): version 4, EdDSALegacy over Ed25519 or RSA of at least 3072
// bits, able to sign, not revoked, with no expiry, created and bound (BoundSince) no later than now, and with no subkey that
// would sign instead. A passphrase opens a protected key; it is used once and not kept. Input that does not parse, is too large,
// or needs a passphrase that is wrong or missing answers ErrInvalid; a key the rules refuse answers ErrUnsupportedKey. The Key
// carries the primary key alone: other subkeys are dropped.
func ParseSecret(armored, passphrase []byte, now time.Time, lim Limits) (*Key, error) {
	lim = lim.withDefaults()
	if len(armored) > lim.MaxSecretBytes {
		return nil, invalid("a secret key is at most %d bytes", lim.MaxSecretBytes)
	}
	blocks, err := splitArmor(armored)
	if err != nil {
		return nil, err
	}
	if len(blocks) != 1 {
		return nil, invalid("expected one armored secret key block, got %d", len(blocks))
	}
	body, err := armorDecode(blocks[0], typePrivateKey, lim.MaxSecretBytes)
	if err != nil {
		return nil, err
	}
	ents, err := readEntities(body, 1)
	if err != nil {
		return nil, err
	}
	e := ents[0]
	if e.PrivateKey == nil {
		return nil, invalid("the block holds no secret key material")
	}
	if err := checkImportRules(e, now); err != nil {
		return nil, err
	}
	if err := openPrivate(e, passphrase); err != nil {
		return nil, err
	}
	e.Subkeys = nil
	signer := e.PrivateKey
	if err := selfTest(signer); err != nil {
		return nil, invalid("the secret key does not match its public key: %v", err)
	}
	return newKey(e), nil
}

func newKey(e *pgp.Entity) *Key {
	c := newCertificate(e)
	return &Key{Fingerprint: c.Fingerprint, Algorithm: c.Algorithm, Bits: c.Bits, Created: c.Created, BoundSince: c.BoundSince, entity: e}
}

// SecretArmored returns the key as an unprotected armored secret key: the form Generate returns, for the caller to seal.
func (k Key) SecretArmored() ([]byte, error) {
	cfg := &packet.Config{DefaultHash: crypto.SHA512}
	return armorEncode(typePrivateKey, func(w io.Writer) error { return k.entity.SerializePrivateWithoutSigning(w, cfg) })
}

// readEntities reads the keys of a binary key packet stream. A key the module cannot read is an error, not a skipped entry, so
// a certificate is never dropped silently. More than max entities is an error.
func readEntities(body []byte, max int) ([]*pgp.Entity, error) {
	r := packet.NewReader(bytes.NewReader(body))
	var out []*pgp.Entity
	for {
		e, err := pgp.ReadEntity(r)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, invalid("key: %v", err)
		}
		if len(out) == max {
			return nil, tooMany("keys", max+1, max)
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, invalid("no key found")
	}
	return out, nil
}

func checkImportRules(e *pgp.Entity, now time.Time) error {
	pk := e.PrimaryKey
	if pk.Version != 4 {
		return unsupported("version %d keys are not supported; use a version 4 key", pk.Version)
	}
	switch uint8(pk.PubKeyAlgo) {
	case AlgorithmEdDSALegacy:
		if curve, err := pk.Curve(); err != nil || curve != packet.Curve25519 {
			return unsupported("EdDSA keys must use the curve Ed25519")
		}
	case AlgorithmRSA:
		if bits, err := pk.BitLength(); err != nil || bits < MinImportRSABits {
			return unsupported("RSA keys must have at least %d bits, this one has %d", MinImportRSABits, bits)
		}
	case AlgorithmEd25519:
		return unsupported("public-key algorithm 27 (Ed25519 of RFC 9580) cannot be read by GnuPG 2.2 and 2.4; use EdDSALegacy (22) or RSA of at least %d bits", MinImportRSABits)
	default:
		return unsupported("public-key algorithm %d is not supported", pk.PubKeyAlgo)
	}
	if e.PrivateKey.Dummy() {
		return unsupported("a key whose secret part is held elsewhere cannot sign here")
	}
	if e.Revoked(now) {
		return unsupported("the key is revoked")
	}
	if sig, _ := e.PrimarySelfSignature(); sig != nil && sig.FlagsValid && !sig.FlagSign {
		return unsupported("the key cannot sign")
	}
	if hasExpiry(e) {
		return unsupported("the key has an expiry; remove it before the import")
	}
	if pk.CreationTime.After(now) {
		return unsupported("the key was created in the future (%s)", pk.CreationTime.UTC().Format(time.RFC3339))
	}
	since, ok := boundSince(e)
	if !ok {
		return unsupported("the key has no binding self-signature")
	}
	if since.After(now) {
		return unsupported("the key's earliest binding self-signature lies in the future (%s)", since.UTC().Format(time.RFC3339))
	}
	for _, s := range e.Subkeys {
		if len(s.Revocations) == 0 && canSign(s.PublicKey, s.Sig) {
			return unsupported("the key has a signing subkey, which would sign instead of the primary key")
		}
	}
	return nil
}

// hasExpiry reports whether the current binding self-signature of any identity, or the newest direct-key signature, sets a key
// lifetime.
func hasExpiry(e *pgp.Entity) bool {
	limited := func(s *packet.Signature) bool { return s != nil && s.KeyLifetimeSecs != nil && *s.KeyLifetimeSecs != 0 }
	for _, id := range e.Identities {
		if limited(id.SelfSignature) {
			return true
		}
	}
	var newest *packet.Signature
	for _, s := range e.Signatures {
		if s.SigType == packet.SigTypeDirectSignature && s.CheckKeyIdOrFingerprint(e.PrimaryKey) && (newest == nil || s.CreationTime.After(newest.CreationTime)) {
			newest = s
		}
	}
	return limited(newest)
}

// canSign reports whether a subkey may sign: its binding says so, or says nothing and its algorithm can.
func canSign(pk *packet.PublicKey, binding *packet.Signature) bool {
	if binding != nil && binding.FlagsValid {
		return binding.FlagSign
	}
	return pk.PubKeyAlgo.CanSign()
}

func openPrivate(e *pgp.Entity, passphrase []byte) error {
	if !e.PrivateKey.Encrypted {
		return nil
	}
	if len(passphrase) == 0 {
		return invalid("the secret key is protected by a passphrase, which was not given")
	}
	if err := e.PrivateKey.Decrypt(passphrase); err != nil {
		return invalid("the passphrase does not open the secret key")
	}
	return nil
}

// selfTest signs a byte and checks the signature with the public key, so a corrupt or mismatched key fails at the import and not
// at the first index build.
func selfTest(priv *packet.PrivateKey) error {
	at := priv.CreationTime.Add(time.Second)
	sig := newSignature(priv, packet.SigTypeBinary, at, crypto.SHA512)
	cfg := signConfig(at)
	if err := signData(sig, priv, []byte{0}, cfg); err != nil {
		return err
	}
	h := crypto.SHA512.New()
	_, _ = h.Write([]byte{0})
	return priv.VerifySignature(h, sig)
}
