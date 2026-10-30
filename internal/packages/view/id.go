package view

import (
	"encoding/base32"
	"strings"
)

// IDPrefix starts every package id of the management API.
const IDPrefix = "pkg_"

func idEncoding() *base32.Encoding { return base32.StdEncoding.WithPadding(base32.NoPadding) }

// ID is the package id of a format and a normalized name. It is derived, not
// stored: the same package has the same id on every replica and after a restore,
// and an id resolves without a table. It is opaque to clients (Q-API-164); the
// encoding carries nothing a caller who knows the id cannot read from the package
// itself, and the id of a package the caller may not read resolves to the same
// 404 as an id that names no package.
func ID(format, name string) string {
	return IDPrefix + strings.ToLower(idEncoding().EncodeToString([]byte(format+"\x00"+name)))
}

// ParseID is the inverse of [ID]; ok is false for anything that is not an id.
func ParseID(id string) (format, name string, ok bool) {
	rest, found := strings.CutPrefix(id, IDPrefix)
	if !found || rest == "" {
		return "", "", false
	}
	b, err := idEncoding().DecodeString(strings.ToUpper(rest))
	if err != nil {
		return "", "", false
	}
	format, name, found = strings.Cut(string(b), "\x00")
	if !found || format == "" || name == "" || strings.ContainsRune(name, 0) {
		return "", "", false
	}
	return format, name, true
}
