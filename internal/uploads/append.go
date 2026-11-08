package uploads

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
)

// maxTailSegments is the most segments a tail has (Q-STOR-174): the request that would make one more merges them into one object
// instead, which reads the tail once. A client that sends very small chunks therefore pays one read of up to one part per this many
// requests, and nothing in between.
const maxTailSegments = 32

// recordTimeout bounds a record transaction that runs under a context of its own (see recordCtx).
const recordTimeout = 30 * time.Second

// errTooLarge says that a request would raise the received count above the effective maximum upload size.
var errTooLarge = errors.New("the upload exceeds the maximum size")

func tooLarge(limit int64) error {
	return errs.E(errs.TooLarge, errs.CodeOCISizeInvalid, fmt.Sprintf("The upload exceeds the maximum size of %d bytes.", limit), errs.WithCause(errTooLarge))
}

// recordCtx is the context of a record transaction: it ends neither with the request (a drain cancels the request, and a COMMIT that
// was sent is not taken back by it: the outcome of the transaction is then unknown to the request, which is why nothing is deleted on
// an error that is not errLostClaim) nor lasts longer than recordTimeout.
func recordCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
}

// Append writes body at offset of the session of the scope (R-STOR-011, "A request that carries bytes"); an offset below zero
// writes at the received count. limit is the effective maximum upload size, 0 for the system's. An offset other than the received
// count and a claim another request holds are an *errs.Error of kind RangeNotSatisfiable, and the bytes the request had read are
// not recorded beyond the parts that were. Whatever the outcome, received is the byte count the session has recorded after the
// call.
func (s *Service) Append(ctx context.Context, id uuid.UUID, sc Scope, offset int64, body io.Reader, limit int64) (received int64, err error) {
	r, err := s.lookup(ctx, id, sc)
	if err != nil {
		return 0, err
	}
	if r.State != StateOpen {
		return 0, unknown()
	}
	if offset >= 0 && offset != r.Received {
		return r.Received, outOfOrder("The chunk does not start at the received count.")
	}
	r, writer, err := s.claim(ctx, id, sc, offset)
	if err != nil {
		return r.Received, err
	}
	held := true
	defer func() {
		if held && !errors.Is(err, blob.ErrFault) {
			s.release(ctx, id, writer)
		}
	}()
	if err = s.fault("append.claimed"); err != nil {
		return r.Received, err
	}

	st := &streamState{received: r.Received}
	res, err := s.stream(ctx, r, writer, body, limit, st)
	if err != nil {
		held = !errors.Is(err, errLostClaim)
		return s.failed(ctx, r, st, err)
	}
	if !res.changed { // a request without bytes changes nothing
		return r.Received, nil
	}
	if err = s.fault("append.before_record"); err != nil {
		return st.received, err
	}
	rctx, cancel := recordCtx(ctx)
	err = s.rows.recordEnd(rctx, id, writer, res.e)
	cancel()
	if err != nil {
		lost := errors.Is(err, errLostClaim)
		held = !lost
		if lost { // certainly not recorded: the tail object this request wrote is the only thing that refers to it. Any other error may
			// be a COMMIT that landed, and the object is then the recorded one: it is left to the end of the session.
			s.removeObjects(ctx, res.newObjects)
		}
		return s.failed(ctx, r, st, err)
	}
	held = false
	s.removeObjects(ctx, res.oldObjects)
	return res.e.Received, nil
}

// failed maps the failure of a request that held a claim to its answer and the received count to report.
func (s *Service) failed(ctx context.Context, r row, st *streamState, err error) (int64, error) {
	switch {
	case errors.Is(err, blob.ErrFault):
		return st.received, err
	case errors.Is(err, errLostClaim):
		// another request took the session while this one wrote, or the session ended under it
		cur, ok := s.current(ctx, r)
		if !ok || cur.State != StateOpen || cur.TooOld { // a session past its age lost its claim to the renewal that refuses it
			return st.received, unknown()
		}
		return cur.Received, outOfOrder("Another request holds the upload.")
	case errors.Is(err, errTooLarge):
		s.abortAndClean(ctx, r, blob.SessionAborted)
		return st.received, err
	}
	return st.received, err
}

// abortAndClean ends a session that cannot go on and removes what it staged; the outcome is counted when this call ended it.
func (s *Service) abortAndClean(ctx context.Context, r row, outcome string) {
	if cur, ok := s.current(ctx, r); ok { // the multipart upload may have been created since r was read
		r = cur
	}
	live, uploadID, err := s.rows.abort(ctx, r.ID)
	if err != nil {
		s.log.Warn("could not abort an upload session", "session", r.ID, "error", err)
	}
	if live {
		s.metrics.UploadSession(outcome, 1)
		r.UploadID = uploadID // exactly the upload at the moment of the abort: no part can be recorded after it
	}
	s.endAndRemove(ctx, r)
}

// current reads the session again for the answer of a request that lost its claim.
func (s *Service) current(ctx context.Context, r row) (row, bool) {
	cur, found, err := s.rows.get(ctx, r.ID, s.cfg.SessionMaxAge)
	if err != nil {
		return r, true
	}
	return cur, found
}

// claim takes the claim of the session for a request that writes at offset (R-STOR-011 step 2). When the session does not take the
// request the answer says why: unknown for a session that is gone, over, past its age or not open, 416 for a session that is open
// but at another count or held by another request; the returned row is the session as it stands then.
func (s *Service) claim(ctx context.Context, id uuid.UUID, sc Scope, offset int64) (row, string, error) {
	writer := uuid.New().String()
	r, ok, err := s.rows.claim(ctx, id, writer, offset, s.cfg.WriterLease, s.cfg.SessionMaxAge)
	if err != nil {
		return row{}, "", err
	}
	if ok {
		return r, writer, nil
	}
	cur, err := s.lookup(ctx, id, sc)
	if err != nil {
		return row{}, "", err
	}
	if cur.State != StateOpen {
		return row{}, "", unknown()
	}
	if cur.Claimed {
		return cur, "", outOfOrder("Another request holds the upload.")
	}
	return cur, "", outOfOrder("The chunk does not start at the received count.")
}

// streamState is what a request has recorded so far, for the Range of its answer when it fails.
type streamState struct{ received int64 }

// streamed is what the body of a request became: the hash functions after it, what to record at its end, and the tail objects to
// remove once that is recorded or once it certainly was not.
type streamed struct {
	h *hashes
	e end
	// changed says that the request carried bytes.
	changed bool
	// newObjects are the tail objects this request wrote, removed when its record certainly failed (errLostClaim).
	newObjects []string
	// oldObjects are the tail objects the recorded tail supersedes, removed after the record succeeded.
	oldObjects []string
}

// stream is steps 3 and 4 of a request that carries bytes (Q-STOR-174). The body is read first, in segments: the first completes the
// part that the recorded tail began, the next ones are whole parts. A segment that completes a part is joined with the recorded tail
// segments (which are read here, once), sent as the next part and recorded; whatever the body leaves, fewer bytes than are missing for
// a part, is written as one more tail segment and nothing is read back. Each byte is therefore written once and read once, and the
// spool holds no more than the request's own bytes plus the tail of the part it completes.
func (s *Service) stream(ctx context.Context, r row, writer string, body io.Reader, limit int64, st *streamState) (streamed, error) {
	h, err := restoreHashes(r.HashState)
	if err != nil {
		return streamed{}, errs.E(errs.Internal, errs.CodeInternal, "The upload session has no valid hash state.", errs.WithCause(err))
	}
	unchanged := streamed{h: h, e: end{Received: r.Received, HashState: r.HashState, TailSize: r.TailSize}}
	if body == nil {
		body = http.NoBody
	}

	// A body larger than the limit is refused as soon as the byte that crosses it arrives. The limit never exceeds what the session's
	// parts can hold, whatever the configuration was when the session started.
	effective := s.maxSize
	if limit > 0 && (effective == 0 || limit < effective) {
		effective = limit
	}
	if ceiling := r.PartSize * maxParts; effective == 0 || effective > ceiling {
		effective = ceiling
	}
	tee := func(p []byte) error {
		if h.n+int64(len(p)) > effective {
			return tooLarge(effective)
		}
		h.write(p)
		return nil
	}

	tails, tailSize, uploadID, number := r.Tails, r.TailSize, r.UploadID, r.Parts+1
	changed := false
	for {
		room := r.PartSize - tailSize
		seg, err := s.spool.IngestSegment(ctx, nil, body, room, tee)
		if err != nil {
			return streamed{}, err
		}
		if seg.Size == 0 {
			_ = seg.Close()
			if !changed {
				return unchanged, nil
			}
			return s.endOfBody(ctx, r, writer, h, seg, tails, tailSize)
		}
		changed = true
		if seg.Size < room {
			return s.endOfBody(ctx, r, writer, h, seg, tails, tailSize)
		}

		// the segment completes a part
		if number > maxParts {
			_ = seg.Close()
			return streamed{}, tooLarge(r.PartSize * maxParts)
		}
		part := seg
		if len(tails) > 0 {
			joined, jerr := s.joinTails(ctx, r, tails, seg, s.renewer(r.ID, writer))
			_ = seg.Close()
			if jerr != nil {
				if errors.Is(jerr, errTailGone) {
					jerr = s.tailLost(ctx, r, jerr)
				}
				return streamed{}, jerr
			}
			part = joined
		}
		p, created, serr := s.sendPart(ctx, r.ID, uploadID, number, part, s.renewer(r.ID, writer))
		_ = part.Close()
		if serr != nil {
			return streamed{}, serr
		}
		if err := s.fault("append.part_uploaded"); err != nil {
			return streamed{}, err
		}
		hs, merr := h.marshal()
		if merr != nil {
			return streamed{}, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(merr))
		}
		rctx, cancel := recordCtx(ctx)
		err = s.rows.recordPart(rctx, r.ID, writer, h.n, hs, s.cfg.WriterLease, p, created)
		cancel()
		if err != nil {
			if errors.Is(err, errLostClaim) { // certainly not recorded: the upload this request created belongs to nobody
				s.abortUpload(ctx, r.ID, created)
			}
			return streamed{}, err
		}
		st.received = h.n
		if created != "" {
			uploadID = created
		}
		number++
		// the recorded tail is inside the part now
		for _, t := range tails {
			s.removeObject(ctx, s.tailKey(r.ID, t.Writer))
		}
		tails, tailSize = nil, 0
	}
}

// endOfBody writes what the body left as one more tail segment (step 4), or, when the tail has as many segments as it may, merges the
// tail and the bytes into one segment. seg may be empty: the body ended at a part boundary. The record is the caller's.
func (s *Service) endOfBody(ctx context.Context, r row, writer string, h *hashes, seg *blob.Spooled, tails []tailSeg, tailSize int64) (streamed, error) {
	defer seg.Close()
	if err := s.fault("append.body_ended"); err != nil {
		return streamed{}, err
	}
	hs, err := h.marshal()
	if err != nil {
		return streamed{}, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	out := streamed{h: h, changed: true, e: end{Received: h.n, HashState: hs, TailSize: tailSize}}
	if seg.Size > 0 {
		key := s.tailKey(r.ID, writer)
		write, next := seg, 1
		if n := len(tails); n < maxTailSegments {
			if n > 0 {
				next = tails[n-1].Seq + 1
			}
		} else {
			merged, jerr := s.joinTails(ctx, r, tails, seg, s.renewer(r.ID, writer))
			if jerr != nil {
				if errors.Is(jerr, errTailGone) {
					jerr = s.tailLost(ctx, r, jerr)
				}
				return streamed{}, jerr
			}
			defer merged.Close()
			write = merged
			out.e.ReplaceTails = true
			for _, t := range tails {
				out.oldObjects = append(out.oldObjects, s.tailKey(r.ID, t.Writer))
			}
		}
		if err := s.objects.Put(ctx, key, write.Reader(), write.Size, s.checksumOf(write)); err != nil {
			return streamed{}, blob.MapStoreError(err)
		}
		out.newObjects = []string{key}
		out.e.NewTail = &tailSeg{Seq: next, Writer: writer, Size: write.Size, SHA256: bytes.Clone(write.SHA256[:])}
		if out.e.ReplaceTails {
			out.e.TailSize = write.Size
		} else {
			out.e.TailSize += seg.Size
		}
	}
	if err := s.fault("append.tail_written"); err != nil {
		return streamed{}, err
	}
	return out, nil
}

// joinTails reads the recorded tail segments, each checked against its size and SHA-256, followed by extra (which may be nil), into
// one spooled body of at most a part. It is the only place the segments are read.
func (s *Service) joinTails(ctx context.Context, r row, tails []tailSeg, extra *blob.Spooled, renew func(context.Context) error) (*blob.Spooled, error) {
	// The claim is extended before the segments are read: a request whose body arrived slowly may have lost it, and the request that took
	// the session may have removed the segments since (they are inside the part it completed). Such a request must stop with errLostClaim,
	// never conclude that the session is damaged.
	if renew != nil {
		if err := renew(ctx); err != nil {
			return nil, err
		}
	}
	src := &segReader{ctx: ctx, s: s, id: r.ID, tails: tails}
	defer src.Close()
	var body io.Reader
	if extra != nil {
		body = extra.Reader()
	}
	out, err := s.spool.IngestSegment(ctx, src, body, r.PartSize, nil)
	if err != nil {
		if errors.Is(err, errTailCorrupt) || errors.Is(err, errTailGone) {
			if renew != nil {
				if rerr := renew(ctx); rerr != nil { // the claim ran out while the segments were read: they are not this request's to judge
					return nil, rerr
				}
			}
		}
		switch {
		case errors.Is(err, errTailCorrupt):
			return nil, s.tailCorrupt(ctx, r, err)
		case errors.Is(err, errTailGone):
			return nil, errTailGone
		}
		return nil, err
	}
	want := extraSize(extra)
	for _, t := range tails {
		want += t.Size
	}
	if out.Size != want {
		_ = out.Close()
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The joined tail has another size than its parts.", errs.WithCause(fmt.Errorf("%w: %d bytes, want %d", errTailCorrupt, out.Size, want)))
	}
	return out, nil
}

func extraSize(b *blob.Spooled) int64 {
	if b == nil {
		return 0
	}
	return b.Size
}

// checksumOf is the payload checksum of a segment (R-STOR-006).
func (s *Service) checksumOf(seg *blob.Spooled) blob.Checksum {
	switch s.algorithm {
	case "sha256":
		return blob.Checksum{Algorithm: "sha256", Value: bytes.Clone(seg.SHA256[:])}
	case "md5":
		return blob.Checksum{Algorithm: "md5", Value: bytes.Clone(seg.MD5[:])}
	}
	return blob.Checksum{}
}

// partDeadline is the longest a part upload may take: below the lease the claim was renewed to right before, so that no upload of a
// part outlives the claim it was started under (a stale writer's part would otherwise land after the recorded one).
func (s *Service) partDeadline() time.Duration { return s.cfg.WriterLease - s.cfg.WriterLease/4 }

// sendPart sends a full segment as the part with the given number. The multipart upload is created here, with the first part, and its
// id returned as created for the record. With renew the claim is extended right before the upload, and a request that lost it sends
// nothing (errLostClaim); a finalize that took the short claim of its step has just done that.
func (s *Service) sendPart(ctx context.Context, id uuid.UUID, uploadID string, number int, seg *blob.Spooled, renew func(context.Context) error) (p part, created string, err error) {
	// The claim is checked before anything is created: a request that lost it creates no upload that nobody would record or abort.
	if renew != nil {
		if err := renew(ctx); err != nil {
			return part{}, "", err
		}
	}
	if uploadID == "" {
		created, err = s.objects.CreateMultipart(ctx, s.dataKey(id), s.algorithm)
		if err != nil {
			return part{}, "", blob.MapStoreError(err)
		}
		uploadID = created
		defer func() {
			if err != nil { // nothing records the upload that was just created: it is aborted here, the sweep is the second line
				s.abortUpload(ctx, id, created)
				created = ""
			}
		}()
		if renew != nil { // the lease starts again after the creation
			if err = renew(ctx); err != nil {
				return part{}, "", err
			}
		}
	}
	pctx, cancel := context.WithTimeout(ctx, s.partDeadline())
	defer cancel()
	up, err := s.objects.UploadPart(pctx, s.dataKey(id), uploadID, number, seg.Reader(), seg.Size, s.checksumOf(seg))
	if err != nil {
		return part{}, "", blob.MapStoreError(err)
	}
	return part{Number: number, Size: seg.Size, ETag: up.ETag, SHA256: bytes.Clone(seg.SHA256[:])}, created, nil
}

// abortUpload aborts a multipart upload of the session that no record names, with a context of its own.
func (s *Service) abortUpload(ctx context.Context, id uuid.UUID, uploadID string) {
	if uploadID == "" {
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.objects.AbortMultipart(actx, s.dataKey(id), uploadID); err != nil && !blob.IsNoSuchUpload(err) {
		s.log.Warn("could not abort a multipart upload that no session records; the sweep will", "session", id, "error", err)
	}
}

func (s *Service) renewer(id uuid.UUID, writer string) func(context.Context) error {
	return func(ctx context.Context) error {
		rctx, cancel := recordCtx(ctx)
		defer cancel()
		return s.rows.renew(rctx, id, writer, s.cfg.WriterLease, s.cfg.SessionMaxAge)
	}
}

func (s *Service) completingRenewer(id uuid.UUID, writer string) func(context.Context) error {
	return func(ctx context.Context) error {
		rctx, cancel := recordCtx(ctx)
		defer cancel()
		return s.rows.renewCompleting(rctx, id, writer, s.cfg.WriterLease)
	}
}

// removeObject deletes a staging object after the record that makes it unnecessary; a failure is left to the collection run.
func (s *Service) removeObject(ctx context.Context, key string) {
	if key == "" {
		return
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.objects.Delete(dctx, key); err != nil {
		s.log.Warn("could not delete a superseded staging object; the collection run will", "key", key, "error", err)
	}
}

func (s *Service) removeObjects(ctx context.Context, keys []string) {
	for _, k := range keys {
		s.removeObject(ctx, k)
	}
}

// errTailCorrupt says that a tail segment read back from the object store is not the one the session recorded; errTailGone that it is
// not in the bucket.
var (
	errTailCorrupt = errors.New("a tail segment of the upload differs from the recorded one")
	errTailGone    = errors.New("a tail segment of the upload is not in the bucket")
)

// tailReader reads a tail segment and checks it against its recorded size and SHA-256 (R-STOR-011 step 3): more bytes than recorded
// fail at once, fewer or other bytes fail at the end, so the bytes of a segment that is not the recorded one never reach a part.
type tailReader struct {
	r    io.Reader
	h    hash.Hash
	size int64
	n    int64
	want []byte
}

func (t *tailReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.h.Write(p[:n])
	t.n += int64(n)
	switch {
	case t.n > t.size:
		return n, errTailCorrupt
	case errors.Is(err, io.EOF) && (t.n != t.size || !bytes.Equal(t.h.Sum(nil), t.want)):
		return n, errTailCorrupt
	}
	return n, err
}

// segReader reads the tail segments one after the other, opening each object when the one before it has ended.
type segReader struct {
	ctx   context.Context
	s     *Service
	id    uuid.UUID
	tails []tailSeg
	cur   *tailReader
	rc    io.Closer
}

func (g *segReader) Read(p []byte) (int, error) {
	for {
		if g.cur == nil {
			if len(g.tails) == 0 {
				return 0, io.EOF
			}
			t := g.tails[0]
			g.tails = g.tails[1:]
			rc, _, err := g.s.objects.Get(g.ctx, g.s.tailKey(g.id, t.Writer), nil)
			if err != nil {
				if errors.Is(err, blob.ErrNotFound) {
					return 0, errTailGone
				}
				return 0, blob.MapStoreError(err)
			}
			g.rc = rc
			g.cur = &tailReader{r: rc, h: sha256.New(), size: t.Size, want: t.SHA256}
		}
		n, err := g.cur.Read(p)
		if errors.Is(err, io.EOF) {
			_ = g.rc.Close()
			g.cur, g.rc = nil, nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (g *segReader) Close() {
	if g.rc != nil {
		_ = g.rc.Close()
	}
}

// tailLost handles a tail segment that is gone although the claim of the request was valid when the segments were read (joinTails checks
// it first): nobody else can have removed it, so the session cannot be continued. It fails the session, and the client restarts the upload.
func (s *Service) tailLost(ctx context.Context, r row, err error) error {
	return s.failSession(ctx, r, "a recorded tail segment of the upload is missing", err)
}

func (s *Service) tailCorrupt(ctx context.Context, r row, err error) error {
	return s.failSession(ctx, r, "a recorded tail segment of the upload does not match its checksum", err)
}

// failSession aborts a session whose staged bytes cannot be trusted and reports it as an internal error; the client restarts
// the upload.
func (s *Service) failSession(ctx context.Context, r row, detail string, cause error) error {
	s.abortAndClean(ctx, r, blob.SessionAborted)
	s.log.Error("an upload session failed", "session", r.ID, "reason", detail, "error", cause)
	return errs.E(errs.Internal, errs.CodeInternal, "The upload failed; start it again.", errs.WithCause(fmt.Errorf("%s: %w", detail, cause)))
}
