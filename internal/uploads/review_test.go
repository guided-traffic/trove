package uploads

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/observability"
)

func TestR_STOR_011_ACommitThatLandedWhileTheRequestSawAnErrorKeepsTheRecordedTail(t *testing.T) {
	t.Run("a chunk", func(t *testing.T) {
		f := newFixture(t)
		s := f.replica()
		data := random(40)
		sess, _ := s.Start(context.Background(), 3, f.sc)
		if _, err := f.append(s, sess.ID, 0, data[:5]); err != nil {
			t.Fatal(err)
		}
		f.rows.afterRecordEnd = errors.New("connection reset by peer") // the change is recorded, the answer is lost
		if _, err := f.append(s, sess.ID, 5, data[5:8]); err == nil {
			t.Fatal("the error was swallowed")
		}
		f.rows.afterRecordEnd = nil
		// both segments are recorded, so both objects must exist: the next request reads them
		if got := len(f.rows.tailsOf(sess.ID)); got != 2 {
			t.Fatalf("%d tail segments recorded", got)
		}
		if _, err := f.append(f.replica(), sess.ID, 8, data[8:]); err != nil {
			t.Fatalf("the next request: %v", err)
		}
		if err := f.finalize(s, sess.ID, data, nil); err != nil {
			t.Fatal(err)
		}
		if !f.stored(data) {
			t.Fatal("the stored blob is not the file")
		}
	})
	t.Run("the closing request", func(t *testing.T) {
		f := newFixture(t)
		s := f.replica()
		data := random(10)
		sess, _ := s.Start(context.Background(), 3, f.sc)
		f.rows.afterRecordEnd = errors.New("connection reset by peer")
		if err := f.finalize(s, sess.ID, data, data); err == nil {
			t.Fatal("the error was swallowed")
		}
		f.rows.afterRecordEnd = nil
		if err := f.finalize(f.replica(), sess.ID, data, nil); err != nil {
			t.Fatalf("the retry: %v", err)
		}
		if !f.stored(data) {
			t.Fatal("the stored blob is not the file")
		}
	})
}

func TestR_STOR_011_SmallChunksAreWrittenOnceAndReadOnceByTheRequestThatCompletesAPart(t *testing.T) {
	f := newFixture(t)
	f.cfg.Uploads.PartSize = 200
	f.obj.MinPartSize = 200
	s := f.replica()
	data := random(450)
	sess, _ := s.Start(context.Background(), 3, f.sc)
	// 150 one-byte requests: nothing is read back until a part is complete
	for i := 0; i < 150; i++ {
		if _, err := f.append(s, sess.ID, int64(i), data[i:i+1]); err != nil {
			t.Fatalf("byte %d: %v", i, err)
		}
		if i < 31 && f.obj.Count("get") != 0 { // up to maxTailSegments segments, nothing is read
			t.Fatalf("request %d read %d objects", i, f.obj.Count("get"))
		}
	}
	// the cap on the segments merged them, reading each segment of the merge once
	if got := f.obj.Count("get"); got > 150 { // at most one read per request, amortized
		t.Fatalf("%d reads for 150 requests", got)
	}
	if got := len(f.rows.tailsOf(sess.ID)); got > maxTailSegments {
		t.Fatalf("%d tail segments", got)
	}
	if _, err := f.append(s, sess.ID, 150, data[150:]); err != nil {
		t.Fatal(err)
	}
	if err := f.finalize(s, sess.ID, data, nil); err != nil {
		t.Fatal(err)
	}
	if !f.stored(data) {
		t.Fatal("the stored blob is not the file")
	}
	f.nothingStaged()
}

func TestR_STOR_011_ASessionCostsNoObjectBeforeItsFirstPart(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, err := s.Start(context.Background(), 3, f.sc)
	if err != nil {
		t.Fatal(err)
	}
	if f.obj.OpenUploads() != 0 || len(f.obj.Keys("")) != 0 {
		t.Fatalf("a started session staged %d uploads and %v", f.obj.OpenUploads(), f.obj.Keys(""))
	}
	if _, err := f.append(s, sess.ID, 0, random(5)); err != nil {
		t.Fatal(err)
	}
	if f.obj.OpenUploads() != 0 {
		t.Fatal("a tail created a multipart upload")
	}
	if _, err := f.append(s, sess.ID, 5, random(20)); err != nil {
		t.Fatal(err)
	}
	if f.obj.OpenUploads() != 1 {
		t.Fatalf("%d multipart uploads after the first part", f.obj.OpenUploads())
	}
	r, _, _ := f.rows.get(context.Background(), sess.ID, time.Hour)
	if r.UploadID == "" {
		t.Fatal("the upload id of the first part was not recorded")
	}
}

// a store whose UploadPart waits for its context, to observe the deadline of a part
type waitingParts struct {
	*blobtest.Store
	calls    atomic.Int32
	deadline atomic.Int64
}

func (w *waitingParts) UploadPart(ctx context.Context, key, id string, n int, body io.ReadSeeker, size int64, sum blob.Checksum) (blob.Part, error) {
	w.calls.Add(1)
	if dl, ok := ctx.Deadline(); ok {
		w.deadline.Store(int64(time.Until(dl)))
	}
	<-ctx.Done()
	return blob.Part{}, ctx.Err()
}

func TestR_STOR_011_APartUploadCannotOutliveItsClaim(t *testing.T) {
	f := newFixture(t)
	f.cfg.Uploads.WriterLease = 400 * time.Millisecond
	var w *waitingParts
	f.wrap = func(m *blobtest.Store) ObjectStore { w = &waitingParts{Store: m}; return w }
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	start := time.Now()
	_, err := f.append(s, sess.ID, 0, random(3*testPart))
	if err == nil {
		t.Fatal("a part upload that never ends succeeded")
	}
	if took := time.Since(start); took >= 400*time.Millisecond {
		t.Fatalf("the part upload ran %s, past the lease of 400ms", took)
	}
	if w.calls.Load() != 1 {
		t.Fatalf("%d part uploads", w.calls.Load())
	}
	if got := f.rows.sessions[sess.ID].Received; got != 0 {
		t.Fatalf("received = %d", got)
	}
}

func TestR_STOR_011_AStaleWriterSendsNoPartAfterTheRecordedOne(t *testing.T) {
	f := newFixture(t)
	stale, other := f.replica(), f.replica()
	data := random(2 * testPart)
	sess, _ := stale.Start(context.Background(), 3, f.sc)
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	stale.Fault = func(point string) error {
		if point == "append.claimed" {
			once.Do(func() {
				close(reached)
				<-resume
			})
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := f.append(stale, sess.ID, 0, data); done <- err }()
	<-reached
	f.rows.advance(2 * f.cfg.Uploads.WriterLease) // its lease ran out; another request takes the session and records the parts
	if _, err := f.append(other, sess.ID, 0, data); err != nil {
		t.Fatal(err)
	}
	before := f.obj.Count("upload_part")
	close(resume)
	if err := <-done; errKind(err) != errs.RangeNotSatisfiable {
		t.Fatalf("the stale writer: %v", err)
	}
	if got := f.obj.Count("upload_part"); got != before {
		t.Fatalf("the stale writer sent %d parts after the claim moved", got-before)
	}
	if err := f.finalize(other, sess.ID, data, nil); err != nil {
		t.Fatal(err)
	}
	if !f.stored(data) {
		t.Fatal("the stored blob is not the file")
	}
}

func TestR_STOR_011_ARequestOnASessionAbortedUnderItIsNotFound(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.Fault = func(point string) error {
		if point == "append.body_ended" {
			once.Do(func() {
				close(reached)
				<-resume
			})
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := f.append(s, sess.ID, 0, random(5)); done <- err }()
	<-reached
	if err := f.replica().Abort(context.Background(), sess.ID, f.sc); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-done; errKind(err) != errs.NotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestR_STOR_011_ARepeatedClosingRequestIsAnsweredFromTheRecord(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	data := random(20)
	sess, _ := s.Start(context.Background(), 3, f.sc)
	var commits atomic.Int32
	commit := func(ctx context.Context, st *blob.Spooled, md func(context.Context, meta.Tx, int64) error) error {
		commits.Add(1)
		return f.commit(ctx, st, md)
	}
	res, err := s.Finalize(context.Background(), sess.ID, f.sc, -1, sumOf(data), bytes.NewReader(data), 0, commit)
	if err != nil || res.Replayed || res.ItemID != 42 {
		t.Fatalf("first: %+v, %v", res, err)
	}
	res, err = f.replica().Finalize(context.Background(), sess.ID, f.sc, -1, sumOf(data), nil, 0, commit)
	if err != nil || !res.Replayed || res.ItemID != 42 || commits.Load() != 1 {
		t.Fatalf("repeat: %+v, %v, %d commits", res, err, commits.Load())
	}
}

func TestR_STOR_011_ACommitThatFailsAfterTheSessionWasMarkedLeavesItAssembled(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	data := random(20)
	sess, _ := s.Start(context.Background(), 3, f.sc)
	f.failAfterMarkDone = errors.New("the storage hook failed")
	if err := f.finalize(s, sess.ID, data, data); err == nil {
		t.Fatal("the hook failure was swallowed")
	}
	if got := f.rows.state(sess.ID); got != StateAssembled {
		t.Fatalf("state = %s, want assembled: the end of the session rolled back with the item", got)
	}
	f.failAfterMarkDone = nil
	if err := f.finalize(f.replica(), sess.ID, data, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.rows.state(sess.ID); got != StateDone || !f.stored(data) {
		t.Fatalf("state = %s", got)
	}
}

func TestR_STOR_011_ATailObjectLongerThanRecordedFailsAtOnce(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	if _, err := f.append(s, sess.ID, 0, random(5)); err != nil {
		t.Fatal(err)
	}
	r, _, _ := f.rows.get(context.Background(), sess.ID, time.Hour)
	f.obj.Set(s.tailKey(sess.ID, r.Tails[0].Writer), random(5*testPart), time.Now())
	if _, err := f.append(s, sess.ID, 5, random(30)); errKind(err) != errs.Internal {
		t.Fatalf("err = %v", err)
	}
}

func TestR_STOR_011_ABodyBeyondWhatThePartsCanHoldIsRefused(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	// 10,000 parts of testPart bytes: the session cannot take more, whatever the configured maximum size says
	_, err := s.Append(context.Background(), sess.ID, f.sc, 0, io.LimitReader(zeros{}, testPart*maxParts+1), 0)
	if errKind(err) != errs.TooLarge {
		t.Fatalf("err = %v", err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestR_STOR_011_SessionsAreCountedByHowTheyEnded(t *testing.T) {
	f := newFixture(t)
	m := blob.NewMetrics(observability.NewMetrics("packages"))
	cfg := f.cfg
	cfg.Spool.Dir = t.TempDir()
	sp, err := blob.NewSpool(cfg.Spool)
	if err != nil {
		t.Fatal(err)
	}
	s := newService(f.rows, Options{Objects: f.obj, Spool: sp, Storage: cfg, Metrics: m})
	count := func(outcome string) float64 { return metricValue(t, m, outcome) }

	done, _ := s.Start(context.Background(), 3, f.sc)
	data := random(30)
	if _, err := s.Finalize(context.Background(), done.ID, f.sc, -1, sumOf(data), bytes.NewReader(data), 0, f.commit); err != nil {
		t.Fatal(err)
	}
	gone, _ := s.Start(context.Background(), 3, f.sc)
	if err := s.Abort(context.Background(), gone.ID, f.sc); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Start(context.Background(), 3, f.sc)
	f.rows.advance(cfg.Uploads.SessionMaxAge + time.Minute)
	if _, err := s.Get(context.Background(), old.ID, f.sc); errKind(err) != errs.NotFound {
		t.Fatal(err)
	}
	big, _ := s.Start(context.Background(), 3, f.sc)
	if _, err := s.Append(context.Background(), big.ID, f.sc, 0, bytes.NewReader(random(50)), 20); errKind(err) != errs.TooLarge {
		t.Fatal(err)
	}
	if count("completed") != 1 || count("aborted") != 2 || count("expired") != 1 {
		t.Fatalf("completed %v, aborted %v, expired %v", count("completed"), count("aborted"), count("expired"))
	}
}

func sumOf(b []byte) [32]byte { return sha256.Sum256(b) }

func metricValue(t *testing.T, m *blob.Metrics, outcome string) float64 {
	t.Helper()
	var d dto.Metric
	if err := m.UploadSessionCounter(outcome).Write(&d); err != nil {
		t.Fatal(err)
	}
	return d.GetCounter().GetValue()
}
