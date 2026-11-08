package debstub

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// The test keys. The stub signs with the OpenPGP module directly and not through trove's internal/openpgp, so that trove's
// verifier is tested against an independent signer. Every key is EdDSALegacy (algorithm 22) over Ed25519, version 4, made from a
// fixed seed, so its fingerprint is the same in every process and a test can pin it:
//
//	a  the archive's key, created KeyCreated, no expiry
//	b  a second key of the archive, for co-signed releases (the rotation of R-DEB-017)
//	c  a key nobody configured: "a signature by an unknown key"
//	d  a key that expired (created KeyExpiredCreated, valid for a year) whose signatures the stub dates inside its validity:
//	   "an expired key with a backdated signature time"
//
// The creation time of a, b and c is a fixed date in the past instead of "an hour back": it is deterministic, which the
// fingerprints need, and earlier than any signature the stub makes by more than an hour as long as the clock is after it.
var (
	// KeyCreated is the creation time of the keys a, b and c.
	KeyCreated = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// KeyExpiredCreated is the creation time of the key d; it expires a year later.
	KeyExpiredCreated = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// ExpiredSignatureTime is the creation time of the signatures of the key d: inside its validity, long before its expiry.
	ExpiredSignatureTime = time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
)

// KeyNames are the names of the test keys.
func KeyNames() []string { return []string{"a", "b", "c", "d"} }

// KeyInfo describes a test key.
type KeyInfo struct {
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	Created     time.Time `json:"created"`
	// Expires is the zero time for a key without expiry.
	Expires time.Time `json:"expires,omitzero"`
	// Public is the ASCII-armored public certificate.
	Public string `json:"public"`
}

type testKey struct {
	name    string
	entity  *pgp.Entity
	created time.Time
	expires time.Time
	public  []byte
}

// seedReader is a deterministic byte stream: the SHA-256 of the seed and a counter, block after block. The module reads 32 bytes
// from it for the private key of the primary key, which is all the fingerprint depends on.
type seedReader struct {
	seed    string
	counter uint64
	buf     []byte
}

func (r *seedReader) Read(p []byte) (int, error) {
	for len(r.buf) < len(p) {
		var c [8]byte
		binary.BigEndian.PutUint64(c[:], r.counter)
		r.counter++
		sum := sha256.Sum256(append([]byte(r.seed), c[:]...))
		r.buf = append(r.buf, sum[:]...)
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func makeKey(name string, created time.Time, lifetime time.Duration) (*testKey, error) {
	off := false
	cfg := &packet.Config{
		Rand:        &seedReader{seed: "trove debstub test key " + name},
		Algorithm:   packet.PubKeyAlgoEdDSA,
		Curve:       packet.Curve25519,
		DefaultHash: crypto.SHA512,
		Time:        func() time.Time { return created },
		// Without the random salt notation the binding self-signature, and with it the whole certificate, is reproducible.
		NonDeterministicSignaturesViaNotation: &off,
		KeyLifetimeSecs:                       uint32(lifetime / time.Second), //nolint:gosec // the lifetimes are a year at most
	}
	e, err := pgp.NewEntity("trove debstub key "+name, "", "", cfg)
	if err != nil {
		return nil, err
	}
	e.Subkeys = nil
	var pub bytes.Buffer
	w, err := armor.Encode(&pub, pgp.PublicKeyType, nil)
	if err != nil {
		return nil, err
	}
	if err := e.Serialize(w); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	k := &testKey{name: name, entity: e, created: created, public: append(pub.Bytes(), '\n')}
	if lifetime > 0 {
		k.expires = created.Add(lifetime)
	}
	return k, nil
}

func makeKeys() (map[string]*testKey, error) {
	specs := []struct {
		name     string
		created  time.Time
		lifetime time.Duration
	}{
		{"a", KeyCreated, 0}, {"b", KeyCreated, 0}, {"c", KeyCreated, 0}, {"d", KeyExpiredCreated, 365 * 24 * time.Hour},
	}
	out := map[string]*testKey{}
	for _, s := range specs {
		k, err := makeKey(s.name, s.created, s.lifetime)
		if err != nil {
			return nil, fmt.Errorf("debstub: key %s: %w", s.name, err)
		}
		out[s.name] = k
	}
	return out, nil
}

func (k *testKey) info() KeyInfo {
	return KeyInfo{Name: k.name, Fingerprint: strings.ToUpper(fmt.Sprintf("%x", k.entity.PrimaryKey.Fingerprint)), Created: k.created, Expires: k.expires, Public: string(k.public)}
}

// Keys describes the test keys.
func (s *Server) Keys() []KeyInfo {
	names := KeyNames()
	sort.Strings(names)
	out := make([]KeyInfo, 0, len(names))
	for _, n := range names {
		out = append(out, s.keys[n].info())
	}
	return out
}

// Key returns one test key by name ("a" to "d").
func (s *Server) Key(name string) (KeyInfo, bool) {
	k, ok := s.keys[name]
	if !ok {
		return KeyInfo{}, false
	}
	return k.info(), true
}

// Keyring returns the public certificates of the named keys as one armored block, the form of deb.upstreamKeys.
func (s *Server) Keyring(names ...string) ([]byte, error) {
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, pgp.PublicKeyType, nil)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		k, ok := s.keys[n]
		if !ok {
			return nil, fmt.Errorf("debstub: no key %q", n)
		}
		if err := k.entity.Serialize(w); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return append(buf.Bytes(), '\n'), nil
}

// hashByName maps the Mode.SignHash values.
func hashByName(name string) (crypto.Hash, string, error) {
	switch strings.ToLower(name) {
	case "", "sha512":
		return crypto.SHA512, "SHA512", nil
	case "sha256":
		return crypto.SHA256, "SHA256", nil
	case "sha1":
		return crypto.SHA1, "SHA1", nil
	}
	return 0, "", fmt.Errorf("sign_hash %q: want sha512, sha256 or sha1", name)
}

// signer is one signature to make: a key, the time it is dated and the hash.
type signer struct {
	key  *testKey
	at   time.Time
	hash crypto.Hash
}

func (sg signer) signature(typ packet.SignatureType, data []byte) (*packet.Signature, error) {
	priv := sg.key.entity.PrivateKey
	id := priv.KeyId
	sig := &packet.Signature{
		Version: priv.Version, SigType: typ, PubKeyAlgo: priv.PubKeyAlgo, Hash: sg.hash, CreationTime: sg.at,
		IssuerKeyId: &id, IssuerFingerprint: priv.Fingerprint, IssuerKeyVersion: uint8(priv.Version), //nolint:gosec // the version of a key made here is 4
	}
	// The module cannot make its random salt notation for SHA-1, and a signature that is a test input need not have one.
	off := false
	cfg := &packet.Config{DefaultHash: sg.hash, Time: func() time.Time { return sg.at }, NonDeterministicSignaturesViaNotation: &off}
	if sg.hash != crypto.SHA1 {
		cfg.NonDeterministicSignaturesViaNotation = nil
	}
	h, err := sig.PrepareSign(cfg)
	if err != nil {
		return nil, err
	}
	var w io.Writer = h
	if typ == packet.SigTypeText {
		w = pgp.NewCanonicalTextHash(h)
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := sig.Sign(h, priv, cfg); err != nil {
		return nil, err
	}
	return sig, nil
}

func armoredSignatures(signers []signer, typ packet.SignatureType, data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, "PGP SIGNATURE", nil)
	if err != nil {
		return nil, err
	}
	for _, sg := range signers {
		sig, err := sg.signature(typ, data)
		if err != nil {
			return nil, err
		}
		if err := sig.Serialize(w); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return append(buf.Bytes(), '\n'), nil
}

// detachSign is a Release.gpg: the signatures of the exact bytes, in one armored block.
func detachSign(signers []signer, data []byte) ([]byte, error) {
	return armoredSignatures(signers, packet.SigTypeBinary, data)
}

// clearSign frames text as gpg does and signs it: the text without its last line ending is what the signatures cover (the line
// ending before the armor header belongs to the framing), lines that start with a dash are escaped, and a Hash header names the
// hashes unless noHeader.
func clearSign(signers []signer, text []byte, noHeader bool) ([]byte, error) {
	body := bytes.TrimSuffix(text, []byte("\n"))
	sigs, err := armoredSignatures(signers, packet.SigTypeText, body)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString("-----BEGIN PGP SIGNED MESSAGE-----\n")
	if !noHeader {
		var names []string
		for _, sg := range signers {
			_, name, _ := hashByName(hashShort(sg.hash))
			if !contains(names, name) {
				names = append(names, name)
			}
		}
		out.WriteString("Hash: " + strings.Join(names, ",") + "\n")
	}
	out.WriteString("\n")
	for _, line := range bytes.Split(body, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("-")) {
			out.WriteString("- ")
		}
		out.Write(line)
		out.WriteString("\n")
	}
	out.Write(sigs)
	return out.Bytes(), nil
}

func hashShort(h crypto.Hash) string {
	switch h {
	case crypto.SHA1:
		return "sha1"
	case crypto.SHA256:
		return "sha256"
	}
	return "sha512"
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}
