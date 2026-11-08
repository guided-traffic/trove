package openpgp

import (
	"bytes"
	"crypto"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// The reasons a verification fails, for the metric label of trove_deb_upstream_signature_failures_total and the audit event
// (R-DEB-020). Reason(err) returns one.
const (
	ReasonMalformed    = "malformed"    // not a cleartext message or a signature, or trailing text
	ReasonNoSignature  = "no_signature" // the document carries no signature packet
	ReasonTooMany      = "too_many"     // more signatures than Limits.MaxSignatures, or a document over Limits.MaxMessageBytes
	ReasonUnknownKey   = "unknown_key"  // no signature names a configured certificate
	ReasonKeyInvalid   = "key_invalid"  // the key is expired, revoked, or was created after the signature
	ReasonFuture       = "future"       // the signature time is more than ClockSkew after now
	ReasonExpiredSig   = "signature_expired"
	ReasonWeakHash     = "weak_hash"     // a hash below SHA-256
	ReasonHashHeader   = "hash_header"   // the Hash armor header, when present, does not name the signature's hash
	ReasonBadSignature = "bad_signature" // the signature does not verify
)

// reasonRank orders the reasons by how much the verifier learned: a failure of a known key beats an unknown key.
func reasonRank(reason string) int {
	switch reason {
	case ReasonNoSignature:
		return 0
	case ReasonMalformed:
		return 1
	case ReasonUnknownKey:
		return 2
	case ReasonHashHeader:
		return 3
	case ReasonWeakHash:
		return 4
	case ReasonFuture:
		return 5
	case ReasonExpiredSig:
		return 6
	case ReasonKeyInvalid:
		return 7
	case ReasonBadSignature:
		return 8
	}
	return 9
}

// VerifyError is the error of a failed verification: ErrVerify with the strongest reason found.
type VerifyError struct {
	Reason string
	Detail string
}

func (e *VerifyError) Error() string {
	return "openpgp: signature verification failed: " + e.Reason + ": " + e.Detail
}

// Is makes errors.Is(err, ErrVerify) true.
func (e *VerifyError) Is(target error) bool { return target == ErrVerify }

// Reason returns the Reason of a verification error, or "" for any other error.
func Reason(err error) string {
	var ve *VerifyError
	if errors.As(err, &ve) {
		return ve.Reason
	}
	return ""
}

func verifyFailure(reason, format string, args ...any) *VerifyError {
	return &VerifyError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// failures collects why signatures did not verify and reports the most telling.
type failures struct{ best *VerifyError }

func (f *failures) add(reason, format string, args ...any) {
	if f.best == nil || reasonRank(reason) > reasonRank(f.best.Reason) {
		f.best = verifyFailure(reason, format, args...)
	}
}

func (f *failures) err() error {
	if f.best == nil {
		return verifyFailure(ReasonNoSignature, "the document carries no signature")
	}
	return f.best
}

// VerifyClear verifies a cleartext-signed message (an InRelease) against the certificates and returns the signed text, the
// dash-unescaped lines joined by newlines as the signature covers them, and the fingerprint of the certificate whose signature
// verified. It succeeds when at least one signature verifies; signatures by other keys are ignored (R-DEB-020, Q-DEB-35).
//
// A signature verifies when: its hash is SHA-256 or stronger; its creation time is no more than ClockSkew after now; the signing
// key is valid now (not expired, not revoked) and at the signature's time, and was created before the signature; and the
// signature is cryptographically correct. A Hash armor header is checked only when present: then it must name the signature's
// hash. The message is one cleartext message with nothing before or after it, at most Limits.MaxMessageBytes, with at most
// Limits.MaxSignatures signatures.
func VerifyClear(msg []byte, certs []Certificate, now time.Time, lim Limits) (text []byte, signer string, err error) {
	lim = lim.withDefaults()
	if len(msg) > lim.MaxMessageBytes {
		return nil, "", verifyFailure(ReasonTooMany, "the message is over %d bytes", lim.MaxMessageBytes)
	}
	trimmed := bytes.TrimLeft(msg, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("-----BEGIN PGP SIGNED MESSAGE-----")) {
		return nil, "", verifyFailure(ReasonMalformed, "not a cleartext signed message")
	}
	block, rest := clearsign.Decode(trimmed)
	if block == nil || block.ArmoredSignature == nil {
		return nil, "", verifyFailure(ReasonMalformed, "not a cleartext signed message")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, "", verifyFailure(ReasonMalformed, "text after the signature")
	}
	if block.ArmoredSignature.Type != typeSignature {
		return nil, "", verifyFailure(ReasonMalformed, "the signature block is a %q", block.ArmoredSignature.Type)
	}
	sigBody, err := io.ReadAll(io.LimitReader(block.ArmoredSignature.Body, int64(lim.MaxMessageBytes)+1))
	if err != nil {
		return nil, "", verifyFailure(ReasonMalformed, "signature armor: %v", err)
	}
	sigs, err := readSignatures(sigBody, lim.MaxSignatures)
	if err != nil {
		return nil, "", err
	}
	var allowed map[string]bool
	if hdr := block.Headers["Hash"]; len(hdr) > 0 {
		allowed = make(map[string]bool, len(hdr))
		for _, h := range hdr {
			allowed[strings.ToUpper(h)] = true
		}
	}
	signer, err = verifySignatures(sigs, certs, now, packet.SigTypeText, block.Bytes, allowed)
	if err != nil {
		return nil, "", err
	}
	return block.Plaintext, signer, nil
}

// VerifyDetached verifies a detached signature (a Release.gpg, armored or binary) over data under the same rules as
// VerifyClear, without the cleartext framework and its Hash header, and returns the fingerprint of the certificate whose
// signature verified.
func VerifyDetached(data, sig []byte, certs []Certificate, now time.Time, lim Limits) (signer string, err error) {
	lim = lim.withDefaults()
	if len(data) > lim.MaxMessageBytes {
		return "", verifyFailure(ReasonTooMany, "the data is over %d bytes", lim.MaxMessageBytes)
	}
	if len(sig) > lim.MaxMessageBytes {
		return "", verifyFailure(ReasonTooMany, "the signature is over %d bytes", lim.MaxMessageBytes)
	}
	body := sig
	if bytes.HasPrefix(bytes.TrimLeft(sig, " \t\r\n"), []byte(armorBegin)) {
		blocks, err := splitArmor(sig)
		if err != nil || len(blocks) != 1 {
			return "", verifyFailure(ReasonMalformed, "expected one armored signature block")
		}
		if body, err = armorDecode(blocks[0], typeSignature, lim.MaxMessageBytes); err != nil {
			return "", verifyFailure(ReasonMalformed, "%v", err)
		}
	}
	sigs, err := readSignatures(body, lim.MaxSignatures)
	if err != nil {
		return "", err
	}
	return verifySignatures(sigs, certs, now, packet.SigTypeBinary, data, nil)
}

func readSignatures(body []byte, max int) ([]*packet.Signature, error) {
	r := packet.NewReader(bytes.NewReader(body))
	var out []*packet.Signature
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, verifyFailure(ReasonMalformed, "signature packet: %v", err)
		}
		s, ok := p.(*packet.Signature)
		if !ok {
			return nil, verifyFailure(ReasonMalformed, "a packet that is no signature")
		}
		if len(out) == max {
			return nil, verifyFailure(ReasonTooMany, "more than %d signatures", max)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, verifyFailure(ReasonNoSignature, "the document carries no signature")
	}
	return out, nil
}

// hashName is the OpenPGP name of a hash a signature may use: SHA-256 or stronger (Q-DEB-35).
func hashName(h crypto.Hash) (string, bool) {
	switch h {
	case crypto.SHA256:
		return "SHA256", true
	case crypto.SHA384:
		return "SHA384", true
	case crypto.SHA512:
		return "SHA512", true
	case crypto.SHA3_256:
		return "SHA3-256", true
	case crypto.SHA3_512:
		return "SHA3-512", true
	}
	return "", false
}

func verifySignatures(sigs []*packet.Signature, certs []Certificate, now time.Time, wantType packet.SignatureType, data []byte, allowedHashes map[string]bool) (string, error) {
	var f failures
	for _, sig := range sigs {
		name, strong := hashName(sig.Hash)
		if !strong {
			f.add(ReasonWeakHash, "a signature uses a hash below SHA-256")
			continue
		}
		if allowedHashes != nil && !allowedHashes[name] {
			f.add(ReasonHashHeader, "the Hash header does not name %s", name)
			continue
		}
		if sig.SigType != wantType {
			f.add(ReasonBadSignature, "a signature of type %#x where %#x is expected", uint8(sig.SigType), uint8(wantType))
			continue
		}
		matched := false
		for _, c := range certs {
			if c.entity == nil {
				continue
			}
			for _, k := range signingKeys(c.entity) {
				if !sig.CheckKeyIdOrFingerprint(k.pub) {
					continue
				}
				matched = true
				if fail, ok := checkSigner(c, k, sig, now); !ok {
					f.add(fail.Reason, "%s", fail.Detail)
					continue
				}
				if err := verifyData(k.pub, sig, data); err != nil {
					f.add(ReasonBadSignature, "the signature of %s does not verify", c.Fingerprint)
					continue
				}
				return c.Fingerprint, nil
			}
		}
		if !matched {
			f.add(ReasonUnknownKey, "a signature names no configured key")
		}
	}
	return "", f.err()
}

// checkSigner applies the key rules of Q-DEB-35 for one signature by one key of a certificate.
func checkSigner(c Certificate, k signerKey, sig *packet.Signature, now time.Time) (*VerifyError, bool) {
	t := sig.CreationTime
	if t.After(now.Add(ClockSkew)) {
		return verifyFailure(ReasonFuture, "the signature of %s is dated %s, over %s after now", c.Fingerprint, t.UTC().Format(time.RFC3339), ClockSkew), false
	}
	if sig.SigLifetimeSecs != nil && *sig.SigLifetimeSecs != 0 && now.After(t.Add(time.Duration(*sig.SigLifetimeSecs)*time.Second)) {
		return verifyFailure(ReasonExpiredSig, "the signature of %s has expired", c.Fingerprint), false
	}
	if !k.pub.CreationTime.Before(t) {
		return verifyFailure(ReasonKeyInvalid, "the key %s was created at or after its signature", c.Fingerprint), false
	}
	if c.entity.Revoked(now) || (k.sub != nil && k.sub.Revoked(now)) {
		return verifyFailure(ReasonKeyInvalid, "the key %s is revoked", c.Fingerprint), false
	}
	primarySelf, _ := c.entity.PrimarySelfSignature()
	for _, at := range []time.Time{now, t} {
		if primarySelf != nil && c.entity.PrimaryKey.KeyExpired(primarySelf, at) {
			return verifyFailure(ReasonKeyInvalid, "the key %s is expired at %s", c.Fingerprint, at.UTC().Format(time.RFC3339)), false
		}
		if k.sub != nil && k.pub.KeyExpired(k.binding, at) {
			return verifyFailure(ReasonKeyInvalid, "the signing subkey of %s is expired at %s", c.Fingerprint, at.UTC().Format(time.RFC3339)), false
		}
	}
	return nil, true
}

func verifyData(pk *packet.PublicKey, sig *packet.Signature, data []byte) error {
	h, err := sig.PrepareVerify()
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
	return pk.VerifySignature(h, sig)
}
