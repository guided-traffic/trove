package ansible

import (
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
)

// The names of the parameters of a match.
const (
	paramNamespace = "ns"
	paramName      = "name"
	paramVersion   = "version"
	paramFile      = "file"
	paramID        = "id"
)

// The first segments of the protocol paths (R-ANS-003 to R-ANS-009, R-ANS-020).
const (
	segV3          = "v3"
	segV1          = "v1"
	segCollections = "collections"
	segVersions    = "versions"
	segDocsBlob    = "docs-blob"
	segArtifacts   = "artifacts"
	segImports     = "imports"
	segRoles       = "roles"
	segArchives    = "archives"
)

// protocolSegments splits the escaped repository-relative path of a request into decoded segments. One trailing slash is not a
// segment: the clients put it on every address they build, and a request without it is served the same (R-ANS-003). A segment that
// decodes to the empty string or to something with a '/' is no match, so a %2F never turns into a separator here.
func protocolSegments(escaped string) ([]string, bool) {
	p := strings.TrimSuffix(strings.TrimPrefix(escaped, "/"), "/")
	if p == "" {
		return nil, true
	}
	parts := strings.Split(p, "/")
	for i, s := range parts {
		d, err := url.PathUnescape(s)
		if err != nil || d == "" || strings.Contains(d, "/") {
			return nil, false
		}
		parts[i] = d
	}
	return parts, true
}

// shape matches a path of the given number of segments whose literal segments are fixed (lits maps the index to the literal) and
// returns the other segments by index; the caller names them.
func shape(escaped string, n int, lits map[int]string) ([]string, bool) {
	s, ok := protocolSegments(escaped)
	if !ok || len(s) != n {
		return nil, false
	}
	for i, l := range lits {
		if s[i] != l {
			return nil, false
		}
	}
	return s, true
}

// matchRoot matches the repository URL itself, with or without its trailing slash (R-ANS-003).
func matchRoot(escaped string) (formats.Params, bool) {
	s, ok := protocolSegments(escaped)
	if !ok || len(s) != 0 {
		return nil, false
	}
	return formats.Params{}, true
}

// matchCollection matches "v3/collections/{ns}/{name}".
func matchCollection(escaped string) (formats.Params, bool) {
	s, ok := shape(escaped, 4, map[int]string{0: segV3, 1: segCollections})
	if !ok {
		return nil, false
	}
	return formats.Params{paramNamespace: s[2], paramName: s[3]}, true
}

// matchVersions matches "v3/collections/{ns}/{name}/versions".
func matchVersions(escaped string) (formats.Params, bool) {
	s, ok := shape(escaped, 5, map[int]string{0: segV3, 1: segCollections, 4: segVersions})
	if !ok {
		return nil, false
	}
	return formats.Params{paramNamespace: s[2], paramName: s[3]}, true
}

// matchVersion matches "v3/collections/{ns}/{name}/versions/{version}".
func matchVersion(escaped string) (formats.Params, bool) {
	s, ok := shape(escaped, 6, map[int]string{0: segV3, 1: segCollections, 4: segVersions})
	if !ok {
		return nil, false
	}
	return formats.Params{paramNamespace: s[2], paramName: s[3], paramVersion: s[5]}, true
}

// matchDocs matches "v3/collections/{ns}/{name}/versions/{version}/docs-blob", the documentation of a version (R-ANS-035).
func matchDocs(escaped string) (formats.Params, bool) {
	s, ok := shape(escaped, 7, map[int]string{0: segV3, 1: segCollections, 4: segVersions, 6: segDocsBlob})
	if !ok {
		return nil, false
	}
	return formats.Params{paramNamespace: s[2], paramName: s[3], paramVersion: s[5]}, true
}

// matchArtifact matches "v3/artifacts/collections/{file}".
func matchArtifact(escaped string) (formats.Params, bool) {
	s, ok := shape(escaped, 4, map[int]string{0: segV3, 1: segArtifacts, 2: segCollections})
	if !ok {
		return nil, false
	}
	return formats.Params{paramFile: s[3]}, true
}

// matchPublish matches "v3/artifacts/collections", the address a collection is published to.
func matchPublish(escaped string) (formats.Params, bool) {
	if _, ok := shape(escaped, 3, map[int]string{0: segV3, 1: segArtifacts, 2: segCollections}); !ok {
		return nil, false
	}
	return formats.Params{}, true
}

// matchImport matches "v3/imports/collections/{id}".
func matchImport(escaped string) (formats.Params, bool) {
	s, ok := shape(escaped, 4, map[int]string{0: segV3, 1: segImports, 2: segCollections})
	if !ok {
		return nil, false
	}
	return formats.Params{paramID: s[3]}, true
}

// matchRoleFind matches "v1/roles", the lookup of a role by owner and name.
func matchRoleFind(escaped string) (formats.Params, bool) {
	if _, ok := shape(escaped, 2, map[int]string{0: segV1, 1: segRoles}); !ok {
		return nil, false
	}
	return formats.Params{}, true
}

// matchRoleVersions matches "v1/roles/{id}/versions".
func matchRoleVersions(escaped string) (formats.Params, bool) {
	s, ok := shape(escaped, 4, map[int]string{0: segV1, 1: segRoles, 3: segVersions})
	if !ok {
		return nil, false
	}
	return formats.Params{paramID: s[2]}, true
}

// matchRoleArchive matches "v1/roles/archives/{id}/{file}".
func matchRoleArchive(escaped string) (formats.Params, bool) {
	s, ok := shape(escaped, 5, map[int]string{0: segV1, 1: segRoles, 2: segArchives})
	if !ok {
		return nil, false
	}
	return formats.Params{paramID: s[3], paramFile: s[4]}, true
}
