package iface

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SyntheticCursorPrefix starts the synthetic cursor "t:<RFC 3339>" a starting
// replica passes as after to read the feed history of an answer's lifetime
// (R-BAS-015, R-BAS-026, Q-BAS-33).
const SyntheticCursorPrefix = "t:"

// ErrCursor is the error of a cursor that does not parse; the main installation
// answers it as 400 invalid_cursor.
var ErrCursor = errors.New("bastion feed cursor: malformed")

// Cursor is a position in the feed (R-BAS-015): the time and ID of the last entry
// a read returned, or, with no ID, a bare time (the synthetic form). The feed
// orders entries by (occurred_at, id) of the audit table, so the ID is an audit
// event ID.
//
// The textual form of an entry cursor is opaque to the Bastion: URL-safe base64
// without padding of "<RFC 3339 time>|<ID>". The Bastion stores and returns it
// unchanged and compares nothing but equality.
type Cursor struct {
	// At is the occurred_at of the entry, in UTC.
	At time.Time
	// ID is the entry's ID; empty for a synthetic cursor.
	ID string
}

// Synthetic reports whether the cursor is a bare time.
func (c Cursor) Synthetic() bool { return c.ID == "" }

// String returns the wire form: "t:<RFC 3339>" for a synthetic cursor, the opaque
// form otherwise.
func (c Cursor) String() string {
	at := c.At.UTC().Format(time.RFC3339Nano)
	if c.Synthetic() {
		return SyntheticCursorPrefix + at
	}
	return base64.RawURLEncoding.EncodeToString([]byte(at + "|" + c.ID))
}

// ParseCursor parses the wire form of either kind.
func ParseCursor(s string) (Cursor, error) {
	if rest, ok := strings.CutPrefix(s, SyntheticCursorPrefix); ok {
		at, err := time.Parse(time.RFC3339Nano, rest)
		if err != nil {
			return Cursor{}, fmt.Errorf("%w: %q", ErrCursor, s)
		}
		return Cursor{At: at.UTC()}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %q", ErrCursor, s)
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return Cursor{}, fmt.Errorf("%w: %q", ErrCursor, s)
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %q", ErrCursor, s)
	}
	return Cursor{At: t.UTC(), ID: id}, nil
}
