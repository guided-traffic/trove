package items

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// failingRunner refuses every transaction: PostgreSQL is away.
type failingRunner struct{ calls int }

func (f *failingRunner) InTx(context.Context, func(meta.Tx) error) error {
	f.calls++
	return errors.New("database unavailable")
}

func TestStatsBufferKeepsWhatItCouldNotWrite(t *testing.T) {
	run := &failingRunner{}
	s := NewStats(run, time.Hour, nil)
	t.Run("R-STOR-015 an empty buffer writes nothing", func(t *testing.T) {
		if err := s.Flush(context.Background()); err != nil || run.calls != 0 {
			t.Fatalf("err %v, %d transactions", err, run.calls)
		}
	})
	t.Run("R-STOR-015 counts are aggregated per item in memory", func(t *testing.T) {
		s.Add(7, "user-a")
		s.Add(7, "user-b")
		s.Add(3, "")
		s.mu.Lock()
		defer s.mu.Unlock()
		if e := s.pending[7]; e == nil || e.n != 2 || e.who != "user-b" {
			t.Fatalf("pending[7] = %+v", e)
		}
		if len(s.pending) != 2 {
			t.Fatalf("pending = %v", s.pending)
		}
	})
	t.Run("R-STOR-015 a batch that fails stays in the buffer, merged with what arrived meanwhile", func(t *testing.T) {
		if err := s.Flush(context.Background()); err == nil {
			t.Fatal("the flush did not fail")
		}
		s.Add(7, "user-c")
		if err := s.Flush(context.Background()); err == nil {
			t.Fatal("the flush did not fail")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if e := s.pending[7]; e == nil || e.n != 3 || e.who != "user-c" {
			t.Fatalf("pending[7] = %+v: counts were lost", e)
		}
		if e := s.pending[3]; e == nil || e.n != 1 {
			t.Fatalf("pending[3] = %+v", e)
		}
	})
	t.Run("R-STOR-015 a nil buffer counts nothing", func(t *testing.T) {
		var none *Stats
		none.Add(1, "x") // must not panic
	})
}

func TestActivityBufferKeepsWhatItCouldNotWrite(t *testing.T) {
	run := &failingRunner{}
	a := repository.NewActivity(run, time.Hour, nil)
	a.Touch("12")
	a.Touch("12")
	a.Touch("")
	if err := a.Flush(context.Background()); err == nil || run.calls != 1 {
		t.Fatalf("err %v, %d transactions", err, run.calls)
	}
	if err := a.Flush(context.Background()); err == nil || run.calls != 2 {
		t.Fatalf("the activity of repository 12 was dropped by the failed flush: %v, %d transactions", err, run.calls)
	}
	var none *repository.Activity
	none.Touch("1") // must not panic
}
