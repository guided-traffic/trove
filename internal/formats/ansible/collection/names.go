package collection

import "strings"

// MaxNamePart bounds the namespace and the name of a collection (R-ANS-002).
const MaxNamePart = 64

// ValidPart reports whether s is a namespace or a collection name by the rule of R-ANS-002: 1 to 64 characters of [a-z0-9_], not
// starting with "_", with at least one letter. Upper case is not folded here; a request folds it before it asks (the format's
// Names.Normalize), a publish refuses it.
func ValidPart(s string) bool {
	if s == "" || len(s) > MaxNamePart || s[0] == '_' {
		return false
	}
	letter := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z':
			letter = true
		case c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return letter
}

// SplitName splits "<ns>.<name>" into its parts; ok is false unless both parts are valid and the string holds exactly one dot.
func SplitName(full string) (namespace, name string, ok bool) {
	namespace, name, found := strings.Cut(full, ".")
	if !found || !ValidPart(namespace) || !ValidPart(name) {
		return "", "", false
	}
	return namespace, name, true
}

// ArchiveSuffix ends the file name of a collection archive.
const ArchiveSuffix = ".tar.gz"

// FileName is the file name of the archive of a version, "<ns>-<name>-<version>.tar.gz", the name `ansible-galaxy` builds and downloads.
func FileName(namespace, name, version string) string {
	return namespace + "-" + name + "-" + version + ArchiveSuffix
}

// ParseFileName splits the file name of an archive into namespace, name and version. A namespace and a name hold no hyphen, so the
// first two hyphens end them and the rest is the version (which may hold hyphens). ok is false for a name that has no suffix or
// whose parts are not valid; the version is not checked here.
func ParseFileName(file string) (namespace, name, version string, ok bool) {
	base, found := strings.CutSuffix(file, ArchiveSuffix)
	if !found {
		return "", "", "", false
	}
	namespace, rest, found := strings.Cut(base, "-")
	if !found {
		return "", "", "", false
	}
	name, version, found = strings.Cut(rest, "-")
	if !found || version == "" || !ValidPart(namespace) || !ValidPart(name) {
		return "", "", "", false
	}
	return namespace, name, version, true
}
