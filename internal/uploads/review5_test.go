package uploads

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// The abort reads the session, a request records the first part (and with it the multipart upload) and the abort follows: the upload that
// the abort removes is the one recorded at the abort, not the empty id the row had when it was read.
func TestR_STOR_011_AnAbortThatRacesTheFirstPartAbortsTheUploadThePartCreated(t *testing.T) {
	f := newFixture(t)
	aborter, writer := f.replica(), f.replica()
	sess, _ := aborter.Start(context.Background(), 3, f.sc)
	reached, resume := make(chan struct{}), make(chan struct{})
	aborter.Fault = func(p string) error {
		if p == "abort.read" {
			close(reached)
			<-resume
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- aborter.Abort(context.Background(), sess.ID, f.sc) }()
	<-reached
	if _, err := f.append(writer, sess.ID, 0, random(2*testPart+3)); err != nil { // records parts, and with them an upload
		t.Fatal(err)
	}
	if f.obj.OpenUploads() != 1 {
		t.Fatalf("%d uploads before the abort", f.obj.OpenUploads())
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.nothingStaged()
	if _, err := f.append(writer, sess.ID, 2*testPart+3, random(1)); errKind(err) != errs.NotFound {
		t.Fatalf("append after the abort: %v", err)
	}
}

// Two finalizes of a small blob with several segments: the second puts the merged object after the first cleaned up, and removes it again.
func TestR_STOR_011_AContinuationThatEndsOnTheRecordedItemCleansUpWhatItPut(t *testing.T) {
	f := newFixture(t)
	a, b := f.replica(), f.replica()
	data := random(9)
	sess, _ := a.Start(context.Background(), 3, f.sc)
	for i := 0; i < 3; i++ {
		if _, err := f.append(a, sess.ID, int64(i*3), data[i*3:i*3+3]); err != nil {
			t.Fatal(err)
		}
	}
	// b reads the row (completing), then a finishes and cleans up, then b merges and puts "data" and finds the session done
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
		t.Fatal(err)
	}
	f.nothingStaged()
}
