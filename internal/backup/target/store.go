// Package target is the port of the backup target (R-BAK-002, R-BAK-006, R-BAK-026): the operations the backup lane needs
// of the S3 location backups are written to, with the target's own credentials and its own encryption settings.
//
// The interface is here so that the create pipeline, the retention and sweep code, verify and restore can be written and
// tested against an in-memory implementation (internal/backup/target/targettest) while the S3 implementation is built in
// internal/blob/s3, the only package that imports the AWS SDK (R-ARCH-002). The composition root of the backup commands
// (cmd/trove) joins the two; neither side imports the other.
//
// Every write applies the configured server-side encryption (backup.target.sse); a caller never passes encryption
// parameters, so a copy request cannot forget them (R-BAK-026).
package target

import (
	"context"
	"errors"
	"io"
	"time"
)

// Errors a Store returns. Anything else is a transport or store failure; the lane retries or fails by its own rules
// (R-BAK-013, exit 75 for an unreachable target).
var (
	// ErrNotFound is a missing object.
	ErrNotFound = errors.New("target: object not found")
	// ErrExists is a conditional create (PutOptions.IfAbsent) that found the object.
	ErrExists = errors.New("target: object exists")
	// ErrConditionalUnsupported is a store that rejects conditional writes. The marker and the pool never need one for
	// correctness: Q-BAK-4 keeps the one-run rule in PostgreSQL.
	ErrConditionalUnsupported = errors.New("target: the store does not support conditional writes")
)

// Object describes a stored object.
type Object struct {
	Key  string
	Size int64
	// ETag is the store's entity tag. It is not a content digest for multipart or encrypted objects.
	ETag         string
	LastModified time.Time
	// SSE is the server-side encryption the store reports: "AES256", "aws:kms" or "" for none.
	SSE      string
	KMSKeyID string
	// SHA256 is the full-object SHA-256 as lowercase hex, where the store reports one for the object (R-BAK-006). Empty
	// when it does not.
	SHA256 string
}

// PutOptions are the optional parts of a write.
type PutOptions struct {
	// IfAbsent makes the write a conditional create: it fails with ErrExists when the key exists.
	IfAbsent bool
	// ContentType is stored with the object.
	ContentType string
	// SizeHint is the expected size of a streaming write, 0 when unknown. The writer picks its part size from it so that the
	// object fits S3's 10,000 parts; a hint above the 5 TiB object limit fails Writer up front. Without a hint the part size
	// grows with the part number, which reaches about 850 GiB.
	SizeHint int64
}

// ObjectRef names an object in a bucket that is not the target, the source of a server-side copy.
type ObjectRef struct {
	Endpoint string
	Bucket   string
	Key      string
	Size     int64
	// Progress, when set, is called with the bytes copied so far: after each finished part of a multipart copy and about every
	// 10 seconds while a request is outstanding, so that a long server-side copy ticks the run's lease (R-BAK-013). It runs on
	// a goroutine of the adapter, concurrently with the caller's own code, and must be safe for that; it must not block.
	Progress func(copied int64)
}

// CopyMode says how a pool object reached the target (R-BAK-006), the label of
// trove_backup_copied_objects_total.
type CopyMode string

// The copy modes.
const (
	// CopyServer moved the bytes inside the store.
	CopyServer CopyMode = "server"
	// CopyStream moved them through the pod.
	CopyStream CopyMode = "stream"
)

// ObjectWriter is a streaming upload: the table files and the blob list are written through one and never touch local disk
// (R-BAK-005 step 5). The implementation uploads in parts of backup.database.part_size.
type ObjectWriter interface {
	io.Writer
	// Close completes the upload and returns the stored object. After a failed Close the upload is aborted.
	Close(ctx context.Context) (Object, error)
	// Abort discards the upload, best effort.
	Abort(ctx context.Context) error
}

// Store is the target.
type Store interface {
	// Head returns the metadata of an object, or ErrNotFound.
	Head(ctx context.Context, key string) (Object, error)
	// Get opens an object, or ErrNotFound. The caller closes the body.
	Get(ctx context.Context, key string) (io.ReadCloser, Object, error)
	// Put writes a small object in one request: the marker, the manifest.
	Put(ctx context.Context, key string, body []byte, opts PutOptions) (Object, error)
	// Writer starts a streaming upload of an object of unknown size.
	Writer(ctx context.Context, key string, opts PutOptions) (ObjectWriter, error)
	// List calls fn for every object whose key starts with prefix, in lexicographic key order, page by page. The pool sweep
	// depends on the order (R-BAK-017). fn returning an error stops the listing with that error.
	List(ctx context.Context, prefix string, fn func(Object) error) error
	// Delete removes objects, in requests of up to 1,000 keys. A missing key is not an error.
	Delete(ctx context.Context, keys []string) error
	// Copy writes a pool object from the blob store (or another bucket) to dstKey. It is a server-side copy
	// (CopyObject, or UploadPartCopy parts above 5 GiB) when the credentials and the endpoints allow it and mode is not
	// "never"; with "auto" it falls back to ErrServerCopyUnavailable so that the caller streams instead.
	Copy(ctx context.Context, src ObjectRef, dstKey string) error
	// Probe writes, reads back and deletes a small object under key, and returns what HeadObject reported, so the
	// preflight can check reachability, writability and the server-side encryption of R-BAK-026.
	Probe(ctx context.Context, key string) (Object, error)
}

// ErrServerCopyUnavailable tells the caller to stream: the store rejected the server-side copy (AccessDenied on the
// source, a different endpoint, a store without cross-bucket copy, or a copy that did not finish within the bound derived from
// the object's size: the caller streams, and the bytes of the abandoned copy are aborted). With backup.copy.server_side "auto" the run probes
// once, remembers the answer and streams from then on; "always" turns it into a run failure.
var ErrServerCopyUnavailable = errors.New("target: server-side copy is not available")
