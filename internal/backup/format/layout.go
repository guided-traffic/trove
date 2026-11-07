package format

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
)

// Object names below the target prefix (R-BAK-002).
const (
	// MarkerName is the target marker object.
	MarkerName = "trove-backup-target.json"
	// PoolDir is the shared blob pool.
	PoolDir = "pool/sha256"
	// BackupsDir holds one directory per backup.
	BackupsDir = "backups"
	// ManifestName, BlobListName and TablesDir are the objects of one backup directory.
	ManifestName = "manifest.json"
	BlobListName = "blobs.idx.zst"
	TablesDir    = "db"
	// TableSuffix ends the name of a table file.
	TableSuffix = ".copy.zst"
)

// Digest is a SHA-256 digest.
type Digest [32]byte

// ParseDigest reads 64 lowercase hex digits.
func ParseDigest(s string) (Digest, error) {
	var d Digest
	if len(s) != 64 || s != strings.ToLower(s) {
		return d, fmt.Errorf("%q is not 64 lowercase hex digits", s)
	}
	if _, err := hex.Decode(d[:], []byte(s)); err != nil {
		return d, fmt.Errorf("%q is not 64 lowercase hex digits", s)
	}
	return d, nil
}

// DigestOf converts the 32 bytes of a blobs.sha256 value.
func DigestOf(b []byte) (Digest, error) {
	var d Digest
	if len(b) != len(d) {
		return d, fmt.Errorf("digest has %d bytes, want 32", len(b))
	}
	copy(d[:], b)
	return d, nil
}

// Hex is the lowercase hex form.
func (d Digest) Hex() string { return hex.EncodeToString(d[:]) }

// Less orders digests ascending, the order of the blob list.
func (d Digest) Less(o Digest) bool { return bytes.Compare(d[:], o[:]) < 0 }

// Layout builds the object keys below one target prefix. The prefix has no leading or trailing slash
// (R-STOR-030); an empty prefix puts the objects at the bucket root.
type Layout struct{ Prefix string }

func (l Layout) join(parts ...string) string {
	if l.Prefix == "" {
		return strings.Join(parts, "/")
	}
	return l.Prefix + "/" + strings.Join(parts, "/")
}

// Marker is the key of the target marker.
func (l Layout) Marker() string { return l.join(MarkerName) }

// PoolPrefix is the key prefix of the pool, with a trailing slash, for listing.
func (l Layout) PoolPrefix() string { return l.join(PoolDir) + "/" }

// Pool is the pool key of a digest, with the fan-out of R-STOR-001: <h0h1>/<h2h3>/<hex>.
func (l Layout) Pool(d Digest) string {
	h := d.Hex()
	return l.join(PoolDir, h[0:2], h[2:4], h)
}

// ParsePool reads a pool key back. It reports false for a key that does not match the pool layout exactly, which the
// sweep treats as foreign (R-BAK-017).
func (l Layout) ParsePool(key string) (Digest, bool) {
	rest, ok := strings.CutPrefix(key, l.PoolPrefix())
	if !ok {
		return Digest{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return Digest{}, false
	}
	d, err := ParseDigest(parts[2])
	if err != nil || parts[0] != parts[2][0:2] || parts[1] != parts[2][2:4] {
		return Digest{}, false
	}
	return d, true
}

// BackupsPrefix is the key prefix of all backup directories, with a trailing slash.
func (l Layout) BackupsPrefix() string { return l.join(BackupsDir) + "/" }

// Dir is the key prefix of one backup directory, with a trailing slash.
func (l Layout) Dir(id ID) string { return l.join(BackupsDir, string(id)) + "/" }

// Manifest, BlobList and Table are the keys of the objects of one backup.
func (l Layout) Manifest(id ID) string { return l.Dir(id) + ManifestName }
func (l Layout) BlobList(id ID) string { return l.Dir(id) + BlobListName }
func (l Layout) Table(id ID, table string) string {
	return l.Dir(id) + TablesDir + "/" + table + TableSuffix
}

// ParseBackupKey splits a key below BackupsPrefix into the backup ID and the path inside its directory. It reports false
// for a key outside the layout.
func (l Layout) ParseBackupKey(key string) (ID, string, bool) {
	rest, ok := strings.CutPrefix(key, l.BackupsPrefix())
	if !ok {
		return "", "", false
	}
	idPart, inner, ok := strings.Cut(rest, "/")
	if !ok || inner == "" {
		return "", "", false
	}
	id, err := ParseID(idPart)
	if err != nil {
		return "", "", false
	}
	return id, inner, true
}
