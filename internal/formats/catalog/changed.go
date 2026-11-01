package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// MetadataChangedArea is the signal area that evicts merged metadata early after a
// change of a package (R-REPO-020). Its name and payload are declared here, once:
// the package writer of the Host attaches the signal, the virtual engine subscribes
// to it, and the cluster registers it. Nothing depends on it for correctness; the
// engine validates every cached entry by revision.
const MetadataChangedArea = "metadata_changed"

// FormatAreas are the notification areas a format may declare in its descriptor
// (formats.Descriptor.NotifyAreas, Q-ARCH-259). Every one is a signal area.
var FormatAreas = []string{MetadataChangedArea}

// PackageChanged is the payload of [MetadataChangedArea]:
// "<repository id>:<hex SHA-256 of the normalized package name>". The name is
// hashed so that a signal on a shared channel does not carry a package name.
func PackageChanged(repoID int64, name string) string {
	sum := sha256.Sum256([]byte(name))
	return strconv.FormatInt(repoID, 10) + ":" + hex.EncodeToString(sum[:])
}

// ParsePackageChanged is the inverse of [PackageChanged]: the repository id and the
// SHA-256 of the name. ok is false for a text that is no payload.
func ParsePackageChanged(s string) (repoID int64, name [32]byte, ok bool) {
	id, hexed, found := strings.Cut(s, ":")
	if !found {
		return 0, name, false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, name, false
	}
	b, err := hex.DecodeString(hexed)
	if err != nil || len(b) != len(name) {
		return 0, name, false
	}
	copy(name[:], b)
	return n, name, true
}
