package jobs

import (
	"errors"
	"fmt"
)

var (
	// ErrNotWorker is returned by NewWorker for a role that does not run
	// workers: a web replica never claims a job (R-ARCH-001).
	ErrNotWorker = errors.New("jobs: only worker and all replicas claim jobs")
	// ErrUnknownKind is returned by Enqueue for a kind nobody registered.
	ErrUnknownKind = errors.New("jobs: unknown job kind")
	// ErrLeaseLost is the cause of a handler's context when the worker no longer
	// owns the job: the heartbeat found the row taken, or could not extend it in
	// time. The handler must stop changing shared state for the job.
	ErrLeaseLost = errors.New("jobs: the job lease was lost")
	// ErrCancelRequested is the cause of a handler's context when the job was
	// cancelled while it ran. The handler returns at its next checkpoint and the
	// job ends cancelled.
	ErrCancelRequested = errors.New("jobs: the job was cancelled")
	// ErrReleased is the cause of a handler's context at shutdown: the handler
	// persists its checkpoint and returns, and the job is released without
	// counting the attempt.
	ErrReleased = errors.New("jobs: the job is released for shutdown")
	// ErrTimeout is the cause of a handler's context when the kind's Timeout
	// passed.
	ErrTimeout = errors.New("jobs: the job timed out")
)

// permanent marks an error that retrying cannot fix.
type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent wraps err so that the job fails without further attempts.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// IsPermanent reports whether err was marked with [Permanent].
func IsPermanent(err error) bool {
	var p permanent
	return errors.As(err, &p)
}

func kindError(kind string, format string, args ...any) error {
	return fmt.Errorf("jobs: kind %q: %s", kind, fmt.Sprintf(format, args...))
}
