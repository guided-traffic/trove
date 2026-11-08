package uploads

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// State is the state of a session (R-STOR-011): open takes bytes, completing has its digest and assembles the staging object,
// assembled has it and commits, done is committed, aborted is over.
type State string

// The states of a session.
const (
	StateOpen       State = "open"
	StateCompleting State = "completing"
	StateAssembled  State = "assembled"
	StateDone       State = "done"
	StateAborted    State = "aborted"
)

// row is a session as the database holds it.
type row struct {
	ID          uuid.UUID
	RepoID      int64
	AddressedID int64
	Binding     string
	Principal   string
	Received    int64
	HashState   []byte
	PartSize    int64
	UploadID    string
	TailSize    int64
	// Tails are the segments of the tail in order; their sizes add up to TailSize.
	Tails  []tailSeg
	Writer string
	// ItemID is the item a done session committed.
	ItemID int64
	// Claimed says that another request holds an unexpired claim.
	Claimed bool
	Digest  []byte
	State   State
	// TooOld says that the session is older than the maximum age.
	TooOld bool
	Parts  int
}

// tailSeg is one recorded segment of the tail: the object tail-<Writer>, written by the request of that claim token (Q-STOR-174).
type tailSeg struct {
	Seq    int
	Writer string
	Size   int64
	SHA256 []byte
}

// part is a recorded part of the multipart upload.
type part struct {
	Number int
	Size   int64
	ETag   string
	SHA256 []byte
}

// end is what a request records when its body has ended.
type end struct {
	Received  int64
	HashState []byte
	TailSize  int64
	// NewTail is a segment to add to the tail, or, with ReplaceTails, the one segment that replaces all of them.
	NewTail      *tailSeg
	ReplaceTails bool
	// Completing records the digest and moves the session to completing (a finalize whose digest matched).
	Completing bool
	Digest     []byte
}

// errLostClaim says that a conditional write found another writer's token: the request lost its claim.
var errLostClaim = errors.New("the claim on the upload session is lost")

// rows is the service's access to the two tables. pgRows runs the sqlc queries; the unit tests of the state machine substitute a
// simulation, and the integration tier runs the same code against PostgreSQL. Every method is one transaction, and the lease of a
// claim and the age of a session are judged by the database's clock.
type rows interface {
	insert(ctx context.Context, r row, maxOpen int) (started bool, err error)
	// get reads a session; maxAge is the age past which TooOld is set.
	get(ctx context.Context, id uuid.UUID, maxAge time.Duration) (row, bool, error)
	// claim takes the claim of R-STOR-011 step 2 and returns the row as it stands; false when the session does not take the
	// request (not open, past its age, another claim, another offset).
	claim(ctx context.Context, id uuid.UUID, writer string, offset int64, lease, maxAge time.Duration) (row, bool, error)
	// recordPart records a completed part with the new count and hash state, conditional on the writer token.
	recordPart(ctx context.Context, id uuid.UUID, writer string, received int64, hashState []byte, lease time.Duration, p part, uploadID string) error
	// renew extends the claim right before a part is sent; errLostClaim if it is not the request's any more.
	renew(ctx context.Context, id uuid.UUID, writer string, lease, maxAge time.Duration) error
	// claimCompleting takes the short claim of the step that sends the tail as the last part of a completing session.
	claimCompleting(ctx context.Context, id uuid.UUID, writer string, lease time.Duration) (bool, error)
	// renewCompleting extends that short claim; errLostClaim if it is not the request's any more.
	renewCompleting(ctx context.Context, id uuid.UUID, writer string, lease time.Duration) error
	// recordEnd records the end of a body and releases the claim, conditional on the writer token.
	recordEnd(ctx context.Context, id uuid.UUID, writer string, e end) error
	release(ctx context.Context, id uuid.UUID, writer string) error
	parts(ctx context.Context, id uuid.UUID) ([]part, error)
	// completeTail records the tail as the last part, clears it and ends the short claim of that step, conditional on the claim.
	completeTail(ctx context.Context, id uuid.UUID, writer string, p part) error
	// setAssembled moves completing to assembled; any other state is left as it is.
	setAssembled(ctx context.Context, id uuid.UUID) error
	// markDone moves assembled to done inside tx, the transaction of the item commit; a session that was aborted meanwhile is
	// errAborted and the transaction rolls back.
	markDone(ctx context.Context, tx meta.Tx, id uuid.UUID, itemID int64) error
	// markCleaned records that the objects of a finished session were removed by the request, so that the collection run deletes the row
	// without a call to the store; deleteCleaned deletes the row of a session whose objects the request removed.
	markCleaned(ctx context.Context, id uuid.UUID) error
	deleteCleaned(ctx context.Context, id uuid.UUID) error
	// abort ends a live session; it reports whether it was live.
	abort(ctx context.Context, id uuid.UUID) (live bool, uploadID string, err error)
}

var errAborted = errors.New("the upload session was aborted")

type pgRows struct{ db blob.DB }

var _ rows = pgRows{}

func queries(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("uploads: the transaction is not one of internal/meta/postgres")
	}
	return q.New(ptx), nil
}

func (p pgRows) do(ctx context.Context, fn func(qs *q.Queries) error) error {
	return p.db.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		return fn(qs)
	})
}

func (p pgRows) insert(ctx context.Context, r row, maxOpen int) (started bool, err error) {
	err = p.do(ctx, func(qs *q.Queries) error {
		// the lock of the repository row makes the starts of a repository one after the other, so that the count below sees the session the
		// start before it inserted (READ COMMITTED takes a new snapshot for the statement that follows a wait)
		// the order of the locks: the addressed repository (a virtual one whose configuration may be updated at the same time) first
		if r.AddressedID != r.RepoID {
			if err := qs.UploadLockAddressed(ctx, r.AddressedID); err != nil {
				return err
			}
		}
		if err := qs.UploadLockRepository(ctx, r.RepoID); err != nil {
			return err
		}
		n, err := qs.UploadInsertSession(ctx, q.UploadInsertSessionParams{
			ID: r.ID.String(), RepoID: r.RepoID, AddressedRepoID: r.AddressedID, Binding: r.Binding, PrincipalID: r.Principal,
			HashState: r.HashState, PartSize: r.PartSize, S3UploadID: r.UploadID, MaxOpen: int64(maxOpen),
		})
		started = n > 0
		return err
	})
	return started, err
}

func fromRow(g q.UploadGetSessionRow) (row, error) {
	id, err := uuid.Parse(g.ID)
	if err != nil {
		return row{}, fmt.Errorf("upload session id: %w", err)
	}
	return row{
		ID: id, RepoID: g.RepoID, AddressedID: g.AddressedRepoID, Binding: g.Binding, Principal: g.PrincipalID, Received: g.Received,
		HashState: g.HashState, PartSize: g.PartSize, UploadID: g.S3UploadID, TailSize: g.TailSize, Writer: g.Writer, ItemID: g.ItemID,
		Claimed: g.Claimed, Digest: g.Digest, State: State(g.State), TooOld: g.TooOld, Parts: int(g.Parts),
	}, nil
}

func (p pgRows) get(ctx context.Context, id uuid.UUID, maxAge time.Duration) (r row, found bool, err error) {
	err = p.do(ctx, func(qs *q.Queries) error {
		r, found, err = getRow(ctx, qs, id, maxAge)
		return err
	})
	return r, found, err
}

// getRow reads the session and its tail segments in the transaction of the caller.
func getRow(ctx context.Context, qs *q.Queries, id uuid.UUID, maxAge time.Duration) (row, bool, error) {
	g, err := qs.UploadGetSession(ctx, q.UploadGetSessionParams{ID: id.String(), MaxAgeSeconds: maxAge.Seconds()})
	if err != nil {
		if postgres.IsNoRows(err) {
			return row{}, false, nil
		}
		return row{}, false, err
	}
	r, err := fromRow(g)
	if err != nil {
		return row{}, false, err
	}
	ts, err := qs.UploadListTails(ctx, id.String())
	if err != nil {
		return row{}, false, err
	}
	for _, t := range ts {
		r.Tails = append(r.Tails, tailSeg{Seq: int(t.Seq), Writer: t.Writer, Size: t.Size, SHA256: t.Sha256})
	}
	return r, true, nil
}

func (p pgRows) claim(ctx context.Context, id uuid.UUID, writer string, offset int64, lease, maxAge time.Duration) (r row, ok bool, err error) {
	err = p.do(ctx, func(qs *q.Queries) error {
		n, err := qs.UploadClaim(ctx, q.UploadClaimParams{
			ID: id.String(), Writer: writer, ClaimOffset: offset, LeaseSeconds: lease.Seconds(), MaxAgeSeconds: maxAge.Seconds(),
		})
		if err != nil || n == 0 {
			ok = false
			return err
		}
		r, ok, err = getRow(ctx, qs, id, maxAge)
		return err
	})
	return r, ok, err
}

func (p pgRows) recordPart(ctx context.Context, id uuid.UUID, writer string, received int64, hashState []byte, lease time.Duration, pt part, uploadID string) error {
	return p.do(ctx, func(qs *q.Queries) error {
		n, err := qs.UploadRecordPart(ctx, q.UploadRecordPartParams{
			ID: id.String(), Writer: writer, Received: received, HashState: hashState, LeaseSeconds: lease.Seconds(), UploadID: uploadID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return errLostClaim
		}
		if err = insertPart(ctx, qs, id, pt); err != nil {
			return err
		}
		return qs.UploadDeleteTails(ctx, id.String()) // the segments are inside the part
	})
}

// insertPart records a part. The number is the next one of a claim that nobody else holds, so a part that is recorded already is a defect
// (the statement does nothing then, and the session would complete with another ETag than the one just sent), not a repetition.
func insertPart(ctx context.Context, qs *q.Queries, id uuid.UUID, pt part) error {
	n, err := qs.UploadInsertPart(ctx, q.UploadInsertPartParams{
		SessionID: id.String(), PartNumber: int32(pt.Number), Size: pt.Size, Etag: pt.ETag, Sha256: pt.SHA256, //nolint:gosec // at most 10,000
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("upload session %s: part %d is recorded already", id, pt.Number)
	}
	return nil
}

func (p pgRows) renew(ctx context.Context, id uuid.UUID, writer string, lease, maxAge time.Duration) error {
	return p.do(ctx, func(qs *q.Queries) error {
		n, err := qs.UploadRenew(ctx, q.UploadRenewParams{ID: id.String(), Writer: writer, LeaseSeconds: lease.Seconds(), MaxAgeSeconds: maxAge.Seconds()})
		if err != nil {
			return err
		}
		if n == 0 {
			return errLostClaim
		}
		return nil
	})
}

func (p pgRows) renewCompleting(ctx context.Context, id uuid.UUID, writer string, lease time.Duration) error {
	return p.do(ctx, func(qs *q.Queries) error {
		n, err := qs.UploadRenewCompleting(ctx, q.UploadRenewCompletingParams{ID: id.String(), Writer: writer, LeaseSeconds: lease.Seconds()})
		if err != nil {
			return err
		}
		if n == 0 {
			return errLostClaim
		}
		return nil
	})
}

func (p pgRows) claimCompleting(ctx context.Context, id uuid.UUID, writer string, lease time.Duration) (ok bool, err error) {
	err = p.do(ctx, func(qs *q.Queries) error {
		n, err := qs.UploadClaimCompleting(ctx, q.UploadClaimCompletingParams{ID: id.String(), Writer: writer, LeaseSeconds: lease.Seconds()})
		ok = n > 0
		return err
	})
	return ok, err
}

func (p pgRows) recordEnd(ctx context.Context, id uuid.UUID, writer string, e end) error {
	return p.do(ctx, func(qs *q.Queries) error {
		n, err := qs.UploadRecordEnd(ctx, q.UploadRecordEndParams{
			ID: id.String(), Writer: writer, Received: e.Received, HashState: e.HashState, TailSize: e.TailSize,
			Completing: e.Completing, Digest: e.Digest,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return errLostClaim
		}
		if e.ReplaceTails {
			if err := qs.UploadDeleteTails(ctx, id.String()); err != nil {
				return err
			}
		}
		if t := e.NewTail; t != nil {
			return qs.UploadInsertTail(ctx, q.UploadInsertTailParams{
				SessionID: id.String(), Seq: int32(t.Seq), Writer: t.Writer, Size: t.Size, Sha256: t.SHA256, //nolint:gosec // a few dozen at most
			})
		}
		return nil
	})
}

func (p pgRows) release(ctx context.Context, id uuid.UUID, writer string) error {
	return p.do(ctx, func(qs *q.Queries) error {
		return qs.UploadRelease(ctx, q.UploadReleaseParams{ID: id.String(), Writer: writer})
	})
}

func (p pgRows) parts(ctx context.Context, id uuid.UUID) (out []part, err error) {
	err = p.do(ctx, func(qs *q.Queries) error {
		rs, err := qs.UploadListParts(ctx, id.String())
		if err != nil {
			return err
		}
		out = make([]part, len(rs))
		for i, r := range rs {
			out[i] = part{Number: int(r.PartNumber), Size: r.Size, ETag: r.Etag, SHA256: r.Sha256}
		}
		return nil
	})
	return out, err
}

func (p pgRows) completeTail(ctx context.Context, id uuid.UUID, writer string, pt part) error {
	return p.do(ctx, func(qs *q.Queries) error {
		n, err := qs.UploadClearTail(ctx, q.UploadClearTailParams{ID: id.String(), Writer: writer})
		if err != nil {
			return err
		}
		if n == 0 {
			return errLostClaim
		}
		if err := insertPart(ctx, qs, id, pt); err != nil {
			return err
		}
		return qs.UploadDeleteTails(ctx, id.String())
	})
}

func (p pgRows) setAssembled(ctx context.Context, id uuid.UUID) error {
	return p.do(ctx, func(qs *q.Queries) error {
		_, err := qs.UploadSetAssembled(ctx, id.String())
		return err
	})
}

func (p pgRows) markDone(ctx context.Context, tx meta.Tx, id uuid.UUID, itemID int64) error {
	qs, err := queries(tx)
	if err != nil {
		return err
	}
	n, err := qs.UploadMarkDone(ctx, q.UploadMarkDoneParams{ID: id.String(), ItemID: itemID})
	if err != nil {
		return err
	}
	if n == 0 {
		return errAborted
	}
	return nil
}

func (p pgRows) markCleaned(ctx context.Context, id uuid.UUID) error {
	return p.do(ctx, func(qs *q.Queries) error { return qs.UploadMarkCleaned(ctx, id.String()) })
}

func (p pgRows) deleteCleaned(ctx context.Context, id uuid.UUID) error {
	return p.do(ctx, func(qs *q.Queries) error {
		_, err := qs.UploadDeleteCleaned(ctx, id.String())
		return err
	})
}

func (p pgRows) abort(ctx context.Context, id uuid.UUID) (live bool, uploadID string, err error) {
	err = p.do(ctx, func(qs *q.Queries) error {
		ids, err := qs.UploadAbort(ctx, id.String())
		if len(ids) > 0 {
			live, uploadID = true, ids[0]
		}
		return err
	})
	return live, uploadID, err
}
