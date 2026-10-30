package npm

import (
	"strings"

	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// The suffixes of the files of a version (R-NPM-006, R-NPM-015).
const (
	tarballSuffix  = ".tgz"
	sigstoreSuffix = ".sigstore"
)

// TarballFile is the file name of a version's tarball, "<basename>-<version>.tgz",
// the basename being the name without its scope (R-NPM-015).
func TarballFile(n name.Name, version string) string {
	return n.Package() + "-" + version + tarballSuffix
}

// SigstoreFile is the file name of a version's provenance bundle.
func SigstoreFile(n name.Name, version string) string {
	return n.Package() + "-" + version + sigstoreSuffix
}

// TarballPath is the canonical path of a version's tarball, the path of its item:
// "@acme/ui/-/ui-1.2.0.tgz" (R-NPM-003, R-NPM-006).
func TarballPath(n name.Name, version string) string {
	return n.String() + "/-/" + TarballFile(n, version)
}

// SigstorePath is the canonical path of a version's provenance bundle.
func SigstorePath(n name.Name, version string) string {
	return n.String() + "/-/" + SigstoreFile(n, version)
}

// VersionOfFile returns the version a file name of a package names, when the file
// is "<basename>-<version><suffix>" and the version is canonical (R-NPM-015: the
// version is what remains after removing "<basename>-" and the suffix and must be
// canonical; otherwise the request is a 404).
func VersionOfFile(n name.Name, file, suffix string) (string, bool) {
	rest, ok := strings.CutPrefix(file, n.Package()+"-")
	if !ok {
		return "", false
	}
	version, ok := strings.CutSuffix(rest, suffix)
	if !ok {
		return "", false
	}
	return canonicalVersion(version)
}

// canonicalVersion reports whether s is a version in the canonical form that
// trove stores and compares: SemVer 2.0.0 as written, without build metadata.
func canonicalVersion(s string) (string, bool) {
	v, err := semver.Parse(s)
	if err != nil || v.Canonical() != s {
		return "", false
	}
	return s, true
}
