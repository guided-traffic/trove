package blob

import (
	"errors"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// Sentinel causes of the *errs.Error values the pipeline returns, so that a
// caller can tell the cases apart with errors.Is while the HTTP status follows
// from the error's kind.
var (
	// ErrTooLarge: the body is above the effective maximum upload size (413).
	ErrTooLarge = errors.New("upload exceeds the maximum size")
	// ErrSpoolFull: the spool budget or the spool volume cannot take the upload
	// (503 with Retry-After).
	ErrSpoolFull = errors.New("spool has no room for the upload")
	// ErrLengthMismatch: the body is not as long as Content-Length said (400).
	ErrLengthMismatch = errors.New("body length differs from the declared length")
	// ErrDigestMismatch: a declared digest differs from the computed one (409).
	ErrDigestMismatch = errors.New("declared digest differs from the content")
	// ErrBlobBusy: the blob row stayed in state deleting longer than
	// storage.upload.blob_wait_timeout (503 with Retry-After).
	ErrBlobBusy = errors.New("blob is being deleted")
	// ErrCommitAttempts: the commit protocol did not settle within
	// storage.upload.commit_attempts rounds (503 with Retry-After).
	ErrCommitAttempts = errors.New("commit did not settle")
	// ErrCorrupt: a download failed verification; the held-back bytes were not
	// released (R-STOR-013).
	ErrCorrupt = errors.New("stored object failed verification")
	// ErrBlobNotFound: no blob row has the digest.
	ErrBlobNotFound = errors.New("blob not found")
	// ErrFault is returned by Collector.Run when a test fault hook killed it.
	ErrFault = errors.New("fault injected")
)

const retryAfter = 5 * time.Second

func tooLarge(max int64) error {
	return errs.E(errs.TooLarge, errs.CodeTooLarge, "the upload exceeds the maximum size of "+strconv.FormatInt(max, 10)+" bytes", errs.WithCause(ErrTooLarge))
}

func spoolFull(detail string) error {
	return errs.E(errs.Unavailable, errs.CodeUnavailable, detail, errs.WithRetryAfter(retryAfter), errs.WithCause(ErrSpoolFull))
}

func lengthMismatch(detail string) error {
	return errs.E(errs.Invalid, errs.CodeInvalid, detail, errs.WithCause(ErrLengthMismatch))
}

func digestMismatch(algorithm string) error {
	return errs.E(errs.Conflict, errs.CodeConflict, "the declared "+algorithm+" digest differs from the content", errs.WithCause(ErrDigestMismatch))
}

// storeUnavailable maps an object-store failure to the 503 of R-STOR-036.
func storeUnavailable(err error) error {
	return errs.E(errs.Unavailable, errs.CodeObjectStoreUnavail, "the object store is unavailable; retry", errs.WithRetryAfter(retryAfter), errs.WithCause(err))
}
