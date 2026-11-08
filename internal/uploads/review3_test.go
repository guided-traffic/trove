package uploads

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestR_STOR_011_AStartAboveTheCapOfOpenSessionsIsRefused(t *testing.T) {
	f := newFixture(t)
	f.cfg.Uploads.MaxOpenSessions = 2
	s := f.replica()
	ctx := context.Background()
	first, err := s.Start(ctx, 3, f.sc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, 3, f.sc); err != nil {
		t.Fatal(err)
	}
	_, err = s.Start(ctx, 3, f.sc)
	var e *errs.Error
	if errKind(err) != errs.RateLimited || !errors.As(err, &e) || e.RetryAfter == 0 {
		t.Fatalf("the third start: %v", err)
	}
	// the cap is per principal and per repository
	other := f.sc
	other.Principal = uuid.NewString()
	if _, err := s.Start(ctx, 3, other); err != nil {
		t.Fatalf("another principal: %v", err)
	}
	if _, err := s.Start(ctx, 4, f.sc); err != nil {
		t.Fatalf("another repository: %v", err)
	}
	// a session that ends frees its place
	if err := s.Abort(ctx, first.ID, f.sc); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, 3, f.sc); err != nil {
		t.Fatalf("after an abort: %v", err)
	}
}

// A request that is still going when its session passes the maximum age loses the renewal that refuses it and answers 404, not 416.
func TestR_STOR_011_ARenewalRefusesASessionPastItsMaximumAge(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	sess, _ := s.Start(context.Background(), 3, f.sc)
	reached, resume := make(chan struct{}), make(chan struct{})
	s.Fault = func(p string) error {
		if p == "append.claimed" {
			close(reached)
			<-resume
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := f.append(s, sess.ID, 0, random(2*testPart)); done <- err }()
	<-reached
	f.rows.advance(f.cfg.Uploads.SessionMaxAge + time.Minute)
	close(resume)
	if err := <-done; errKind(err) != errs.NotFound {
		t.Fatalf("err = %v", err)
	}
	if f.obj.OpenUploads() != 0 {
		t.Fatal("a multipart upload was created for a session past its age")
	}
}
