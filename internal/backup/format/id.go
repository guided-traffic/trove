package format

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"time"
)

// ID is a backup ID: <yyyymmdd>T<hhmmss>Z-<4 lowercase hex>, the UTC start time of the run and 16 random bits
// (R-BAK-002). Lexical order is chronological order.
type ID string

var idPattern = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{4}$`)

// idLayout is the time part of an ID.
const idLayout = "20060102T150405Z"

// NewID builds the ID of a run that started at start. A nil entropy reader means crypto/rand.
func NewID(start time.Time, entropy io.Reader) (ID, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	var b [2]byte
	if _, err := io.ReadFull(entropy, b[:]); err != nil {
		return "", fmt.Errorf("backup id: %w", err)
	}
	return ID(start.UTC().Format(idLayout) + "-" + hex.EncodeToString(b[:])), nil
}

// ParseID validates the text form of an ID.
func ParseID(s string) (ID, error) {
	if !idPattern.MatchString(s) {
		return "", fmt.Errorf("%q is not a backup ID (want yyyymmddThhmmssZ-xxxx)", s)
	}
	if _, err := time.Parse(idLayout, s[:16]); err != nil {
		return "", fmt.Errorf("%q is not a backup ID: %w", s, err)
	}
	return ID(s), nil
}

// String implements fmt.Stringer.
func (id ID) String() string { return string(id) }

// Time is the start time the ID encodes.
func (id ID) Time() time.Time {
	t, _ := time.Parse(idLayout, string(id)[:16])
	return t
}

// Less orders IDs chronologically; the random suffix breaks a tie.
func (id ID) Less(o ID) bool { return id < o }
