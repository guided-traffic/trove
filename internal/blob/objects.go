package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrNotFound is returned by an ObjectStore for a missing key. It is not a
// failure of the store: the circuit breaker ignores it (R-STOR-033).
var ErrNotFound = errors.New("object not found")

// ErrCircuitOpen is returned by a Guard while its breaker is open: the blob
// operation fails fast (R-STOR-033, R-STOR-036).
var ErrCircuitOpen = errors.New("object store circuit is open")

// ErrorKind classifies a StoreError for the breaker and the startup check.
type ErrorKind uint8

// The kinds of StoreError.
const (
	// KindTransient: network errors, timeouts, 5xx, throttling and credential
	// retrieval failures. Counted by the breaker, retried by the startup check.
	KindTransient ErrorKind = iota
	// KindConfig: AccessDenied, NoSuchBucket, invalid region or credentials.
	// The startup check stops the process on these (R-STOR-035).
	KindConfig
	// KindOther: any other 4xx. Neither counted nor retried.
	KindOther
)

// StoreError is an error of the object store with its classification.
type StoreError struct {
	Op     string // the operation, for example "PutObject"
	Key    string // the key, when the operation has one
	Code   string // the store's error code, for example "AccessDenied"
	Status int    // the HTTP status, 0 when the request got no answer
	Kind   ErrorKind
	Err    error
}

func (e *StoreError) Error() string {
	s := "object store " + e.Op
	if e.Key != "" {
		s += " " + e.Key
	}
	if e.Code != "" {
		s += ": " + e.Code
	}
	if e.Status != 0 {
		s += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the underlying error.
func (e *StoreError) Unwrap() error { return e.Err }

// IsTransient reports whether err counts against the circuit breaker.
func IsTransient(err error) bool {
	var se *StoreError
	return errors.As(err, &se) && se.Kind == KindTransient
}

// IsConfig reports whether err is a configuration or permission problem of the
// store that retrying does not fix.
func IsConfig(err error) bool {
	var se *StoreError
	return errors.As(err, &se) && se.Kind == KindConfig
}

// Checksum is the payload checksum a request carries for the store to verify
// (R-STOR-006, storage.s3.payload_checksum).
type Checksum struct {
	// Algorithm is "sha256", "md5" or empty for none.
	Algorithm string
	Value     []byte
}

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}

// ByteRange selects Length bytes starting at Offset; a negative Length means
// to the end of the object.
type ByteRange struct {
	Offset int64
	Length int64
}

// Part is a finished part of a multipart upload.
type Part struct {
	Number   int
	ETag     string
	Checksum Checksum
	Size     int64
}

// MultipartUpload is an unfinished multipart upload found in the bucket.
type MultipartUpload struct {
	Key       string
	UploadID  string
	Initiated time.Time
}

// ListOptions narrows a listing.
type ListOptions struct {
	// Delimiter groups keys below a common prefix into ListPage.Prefixes.
	Delimiter string
	// StartAfter lists keys after this one.
	StartAfter string
	// Token continues a listing from ListPage.Next.
	Token string
	// Max is the largest page; zero means the store's default.
	Max int
}

// ListPage is one page of ListObjectsV2.
type ListPage struct {
	Objects  []ObjectInfo
	Prefixes []string
	Next     string // empty when the listing is complete
}

// DeleteFailure is a key a batch delete did not remove.
type DeleteFailure struct {
	Key  string
	Code string
}

// ObjectStore is the object-store client trove needs (R-STOR-030). Keys are
// full keys including the configured prefix. Implementations return
// ErrNotFound for a missing key and a *StoreError for every other store error.
// The interface is small on purpose: internal/blob/s3 implements it, Guard
// decorates it and internal/blob/blobtest fakes it.
type ObjectStore interface {
	// Put writes body, size bytes long, with one request. The body is seekable so
	// that a retry can send it again.
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sum Checksum) error
	// CreateMultipart starts a multipart upload; algorithm is the payload
	// checksum algorithm the parts will carry ("sha256", "md5" or "").
	CreateMultipart(ctx context.Context, key, algorithm string) (uploadID string, err error)
	// UploadPart sends one part.
	UploadPart(ctx context.Context, key, uploadID string, number int, body io.ReadSeeker, size int64, sum Checksum) (Part, error)
	// CompleteMultipart makes the object visible.
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error
	// AbortMultipart discards an upload and its parts.
	AbortMultipart(ctx context.Context, key, uploadID string) error
	// Head returns the size and modification time of an object.
	Head(ctx context.Context, key string) (ObjectInfo, error)
	// Get opens an object, optionally a range of it. The caller closes the body.
	Get(ctx context.Context, key string, rng *ByteRange) (io.ReadCloser, ObjectInfo, error)
	// Delete removes an object; a missing key is success.
	Delete(ctx context.Context, key string) error
	// DeleteMany removes up to 1000 objects with one request (DeleteObjects).
	// Keys the store reports as not deleted come back in failed.
	DeleteMany(ctx context.Context, keys []string) (failed []DeleteFailure, err error)
	// List lists keys below prefix (ListObjectsV2).
	List(ctx context.Context, prefix string, opts ListOptions) (ListPage, error)
	// ListMultipart lists unfinished multipart uploads below prefix.
	ListMultipart(ctx context.Context, prefix string) ([]MultipartUpload, error)
	// Presign returns a URL that serves GET of the key for ttl.
	Presign(ctx context.Context, key string, ttl time.Duration) (string, error)
}
