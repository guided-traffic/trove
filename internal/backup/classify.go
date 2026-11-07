package backup

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// Classify gives an error from a dependency the exit code of R-BAK-013: an error that already carries a code, and the
// sentinels [ExitCode] maps, pass through; a database or target that cannot be reached is temporary (75); anything else is a
// failure of the run (1).
func Classify(err error) error {
	if err == nil {
		return nil
	}
	var ee *ExitError
	if errors.As(err, &ee) || ExitCode(err) != ExitFailed {
		return err
	}
	if errors.Is(err, ErrSuperseded) || errors.Is(err, ErrHoldLost) || errors.Is(err, ErrPoolDamaged) || errors.Is(err, ErrInvariantViolation) {
		return err
	}
	if Temporary(err) {
		return Fail(ExitTemporary, CodeTemporary, err)
	}
	return err
}

// Temporary reports an unreachable database or target, or a request that ran into its deadline: retrying later is useful.
func Temporary(err error) bool {
	var ne net.Error
	switch {
	case postgres.IsUnavailable(err), blob.IsTransient(err):
		return true
	case errors.As(err, &ne), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.EPIPE), errors.Is(err, context.DeadlineExceeded):
		return true
	}
	return false
}
