package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/config"
)

const (
	envelopeVersion = "v1"
	keyBytes        = 32
	canaryPlain     = "trove key canary v1"
	fingerprintInfo = "trove fingerprint v1"
)

// Errors returned by Open. They never contain key material or plaintext.
var (
	// ErrMalformed marks a value that is not in the v1 envelope format.
	ErrMalformed = errors.New("sealed value is malformed")
	// ErrUnknownKey marks a value sealed under a key ID that the ring lacks.
	ErrUnknownKey = errors.New("sealed value references a key that is not in the key ring")
	// ErrAuthentication marks a value that failed AES-GCM authentication: wrong
	// key, tampered ciphertext, or a ciphertext moved to another row or column.
	ErrAuthentication = errors.New("sealed value failed authentication (wrong key, tampered value, or moved to another row or column)")
	// ErrUnregistered marks a column that is not in the sealed-column registry.
	ErrUnregistered = errors.New("column is not registered as a sealed column")
	// ErrRowKey marks a row key whose part count differs from the column's
	// primary key columns.
	ErrRowKey = errors.New("row key does not match the primary key of the column")
)

func keyIDPattern() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`) }

// KeyRing holds the encryption keys of this replica: every listed key opens
// values, the active key seals them. It is immutable and safe for concurrent
// use.
type KeyRing struct {
	order  []string // key IDs in the configured order
	aeads  map[string]cipher.AEAD
	active string
	fpKey  []byte // HKDF-derived fingerprint key
}

// Load reads the key files named by cfg. A missing or unreadable file, a file
// that does not hold 32 base64-encoded bytes, a duplicate or invalid key ID, or
// an active key ID outside the ring is an error that names the file or ID but
// never prints key material.
func Load(cfg config.Secrets) (*KeyRing, error) {
	if len(cfg.EncryptionKeyFiles) == 0 {
		return nil, errors.New("secrets: no encryption key configured: secrets.encryption_key_files is empty; " +
			"create a key Secret before the first start (see docs/developer/secrets.md)")
	}
	if cfg.ActiveKeyID == "" {
		return nil, errors.New("secrets: secrets.active_key_id is empty")
	}
	idRe := keyIDPattern()
	r := &KeyRing{aeads: map[string]cipher.AEAD{}, active: cfg.ActiveKeyID}
	raw := map[string][]byte{}
	for _, file := range cfg.EncryptionKeyFiles {
		id := config.KeyIDOf(file)
		if !idRe.MatchString(id) {
			return nil, fmt.Errorf("secrets: key file %s: key ID %q (file name without .key) must match %s", file, id, idRe)
		}
		if _, dup := r.aeads[id]; dup {
			return nil, fmt.Errorf("secrets: key file %s: key ID %q is listed twice", file, id)
		}
		key, err := readKeyFile(file)
		if err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("secrets: key file %s: %w", file, err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("secrets: key file %s: %w", file, err)
		}
		r.aeads[id] = gcm
		r.order = append(r.order, id)
		raw[id] = key
	}
	if _, ok := r.aeads[r.active]; !ok {
		return nil, fmt.Errorf("secrets: secrets.active_key_id %q is not among the loaded keys %v", r.active, r.order)
	}
	fp, err := hkdf.Key(sha256.New, raw[r.active], nil, fingerprintInfo, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("secrets: derive fingerprint key: %w", err)
	}
	r.fpKey = fp
	return r, nil
}

func readKeyFile(file string) ([]byte, error) {
	b, err := os.ReadFile(file) //nolint:gosec // operator-configured path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("secrets: key file %s does not exist: mount the key Secret or fix secrets.encryption_key_files", file)
		}
		return nil, fmt.Errorf("secrets: read key file %s: %w", file, err)
	}
	// A file of exactly 32 bytes is the key itself, as a generator of random
	// bytes writes it (a `type: bytes` field of internal-secrets-operator). It is
	// checked before anything is trimmed, because a random key may start or end
	// with a white-space byte. Base64 text of 32 bytes is 43 or 44 characters
	// long, so the length cannot be mistaken for it.
	if len(b) == keyBytes {
		return b, nil
	}
	enc := strings.TrimSpace(string(b))
	key, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		if key, err = base64.RawStdEncoding.DecodeString(enc); err != nil {
			return nil, fmt.Errorf("secrets: key file %s is not base64", file)
		}
	}
	if len(key) != keyBytes {
		return nil, fmt.Errorf("secrets: key file %s holds %d bytes, want %d (32 random bytes, either raw or base64-encoded)", file, len(key), keyBytes)
	}
	return key, nil
}

// ActiveKeyID returns the key ID that seals.
func (r *KeyRing) ActiveKeyID() string { return r.active }

// KeyIDs returns the key IDs of the ring in the configured order.
func (r *KeyRing) KeyIDs() []string { return append([]string(nil), r.order...) }

// Has reports whether the ring holds the key ID.
func (r *KeyRing) Has(keyID string) bool { _, ok := r.aeads[keyID]; return ok }

// String and LogValue render key IDs only, so a ring that is logged by mistake
// shows no key material.
func (r *KeyRing) String() string {
	return fmt.Sprintf("keyring{active=%s keys=%v}", r.active, r.order)
}

// LogValue implements slog.LogValuer.
func (r *KeyRing) LogValue() slog.Value {
	return slog.GroupValue(slog.String("active_key_id", r.active), slog.Any("key_ids", r.order))
}

// RowKey is the primary key value of the row a sealed value belongs to: one
// part per primary key column of the Column, in the registered order.
type RowKey struct{ parts []string }

// Row builds a RowKey from the primary key values of a row, in the order of
// Column.PrimaryKeyColumns. For a one-column key that is Row(id).
func Row(parts ...string) RowKey { return RowKey{parts: append([]string(nil), parts...)} }

// String renders the parts for error texts; it is not the AAD encoding.
func (k RowKey) String() string { return "(" + strings.Join(k.parts, ", ") + ")" }

// checkRow requires one part per primary key column of c.
func checkRow(c Column, row RowKey) error {
	if want := len(c.PrimaryKeyColumns()); len(row.parts) != want {
		return fmt.Errorf("%w: %s has a primary key of %d column(s) (%s), the row key has %d part(s)",
			ErrRowKey, c, want, c.pk, len(row.parts))
	}
	return nil
}

// aad binds a ciphertext to its table, column and row, so a database writer
// cannot move it to another row or column. Every element is length-prefixed
// and the row key carries its part count, so no two different (table, column,
// row) triples encode alike: ("a:b") and ("a", "b") are different rows.
func aad(c Column, row RowKey) []byte {
	var b bytes.Buffer
	put := func(n uint64) {
		var l [binary.MaxVarintLen64]byte
		b.Write(l[:binary.PutUvarint(l[:], n)])
	}
	for _, p := range []string{"trove/sealed/v1", c.table, c.column} {
		put(uint64(len(p)))
		b.WriteString(p)
	}
	put(uint64(len(row.parts)))
	for _, p := range row.parts {
		put(uint64(len(p)))
		b.WriteString(p)
	}
	return b.Bytes()
}

// Seal encrypts plaintext for the row of the column under the active key and
// returns the envelope v1:<key id>:<base64(nonce‖ciphertext‖tag)>. row holds
// the row's primary key values (Row(id), or Row(repositoryID, name) for a
// composite key).
func (r *KeyRing) Seal(c Column, row RowKey, plaintext []byte) (string, error) {
	return r.sealWith(r.active, c, row, plaintext)
}

func (r *KeyRing) sealWith(keyID string, c Column, row RowKey, plaintext []byte) (string, error) {
	if !registered(c) {
		return "", fmt.Errorf("%w: %s", ErrUnregistered, c)
	}
	if err := checkRow(c, row); err != nil {
		return "", err
	}
	gcm, ok := r.aeads[keyID]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownKey, keyID)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	out := gcm.Seal(nonce, nonce, plaintext, aad(c, row))
	return envelopeVersion + ":" + keyID + ":" + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts an envelope with the key its key ID names. It fails with
// ErrMalformed, ErrUnknownKey or ErrAuthentication, with ErrUnregistered for a
// column outside the registry and ErrRowKey for a row key of the wrong arity.
func (r *KeyRing) Open(c Column, row RowKey, sealed string) ([]byte, error) {
	if !registered(c) {
		return nil, fmt.Errorf("%w: %s", ErrUnregistered, c)
	}
	if err := checkRow(c, row); err != nil {
		return nil, err
	}
	keyID, payload, err := parse(sealed)
	if err != nil {
		return nil, err
	}
	gcm, ok := r.aeads[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: %q (ring holds %v)", ErrUnknownKey, keyID, r.order)
	}
	if len(payload) < gcm.NonceSize()+gcm.Overhead() {
		return nil, fmt.Errorf("%w: too short", ErrMalformed)
	}
	nonce, ct := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, aad(c, row))
	if err != nil {
		return nil, ErrAuthentication
	}
	return pt, nil
}

func parse(sealed string) (keyID string, payload []byte, err error) {
	parts := strings.SplitN(sealed, ":", 3)
	if len(parts) != 3 || parts[0] != envelopeVersion || parts[1] == "" {
		return "", nil, fmt.Errorf("%w: expected %s:<key id>:<base64>", ErrMalformed, envelopeVersion)
	}
	payload, err = base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return "", nil, fmt.Errorf("%w: payload is not base64", ErrMalformed)
	}
	return parts[1], payload, nil
}

// KeyIDOf returns the key ID an envelope references without opening it, for the
// per-key counts and the re-encryption job.
func KeyIDOf(sealed string) (string, error) {
	id, _, err := parse(sealed)
	return id, err
}

// IsSealed reports whether s looks like a v1 envelope.
func IsSealed(s string) bool {
	_, _, err := parse(s)
	return err == nil
}

// NeedsReseal reports whether the envelope is not under the active key.
func (r *KeyRing) NeedsReseal(sealed string) (bool, error) {
	id, err := KeyIDOf(sealed)
	if err != nil {
		return false, err
	}
	return id != r.active, nil
}

// Reseal opens a value and seals it under the active key. It returns the new
// envelope and whether anything changed; a value already under the active key
// is returned as is, which makes the re-encryption job idempotent.
func (r *KeyRing) Reseal(c Column, row RowKey, sealed string) (string, bool, error) {
	need, err := r.NeedsReseal(sealed)
	if err != nil {
		return "", false, err
	}
	if !need {
		return sealed, false, nil
	}
	pt, err := r.Open(c, row, sealed)
	if err != nil {
		return "", false, err
	}
	out, err := r.Seal(c, row, pt)
	return out, err == nil, err
}

// Fingerprint returns the HMAC-SHA-256 of value under a key derived with
// HKDF-SHA-256 from the active key (info "trove fingerprint v1"). After a
// rotation of the active key every fingerprint changes once.
func (r *KeyRing) Fingerprint(value []byte) []byte {
	h := hmac.New(sha256.New, r.fpKey)
	h.Write(value)
	return h.Sum(nil)
}

// canaryColumn is the column of the secret_key_canary table (R-ARCH-021). It is
// not in the registry of user secrets: the per-key counts must not include it.
func canaryColumn() Column {
	return Column{table: "secret_key_canary", pk: "key_id", column: "ciphertext", canary: true}
}

// CanaryColumn returns the handle of the canary column, for the migration and
// the start-up check.
func CanaryColumn() Column { return canaryColumn() }

// SealCanary seals the known canary plaintext under the named key, for the row
// secret_key_canary(key_id = keyID).
func (r *KeyRing) SealCanary(keyID string) (string, error) {
	return r.sealWith(keyID, canaryColumn(), Row(keyID), []byte(canaryPlain))
}

// CheckCanary opens the canary row of keyID and compares it to the known text.
func (r *KeyRing) CheckCanary(keyID, sealed string) error {
	pt, err := r.Open(canaryColumn(), Row(keyID), sealed)
	if err != nil {
		return fmt.Errorf("canary of key %q: %w", keyID, err)
	}
	if string(pt) != canaryPlain {
		return fmt.Errorf("canary of key %q: unexpected content", keyID)
	}
	return nil
}

// MismatchError is the start-up and restore error when the ring lacks keys that
// stored values reference: "key ring does not match <what>: missing key <id>".
type MismatchError struct {
	What    string // "database" or "backup"
	Missing []string
}

func (e *MismatchError) Error() string {
	noun := "key"
	if len(e.Missing) > 1 {
		noun = "keys"
	}
	return fmt.Sprintf("key ring does not match %s: missing %s %s", e.What, noun, strings.Join(e.Missing, ", "))
}

// RequireKeys returns a *MismatchError naming the key IDs of referenced that the
// ring lacks, or nil. The caller logs the expected and found key IDs; the error
// carries IDs only.
func (r *KeyRing) RequireKeys(what string, referenced []string) error {
	var missing []string
	seen := map[string]bool{}
	for _, id := range referenced {
		if !r.Has(id) && !seen[id] {
			missing = append(missing, id)
		}
		seen[id] = true
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return &MismatchError{What: what, Missing: missing}
}
