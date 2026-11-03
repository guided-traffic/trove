// Package cache is the Bastion's cache: the interface of a backend that holds content-addressed bytes, signed
// entries, access markers and the audit spool (R-BAS-030 to R-BAS-032), the entry types, and the HMAC signer
// of R-BAS-032. The bucket backend and the volume backend implement [Backend]; the engine and the download
// buffer use the [Cache] around it and know neither.
//
// The layout below the backend's root (a bucket prefix or the volume directory) is the same for both:
//
//	sha256/<h0h1>/<h2h3>/<64 hex>               the bytes of an artifact, by content address
//	documents/<h0h1>/<h2h3>/<64 hex>            the bytes of a shared document (see [WithArea], Q-BAS-87)
//	entries/<k0k1>/<sha256 of the cache key>.json   signed entries
//	access/<64 hex>                              empty access markers; the modification time is the last access
//	audit-spool/<replica>/<unix ms>.jsonl        events a draining replica could not send
//
// A fill is spooled to temporary storage and committed under its content address only after its SHA-256 was
// verified (R-BAS-021); a cache failure after the first readiness turns hits into misses that are passed
// through, counted in trove_bastion_cache_errors_total, never into failed requests (R-BAS-030).
package cache

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// Errors of a backend.
var (
	// ErrNotFound: the object, entry or marker does not exist. A read of an entry whose bytes are gone is a
	// miss and removes the entry (R-BAS-031).
	ErrNotFound = errors.New("cache: not found")
	// ErrBadSignature: a record failed its signature; it is ignored and deleted (R-BAS-032).
	ErrBadSignature = errors.New("cache: bad signature")
	// ErrMismatch: bytes offered under a content address do not have that SHA-256 or that size.
	ErrMismatch = errors.New("cache: the bytes do not match their content address")
	// ErrCorrupt: stored bytes failed verification while they were served (R-BAS-032); the object is deleted.
	ErrCorrupt = errors.New("cache: stored bytes failed verification")
	// ErrTooLarge: a fill grew past the limit of its kind (bastion.cache.max_object_bytes, documents_max_bytes).
	ErrTooLarge = errors.New("cache: the object is larger than the cache keeps")
	// ErrInvalid: a content address, key hash or replica name that is not of the layout's form.
	ErrInvalid = errors.New("cache: invalid name")
)

// EntryKind says what an entry holds.
type EntryKind string

// The kinds of entries.
const (
	// EntryArtifact: the bytes are an artifact, kept per cache key (R-BAS-022).
	EntryArtifact EntryKind = "artifact"
	// EntryDocument: a shared document (class shared), within bastion.cache.documents_max_bytes (R-BAS-023).
	EntryDocument EntryKind = "document"
)

// Entry is what is kept per cache key, signed (R-BAS-022, R-BAS-023, R-BAS-032). The cache key is the path and
// query as received plus the normalized values of the Vary headers (R-BAS-018). The signature covers every
// field and the cache key, so an entry moved to another key, or pointed at other bytes, fails verification.
type Entry struct {
	Kind EntryKind `json:"kind"`
	// SHA256 is the content address of the bytes, lower-case hex.
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Header holds the response headers to replay: Content-Type, ETag, Cache-Control, Content-Disposition,
	// Vary and the like, never a Trove-Bastion-* header, Set-Cookie or a hop-by-hop header.
	Header http.Header `json:"header"`
	// Repository is the addressed repository key (Trove-Bastion-Repository).
	Repository string `json:"repository"`
	// Status is the status the main installation answered (200).
	Status int `json:"status"`
	// ValidatedAt is the time the main installation last answered 200 or 304 for the entry.
	ValidatedAt time.Time `json:"validatedAt"`
}

// ObjectInfo describes stored bytes.
type ObjectInfo struct {
	Size int64
	// LastAccess is the object's own modification time. The access markers are read by the sweep only; a
	// head does not cost a second request for them.
	LastAccess time.Time
}

// SpoolFile names a file of the audit spool.
type SpoolFile struct {
	Replica string
	Name    string
	Size    int64
}

// Usage is what the backend holds by area (trove_bastion_cache_bytes).
type Usage struct {
	Artifacts int64
	Documents int64
}

// Backend is a cache backend. Implementations are safe for concurrent use and apply the layout above; only
// PutObject, GetObject, HeadObject, ListObjectsV2 and DeleteObject of S3 are used by the bucket backend, no
// versioning and no conditional write (R-BAS-030).
type Backend interface {
	// Check is the first-readiness check (R-STOR-035's pattern, R-BAS-043): write, read and delete a probe.
	Check(ctx context.Context) error

	// PutObject stores the bytes of r under their content address; the caller has verified sha256 and size
	// (the verifying reader, R-BAS-021). Storing an existing address is not an error.
	PutObject(ctx context.Context, sha256 string, size int64, r io.Reader) error
	// GetObject opens the bytes; offset and length select a range (length -1 is to the end). The reader's
	// caller verifies the SHA-256 while serving (R-BAS-032).
	GetObject(ctx context.Context, sha256 string, offset, length int64) (io.ReadCloser, error)
	// HeadObject returns the size and the last access, or [ErrNotFound].
	HeadObject(ctx context.Context, sha256 string) (ObjectInfo, error)
	// DeleteObject removes the bytes and their access marker; the entries that point at them are removed by
	// the sweep and on read (R-BAS-031).
	DeleteObject(ctx context.Context, sha256 string) error

	// PutEntry stores a signed entry record under the SHA-256 of the cache key (hex); GetEntry reads it;
	// DeleteEntry removes it. The record is opaque to the backend: [Signer] writes and verifies it.
	PutEntry(ctx context.Context, keyHash string, record []byte) error
	GetEntry(ctx context.Context, keyHash string) ([]byte, error)
	DeleteEntry(ctx context.Context, keyHash string) error

	// Touch writes the access marker of a content address (R-BAS-031); the engine calls it at most once per
	// bastion.cache.access_resolution per replica.
	Touch(ctx context.Context, sha256 string) error

	// Usage reports the bytes held. Sweep deletes in order of last access until target bytes remain
	// when the total is above high (95 % of a limit down to 85 %, R-BAS-031) and reports what it evicted.
	Usage(ctx context.Context) (Usage, error)
	Sweep(ctx context.Context, area string, high, target int64) (evicted int, err error)

	// The audit spool (R-BAS-039): a draining replica writes the events it could not send; any replica reads,
	// sends and deletes a file after a 2xx.
	SpoolWrite(ctx context.Context, replica string, jsonl []byte) error
	SpoolList(ctx context.Context) ([]SpoolFile, error)
	SpoolRead(ctx context.Context, f SpoolFile) ([]byte, error)
	SpoolDelete(ctx context.Context, f SpoolFile) error
}
