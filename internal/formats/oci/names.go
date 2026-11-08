package oci

import (
	"regexp"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/oci/manifest"
)

// The grammars of R-OCI-002. They are the ones of the distribution specification
// (https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md#pulling-manifests): an image name is components
// separated by '/', a component is lower-case alphanumeric runs separated by '.', '_', '__' or runs of '-'; a tag is
// [a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}; a digest is sha256: and 64 lower-case hexadecimal digits (Q-OCI-9).
const (
	// MaxFullNameBytes bounds "<projectKey>/<repoKey>/<name>" as a client sends it (R-OCI-002).
	MaxFullNameBytes = 255
	// MaxManifestBytes bounds a manifest (R-OCI-020, Q-OCI-8).
	MaxManifestBytes = 4 << 20
)

var (
	nameRE = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagRE  = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)
)

// patternChars are the characters of a name pattern (R-AUTHZ-005); a string that holds one is a pattern and is not checked
// against the grammar.
const patternChars = `*?[]{},!\`

// ValidName reports whether s is an image name below the repository: the component grammar, without the bound on the whole name.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// ValidFullName reports whether the whole name a client sends, "<projectKey>/<repoKey>/<name>", is within the bound.
func ValidFullName(project, repo, name string) bool {
	return len(project)+len(repo)+len(name)+2 <= MaxFullNameBytes
}

// ValidTag reports whether s is a tag.
func ValidTag(s string) bool { return tagRE.MatchString(s) }

// ValidDigest reports whether s is a digest: sha256 and 64 lower-case hexadecimal digits.
func ValidDigest(s string) bool { return manifest.ValidDigest(s) }

// names is the package-name rule of the format (R-AUTHZ-005): the package is the image name. A name is never folded
// (upper-case letters are refused), so Normalize either returns the string as it is or refuses it; a pattern is left
// unchanged.
type names struct{}

func (names) Normalize(s string) (string, bool) {
	if strings.ContainsAny(s, patternChars) {
		return s, true
	}
	return s, ValidName(s)
}
