package uploads

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestR_STOR_011_AnAbortThatRemovedTheObjectsDeletesTheRowAndOneThatDidNotLeavesItForTheRun(t *testing.T) {
	ctx := context.Background()
	t.Run("the objects are removed", func(t *testing.T) {
		f := newFixture(t)
		s := f.replica()
		sess, _ := s.Start(ctx, 3, f.sc)
		if _, err := f.append(s, sess.ID, 0, random(40)); err != nil {
			t.Fatal(err)
		}
		if err := s.Abort(ctx, sess.ID, f.sc); err != nil {
			t.Fatal(err)
		}
		if got := f.rows.state(sess.ID); got != stateGone {
			t.Fatalf("state = %q, want the row gone", got)
		}
		f.nothingStaged()
		// a start and a cancel repeated by one principal leaves no row behind
		for range 150 {
			x, err := s.Start(ctx, 3, f.sc)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Abort(ctx, x.ID, f.sc); err != nil {
				t.Fatal(err)
			}
		}
		if n := len(f.rows.sessions); n != 0 {
			t.Fatalf("%d rows left", n)
		}
	})
	t.Run("the store fails", func(t *testing.T) {
		f := newFixture(t)
		s := f.replica()
		sess, _ := s.Start(ctx, 3, f.sc)
		if _, err := f.append(s, sess.ID, 0, random(40)); err != nil {
			t.Fatal(err)
		}
		f.obj.SetFault(func(op, _ string) error {
			if op == "list" {
				return errors.New("store unavailable")
			}
			return nil
		})
		if err := s.Abort(ctx, sess.ID, f.sc); err != nil {
			t.Fatal(err)
		}
		if got := f.rows.state(sess.ID); got != StateAborted {
			t.Fatalf("state = %q, want the aborted row kept for the collection run", got)
		}
		if _, err := s.Get(ctx, sess.ID, f.sc); errKind(err) != errs.NotFound {
			t.Fatalf("an aborted session is still found: %v", err)
		}
	})
}

func TestR_STOR_011_AFinishedSessionIsMarkedCleanedWhenItsRequestRemovedTheObjects(t *testing.T) {
	f := newFixture(t)
	s := f.replica()
	data := random(30)
	sess, _ := s.Start(context.Background(), 3, f.sc)
	if err := f.finalize(s, sess.ID, data, data); err != nil {
		t.Fatal(err)
	}
	if got := f.rows.state(sess.ID); got != StateDone || !f.rows.sessions[sess.ID].cleaned {
		t.Fatalf("state %q, cleaned %v", got, f.rows.sessions[sess.ID].cleaned)
	}
}
