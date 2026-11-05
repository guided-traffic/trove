package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The persistence port of the engine: mirror_state and mirror_pending (R-REPO-053's data model). The PostgreSQL
// implementation is PGStore; the engine's unit tests use an in-memory one. Every method that writes for a pass takes the
// pass's Fence and returns ErrFenceLost when no row matches it (Q-ARCH-287).

// ErrFenceLost says that another job owns the pass (or the row is gone): the job stops at once without writing more.
var ErrFenceLost = errors.New("mirror: the pass is owned by another job")

// The limits of a package's attempts in one pass (R-REPO-053 step 3).
const (
	// MaxPackageAttempts is the number of attempts after which a failing package is left to the next pass.
	MaxPackageAttempts = 3
	// PackageRetryStep is the wait between the attempts of a failing package, per attempt already made.
	PackageRetryStep = time.Minute
	// MaxPending bounds the names a pass holds pending: discovery pauses at this number (R-REPO-053 step 2).
	MaxPending = 10000
)

// Fence names the owner of a pass: the repository, the job and the instance of the process that claimed it.
type Fence struct {
	RepoID int64
	JobID  string
	Holder string
}

// State is a row of mirror_state as the engine reads it. A time that is not set is the zero time.
type State struct {
	RepoID int64

	NextPassAt        time.Time
	PassRequestedAt   time.Time
	PassRequestedFull bool

	// The pass in progress; PassStartedAt is zero when none is.
	PassStartedAt      time.Time
	PassKind           discover.PassKind
	PassMethod         discover.Method
	PassJobID          string
	PassHolder         string
	PassCursor         json.RawMessage
	PassDiscoveryDone  bool
	PassNames          int64
	PassPackagesDone   int64
	PassPackagesFailed int64
	PassItemsStored    int64
	PassBytesStored    int64
	// PassDiscoveryComplete is false when the upstream truncated the method; PassDiscoveryNote says how.
	PassDiscoveryComplete bool
	PassDiscoveryNote     string

	// The last complete pass.
	FeedCursor          json.RawMessage
	FeedMethod          discover.Method
	PatternsFingerprint []byte
	LastFullPassAt      time.Time

	ConsecutiveFailures int
	RateLimitedUntil    time.Time
	BoundReached        string
	BoundReachedAt      time.Time
}

// InProgress reports whether a pass is in progress.
func (s State) InProgress() bool { return !s.PassStartedAt.IsZero() }

// Claim is the input of Store.Claim.
type Claim struct {
	Fence
	// Full is a full pass asked for by the job's arguments; the request recorded in the row counts too.
	Full bool
	// KindIfNew is the kind of a pass that starts now (the caller's decision, DecideKind).
	KindIfNew discover.PassKind
	Now       time.Time
}

// PendingName is a pending name with the attempts made so far.
type PendingName struct {
	Name     string
	Attempts int
}

// Names is one batch of discovered names for Store.AddNames.
type Names struct {
	Names []string
	// SetProgress stores the cursor and the flags of the pass with the names; false only adds the names.
	SetProgress bool
	Cursor      discover.Cursor
	Done        bool
	Truncated   bool
	Note        string
}

// PackageResult is what a package's attempt leaves: the items it stored count whatever happened to it.
type PackageResult struct {
	Name  string
	Items int64
	Bytes int64
	Now   time.Time
	// Error is the message recorded with a failed package.
	Error string
}

// End is the end of a pass or of its job (R-REPO-053 step 4, the stops).
type End struct {
	Outcome string
	// LastPass is the summary the status shows (R-REPO-056).
	LastPass json.RawMessage
	Now      time.Time
	// NextPassAt is when the next pass is due.
	NextPassAt time.Time
	// KeepPass leaves the pass in progress for the next job (a pause, offline); false clears it.
	KeepPass bool
	// ClearPending deletes the pending names (a mode change).
	ClearPending bool
	// Advance moves the pass's cursor to the cursor of the last complete pass and records the time of the last full pass
	// and the fingerprint of the patterns.
	Advance       bool
	Fingerprint   []byte
	ResetFailures bool
	// Bound sets the bound that ended the pass; ClearBound clears it.
	Bound      string
	ClearBound bool
}

// Usage is the folded counter of a repository's logical bytes with the time of its last fold and the store's clock when it
// was read (R-REPO-052).
type Usage struct {
	LogicalBytes int64
	// FoldedAt is the time of the counter's last fold; zero when the repository has no counter row.
	FoldedAt time.Time
	// ReadAt is the store's clock at the read.
	ReadAt time.Time
}

// GaugeRow is a repository's values for the gauges read from PostgreSQL (R-REPO-057).
type GaugeRow struct {
	Key     string
	Pending int64
	// LastComplete is the end of the last pass that ended complete, partial or at its bound; zero when none did.
	LastComplete time.Time
}

// Failure is what RecordFailure wrote.
type Failure struct {
	Failures   int
	NextPassAt time.Time
}

// Store is the persistence of the engine.
type Store interface {
	// EnsureStates creates the row of every repository that has none.
	EnsureStates(ctx context.Context, repoIDs []int64) error
	// Due returns the ids among repoIDs that are due at now.
	Due(ctx context.Context, repoIDs []int64, now time.Time) ([]int64, error)
	// ClearInactive clears the pass in progress, its fence and the pending names of the repositories in left, the ones of the
	// snapshot that are not in mode mirror any more.
	ClearInactive(ctx context.Context, left []int64) error

	// State reads the row; false when there is none.
	State(ctx context.Context, repoID int64) (State, bool, error)
	// Claim makes the job the owner of the repository's pass (R-REPO-053 step 1); false when the repository has no row.
	Claim(ctx context.Context, c Claim) (bool, error)
	// SetMethod stores the method and the kind of the pass.
	SetMethod(ctx context.Context, f Fence, method discover.Method, kind discover.PassKind) error

	// PendingCount counts the pending names a pass still takes.
	PendingCount(ctx context.Context, repoID int64) (int64, error)
	// AddNames inserts names and, with SetProgress, stores the cursor and the flags in one transaction; it returns the
	// number of names that were new.
	AddNames(ctx context.Context, f Fence, n Names) (added int64, err error)
	// HeldNames returns up to limit names of the packages the repository holds after one name, in name order.
	HeldNames(ctx context.Context, repoID int64, after string, limit int) ([]string, error)
	// NextPending returns up to limit pending names after one name in name order that a pass takes at now.
	NextPending(ctx context.Context, repoID int64, now time.Time, after string, limit int) ([]PendingName, error)
	// NextWake returns the earliest time a waiting pending name becomes due; zero when none waits.
	NextWake(ctx context.Context, repoID int64, now time.Time) (time.Time, error)

	// PackageDone deletes the pending row and adds to the counters in one transaction.
	PackageDone(ctx context.Context, f Fence, r PackageResult) error
	// PackageFailed counts one more attempt of the package and returns the attempts made; the attempt that reaches
	// MaxPackageAttempts counts the package as failed.
	PackageFailed(ctx context.Context, f Fence, r PackageResult) (attempts int, err error)
	// PackageKept adds to the counters and leaves the row as it is.
	PackageKept(ctx context.Context, f Fence, r PackageResult) error

	// RecordRateLimit counts one wait of the pass.
	RecordRateLimit(ctx context.Context, f Fence, now time.Time, wait time.Duration, status int) error
	// Defer stores the end of a long wait: the pass stays, the next job is due at until.
	Defer(ctx context.Context, f Fence, until time.Time) error
	// EndPass writes the end of a pass; boundNew says that the bound it set was not set before.
	EndPass(ctx context.Context, f Fence, e End) (boundNew bool, err error)
	// RecordFailure records a failure of the pass itself, counts it and sets next_pass_at after the back-off; jitter is the
	// factor in [0.8, 1.2] that scales it.
	RecordFailure(ctx context.Context, f Fence, now time.Time, code, message string, jitter float64) (Failure, error)

	// Usage reads the folded counter of the repository.
	Usage(ctx context.Context, repoID int64) (Usage, error)
}
