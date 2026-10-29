package principal

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"slices"
	"time"
)

// The fixed identifiers of the two built-in principals (migration 00007).
const (
	AnonymousID     = "00000000-0000-0000-0000-000000000001"
	AuthenticatedID = "00000000-0000-0000-0000-000000000002"
)

// NewID returns a UUIDv7 (RFC 9562): 48 bits of Unix milliseconds, then random
// bits, so identifiers sort by creation time. Principals, groups and tokens get
// their identifiers from the application, not from the database.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { // crypto/rand does not fail on supported platforms
		panic(err)
	}
	ms := uint64(time.Now().UnixMilli()) //nolint:gosec // after 1970
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:], b[10:])
	return string(s[:])
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsID reports whether s has the form of an identifier. A lookup by anything
// else is "not found" without a database round trip.
func IsID(s string) bool { return uuidPattern.MatchString(s) }

// IDOrEmpty returns s when it is a principal ID and "" otherwise. It is the one
// rule for the principal ID of an audit actor in a created_by or updated_by
// column: a break-glass session, an admin command and an anonymous caller have no
// row of the principal table.
func IDOrEmpty(s string) string {
	if IsID(s) {
		return s
	}
	return ""
}

// The name rules of the principal kinds. They are the CHECK constraints of
// migration 00007 (R-AUTH-007, R-AUTH-018, R-AUTH-020); the application
// validates first and answers with the specified 4xx.
var (
	userNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._@+-]{0,127}$`)
	groupNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	saNamePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
)

// reservedUserNames are the usernames no source may produce (R-AUTH-007). The
// configured break-glass username is reserved as well; the Manager and the
// Provisioner take it as an option.
var reservedUserNames = [...]string{"anonymous", "authenticated", "system", "me"}

// ValidUserName reports whether name is a legal username. extraReserved are
// further reserved names such as the break-glass username.
func ValidUserName(name string, extraReserved ...string) bool {
	return userNamePattern.MatchString(name) && !ReservedUserName(name, extraReserved...)
}

// ReservedUserName reports whether name is reserved, which the API answers with
// 422 reserved_name (R-AUTH-016).
func ReservedUserName(name string, extraReserved ...string) bool {
	return slices.Contains(reservedUserNames[:], name) || slices.Contains(extraReserved, name)
}

// ValidGroupName reports whether name is a legal group name (R-AUTH-018).
func ValidGroupName(name string) bool { return groupNamePattern.MatchString(name) }

// ValidServiceAccountName reports whether name is a legal service-account name (R-AUTH-020).
func ValidServiceAccountName(name string) bool { return saNamePattern.MatchString(name) }
