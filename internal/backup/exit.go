package backup

import (
	"errors"
	"fmt"

	"github.com/guided-traffic/trove/internal/backup/format"
)

// The exit codes of the backup commands (R-BAK-013). The Job's pod failure policy reads 2, 3, 4 and 6 as FailJob and
// retries the others (R-BAK-012), so a command must use exactly these.
const (
	// ExitOK is success, also completed_with_warnings.
	ExitOK = 0
	// ExitFailed is an error the run could not handle: hold_lost, superseded, pool_damaged, a store or database error
	// after retries.
	ExitFailed = 1
	// ExitUsage is a usage or configuration error.
	ExitUsage = 2
	// ExitBusy is another live target-writing run (create, delete).
	ExitBusy = 3
	// ExitForeignTarget is a target of another installation or incarnation, or an adoption whose condition does not hold
	// (create, delete).
	ExitForeignTarget = 4
	// ExitFindings is verify with findings.
	ExitFindings = 5
	// ExitPrecondition is a restore or format precondition that does not hold, or an unknown manifest format (restore,
	// verify).
	ExitPrecondition = 6
	// ExitTemporary is a temporary failure worth retrying: database or target unreachable, a lock or migration wait that
	// timed out, a restore in progress, a stall, a SIGTERM.
	ExitTemporary = 75
)

// The failure codes written to backup_run.error.code, the audit event system.backup.failed and the log (R-BAK-009,
// R-BAK-013).
const (
	CodeHoldLost           = "hold_lost"
	CodeSuperseded         = "superseded"
	CodePoolDamaged        = "pool_damaged"
	CodeInvariantViolation = "invariant_violation"
	CodeStalled            = "stalled"
	CodeLeaseExpired       = "lease_expired"
	CodeConfig             = "config"
	CodeBusy               = "busy"
	CodeForeignTarget      = "foreign_target"
	CodeFindings           = "findings"
	CodePrecondition       = "precondition"
	CodeTemporary          = "temporary"
	CodeRestoreInProgress  = "restore_in_progress"
	CodeInterrupted        = "interrupted"
	CodeInternal           = "internal"
	CodeDeletionNotAllowed = "deletion_not_allowed"
	CodeKeyRingMismatch    = "key_ring_mismatch"
	CodeJournalUnavailable = "journal_unavailable"
	CodeFormatUnknown      = "format_unknown"
	CodeSchemaStepMismatch = "schema_step_mismatch"
	CodeDatabaseNotEmpty   = "database_not_empty"
	CodeBlobPrefixNotEmpty = "blob_prefix_not_empty"
	CodeOwnerPrivilege     = "owner_privilege_missing"
	CodeBackupRoleGrant    = "backup_grant_missing"
	CodeTargetSameBucket   = "target_same_bucket"
	CodeEncryptionMismatch = "encryption_mismatch"
	CodeStandby            = "database_in_recovery"
	CodeMigrationWait      = "migration_wait"
	CodeTableLockTimeout   = "table_lock_timeout"
	CodeCatalogChanged     = "catalog_changed"
	CodeMissingPoolObject  = "missing_pool_object"
)

// Sentinel errors of the lane. Wrap them with context; [ExitCode] and [ErrorCode] find them with errors.Is.
var (
	// ErrSuperseded is a heartbeat that matched no row: the run was abandoned, so another run took over (R-BAK-010).
	ErrSuperseded = errors.New("backup: the run was superseded")
	// ErrStalled is a run without progress for backup.stall_timeout (R-BAK-013, Q-BAK-11).
	ErrStalled = errors.New("backup: the run made no progress for backup.stall_timeout")
	// ErrHoldLost is a blob that vanished from the source after the hold was lost (R-BAK-006).
	ErrHoldLost = errors.New("backup: a blob left the source while the run still had to copy it")
	// ErrPoolDamaged is the spot check finding a missing pool object (R-BAK-009).
	ErrPoolDamaged = errors.New("backup: a pool object of an earlier backup is missing; run trove backup verify")
	// ErrInvariantViolation is a referenced blob row in state deleting (R-BAK-006).
	ErrInvariantViolation = errors.New("backup: a referenced blob row is in state deleting")
	// ErrFindings is verify with findings.
	ErrFindings = errors.New("backup: verify found missing, damaged or mismatching objects")
	// ErrNotBuilt is returned by a command whose implementation a later work package fills. It exits 1.
	ErrNotBuilt = errors.New("not built yet")
	// ErrRestoreInProgress is a create or delete while system_state holds restore_in_progress (R-BAK-009).
	ErrRestoreInProgress = errors.New("backup: the installation is being restored or migrated; nothing was written")
)

// ExitError carries an exit code, a failure code and the cause. cmd/trove prints the message and exits with Code.
type ExitError struct {
	Code    int
	Failure string
	Err     error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode lets cmd/trove's run function find the code (it asks errors.As for an ExitCode method).
func (e *ExitError) ExitCode() int { return e.Code }

// Fail wraps err with an exit code and a failure code.
func Fail(code int, failure string, err error) error {
	return &ExitError{Code: code, Failure: failure, Err: err}
}

// Failf is Fail with a formatted message.
func Failf(code int, failure, format string, args ...any) error {
	return &ExitError{Code: code, Failure: failure, Err: fmt.Errorf(format, args...)}
}

// BusyError is the refusal of exit 3: another live run holds the slot. It names the run (R-BAK-010).
type BusyError struct{ Active Run }

func (e *BusyError) Error() string {
	return fmt.Sprintf("another backup run is active: %s (%s, state %s, pod %s, started %s)",
		e.Active.ID, e.Active.Operation, e.Active.State, e.Active.Pod, e.Active.StartedAt.UTC().Format("2006-01-02T15:04:05Z"))
}

// ExitCode returns the exit code for an error from a backup command: the code of an ExitError, otherwise the code of the
// sentinel or typed error found in the chain, otherwise 1. A nil error is 0.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	var busy *BusyError
	var uf *format.UnknownFormatError
	switch {
	case errors.As(err, &busy):
		return ExitBusy
	case errors.Is(err, format.ErrForeignTarget):
		return ExitForeignTarget
	case errors.As(err, &uf):
		return ExitPrecondition
	case errors.Is(err, ErrFindings):
		return ExitFindings
	case errors.Is(err, ErrStalled), errors.Is(err, ErrRestoreInProgress):
		return ExitTemporary
	}
	return ExitFailed
}

// ErrorCode returns the failure code to record for an error.
func ErrorCode(err error) string {
	var ee *ExitError
	var busy *BusyError
	var uf *format.UnknownFormatError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ee) && ee.Failure != "":
		return ee.Failure
	case errors.As(err, &busy):
		return CodeBusy
	case errors.Is(err, format.ErrForeignTarget):
		return CodeForeignTarget
	case errors.As(err, &uf):
		return CodeFormatUnknown
	case errors.Is(err, ErrSuperseded):
		return CodeSuperseded
	case errors.Is(err, ErrStalled):
		return CodeStalled
	case errors.Is(err, ErrHoldLost):
		return CodeHoldLost
	case errors.Is(err, ErrPoolDamaged):
		return CodePoolDamaged
	case errors.Is(err, ErrInvariantViolation):
		return CodeInvariantViolation
	case errors.Is(err, ErrFindings):
		return CodeFindings
	case errors.Is(err, ErrRestoreInProgress):
		return CodeRestoreInProgress
	}
	return CodeInternal
}
