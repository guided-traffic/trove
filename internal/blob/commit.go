package blob

import (
	"context"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// LinkFunc adds the reference to the blob inside the commit transaction: it
// inserts or updates the item and runs everything that must commit with it. The
// guard trigger of the reference column re-checks that the blob is live under
// the same lock. It runs once per transaction attempt and must be safe to run
// again.
type LinkFunc func(ctx context.Context, tx meta.Tx) error

// CommitResult says what a Commit did.
type CommitResult struct {
	// Wrote reports whether this call wrote the object.
	Wrote bool
	// Inserted reports whether this call inserted the blob row.
	Inserted bool
	// Deduplicated reports that the row already existed and live: no object was
	// written and no row was inserted.
	Deduplicated bool
	// Rounds is the number of rounds of the protocol loop.
	Rounds int
}

type action uint8

const (
	actDone action = iota
	actPut
	actRetry
	actWaitDeleting
	actRepair
)

// errRollback ends a transaction attempt without committing; the action says
// what to do next.
var errRollback = errors.New("rollback")

// Commit runs the commit protocol of R-STOR-005: it makes sure a live blob row
// exists whose object is in the bucket, and runs link in the transaction that
// holds the row's FOR SHARE lock. The guarantee: a committed reference points
// at a live row whose object exists. No lock is held across a transfer to the
// object store.
//
// link may be nil to commit the blob alone.
func (s *Store) Commit(ctx context.Context, sp *Spooled, link LinkFunc) (CommitResult, error) {
	var (
		res     CommitResult
		putDone bool
		putAt   time.Time
	)
	attempts := s.cfg.Upload.CommitAttempts
	if attempts < 1 {
		attempts = 1
	}
	deadline := s.cfg.Upload.CommitDeadline

	fail := func(err error) (CommitResult, error) {
		if putDone || res.Wrote {
			s.enqueueAfterFailure(ctx, sp.SHA256)
		}
		return res, err
	}

	for round := 1; round <= attempts; round++ {
		res.Rounds = round
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		canInsert := putDone && s.now().Sub(putAt) <= deadline
		act, inserted, err := s.commitRound(ctx, sp, link, canInsert)
		if err != nil {
			return fail(err)
		}
		switch act {
		case actDone:
			res.Inserted = inserted
			res.Deduplicated = !inserted && !res.Wrote
			return res, nil
		case actPut:
			if err := s.putObject(ctx, sp); err != nil {
				return fail(err)
			}
			putDone, putAt, res.Wrote = true, s.now(), true
		case actRepair:
			if err := s.putObject(ctx, sp); err != nil {
				return fail(err)
			}
			res.Wrote = true
			if err := s.db.Tx(ctx, func(tx meta.Tx) error {
				_, err := tx.Exec(ctx, sqlMarkOK, sp.SHA256.Bytes())
				return err
			}); err != nil {
				return fail(err)
			}
		case actWaitDeleting:
			// A GC delete may still land on the key: the object we wrote (if any)
			// is not trustworthy, so write it again after the row is gone.
			putDone = false
			if err := s.waitGone(ctx, sp.SHA256); err != nil {
				return fail(err)
			}
		case actRetry:
		}
	}
	return fail(errs.E(errs.Unavailable, errs.CodeUnavailable, "the upload could not be committed; retry",
		errs.WithRetryAfter(retryAfter), errs.WithCause(ErrCommitAttempts)))
}

// commitRound is one pass of the protocol's loop body in one transaction.
func (s *Store) commitRound(ctx context.Context, sp *Spooled, link LinkFunc, canInsert bool) (act action, inserted bool, err error) {
	err = s.db.Tx(ctx, func(tx meta.Tx) error {
		act, inserted = actDone, false // the function may run again
		var state, status string
		found, err := queryOne(ctx, tx, sqlLockShare, []any{sp.SHA256.Bytes()}, &state, &status)
		if err != nil {
			return err
		}
		switch {
		case !found:
			if !canInsert {
				act = actPut
				return errRollback
			}
			n, err := tx.Exec(ctx, sqlInsertBlob, sp.SHA256.Bytes(), sp.SHA1[:], sp.MD5[:], sp.SHA512[:], sp.Size)
			if err != nil {
				return err
			}
			if n == 0 { // a concurrent uploader won; read again
				act = actRetry
				return errRollback
			}
			inserted = true
		case state == "deleting":
			act = actWaitDeleting
			return errRollback
		case status != "ok":
			act = actRepair
			return errRollback
		}
		if link != nil {
			if err := link(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errRollback):
		return act, false, nil
	case err != nil && sqlState(err) == sqlStateAS001:
		// The guard saw a row that is not live (a collector claimed it between
		// our lock and the reference): start the protocol again.
		return actRetry, false, nil
	case err != nil:
		return actDone, false, err
	}
	return act, inserted, nil
}

// waitGone polls until the blob row has disappeared, for at most
// storage.upload.blob_wait_timeout.
func (s *Store) waitGone(ctx context.Context, d Digest) error {
	deadline := s.now().Add(s.cfg.Upload.BlobWaitTimeout)
	for {
		var exists bool
		if err := s.db.Tx(ctx, func(tx meta.Tx) error {
			var one int
			var err error
			exists, err = queryOne(ctx, tx, sqlBlobExists, []any{d.Bytes()}, &one)
			return err
		}); err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if !s.now().Before(deadline) {
			return errs.E(errs.Unavailable, errs.CodeUnavailable, "the content is being removed by garbage collection; retry",
				errs.WithRetryAfter(retryAfter), errs.WithCause(ErrBlobBusy))
		}
		t := time.NewTimer(s.wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// enqueueAfterFailure puts the digest in blob_gc_queue after a failure that may
// have left an object or a reference-less row, so that the incremental
// collection finds it (R-STOR-005). It is best effort and uses a context of its
// own because the request context may be what failed.
func (s *Store) enqueueAfterFailure(ctx context.Context, d Digest) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.db.Tx(ctx, func(tx meta.Tx) error {
		_, err := tx.Exec(ctx, sqlEnqueue, d.Bytes())
		return err
	}); err != nil {
		s.log.Warn("could not queue a digest for collection after a failed commit", "sha256", d.Hex(), "error", err)
	}
}

// Lookup returns the blob row of a digest, ErrBlobNotFound when there is none.
func (s *Store) Lookup(ctx context.Context, d Digest) (Row, error) {
	var r Row
	var found bool
	err := s.db.Tx(ctx, func(tx meta.Tx) error {
		var sha256, sha1, md5, sha512 []byte
		var err error
		found, err = queryOne(ctx, tx, sqlRow, []any{d.Bytes()}, &sha256, &sha1, &md5, &sha512, &r.Size, &r.State, &r.ContentStatus, &r.CreatedAt)
		if err != nil || !found {
			return err
		}
		copy(r.SHA256[:], sha256)
		copy(r.SHA1[:], sha1)
		copy(r.MD5[:], md5)
		copy(r.SHA512[:], sha512)
		return nil
	})
	if err != nil {
		return Row{}, err
	}
	if !found {
		return Row{}, ErrBlobNotFound
	}
	return r, nil
}
