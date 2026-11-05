package mirror

import (
	"context"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/repository"
)

// Host is what the engine needs from the remote engine (internal/proxy) and the format host (internal/formathost) for the
// passes of one process (Q-ARCH-288, docs/developer/mirror.md). The composition root builds it; the engine's tests use a
// fake. Every request it sends is a background request (proxy.PriorityBackground): it takes a background slot of the
// replica within the share B of R-REPO-055 that Pass.Ceiling carries, a client fetch may pre-empt it, its outcomes neither
// move nor reset the circuit of R-REPO-029, and its upstream log record carries flight_role mirror and the pass's job
// (R-REPO-057).
//
// The errors of Upstream's methods and of Fill tell the engine what to do with the request:
//
//   - an error for which discover.AsRateLimited reports true: a rate-limit answer (R-REPO-054), the engine waits, halves its
//     window and repeats the request;
//   - an error wrapping ErrPreempted: a client took the slot (R-REPO-055), the item is fetched again later and is neither a
//     failure nor a rate limit;
//   - a *WaitError: the request was not sent (the circuit is open, the spool refused admission); the engine waits After
//     and repeats it without counting a failure;
//   - any other error: a failure of the item (R-REPO-053).
type Host interface {
	// Lister returns the lister of a format's remote adapter, false when the format does not discover (the catalog fact
	// Mirror, R-REPO-050).
	Lister(format string) (discover.Lister, bool)
	// Admits returns the name check of discover.Scope for one pass: the repository's include and exclude patterns
	// (R-REPO-005), the claims that refuse a name at the remote (R-REPO-023, R-REPO-025 step 2) and the format's name
	// rule, applied to a name the format normalized.
	Admits(ctx context.Context, repo repository.Repository) (func(name string) bool, error)
	// Upstream lends a format the repository's upstream for one pass (discover.Upstream): Get and Document as background
	// requests of the pass.
	Upstream(repo repository.Repository, pass Pass) (discover.Upstream, error)
	// Fill stores one artifact of a plan through the format's artifact gate (BeforeArtifact, the integrity of R-REPO-041)
	// and the remote engine (R-REPO-025 steps 4 to 10) as a background request of the pass. An artifact that is stored
	// already costs no upstream request and answers Present. The fill writes the populate event of a cache fill with the
	// system actor and reason mirror in its transaction (R-REPO-057, Q-REPO-145).
	Fill(ctx context.Context, repo repository.Repository, pass Pass, item discover.Item) (FillResult, error)
}

// Pass names the pass a background request belongs to.
type Pass struct {
	// JobID is the id of the job that runs the pass: the upstream log record names it (R-REPO-057).
	JobID string
	// Ceiling is the background share B of R-REPO-055 for the repository (proxy.BackgroundShare): the most background
	// fetches of the repository the replica runs at a time.
	Ceiling int
}

// FillResult is the outcome of a Fill that did not fail.
type FillResult struct {
	// Present is true when the artifact was stored already and no upstream request was sent.
	Present bool
	// Bytes is the size of the artifact the fill stored; 0 when Present.
	Bytes int64
}

// ErrPreempted is wrapped by the error of a background request that a client fetch pre-empted (R-REPO-055): its upstream
// request was cancelled and nothing was stored.
var ErrPreempted = errors.New("mirror: the background fetch was pre-empted by a client fetch")

// WaitError says that a background request was not sent and can be repeated after a wait that is neither a failure nor a
// rate limit: the circuit of the repository is open (R-REPO-054, Q-REPO-139: the pass reads it and never probes), or the
// spool refused admission (R-REPO-052: retried after 5 s).
type WaitError struct {
	// After is how long the engine waits before it repeats the request.
	After time.Duration
	// Reason is circuit_open or spool_busy.
	Reason string
}

func (e *WaitError) Error() string { return "mirror: background request deferred: " + e.Reason }

// The reasons of a WaitError.
const (
	WaitCircuitOpen = "circuit_open"
	WaitSpoolBusy   = "spool_busy"
)
