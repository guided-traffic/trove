package mirror

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// Database is the PostgreSQL store: transactions and plain reads. *postgres.DB implements it.
type Database interface {
	meta.Runner
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// PGStore is the Store over PostgreSQL (internal/meta/postgres/queries/mirror.sql). Every statement of a pass carries its
// fence; a statement that matches no row is ErrFenceLost.
type PGStore struct{ db Database }

var _ Store = (*PGStore)(nil)

// NewPGStore creates the store.
func NewPGStore(db Database) *PGStore { return &PGStore{db: db} }

func fromUs(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMicro(v).UTC()
}

// write runs one statement of a pass in a transaction of its own, repeated after a serialization failure or a failover.
func (s *PGStore) write(ctx context.Context, fn func(qs *q.Queries) error) error {
	return s.db.InTx(ctx, func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errNotPostgres
		}
		return fn(q.New(ptx))
	})
}

func (s *PGStore) read(ctx context.Context, fn func(qs *q.Queries) error) error {
	return s.db.Read(ctx, func(db q.DBTX) error { return fn(q.New(db)) })
}

type pgError string

func (e pgError) Error() string { return string(e) }

const errNotPostgres = pgError("mirror: the transaction does not come from the PostgreSQL store")

// fenced turns the row count of a fenced statement into an error.
func fenced(n int64) error {
	if n == 0 {
		return ErrFenceLost
	}
	return nil
}

// EnsureStates creates the rows that are missing.
func (s *PGStore) EnsureStates(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	return s.write(ctx, func(qs *q.Queries) error { return qs.MirrorEnsureStates(ctx, ids) })
}

// Due returns the ids that are due.
func (s *PGStore) Due(ctx context.Context, ids []int64, now time.Time) (out []int64, err error) {
	if len(ids) == 0 {
		return nil, nil
	}
	err = s.read(ctx, func(qs *q.Queries) (e error) {
		out, e = qs.MirrorDue(ctx, q.MirrorDueParams{RepoIds: ids, NowUs: now.UnixMicro()})
		return e
	})
	return out, err
}

// ClearInactive clears the passes of the repositories that left the mode.
func (s *PGStore) ClearInactive(ctx context.Context, left []int64) error {
	if len(left) == 0 {
		return nil
	}
	return s.write(ctx, func(qs *q.Queries) error {
		if _, err := qs.MirrorClearInactivePasses(ctx, left); err != nil {
			return err
		}
		_, err := qs.MirrorDeleteInactivePending(ctx, left)
		return err
	})
}

// State reads the row.
func (s *PGStore) State(ctx context.Context, repoID int64) (st State, ok bool, err error) {
	var r q.MirrorStateGetRow
	err = s.read(ctx, func(qs *q.Queries) (e error) {
		r, e = qs.MirrorStateGet(ctx, repoID)
		return e
	})
	if err != nil {
		if postgres.IsNoRows(err) {
			return State{}, false, nil
		}
		return State{}, false, err
	}
	return State{
		RepoID: r.RepoID, NextPassAt: fromUs(r.NextPassUs), PassRequestedAt: fromUs(r.PassRequestedUs), PassRequestedFull: r.PassRequestedFull,
		PassStartedAt: fromUs(r.PassStartedUs), PassKind: discover.PassKind(r.PassKind), PassMethod: discover.Method(r.PassMethod),
		PassJobID: r.PassJobID, PassHolder: r.PassHolder, PassCursor: raw(r.PassCursor), PassDiscoveryDone: r.PassDiscoveryDone,
		PassNames: r.PassNames, PassPackagesDone: r.PassPackagesDone, PassPackagesFailed: r.PassPackagesFailed,
		PassItemsStored: r.PassItemsStored, PassBytesStored: r.PassBytesStored,
		PassDiscoveryComplete: r.PassDiscoveryComplete, PassDiscoveryNote: r.PassDiscoveryNote,
		FeedCursor: raw(r.FeedCursor), FeedMethod: discover.Method(r.FeedMethod), PatternsFingerprint: r.PatternsFingerprint,
		LastFullPassAt: fromUs(r.LastFullPassUs), ConsecutiveFailures: int(r.ConsecutiveFailures),
		RateLimitedUntil: fromUs(r.RateLimitedUntilUs), BoundReached: r.BoundReached, BoundReachedAt: fromUs(r.BoundReachedUs),
	}, true, nil
}

func raw(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// Claim runs the one statement that makes the job the owner of the pass.
func (s *PGStore) Claim(ctx context.Context, c Claim) (claimed bool, err error) {
	var n int64
	err = s.write(ctx, func(qs *q.Queries) (e error) {
		n, e = qs.MirrorClaim(ctx, q.MirrorClaimParams{
			RepoID: c.RepoID, JobID: c.JobID, Holder: c.Holder, WantFull: c.Full, KindIfNew: string(c.KindIfNew), NowUs: c.Now.UnixMicro(),
		})
		return e
	})
	return n > 0, err
}

// SetMethod stores the method and kind of the pass.
func (s *PGStore) SetMethod(ctx context.Context, f Fence, m discover.Method, k discover.PassKind) error {
	return s.write(ctx, func(qs *q.Queries) error {
		n, err := qs.MirrorSetMethod(ctx, q.MirrorSetMethodParams{Method: string(m), Kind: string(k), RepoID: f.RepoID, JobID: f.JobID, Holder: f.Holder})
		if err != nil {
			return err
		}
		return fenced(n)
	})
}

// maxAttempts is the bound of MirrorPendingCount and the pending queries.
const maxAttempts = int32(MaxPackageAttempts)

// PendingCount counts the names a pass still takes.
func (s *PGStore) PendingCount(ctx context.Context, repoID int64) (n int64, err error) {
	err = s.read(ctx, func(qs *q.Queries) (e error) {
		n, e = qs.MirrorPendingCount(ctx, q.MirrorPendingCountParams{RepoID: repoID, MaxAttempts: maxAttempts})
		return e
	})
	return n, err
}

// AddNames inserts the names and stores the progress in one statement.
func (s *PGStore) AddNames(ctx context.Context, f Fence, n Names) (added int64, err error) {
	names := n.Names
	if names == nil {
		names = []string{}
	}
	err = s.write(ctx, func(qs *q.Queries) error {
		r, e := qs.MirrorAddNames(ctx, q.MirrorAddNamesParams{
			RepoID: f.RepoID, JobID: f.JobID, Holder: f.Holder, Names: names, SetProgress: n.SetProgress,
			CursorJson: string(n.Cursor), Done: n.Done, Truncated: n.Truncated, Note: n.Note,
		})
		if e != nil {
			return e
		}
		added = r.Added
		return fenced(r.Fenced)
	})
	return added, err
}

// HeldNames returns names of the packages the repository holds.
func (s *PGStore) HeldNames(ctx context.Context, repoID int64, after string, limit int) (out []string, err error) {
	err = s.read(ctx, func(qs *q.Queries) (e error) {
		out, e = qs.MirrorHeldNames(ctx, q.MirrorHeldNamesParams{RepoID: repoID, After: after, RowLimit: int32(limit)}) //nolint:gosec // a page size
		return e
	})
	return out, err
}

// NextPending returns the pending names a pass takes at now after one name.
func (s *PGStore) NextPending(ctx context.Context, repoID int64, now time.Time, after string, limit int) (out []PendingName, err error) {
	err = s.read(ctx, func(qs *q.Queries) error {
		rows, e := qs.MirrorPendingNext(ctx, q.MirrorPendingNextParams{
			RepoID: repoID, MaxAttempts: maxAttempts, NowUs: now.UnixMicro(), After: after, RowLimit: int32(limit), //nolint:gosec // a page size
		})
		for _, r := range rows {
			out = append(out, PendingName{Name: r.Name, Attempts: int(r.Attempts)})
		}
		return e
	})
	return out, err
}

// NextWake returns when the earliest waiting name is due.
func (s *PGStore) NextWake(ctx context.Context, repoID int64, now time.Time) (t time.Time, err error) {
	var v int64
	err = s.read(ctx, func(qs *q.Queries) (e error) {
		v, e = qs.MirrorPendingNextWake(ctx, q.MirrorPendingNextWakeParams{RepoID: repoID, MaxAttempts: maxAttempts, NowUs: now.UnixMicro()})
		return e
	})
	return fromUs(v), err
}

// PackageDone deletes the pending row and raises the counters.
func (s *PGStore) PackageDone(ctx context.Context, f Fence, r PackageResult) error {
	return s.write(ctx, func(qs *q.Queries) error {
		n, err := qs.MirrorPackageDone(ctx, q.MirrorPackageDoneParams{RepoID: f.RepoID, JobID: f.JobID, Holder: f.Holder, Name: r.Name, Items: r.Items, Bytes: r.Bytes})
		if err != nil {
			return err
		}
		return fenced(n)
	})
}

// PackageFailed counts one more attempt.
func (s *PGStore) PackageFailed(ctx context.Context, f Fence, r PackageResult) (attempts int, err error) {
	err = s.write(ctx, func(qs *q.Queries) error {
		row, e := qs.MirrorPackageFailed(ctx, q.MirrorPackageFailedParams{
			RepoID: f.RepoID, JobID: f.JobID, Holder: f.Holder, NowUs: r.Now.UnixMicro(), RetryStepSecs: int32(PackageRetryStep / time.Second),
			LastError: r.Error, Name: r.Name, MaxAttempts: maxAttempts, Items: r.Items, Bytes: r.Bytes,
		})
		if e != nil {
			return e
		}
		attempts = int(row.Attempts)
		return fenced(row.Fenced)
	})
	return attempts, err
}

// PackageKept raises the counters.
func (s *PGStore) PackageKept(ctx context.Context, f Fence, r PackageResult) error {
	return s.write(ctx, func(qs *q.Queries) error {
		n, err := qs.MirrorPackageKept(ctx, q.MirrorPackageKeptParams{Items: r.Items, Bytes: r.Bytes, RepoID: f.RepoID, JobID: f.JobID, Holder: f.Holder})
		if err != nil {
			return err
		}
		return fenced(n)
	})
}

// RecordRateLimit counts one wait.
func (s *PGStore) RecordRateLimit(ctx context.Context, f Fence, now time.Time, wait time.Duration, status int) error {
	return s.write(ctx, func(qs *q.Queries) error {
		n, err := qs.MirrorRecordRateLimit(ctx, q.MirrorRecordRateLimitParams{
			WaitSeconds: int64(wait.Seconds()), NowUs: now.UnixMicro(), Status: int32(status), //nolint:gosec // an HTTP status
			RepoID: f.RepoID, JobID: f.JobID, Holder: f.Holder,
		})
		if err != nil {
			return err
		}
		return fenced(n)
	})
}

// Defer stores the end of a long wait.
func (s *PGStore) Defer(ctx context.Context, f Fence, until time.Time) error {
	return s.write(ctx, func(qs *q.Queries) error {
		n, err := qs.MirrorDefer(ctx, q.MirrorDeferParams{UntilUs: until.UnixMicro(), RepoID: f.RepoID, JobID: f.JobID, Holder: f.Holder})
		if err != nil {
			return err
		}
		return fenced(n)
	})
}

// EndPass writes the end of a pass.
func (s *PGStore) EndPass(ctx context.Context, f Fence, e End) (boundNew bool, err error) {
	err = s.write(ctx, func(qs *q.Queries) error {
		r, err := qs.MirrorEndPass(ctx, q.MirrorEndPassParams{
			RepoID: f.RepoID, JobID: f.JobID, Holder: f.Holder, ClearPending: e.ClearPending, LastPass: string(e.LastPass), Advance: e.Advance,
			NowUs: e.Now.UnixMicro(), Fingerprint: e.Fingerprint, KeepPass: e.KeepPass, ResetFailures: e.ResetFailures,
			NextPassUs: e.NextPassAt.UnixMicro(), Bound: e.Bound, ClearBound: e.ClearBound,
		})
		if err != nil {
			return err
		}
		boundNew = r.BoundNew
		return fenced(r.Fenced)
	})
	return boundNew && e.Bound != "", err
}

// RecordFailure records a failure of the pass and sets the back-off.
func (s *PGStore) RecordFailure(ctx context.Context, f Fence, now time.Time, code, message string, jitter float64) (out Failure, err error) {
	err = s.write(ctx, func(qs *q.Queries) error {
		n, err := qs.MirrorFailureLock(ctx, q.MirrorFailureLockParams{RepoID: f.RepoID, JobID: f.JobID, Holder: f.Holder})
		if err != nil {
			if postgres.IsNoRows(err) {
				return ErrFenceLost
			}
			return err
		}
		failures := int(n) + 1
		next := now.Add(time.Duration(float64(FailureBackoff(failures)) * jitter))
		rows, err := qs.MirrorFailureWrite(ctx, q.MirrorFailureWriteParams{
			Failures: int32(failures), NowUs: now.UnixMicro(), Code: code, Message: message, NextPassUs: next.UnixMicro(), //nolint:gosec // small
			RepoID: f.RepoID, JobID: f.JobID, Holder: f.Holder,
		})
		if err != nil {
			return err
		}
		out = Failure{Failures: failures, NextPassAt: next}
		return fenced(rows)
	})
	return out, err
}

// Usage reads the folded counter.
func (s *PGStore) Usage(ctx context.Context, repoID int64) (u Usage, err error) {
	err = s.read(ctx, func(qs *q.Queries) error {
		r, e := qs.MirrorUsage(ctx, repoID)
		u = Usage{LogicalBytes: r.LogicalBytes, FoldedAt: fromUs(r.FoldedUs), ReadAt: fromUs(r.ReadUs)}
		return e
	})
	return u, err
}

// Gauges reads the values of the gauges that come from PostgreSQL.
func (s *PGStore) Gauges(ctx context.Context) (out []GaugeRow, err error) {
	err = s.read(ctx, func(qs *q.Queries) error {
		rows, e := qs.MirrorGauges(ctx)
		for _, r := range rows {
			g := GaugeRow{Key: r.Key, Pending: r.Pending}
			if r.LastCompleteS > 0 {
				g.LastComplete = time.UnixMicro(int64(r.LastCompleteS * 1e6)).UTC()
			}
			out = append(out, g)
		}
		return e
	})
	return out, err
}

// ClearAfterRestore is what a restore does to the state (R-REPO-058, Q-REPO-147): the pass in progress, the request, the
// rate-limit wait and the bound are cleared and the next pass is due at now; withoutRemoteCaches also clears the cursor, the
// time of the last full pass and the fingerprint, so that the first pass is full. The restore job of phase 7 calls it in its
// transaction's place once the remote caches are restored or left out.
func (s *PGStore) ClearAfterRestore(ctx context.Context, now time.Time, withoutRemoteCaches bool) (rows int64, err error) {
	err = s.write(ctx, func(qs *q.Queries) (e error) {
		if _, e = qs.MirrorDeleteAllPending(ctx); e != nil {
			return e
		}
		rows, e = qs.MirrorRestoreClear(ctx, q.MirrorRestoreClearParams{NowUs: now.UnixMicro(), WithoutRemoteCaches: withoutRemoteCaches})
		return e
	})
	return rows, err
}
