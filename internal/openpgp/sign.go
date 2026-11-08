package openpgp

import (
	"bytes"
	"crypto"
	"fmt"
	"io"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// EarliestSignature is the first time the key can sign so that a client's verifier accepts the signature: one second after the
// later of the key's creation and its earliest binding self-signature (Q-DEB-40). The index build waits until it has passed.
func (k Key) EarliestSignature() time.Time {
	return laterOf(k.Created, k.BoundSince).Add(time.Second)
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func signConfig(at time.Time) *packet.Config {
	return &packet.Config{DefaultHash: crypto.SHA512, Time: func() time.Time { return at }}
}

func newSignature(k *packet.PrivateKey, typ packet.SignatureType, at time.Time, h crypto.Hash) *packet.Signature {
	id := k.KeyId
	return &packet.Signature{
		Version:           k.Version,
		SigType:           typ,
		PubKeyAlgo:        k.PubKeyAlgo,
		Hash:              h,
		CreationTime:      at,
		IssuerKeyId:       &id,
		IssuerFingerprint: k.Fingerprint,
		IssuerKeyVersion:  uint8(k.Version), //nolint:gosec // the version of a key the module read is 4 or 6
	}
}

// signData signs data (as text or binary, by the signature's type) and leaves the signature in sig.
func signData(sig *packet.Signature, priv *packet.PrivateKey, data []byte, cfg *packet.Config) error {
	h, err := sig.PrepareSign(cfg)
	if err != nil {
		return err
	}
	var w io.Writer = h
	if sig.SigType == packet.SigTypeText {
		w = pgp.NewCanonicalTextHash(h)
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return sig.Sign(h, priv, cfg)
}

func checkSigningTime(keys []*Key, at time.Time) ([]*packet.PrivateKey, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("openpgp: no signing key")
	}
	privs := make([]*packet.PrivateKey, 0, len(keys))
	for _, k := range keys {
		if k == nil || k.entity == nil || k.entity.PrivateKey == nil {
			return nil, fmt.Errorf("openpgp: a signing key is not opened")
		}
		if at.Before(k.EarliestSignature()) {
			return nil, fmt.Errorf("%w: key %s can sign from %s, not at %s", ErrTooEarly, k.Fingerprint,
				k.EarliestSignature().UTC().Format(time.RFC3339), at.UTC().Format(time.RFC3339))
		}
		privs = append(privs, k.entity.PrivateKey)
	}
	return privs, nil
}

// ClearSign signs text in the cleartext signature framework (RFC 9580 section 7, signature type 0x01) with every key, SHA-512,
// each signature made at at. at must be at least one second after the later of each key's creation and BoundSince, else
// ErrTooEarly. The signed text is text byte for byte, so a text that ends in a newline is signed with it.
func ClearSign(text []byte, keys []*Key, at time.Time) ([]byte, error) {
	at = at.UTC().Truncate(time.Second)
	privs, err := checkSigningTime(keys, at)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	w, err := clearsign.EncodeMulti(&out, privs, signConfig(at))
	if err != nil {
		return nil, fmt.Errorf("openpgp: clear-sign: %w", err)
	}
	if _, err := w.Write(text); err != nil {
		return nil, fmt.Errorf("openpgp: clear-sign: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("openpgp: clear-sign: %w", err)
	}
	return out.Bytes(), nil
}

// DetachSign signs data with every key (signature type 0x00, SHA-512, made at at) and returns the signatures as one armored
// block, the Release.gpg of a suite. The time rule is that of ClearSign.
func DetachSign(data []byte, keys []*Key, at time.Time) ([]byte, error) {
	at = at.UTC().Truncate(time.Second)
	privs, err := checkSigningTime(keys, at)
	if err != nil {
		return nil, err
	}
	cfg := signConfig(at)
	sigs := make([]*packet.Signature, 0, len(privs))
	for _, p := range privs {
		sig := newSignature(p, packet.SigTypeBinary, at, crypto.SHA512)
		if err := signData(sig, p, data, cfg); err != nil {
			return nil, fmt.Errorf("openpgp: detached signature: %w", err)
		}
		sigs = append(sigs, sig)
	}
	return armorEncode(typeSignature, func(w io.Writer) error {
		for _, s := range sigs {
			if err := s.Serialize(w); err != nil {
				return err
			}
		}
		return nil
	})
}
