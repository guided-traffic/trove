package cache

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
)

// The top-level names of the layout (R-BAS-030). Artifacts are below sha256/ as the spec has them; the bytes of
// shared documents are below documents/ in the same form, so that the two areas of R-BAS-031 are two listings
// and a document never evicts an artifact (Q-BAS-87).
const (
	dirArtifacts = "sha256"
	dirDocuments = "documents"
	dirEntries   = "entries"
	dirAccess    = "access"
	dirSpool     = "audit-spool"
	dirProbe     = "probe"
)

type areaKey struct{}

// WithArea says which area the bytes of the call belong to: [EntryDocument] for the bytes of a shared document,
// anything else (the default) for an artifact. PutObject, GetObject, HeadObject and DeleteObject find the bytes
// below sha256/ or documents/ by it, so a caller that handles a document carries the area of its entry's kind on
// the context. The wrong area is a miss, never a wrong answer (Q-BAS-87).
func WithArea(ctx context.Context, kind EntryKind) context.Context {
	return context.WithValue(ctx, areaKey{}, kind)
}

func areaOf(ctx context.Context) EntryKind {
	if k, ok := ctx.Value(areaKey{}).(EntryKind); ok && k == EntryDocument {
		return EntryDocument
	}
	return EntryArtifact
}

var (
	hex64   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	replica = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	spoolFn = regexp.MustCompile(`^[0-9]{1,19}\.jsonl$`)
)

func validHash(s string) bool { return hex64.MatchString(s) }

// objectKey is the path of the bytes of a content address: <dir>/<h0h1>/<h2h3>/<64 hex>.
func objectKey(area EntryKind, sha string) (string, error) {
	if !validHash(sha) {
		return "", fmt.Errorf("%w: content address %q", ErrInvalid, shorten(sha))
	}
	dir := dirArtifacts
	if area == EntryDocument {
		dir = dirDocuments
	}
	return dir + "/" + sha[0:2] + "/" + sha[2:4] + "/" + sha, nil
}

// entryKey is the path of an entry: entries/<k0k1>/<sha256 of the cache key>.json.
func entryKey(keyHash string) (string, error) {
	if !validHash(keyHash) {
		return "", fmt.Errorf("%w: key hash %q", ErrInvalid, shorten(keyHash))
	}
	return dirEntries + "/" + keyHash[0:2] + "/" + keyHash + ".json", nil
}

func accessKey(sha string) (string, error) {
	if !validHash(sha) {
		return "", fmt.Errorf("%w: content address %q", ErrInvalid, shorten(sha))
	}
	return dirAccess + "/" + sha, nil
}

func spoolKey(replicaName, name string) (string, error) {
	if !replica.MatchString(replicaName) || !spoolFn.MatchString(name) {
		return "", fmt.Errorf("%w: spool file %q/%q", ErrInvalid, shorten(replicaName), shorten(name))
	}
	return dirSpool + "/" + replicaName + "/" + name, nil
}

func shorten(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// areaDir maps the area label of the metrics to the kind and the directory of its bytes.
func areaDir(area string) (EntryKind, string, error) {
	switch area {
	case metrics.AreaArtifacts:
		return EntryArtifact, dirArtifacts + "/", nil
	case metrics.AreaDocuments:
		return EntryDocument, dirDocuments + "/", nil
	}
	return "", "", fmt.Errorf("%w: area %q", ErrInvalid, area)
}

// base is the last element of a slash path.
func base(key string) string {
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		return key[i+1:]
	}
	return key
}
