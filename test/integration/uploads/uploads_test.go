//go:build integration

package uploads

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/uploads"
)

const mib = 1 << 20

// replica is one trove replica as an upload sees it: its own blob store, spool and session service on the shared database and
// bucket.
type replica struct {
	store *blob.Store
	svc   *uploads.Service
	pg    *pgEnv
	cfg   config.Storage
}

func newReplica(tb testing.TB, e *s3Env, p *pgEnv, mutate func(*config.Storage)) *replica {
	tb.Helper()
	cfg := e.storage(tb)
	if mutate != nil {
		mutate(&cfg)
	}
	store, err := blob.New(cfg, e.raw, p.db, blob.WithLogger(quiet), blob.WithWaitPoll(25*time.Millisecond))
	if err != nil {
		tb.Fatal(err)
	}
	svc, err := uploads.New(uploads.Options{DB: p.db, Objects: e.raw, Spool: store.Spool(), Storage: cfg, Log: quiet})
	if err != nil {
		tb.Fatal(err)
	}
	return &replica{store: store, svc: svc, pg: p, cfg: cfg}
}

// commit is the commit of a finalize as the host runs it: the blob commit protocol with the server-side copy of the staging object,
// and in its transaction the item and the end of the session.
func (r *replica) commit(path string) uploads.CommitFunc {
	return func(ctx context.Context, staged *blob.Spooled, markDone func(context.Context, meta.Tx, int64) error) error {
		_, err := r.store.Commit(ctx, staged, r.pg.link(path, staged, func(ctx context.Context, tx meta.Tx) error { return markDone(ctx, tx, 1) }))
		return err
	}
}

func (r *replica) finalize(sc uploads.Scope, id uuid.UUID, whole, last []byte, path string) error {
	_, err := r.svc.Finalize(context.Background(), id, sc, -1, sha256.Sum256(whole), bytes.NewReader(last), 0, r.commit(path))
	return err
}

func (r *replica) append(sc uploads.Scope, id uuid.UUID, offset int64, data []byte) (int64, error) {
	return r.svc.Append(context.Background(), id, sc, offset, bytes.NewReader(data), 0)
}

func random(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func kind(err error) errs.Kind {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return errs.Internal
}

type world struct {
	e  *s3Env
	p  *pgEnv
	sc uploads.Scope
}

func newWorld(tb testing.TB) *world {
	tb.Helper()
	p := newPG(tb)
	return &world{e: newS3(tb), p: p, sc: uploads.Scope{Addressed: p.repo, Binding: "team/app", Principal: uuid.NewString()}}
}

// storedIs reports that the blob of data is in the bucket with its bytes.
func (w *world) storedIs(tb testing.TB, data []byte) {
	tb.Helper()
	d := blob.Digest(sha256.Sum256(data))
	rc, info, err := w.e.raw.Get(context.Background(), blob.Key(w.e.prefix, d), nil)
	if err != nil {
		tb.Fatalf("the blob is not stored: %v", err)
	}
	defer rc.Close()
	var got bytes.Buffer
	if _, err := got.ReadFrom(rc); err != nil {
		tb.Fatal(err)
	}
	if info.Size >= 0 && info.Size != int64(len(data)) || !bytes.Equal(got.Bytes(), data) {
		tb.Fatalf("the stored blob has %d bytes and is not the file (%d bytes)", got.Len(), len(data))
	}
}

// nothingStaged fails when a session left an object or an unfinished multipart upload.
func (w *world) nothingStaged(tb testing.TB) {
	tb.Helper()
	if k := w.e.keys(tb, blob.UploadsPrefix(w.e.prefix)); len(k) != 0 {
		tb.Errorf("staging objects left: %v", k)
	}
	ups, err := w.e.raw.ListMultipart(context.Background(), blob.UploadsPrefix(w.e.prefix))
	if err != nil {
		tb.Fatal(err)
	}
	if len(ups) != 0 {
		tb.Errorf("%d multipart uploads left: %v", len(ups), ups)
	}
}

func TestR_STOR_011_ThreeChunksThroughThreeReplicasYieldTheBlob(t *testing.T) {
	w := newWorld(t)
	a, b, c := newReplica(t, w.e, w.p, nil), newReplica(t, w.e, w.p, nil), newReplica(t, w.e, w.p, nil)
	data := random(13*mib + 12345)
	ctx := context.Background()
	sess, err := a.svc.Start(ctx, w.p.repo, w.sc)
	if err != nil {
		t.Fatal(err)
	}
	if sess.PartSize != 5*mib {
		t.Fatalf("part size = %d", sess.PartSize)
	}
	// each request of the one upload reaches another replica
	if got, err := b.append(w.sc, sess.ID, 0, data[:6*mib]); err != nil || got != 6*mib {
		t.Fatalf("chunk 1: received %d, err %v", got, err)
	}
	if got, err := c.append(w.sc, sess.ID, 6*mib, data[6*mib:11*mib+7]); err != nil || got != 11*mib+7 {
		t.Fatalf("chunk 2: received %d, err %v", got, err)
	}
	if got, err := a.svc.Get(ctx, sess.ID, w.sc); err != nil || got.Received != 11*mib+7 {
		t.Fatalf("status: %+v, err %v", got, err)
	}
	if err := b.finalize(w.sc, sess.ID, data, data[11*mib+7:], "team/app/_blobs/one"); err != nil {
		t.Fatal(err)
	}
	w.storedIs(t, data)
	w.nothingStaged(t)
	if n := w.p.count(t, "items"); n != 1 {
		t.Fatalf("%d items", n)
	}
	if got := w.p.sessionState(t, sess.ID.String()); got != "done" {
		t.Fatalf("state = %q", got)
	}
}

func TestR_STOR_011_HundredChunksMakeWholeParts(t *testing.T) {
	w := newWorld(t)
	r := newReplica(t, w.e, w.p, nil)
	chunk := 300 * 1024
	data := random(100 * chunk) // 30,000 KiB: five parts of 5 MiB and a tail of 4,400 KiB
	ctx := context.Background()
	sess, err := r.svc.Start(ctx, w.p.repo, w.sc)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		other := newReplica(t, w.e, w.p, nil)
		if _, err := other.append(w.sc, sess.ID, int64(i*chunk), data[i*chunk:(i+1)*chunk]); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	rows := w.p.scalarInt(t, "SELECT count(*) FROM trove.upload_session_parts WHERE size = $1", 5*mib)
	if rows != 5 {
		t.Fatalf("%d parts of 5 MiB recorded, want 5", rows)
	}
	if err := r.finalize(w.sc, sess.ID, data, nil, "team/app/_blobs/many"); err != nil {
		t.Fatal(err)
	}
	w.storedIs(t, data)
	w.nothingStaged(t)
}

func TestR_STOR_011_ConcurrentPatchesOneIsRecordedTheOtherAnswers416(t *testing.T) {
	w := newWorld(t)
	a, b := newReplica(t, w.e, w.p, nil), newReplica(t, w.e, w.p, nil)
	data := random(12 * mib)
	ctx := context.Background()
	sess, err := a.svc.Start(ctx, w.p.repo, w.sc)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, r := range []*replica{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = r.append(w.sc, sess.ID, 0, data)
		}()
	}
	wg.Wait()
	ok, refused := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case kind(err) == errs.RangeNotSatisfiable:
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("%d recorded, %d answered 416; results %v", ok, refused, results)
	}
	if err := a.finalize(w.sc, sess.ID, data, nil, "team/app/_blobs/race"); err != nil {
		t.Fatal(err)
	}
	w.storedIs(t, data)
	w.nothingStaged(t)
}

// A request delayed after its body ended until its claim has expired and a second request has continued the session writes its
// tail and fails its record; the recorded tail is the second request's (R-STOR-011 acceptance).
func TestR_STOR_011_AZombieWriterFailsItsRecordAndLeavesTheRecordedTailAlone(t *testing.T) {
	w := newWorld(t)
	// Both replicas have the normal lease: the zombie's claim is expired in the database below, so the test does not depend on how fast the store
	// sends a part (a short lease cuts a part upload at three quarters of it).
	zombie, other := newReplica(t, w.e, w.p, nil), newReplica(t, w.e, w.p, nil)
	data := random(8 * mib)
	ctx := context.Background()
	sess, err := zombie.svc.Start(ctx, w.p.repo, w.sc)
	if err != nil {
		t.Fatal(err)
	}
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	zombie.svc.Fault = func(point string) error {
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
		n, err := zombie.append(w.sc, sess.ID, 0, data[:3*mib])
		done <- result{n, err}
	}()
	<-reached
	w.p.exec(t, "UPDATE trove.upload_sessions SET writer_until = now() - interval '1 second' WHERE id = $1::uuid", sess.ID.String()) // the lease of the zombie's claim runs out
	got, err := other.append(w.sc, sess.ID, 0, data[:4*mib])
	if err != nil || got != 4*mib {
		t.Fatalf("the second request: received %d, err %v", got, err)
	}
	var tailWriter string
	if err := w.p.admin.QueryRow(ctx, "SELECT COALESCE(string_agg(writer::text, ',' ORDER BY seq), '') FROM trove.upload_session_tails WHERE session_id = $1::uuid", sess.ID.String()).Scan(&tailWriter); err != nil {
		t.Fatal(err)
	}
	close(resume)
	r := <-done
	if kind(r.err) != errs.RangeNotSatisfiable {
		t.Fatalf("the zombie: received %d, err %v", r.received, r.err)
	}
	var after string
	if err := w.p.admin.QueryRow(ctx, "SELECT COALESCE(string_agg(writer::text, ',' ORDER BY seq), '') FROM trove.upload_session_tails WHERE session_id = $1::uuid", sess.ID.String()).Scan(&after); err != nil || after != tailWriter {
		t.Fatalf("the recorded tail changed from %s to %s (%v)", tailWriter, after, err)
	}
	if keys := w.e.keys(t, blob.SessionPrefix(w.e.prefix, sess.ID.String())); len(keys) != 1 {
		t.Fatalf("staged objects = %v, want the one recorded tail", keys)
	}
	if err := other.finalize(w.sc, sess.ID, data, data[4*mib:], "team/app/_blobs/zombie"); err != nil {
		t.Fatal(err)
	}
	w.storedIs(t, data)
	w.nothingStaged(t)
}

// A finalize that is the zombie of the same case is refused with the received count the second request recorded: the 416 of a lost claim
// reports the session as it stands, not 0 (the count comes back with the refusal, ADR-178).
func TestR_STOR_011_AZombieFinalizeIsRefusedWithTheReceivedCountOfTheSession(t *testing.T) {
	w := newWorld(t)
	zombie, other := newReplica(t, w.e, w.p, nil), newReplica(t, w.e, w.p, nil)
	data := random(8 * mib)
	ctx := context.Background()
	sess, err := zombie.svc.Start(ctx, w.p.repo, w.sc)
	if err != nil {
		t.Fatal(err)
	}
	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	zombie.svc.Fault = func(point string) error {
		if point == "append.body_ended" {
			once.Do(func() {
				close(reached)
				<-resume
			})
		}
		return nil
	}
	type result struct {
		res uploads.Result
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := zombie.svc.Finalize(ctx, sess.ID, w.sc, -1, sha256.Sum256(data[:3*mib]), bytes.NewReader(data[:3*mib]), 0, zombie.commit("team/app/_blobs/zf"))
		done <- result{res, err}
	}()
	<-reached
	w.p.exec(t, "UPDATE trove.upload_sessions SET writer_until = now() - interval '1 second' WHERE id = $1::uuid", sess.ID.String())
	if got, err := other.append(w.sc, sess.ID, 0, data[:4*mib]); err != nil || got != 4*mib {
		t.Fatalf("the second request: received %d, err %v", got, err)
	}
	close(resume)
	r := <-done
	if kind(r.err) != errs.RangeNotSatisfiable || r.res.Received != 4*mib {
		t.Fatalf("the zombie finalize: %v, received %d, want a 416 with %d", r.err, r.res.Received, 4*mib)
	}
}

// A finalize killed after the multipart upload completed and before the session recorded it, or after the commit and before the
// clean-up, is repeated on another replica and stores one blob.
func TestR_STOR_011_ACutFinalizeIsCompletedByTheRetryOnAnotherReplica(t *testing.T) {
	for _, point := range []string{"finalize.completing_recorded", "finalize.after_complete", "finalize.committed"} {
		t.Run(point, func(t *testing.T) {
			w := newWorld(t)
			a, b := newReplica(t, w.e, w.p, nil), newReplica(t, w.e, w.p, nil)
			data := random(11*mib + 99)
			ctx := context.Background()
			sess, err := a.svc.Start(ctx, w.p.repo, w.sc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.append(w.sc, sess.ID, 0, data[:7*mib]); err != nil {
				t.Fatal(err)
			}
			a.svc.Fault = func(p string) error {
				if p == point {
					return errors.New("process killed")
				}
				return nil
			}
			err = a.finalize(w.sc, sess.ID, data, data[7*mib:], "team/app/_blobs/cut")
			if !errors.Is(err, blob.ErrFault) {
				t.Fatalf("the cut finalize: %v", err)
			}
			if err := b.finalize(w.sc, sess.ID, data, nil, "team/app/_blobs/cut"); err != nil {
				t.Fatalf("the retry: %v", err)
			}
			w.storedIs(t, data)
			w.nothingStaged(t)
			if n := w.p.count(t, "blobs"); n != 1 {
				t.Fatalf("%d blobs", n)
			}
			if n := w.p.count(t, "items"); n != 1 {
				t.Fatalf("%d items", n)
			}
			// and a repetition after it finished answers the same
			if err := b.finalize(w.sc, sess.ID, data, nil, "team/app/_blobs/cut"); err != nil {
				t.Fatalf("a finalize after done: %v", err)
			}
		})
	}
}

func TestR_STOR_011_ADigestThatDiffersKeepsTheSessionOpenInPostgres(t *testing.T) {
	w := newWorld(t)
	r := newReplica(t, w.e, w.p, nil)
	data := random(6*mib + 1)
	sess, err := r.svc.Start(context.Background(), w.p.repo, w.sc)
	if err != nil {
		t.Fatal(err)
	}
	wrong := sha256.Sum256([]byte("wrong"))
	_, err = r.svc.Finalize(context.Background(), sess.ID, w.sc, -1, wrong, bytes.NewReader(data), 0, r.commit("team/app/_blobs/x"))
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.CodeOCIDigestInvalid {
		t.Fatalf("err = %v", err)
	}
	if got := w.p.sessionState(t, sess.ID.String()); got != "open" {
		t.Fatalf("state = %q", got)
	}
	if err := r.finalize(w.sc, sess.ID, data, nil, "team/app/_blobs/x"); err != nil {
		t.Fatal(err)
	}
	w.storedIs(t, data)
}

func TestR_STOR_011_ARequestOfAnotherPrincipalRepositoryOrBindingAnswers404(t *testing.T) {
	w := newWorld(t)
	r := newReplica(t, w.e, w.p, nil)
	ctx := context.Background()
	sess, err := r.svc.Start(ctx, w.p.repo, w.sc)
	if err != nil {
		t.Fatal(err)
	}
	for name, sc := range map[string]uploads.Scope{
		"another principal":  {Addressed: w.sc.Addressed, Binding: w.sc.Binding, Principal: uuid.NewString()},
		"anonymous":          {Addressed: w.sc.Addressed, Binding: w.sc.Binding},
		"another repository": {Addressed: w.p.other, Binding: w.sc.Binding, Principal: w.sc.Principal},
		"another binding":    {Addressed: w.sc.Addressed, Binding: "team/other", Principal: w.sc.Principal},
	} {
		if _, err := r.svc.Get(ctx, sess.ID, sc); kind(err) != errs.NotFound {
			t.Errorf("%s: Get: %v", name, err)
		}
		if _, err := r.append(sc, sess.ID, 0, []byte("x")); kind(err) != errs.NotFound {
			t.Errorf("%s: Append: %v", name, err)
		}
		if err := r.finalize(sc, sess.ID, nil, nil, "x"); kind(err) != errs.NotFound {
			t.Errorf("%s: Finalize: %v", name, err)
		}
		if err := r.svc.Abort(ctx, sess.ID, sc); kind(err) != errs.NotFound {
			t.Errorf("%s: Abort: %v", name, err)
		}
	}
	if got, err := r.svc.Get(ctx, sess.ID, w.sc); err != nil || got.Received != 0 {
		t.Fatalf("the session was touched: %+v, %v", got, err)
	}
}

func collector(r *replica, w *world, up config.StorageUploads) *blob.Collector {
	return blob.NewCollector(r.cfg.GC, r.cfg.S3.Prefix, w.e.raw, w.p.db, blob.WithCollectorLogger(quiet), blob.WithUploadSessions(up))
}

// A session idle for longer than session_ttl, and an open session older than session_max_age, are aborted by the next collection
// run; their multipart uploads and staging objects are gone (R-STOR-011 Expiry).
func TestR_STOR_011_TheCollectionRunExpiresAbandonedSessions(t *testing.T) {
	w := newWorld(t)
	r := newReplica(t, w.e, w.p, nil)
	ctx := context.Background()
	idle, old, live := uuid.UUID{}, uuid.UUID{}, uuid.UUID{}
	for _, dst := range []*uuid.UUID{&idle, &old, &live} {
		s, err := r.svc.Start(ctx, w.p.repo, w.sc)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.append(w.sc, s.ID, 0, random(7*mib)); err != nil { // a part and a tail
			t.Fatal(err)
		}
		*dst = s.ID
	}
	w.p.exec(t, "UPDATE trove.upload_sessions SET last_activity_at = now() - interval '5 hours' WHERE id = $1::uuid", idle.String())
	w.p.exec(t, "UPDATE trove.upload_sessions SET created_at = now() - interval '21 hours' WHERE id = $1::uuid", old.String())

	res, err := collector(r, w, r.cfg.Uploads).Run(ctx, blob.RunOptions{Mode: blob.GCIncremental})
	if err != nil {
		t.Fatal(err)
	}
	if res.UploadSessionsAborted != 2 || res.UploadSessionsCleaned != 2 {
		t.Fatalf("result = %+v", res)
	}
	for _, id := range []uuid.UUID{idle, old} {
		if got := w.p.sessionState(t, id.String()); got != "" {
			t.Errorf("session %s is still %q", id, got)
		}
		if k := w.e.keys(t, blob.SessionPrefix(w.e.prefix, id.String())); len(k) != 0 {
			t.Errorf("session %s left %v", id, k)
		}
	}
	if got := w.p.sessionState(t, live.String()); got != "open" {
		t.Fatalf("the live session is %q", got)
	}
	if k := w.e.keys(t, blob.SessionPrefix(w.e.prefix, live.String())); len(k) == 0 {
		t.Fatal("the live session lost its objects")
	}
	// the request of a session past its age answers 404 and aborts it without any run
	w.p.exec(t, "UPDATE trove.upload_sessions SET created_at = now() - interval '21 hours' WHERE id = $1::uuid", live.String())
	if _, err := r.append(w.sc, live, 7*mib, random(10)); kind(err) != errs.NotFound {
		t.Fatalf("append past the age: %v", err)
	}
	if got := w.p.sessionState(t, live.String()); got != "aborted" {
		t.Fatalf("state = %q", got)
	}
	if _, err := collector(r, w, r.cfg.Uploads).Run(ctx, blob.RunOptions{Mode: blob.GCIncremental}); err != nil {
		t.Fatal(err)
	}
	w.nothingStaged(t)
}

// The full sweep removes the staging objects and multipart uploads that no session stands for, and nothing else.
func TestR_STOR_011_TheSweepRemovesWhatNoSessionStandsFor(t *testing.T) {
	w := newWorld(t)
	r := newReplica(t, w.e, w.p, nil)
	ctx := context.Background()
	ghost := uuid.NewString()
	ghostTail := blob.SessionKey(w.e.prefix, ghost, "tail-"+uuid.NewString())
	if err := w.e.raw.Put(ctx, ghostTail, bytes.NewReader([]byte("x")), 1, blob.Checksum{}); err != nil {
		t.Fatal(err)
	}
	ghostData := blob.SessionKey(w.e.prefix, ghost, "data")
	if _, err := w.e.raw.CreateMultipart(ctx, ghostData, ""); err != nil {
		t.Fatal(err)
	}
	foreign := blob.UploadsPrefix(w.e.prefix) + "not-a-session/keep"
	if err := w.e.raw.Put(ctx, foreign, bytes.NewReader([]byte("x")), 1, blob.Checksum{}); err != nil {
		t.Fatal(err)
	}
	// the maximum age is short, so that what is planted is older than it when the session below starts, which is younger
	up := config.StorageUploads{SessionTTL: time.Hour, SessionMaxAge: 40 * time.Second}
	time.Sleep(41 * time.Second)
	live, err := r.svc.Start(ctx, w.p.repo, w.sc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.append(w.sc, live.ID, 0, random(7*mib)); err != nil {
		t.Fatal(err)
	}

	res, err := collector(r, w, up).Run(ctx, blob.RunOptions{Mode: blob.GCFull})
	if err != nil {
		t.Fatal(err)
	}
	if res.UploadObjectsDeleted != 1 || res.UploadsAborted != 1 {
		t.Fatalf("result = %+v", res)
	}
	if k := w.e.keys(t, blob.SessionPrefix(w.e.prefix, ghost)); len(k) != 0 {
		t.Fatalf("the unknown session's objects remain: %v", k)
	}
	keys := w.e.keys(t, blob.UploadsPrefix(w.e.prefix))
	foundForeign, foundLive := false, false
	for _, k := range keys {
		if k == foreign {
			foundForeign = true
		} else if id, _, ok := blob.ParseSessionKey(w.e.prefix, k); ok && id == live.ID.String() {
			foundLive = true
		}
	}
	if !foundForeign || !foundLive {
		t.Fatalf("the sweep removed too much: foreign kept %v, live session kept %v (%v)", foundForeign, foundLive, keys)
	}
	if got := w.p.sessionState(t, live.ID.String()); got != "open" {
		t.Fatalf("the live session is %q", got)
	}
}

// A part number that is sent twice with different bytes (a request that lost its claim sends its part after the recorded one) must not
// reach the blob when the completion names the recorded part's ETag: the store assembles the named part or refuses (R-STOR-035 A2).
func TestR_STOR_011_TheStoreAssemblesThePartThatIsNamedWhenANumberIsSentTwice(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for _, c := range blob.ConformanceChecks() {
		if !strings.Contains(c.Name, "assembles the part it names") {
			continue
		}
		opts := blob.ConformanceOptions{Prefix: w.e.prefix, Replica: "it-uploads"}
		if r := blob.RunCheck(ctx, w.e.raw, opts, c); !r.OK {
			t.Fatal(r.Error)
		}
		return
	}
	t.Fatal("the conformance check of the part identity does not exist")
}

// The server-side copy of an object above the single-copy limit is a multipart copy by ranges; the limits are lowered to reach it with a
// small object (R-STOR-011 Finalize step 3).
func TestR_STOR_011_AMultipartServerSideCopyYieldsTheSourcesBytes(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c, ok := w.e.raw.(interface{ SetCopyLimits(single, step int64) })
	if !ok {
		t.Fatal("the object store client has no SetCopyLimits")
	}
	data := random(11 * mib)
	src, dst := blob.SessionKey(w.e.prefix, uuid.NewString(), "data"), blob.SessionKey(w.e.prefix, uuid.NewString(), "data")
	if err := w.e.raw.Put(ctx, src, bytes.NewReader(data), int64(len(data)), blob.Checksum{}); err != nil {
		t.Fatal(err)
	}
	c.SetCopyLimits(5*mib, 5*mib) // three parts: 5, 5 and 1 MiB
	if err := w.e.raw.Copy(ctx, src, dst, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	rc, _, err := w.e.raw.Get(ctx, dst, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	var got bytes.Buffer
	if _, err := got.ReadFrom(rc); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("the copy has %d bytes that differ from the source's %d", got.Len(), len(data))
	}
	if err := w.e.raw.Copy(ctx, src+"-missing", dst, 1); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("copy of a missing source: %v", err)
	}
}

// While the deletion pause is set, the collection run neither aborts nor cleans up sessions, and a request past the maximum age is
// refused all the same (R-STOR-011 Expiry, R-STOR-024).
func TestR_STOR_011_ASessionPastItsAgeIsRefusedWhileTheDeletionPauseKeepsTheRunFromCleaning(t *testing.T) {
	w := newWorld(t)
	r := newReplica(t, w.e, w.p, nil)
	ctx := context.Background()
	s, err := r.svc.Start(ctx, w.p.repo, w.sc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.append(w.sc, s.ID, 0, random(7*mib)); err != nil {
		t.Fatal(err)
	}
	w.p.exec(t, "UPDATE trove.upload_sessions SET created_at = now() - interval '21 hours' WHERE id = $1::uuid", s.ID.String())
	w.p.exec(t, "INSERT INTO trove.system_state (key, value) VALUES ('deletions_paused_at', to_jsonb(now()::text)) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value")
	res, err := collector(r, w, r.cfg.Uploads).Run(ctx, blob.RunOptions{Mode: blob.GCIncremental})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Paused || res.UploadSessionsAborted != 0 {
		t.Fatalf("result = %+v", res)
	}
	if k := w.e.keys(t, blob.SessionPrefix(w.e.prefix, s.ID.String())); len(k) == 0 {
		t.Fatal("the paused run removed the session's objects")
	}
	// the request refuses it all the same and sets it aborted
	if _, err := r.append(w.sc, s.ID, 7*mib, random(10)); kind(err) != errs.NotFound {
		t.Fatalf("append past the age: %v", err)
	}
	if got := w.p.sessionState(t, s.ID.String()); got != "aborted" {
		t.Fatalf("state = %q", got)
	}
	// the run that resumes cleans it
	w.p.exec(t, "DELETE FROM trove.system_state WHERE key = 'deletions_paused_at'")
	if _, err := collector(r, w, r.cfg.Uploads).Run(ctx, blob.RunOptions{Mode: blob.GCIncremental}); err != nil {
		t.Fatal(err)
	}
	w.nothingStaged(t)
}
