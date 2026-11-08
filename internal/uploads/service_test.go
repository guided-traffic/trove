package uploads

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

const testPart = 16

// fixture is the cluster of a test: the tables, the bucket and any number of replicas that share them.
type fixture struct {
	t      *testing.T
	rows   *memRows
	obj    *blobtest.Store
	cfg    config.Storage
	prefix string
	sc     Scope
	// failAfterMarkDone makes the commit fail after the end of the session was written in its transaction.
	failAfterMarkDone error
	// wrap, when set, is the object store the replicas see: the in-memory store behind a test's own wrapper.
	wrap func(*blobtest.Store) ObjectStore
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := blobtest.Storage(t, t.TempDir())
	cfg.Uploads.PartSize = testPart
	obj := blobtest.New()
	obj.MinPartSize = testPart
	return &fixture{
		t: t, rows: newMemRows(), obj: obj, cfg: cfg, prefix: cfg.S3.Prefix,
		sc: Scope{Addressed: 7, Binding: "team/app", Principal: uuid.NewString()},
	}
}

// replica is another process on the same tables and bucket, with a spool of its own.
func (f *fixture) replica() *Service {
	f.t.Helper()
	cfg := f.cfg
	cfg.Spool.Dir = f.t.TempDir()
	sp, err := blob.NewSpool(cfg.Spool)
	if err != nil {
		f.t.Fatal(err)
	}
	var objects ObjectStore = f.obj
	if f.wrap != nil {
		objects = f.wrap(f.obj)
	}
	return newService(f.rows, Options{Objects: objects, Spool: sp, Storage: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// commit is the CommitFunc of a replica without a database: the object write of the commit protocol, the server-side copy of a
// blob that is not stored yet, and the end of the session in the same step.
func (f *fixture) commit(ctx context.Context, staged *blob.Spooled, markDone func(context.Context, meta.Tx, int64) error) error {
	key := blob.Key(f.prefix, staged.SHA256)
	if _, ok := f.obj.Data(key); !ok {
		if err := f.obj.Copy(ctx, staged.StagedKey(), key, staged.Size); err != nil {
			if errors.Is(err, blob.ErrNotFound) {
				return blob.ErrStagingGone
			}
			return err
		}
	}
	if err := markDone(ctx, nil, 42); err != nil {
		f.rows.rollbackTx()
		return err
	}
	if f.failAfterMarkDone != nil { // a hook of the item commit that fails after the session was marked: the transaction rolls back
		f.rows.rollbackTx()
		return f.failAfterMarkDone
	}
	f.rows.commitTx()
	return nil
}

func (f *fixture) stored(data []byte) bool {
	got, ok := f.obj.Data(blob.Key(f.prefix, sha256.Sum256(data)))
	return ok && bytes.Equal(got, data)
}

// nothingStaged fails the test when a session left an object or an unfinished multipart upload behind.
func (f *fixture) nothingStaged() {
	f.t.Helper()
	if keys := f.obj.Keys(blob.UploadsPrefix(f.prefix)); len(keys) != 0 {
		f.t.Errorf("staging objects left: %v", keys)
	}
	if n := f.obj.OpenUploads(); n != 0 {
		f.t.Errorf("%d multipart uploads left", n)
	}
}

func random(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func errKind(err error) errs.Kind {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return errs.Internal
}

func (f *fixture) append(s *Service, id uuid.UUID, offset int64, data []byte) (int64, error) {
	return s.Append(context.Background(), id, f.sc, offset, bytes.NewReader(data), 0)
}

func (f *fixture) finalize(s *Service, id uuid.UUID, data []byte, last []byte) error {
	_, err := s.Finalize(context.Background(), id, f.sc, sha256.Sum256(data), bytes.NewReader(last), 0, f.commit)
	return err
}

func TestR_STOR_011_ChunksSentThroughThreeReplicasYieldTheBlob(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.replica(), f.replica(), f.replica()
	data := random(5*testPart + 5)
	sess, err := a.Start(context.Background(), 3, f.sc)
	if err != nil {
		t.Fatal(err)
	}
	if sess.PartSize != testPart || sess.Received != 0 || sess.Target != 3 || sess.Addressed != 7 {
		t.Fatalf("session = %+v", sess)
	}
	chunks := [][]byte{data[:23], data[23:41], data[41:46], data[46:]}
	for i, chunk := range chunks[:3] {
		s := []*Service{b, c, a}[i]
		got, err := f.append(s, sess.ID, -1, chunk)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		want := int64(0)
		for _, ch := range chunks[:i+1] {
			want += int64(len(ch))
		}
		if got != want {
			t.Fatalf("chunk %d: received %d, want %d", i, got, want)
		}
	}
	if err := f.finalize(c, sess.ID, data, chunks[3]); err != nil {
		t.Fatal(err)
	}
	if !f.stored(data) {
		t.Fatal("the stored blob is not the file")
	}
	f.nothingStaged()
	if got := f.rows.state(sess.ID); got != StateDone {
		t.Fatalf("state = %s", got)
	}
}

func TestR_STOR_011_PartsAreExactlyThePartSize(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	data := random(100) // one byte per request: 6 parts of 16 and a tail of 4
	sess, err := s.Start(context.Background(), 3, f.sc)
	if err != nil {
		t.Fatal(err)
	}
	for i := range data {
		if _, err := f.append(f.replica(), sess.ID, int64(i), data[i:i+1]); err != nil {
			t.Fatalf("byte %d: %v", i, err)
		}
	}
	// the parts are recorded as they complete, and the tail is a smaller object
	ps, _ := f.rows.parts(context.Background(), sess.ID)
	if len(ps) != 6 {
		t.Fatalf("%d parts recorded, want 6", len(ps))
	}
	for _, p := range ps {
		if p.Size != testPart {
			t.Fatalf("part %d has %d bytes", p.Number, p.Size)
		}
	}
	if err := f.finalize(s, sess.ID, data, nil); err != nil {
		t.Fatal(err)
	}
	if !f.stored(data) {
		t.Fatal("the stored blob is not the file")
	}
	f.nothingStaged()
}

func TestR_STOR_011_BlobShapes(t *testing.T) {
	for name, size := range map[string]int{
		"empty": 0, "below one part": 9, "exactly one part": testPart, "two whole parts": 2 * testPart, "parts and a tail": 3*testPart + 1,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			s := f.replica()
			data := random(size)
			sess, err := s.Start(context.Background(), 3, f.sc)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.finalize(f.replica(), sess.ID, data, data); err != nil {
				t.Fatal(err)
			}
			if !f.stored(data) {
				t.Fatal("the stored blob is not the file")
			}
			f.nothingStaged()
		})
	}
}

func TestR_STOR_011_AnOffsetOtherThanReceivedAnswers416WithTheCount(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	got, err := f.append(s, sess.ID, 10, random(10))
	if errKind(err) != errs.RangeNotSatisfiable || got != 0 {
		t.Fatalf("on an empty session: received %d, err %v", got, err)
	}
	if _, err := f.append(s, sess.ID, 0, random(20)); err != nil {
		t.Fatal(err)
	}
	got, err = f.append(s, sess.ID, 5, random(10))
	if errKind(err) != errs.RangeNotSatisfiable || got != 20 {
		t.Fatalf("after 20 bytes: received %d, err %v", got, err)
	}
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.CodeOCIBlobUploadInvalid {
		t.Fatalf("code = %v", err)
	}
}

func TestR_STOR_011_ConcurrentPatchesOneIsRecordedTheOther416(t *testing.T) {
	f := newFixture(t)
	first, second := f.replica(), f.replica()
	data := random(2*testPart + 3)
	sess, _ := first.Start(context.Background(), 3, f.sc)

	inside, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	first.Fault = func(point string) error {
		if point == "append.claimed" {
			once.Do(func() { close(inside) })
			<-release
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.append(first, sess.ID, 0, data)
		done <- err
	}()
	<-inside
	got, err := f.append(second, sess.ID, 0, data)
	if errKind(err) != errs.RangeNotSatisfiable || got != 0 {
		t.Fatalf("the second request: received %d, err %v", got, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := f.finalize(second, sess.ID, data, nil); err != nil {
		t.Fatal(err)
	}
	if !f.stored(data) {
		t.Fatal("the stored blob is not the file")
	}
}

func TestR_STOR_011_ARequestThatLostItsClaimWritesATailAndFailsItsRecord(t *testing.T) {
	f := newFixture(t)
	zombie, other := f.replica(), f.replica()
	data := random(2*testPart + 3)
	sess, _ := zombie.Start(context.Background(), 3, f.sc)
	if _, err := f.append(zombie, sess.ID, 0, data[:5]); err != nil {
		t.Fatal(err)
	}
	before, _, _ := f.rows.get(context.Background(), sess.ID, time.Hour)

	// the first request is held after its body ended until its claim has expired and a second one has continued the session
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	zombie.Fault = func(point string) error {
		if point == "append.body_ended" {
			once.Do(func() {
				close(reached)
				<-resume
			})
		}
		return nil
	}
	type result struct {
		received int64
		err      error
	}
	done := make(chan result, 1)
	go func() {
		n, err := f.append(zombie, sess.ID, 5, data[5:12])
		done <- result{n, err}
	}()
	<-reached
	f.rows.advance(2 * f.cfg.Uploads.WriterLease)
	if _, err := f.append(other, sess.ID, 5, data[5:20]); err != nil {
		t.Fatal(err)
	}
	recorded, _, _ := f.rows.get(context.Background(), sess.ID, time.Hour)
	tailsBefore := f.rows.tailsOf(sess.ID)
	close(resume)
	r := <-done
	if errKind(r.err) != errs.RangeNotSatisfiable || r.received != recorded.Received {
		t.Fatalf("the zombie: received %d, err %v", r.received, r.err)
	}
	after, _, _ := f.rows.get(context.Background(), sess.ID, time.Hour)
	if !slices.EqualFunc(f.rows.tailsOf(sess.ID), tailsBefore, func(a, b tailSeg) bool { return a.Seq == b.Seq && a.Writer == b.Writer && a.Size == b.Size }) || after.TailSize != recorded.TailSize || after.Received != recorded.Received || before.Received == after.Received {
		t.Fatalf("the recorded tail changed: before %+v, after %+v", recorded, after)
	}
	if got := len(f.obj.Keys(blob.SessionPrefix(f.prefix, sess.ID.String()))); got != 1 {
		t.Fatalf("%d objects staged, want the one recorded tail: %v", got, f.obj.Keys(blob.SessionPrefix(f.prefix, sess.ID.String())))
	}
	if err := f.finalize(zombie, sess.ID, data, data[20:]); err != nil {
		t.Fatal(err)
	}
	if !f.stored(data) {
		t.Fatal("the stored blob is not the file")
	}
	f.nothingStaged()
}

func TestR_STOR_011_ADigestThatDiffersKeepsTheSessionOpen(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	data := random(40)
	sess, _ := s.Start(context.Background(), 3, f.sc)
	wrong := sha256.Sum256([]byte("not the data"))
	_, err := s.Finalize(context.Background(), sess.ID, f.sc, wrong, bytes.NewReader(data), 0, f.commit)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.CodeOCIDigestInvalid {
		t.Fatalf("err = %v", err)
	}
	got, err := s.Get(context.Background(), sess.ID, f.sc)
	if err != nil || got.Received != 40 || got.State != StateOpen {
		t.Fatalf("session = %+v, err %v", got, err)
	}
	// the bytes of the closing request were recorded: the client closes again, with nothing more to send
	if err := f.finalize(f.replica(), sess.ID, data, nil); err != nil {
		t.Fatal(err)
	}
	if !f.stored(data) {
		t.Fatal("the stored blob is not the file")
	}
}

func TestR_STOR_011_ACutFinalizeIsRepeatedOnAnotherReplicaAtTheRecordedState(t *testing.T) {
	cuts := []string{
		"finalize.completing_recorded", "finalize.completing", "finalize.after_complete", "finalize.committed",
	}
	for _, point := range cuts {
		t.Run(point, func(t *testing.T) {
			f := newFixture(t)
			a, b := f.replica(), f.replica()
			data := random(3*testPart + 5)
			sess, _ := a.Start(context.Background(), 3, f.sc)
			if _, err := f.append(a, sess.ID, 0, data[:30]); err != nil {
				t.Fatal(err)
			}
			a.Fault = func(p string) error {
				if p == point {
					return errors.New("process killed")
				}
				return nil
			}
			err := f.finalize(a, sess.ID, data, data[30:])
			if !errors.Is(err, blob.ErrFault) {
				t.Fatalf("the cut finalize: %v", err)
			}
			// the retry reaches another replica and does not send its body again
			if err := f.finalize(b, sess.ID, data, nil); err != nil {
				t.Fatalf("the retry: %v", err)
			}
			if !f.stored(data) {
				t.Fatal("the stored blob is not the file")
			}
			f.nothingStaged()
			// and once more after it finished
			if err := f.finalize(a2(f), sess.ID, data, nil); err != nil {
				t.Fatalf("a finalize after done: %v", err)
			}
		})
	}
}

func a2(f *fixture) *Service { return f.replica() }

func TestR_STOR_011_ARetriedFinalizeNeedsTheRecordedDigest(t *testing.T) {
	f := newFixture(t)
	a := f.replica()
	data := random(20)
	sess, _ := a.Start(context.Background(), 3, f.sc)
	a.Fault = func(p string) error {
		if p == "finalize.after_complete" {
			return errors.New("process killed")
		}
		return nil
	}
	_ = f.finalize(a, sess.ID, data, data)
	other := sha256.Sum256([]byte("x"))
	_, err := f.replica().Finalize(context.Background(), sess.ID, f.sc, other, nil, 0, f.commit)
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.CodeOCIDigestInvalid {
		t.Fatalf("err = %v", err)
	}
}

func TestR_STOR_011_TwoFinalizesOfOneSessionStoreOneBlob(t *testing.T) {
	f := newFixture(t)
	a, b := f.replica(), f.replica()
	data := random(3*testPart + 7)
	sess, _ := a.Start(context.Background(), 3, f.sc)
	if _, err := f.append(a, sess.ID, 0, data); err != nil {
		t.Fatal(err)
	}
	// the first finalize records completing and stops before it assembles; the second runs the rest while the first resumes
	paused, resume := make(chan struct{}), make(chan struct{})
	a.Fault = func(p string) error {
		if p == "finalize.completing" {
			close(paused)
			<-resume
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- f.finalize(a, sess.ID, data, nil) }()
	<-paused
	if err := f.finalize(b, sess.ID, data, nil); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatalf("the overlapping finalize: %v", err)
	}
	if !f.stored(data) {
		t.Fatal("the stored blob is not the file")
	}
	f.nothingStaged()
}

func TestR_STOR_011_ASessionOfAnotherScopeDoesNotExist(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	for name, sc := range map[string]Scope{
		"another principal":  {Addressed: f.sc.Addressed, Binding: f.sc.Binding, Principal: uuid.NewString()},
		"anonymous":          {Addressed: f.sc.Addressed, Binding: f.sc.Binding},
		"another repository": {Addressed: f.sc.Addressed + 1, Binding: f.sc.Binding, Principal: f.sc.Principal},
		"another binding":    {Addressed: f.sc.Addressed, Binding: "team/other", Principal: f.sc.Principal},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := s.Get(ctx, sess.ID, sc); errKind(err) != errs.NotFound {
				t.Errorf("Get: %v", err)
			}
			if _, err := s.Append(ctx, sess.ID, sc, 0, strings.NewReader("x"), 0); errKind(err) != errs.NotFound {
				t.Errorf("Append: %v", err)
			}
			if _, err := s.Finalize(ctx, sess.ID, sc, sha256.Sum256(nil), nil, 0, f.commit); errKind(err) != errs.NotFound {
				t.Errorf("Finalize: %v", err)
			}
			if err := s.Abort(ctx, sess.ID, sc); errKind(err) != errs.NotFound {
				t.Errorf("Abort: %v", err)
			}
		})
	}
	// nothing of it happened to the real session
	got, err := s.Get(context.Background(), sess.ID, f.sc)
	if err != nil || got.Received != 0 || got.State != StateOpen {
		t.Fatalf("session = %+v, err %v", got, err)
	}
	if _, err := s.Get(context.Background(), uuid.New(), f.sc); errKind(err) != errs.NotFound {
		t.Fatalf("an id that is none: %v", err)
	}
}

func TestR_STOR_011_ASessionPastItsMaximumAgeIsAbortedByTheRequest(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	if _, err := f.append(s, sess.ID, 0, random(40)); err != nil {
		t.Fatal(err)
	}
	f.rows.advance(f.cfg.Uploads.SessionMaxAge + time.Minute)
	if _, err := f.append(s, sess.ID, 40, random(3)); errKind(err) != errs.NotFound {
		t.Fatalf("append: %v", err)
	}
	if got := f.rows.state(sess.ID); got != StateAborted {
		t.Fatalf("state = %s, want aborted", got)
	}
	if err := f.finalize(s, sess.ID, nil, nil); errKind(err) != errs.NotFound {
		t.Fatalf("finalize: %v", err)
	}
}

func TestR_STOR_011_ARequestThatCrossesTheMaximumSizeAbortsTheSession(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	got, err := s.Append(context.Background(), sess.ID, f.sc, 0, bytes.NewReader(random(50)), 40)
	if errKind(err) != errs.TooLarge {
		t.Fatalf("err = %v", err)
	}
	if got%testPart != 0 || got > 40 {
		t.Fatalf("received = %d, want whole parts below the limit", got)
	}
	if st := f.rows.state(sess.ID); st != stateGone { // the request removed the objects, so it deleted the row
		t.Fatalf("state = %q", st)
	}
	f.nothingStaged()
}

func TestR_STOR_011_AbortRemovesWhatTheSessionStaged(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	if _, err := f.append(s, sess.ID, 0, random(40)); err != nil {
		t.Fatal(err)
	}
	if err := s.Abort(context.Background(), sess.ID, f.sc); err != nil {
		t.Fatal(err)
	}
	f.nothingStaged()
	if _, err := s.Get(context.Background(), sess.ID, f.sc); errKind(err) != errs.NotFound {
		t.Fatalf("Get after abort: %v", err)
	}
}

func TestR_STOR_011_ATailThatIsNotTheRecordedOneFailsTheSession(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	if _, err := f.append(s, sess.ID, 0, random(5)); err != nil {
		t.Fatal(err)
	}
	r, _, _ := f.rows.get(context.Background(), sess.ID, time.Hour)
	f.obj.Set(s.tailKey(sess.ID, r.Tails[0].Writer), random(5), time.Now())
	if _, err := f.append(s, sess.ID, 5, random(30)); errKind(err) != errs.Internal {
		t.Fatalf("append over a damaged tail: %v", err)
	}
	if st := f.rows.state(sess.ID); st != stateGone { // the request removed the objects, so it deleted the row
		t.Fatalf("state = %q", st)
	}
}

func TestR_STOR_011_ARequestWithoutBytesChangesNothing(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	if _, err := f.append(s, sess.ID, 0, random(5)); err != nil {
		t.Fatal(err)
	}
	before := f.obj.Keys(blob.SessionPrefix(f.prefix, sess.ID.String()))
	got, err := f.append(s, sess.ID, -1, nil)
	if err != nil || got != 5 {
		t.Fatalf("received %d, err %v", got, err)
	}
	if after := f.obj.Keys(blob.SessionPrefix(f.prefix, sess.ID.String())); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("objects changed: %v -> %v", before, after)
	}
	// and the lease is not left behind
	if _, err := f.append(s, sess.ID, 5, random(2)); err != nil {
		t.Fatal(err)
	}
}

func TestR_STOR_011_ADrainReleasesTheClaimAtOnce(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	ctx, cancel := context.WithCancel(context.Background())
	body := io.MultiReader(bytes.NewReader(random(3*testPart)), &cancelThenFail{cancel: cancel})
	if _, err := s.Append(ctx, sess.ID, f.sc, 0, body, 0); err == nil {
		t.Fatal("a cut request succeeded")
	}
	// the parts it completed are recorded, and the next request continues at them with no wait for the lease
	got, err := s.Get(context.Background(), sess.ID, f.sc)
	if err != nil || got.Received%testPart != 0 {
		t.Fatalf("session = %+v, err %v", got, err)
	}
	if _, err := f.append(f.replica(), sess.ID, got.Received, random(4)); err != nil {
		t.Fatalf("the request after the drain: %v", err)
	}
}

// cancelThenFail cancels its context and then fails, the way a body does when its replica is drained.
type cancelThenFail struct{ cancel context.CancelFunc }

func (c *cancelThenFail) Read([]byte) (int, error) {
	c.cancel()
	return 0, context.Canceled
}

func TestR_STOR_011_HashStateRoundTrip(t *testing.T) {
	data := random(1000)
	h := newHashes()
	h.write(data[:400])
	b, err := h.marshal()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restoreHashes(b)
	if err != nil {
		t.Fatal(err)
	}
	restored.write(data[400:])
	whole := newHashes()
	whole.write(data)
	if restored.digests() != whole.digests() {
		t.Fatal("a restored hash state continues to another digest")
	}
	for _, bad := range [][]byte{nil, b[:7], b[:len(b)-1], append(bytes.Clone(b), 0)} {
		if _, err := restoreHashes(bad); err == nil {
			t.Fatalf("restoreHashes(%d bytes) succeeded", len(bad))
		}
	}
}
