package uploads

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

// A request whose body arrives slowly loses its lease; another request takes the session and completes the part, which removes the
// tail segments. The slow request's body then completes its own part: it finds the claim gone and stops, and the live session stays open.
func TestR_STOR_011_ARequestThatLostItsClaimMustNotAbortTheLiveSessionWhenTheSegmentsAreGone(t *testing.T) {
	f := newFixture(t)
	f.cfg.Uploads.WriterLease = time.Second
	zombie, other := f.replica(), f.replica()
	data := random(3*testPart + 4)
	sess, _ := zombie.Start(context.Background(), 3, f.sc)
	if _, err := f.append(zombie, sess.ID, 0, data[:5]); err != nil { // one tail segment
		t.Fatal(err)
	}
	reached, resume := make(chan struct{}), make(chan struct{})
	zombie.Fault = func(p string) error {
		if p == "append.claimed" {
			close(reached)
			<-resume // the body is slow: nothing has arrived yet
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := zombie.Append(context.Background(), sess.ID, f.sc, 5, bytes.NewReader(data[5:testPart+2]), 0)
		done <- err
	}()
	<-reached
	f.rows.advance(2 * time.Second) // the zombie's lease ran out
	if got, err := f.append(other, sess.ID, 5, data[5:2*testPart+1]); err != nil || got != 2*testPart+1 {
		t.Fatalf("the live request: %d, %v", got, err)
	}
	close(resume)
	if zerr := <-done; errKind(zerr) != errs.RangeNotSatisfiable {
		t.Fatalf("the zombie: %v", zerr)
	}
	if f.rows.state(sess.ID) != StateOpen {
		t.Fatalf("the zombie ended the live session: %s", f.rows.state(sess.ID))
	}
	if _, err := f.append(other, sess.ID, 2*testPart+1, data[2*testPart+1:]); err != nil {
		t.Fatal(err)
	}
	if err := f.finalize(other, sess.ID, data, nil); err != nil {
		t.Fatal(err)
	}
	if !f.stored(data) {
		t.Fatal("the stored blob is not the file")
	}
}

// The request that would add the 33rd segment merges the recorded ones; if it lost its claim and another request completed the part
// meanwhile, the segments are gone and the live session must stay open.
func TestR_STOR_011_AMergeByARequestThatLostItsClaimMustNotAbortTheLiveSession(t *testing.T) {
	f := newFixture(t)
	f.cfg.Uploads.PartSize = 200
	f.obj.MinPartSize = 200
	f.cfg.Uploads.WriterLease = time.Second
	zombie, other := f.replica(), f.replica()
	data := random(450)
	sess, _ := zombie.Start(context.Background(), 3, f.sc)
	for i := 0; i < maxTailSegments; i++ {
		if _, err := f.append(other, sess.ID, int64(i), data[i:i+1]); err != nil {
			t.Fatal(err)
		}
	}
	reached, resume := make(chan struct{}), make(chan struct{})
	zombie.Fault = func(p string) error {
		if p == "append.claimed" {
			close(reached)
			<-resume
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := zombie.Append(context.Background(), sess.ID, f.sc, maxTailSegments, bytes.NewReader(data[maxTailSegments:maxTailSegments+1]), 0)
		done <- err
	}()
	<-reached
	f.rows.advance(2 * time.Second)
	if _, err := f.append(other, sess.ID, maxTailSegments, data[maxTailSegments:210]); err != nil { // completes the part
		t.Fatal(err)
	}
	close(resume)
	if zerr := <-done; errKind(zerr) != errs.RangeNotSatisfiable {
		t.Fatalf("the zombie: %v", zerr)
	}
	if f.rows.state(sess.ID) != StateOpen {
		t.Fatalf("the zombie's merge ended the live session: %s", f.rows.state(sess.ID))
	}
}

// Two finalizes of a blob without a full part and with several segments: the first finishes and cleans up while the second waits at the
// start of completing; the second then finds the segments gone and answers from the record.
func TestR_STOR_011_AnOverlappingFinalizeOfASmallBlobWithSeveralSegmentsAnswersTheStoredOne(t *testing.T) {
	f := newFixture(t)
	a, b := f.replica(), f.replica()
	data := random(9)
	sess, _ := a.Start(context.Background(), 3, f.sc)
	for i := 0; i < 3; i++ {
		if _, err := f.append(a, sess.ID, int64(i*3), data[i*3:i*3+3]); err != nil {
			t.Fatal(err)
		}
	}
	paused, resume := make(chan struct{}), make(chan struct{})
	b.Fault = func(p string) error {
		if p == "finalize.completing" {
			close(paused)
			<-resume
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- f.finalize(b, sess.ID, data, nil) }()
	<-paused
	if err := f.finalize(a, sess.ID, data, nil); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatalf("the overlapping finalize: %v", err)
	}
}

// A request that lost its claim creates no multipart upload: it checks the claim before the creation.
func TestR_STOR_011_ARequestThatLostItsClaimCreatesNoMultipartUpload(t *testing.T) {
	f := newFixture(t)
	f.cfg.Uploads.WriterLease = time.Second
	zombie, other := f.replica(), f.replica()
	sess, _ := zombie.Start(context.Background(), 3, f.sc)
	reached, resume := make(chan struct{}), make(chan struct{})
	zombie.Fault = func(p string) error {
		if p == "append.claimed" {
			close(reached)
			<-resume
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := f.append(zombie, sess.ID, 0, random(2*testPart)); done <- err }()
	<-reached
	f.rows.advance(2 * time.Second)
	if _, err := f.append(other, sess.ID, 0, random(3)); err != nil { // a tail only: no upload exists
		t.Fatal(err)
	}
	close(resume)
	if err := <-done; errKind(err) != errs.RangeNotSatisfiable {
		t.Fatalf("the zombie: %v", err)
	}
	if f.obj.OpenUploads() != 0 {
		t.Fatalf("%d multipart uploads created by a request without a claim", f.obj.OpenUploads())
	}
}
