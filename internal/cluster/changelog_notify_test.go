package cluster

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
)

// fakeChangeSQL stands in for the two statements of Notify on a fakeTx: it
// records them and raises the epochs the test seeded.
type fakeChangeSQL struct{}

func (fakeChangeSQL) bumpEpoch(_ context.Context, tx meta.Tx, area string) (int64, error) {
	t := tx.(*fakeTx)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stmts = append(t.stmts, "BUMP "+area)
	t.epochs[area]++
	return t.epochs[area], nil
}

func (fakeChangeSQL) insertChange(_ context.Context, tx meta.Tx, area string, _ int64, _ string) error {
	t := tx.(*fakeTx)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stmts = append(t.stmts, "INSERT "+area)
	return nil
}

// attemptTx is a transaction whose attempts can be ended the way the runner
// ends them: committed, or rolled back.
type attemptTx struct {
	meta.TxState
	stmts []string
}

var registerAttemptArea sync.Once

func newAttemptChanges(t *testing.T) *Changes {
	t.Helper()
	registerAttemptArea.Do(func() {
		RegisterArea(Area{Name: "unit_attempt", Owner: "test", Class: ChangeArea, PayloadVersion: 1})
	})
	c, err := NewChanges(ChangesOptions{DB: nopDB{}, Config: config.Cluster{EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	c.sql = recordingChangeSQL{}
	return c
}

type recordingChangeSQL struct{}

func (recordingChangeSQL) bumpEpoch(_ context.Context, tx meta.Tx, area string) (int64, error) {
	t := tx.(*attemptTx)
	t.stmts = append(t.stmts, "BUMP "+area)
	return int64(len(t.stmts)), nil
}

func (recordingChangeSQL) insertChange(_ context.Context, tx meta.Tx, area string, _ int64, _ string) error {
	t := tx.(*attemptTx)
	t.stmts = append(t.stmts, "INSERT "+area)
	return nil
}

func TestNotifyAttempts(t *testing.T) {
	t.Run("R-ARCH-018 a long transaction keeps its change while many attempts around it roll back", func(t *testing.T) {
		// The case that lost a change: notes were kept in a process-wide map and
		// swept by age when it grew, so a transaction that ran for more than a
		// minute alongside 64 rolled-back attempts committed without its epoch
		// bump and its change row. Notes now belong to the attempt.
		c := newAttemptChanges(t)
		long := &attemptTx{}
		if err := c.Notify(long, "unit_attempt", 1, map[string]string{"flush": "x"}); err != nil {
			t.Fatal(err)
		}
		for i := range 200 {
			rolledBack := &attemptTx{}
			if err := c.Notify(rolledBack, "unit_attempt", 1, i); err != nil {
				t.Fatal(err)
			}
			rolledBack.RunRollback()
		}
		if err := c.Notify(&attemptTx{}, "unit_attempt", 1, "trigger"); err != nil {
			t.Fatal(err)
		}
		if err := long.RunBefore(context.Background(), long); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(long.stmts, ","); got != "BUMP unit_attempt,INSERT unit_attempt" {
			t.Fatalf("the committed transaction wrote %q before COMMIT", got)
		}
	})
	t.Run("R-ARCH-018 a retried attempt notifies again from an empty slot", func(t *testing.T) {
		c := newAttemptChanges(t)
		first := &attemptTx{}
		if err := c.Notify(first, "unit_attempt", 1, "a"); err != nil {
			t.Fatal(err)
		}
		first.RunRollback()
		second := &attemptTx{}
		if second.Value(notesKey{}) != nil {
			t.Fatal("a new attempt starts with a note slot")
		}
		if err := c.Notify(second, "unit_attempt", 1, "a"); err != nil {
			t.Fatal(err)
		}
		if err := second.RunBefore(context.Background(), second); err != nil {
			t.Fatal(err)
		}
		if len(second.stmts) != 2 {
			t.Fatalf("statements of the retry: %v", second.stmts)
		}
	})
	t.Run("R-ARCH-018 a failing statement ends the attempt with its error and writes no later change", func(t *testing.T) {
		c := newAttemptChanges(t)
		boom := errors.New("no cluster_epoch row")
		c.sql = failingChangeSQL{err: boom}
		tx := &attemptTx{}
		if err := c.Notify(tx, "unit_attempt", 1, "a"); err != nil {
			t.Fatal(err)
		}
		if err := tx.RunBefore(context.Background(), tx); !errors.Is(err, boom) {
			t.Fatalf("before hook: %v", err)
		}
	})
}

type failingChangeSQL struct{ err error }

func (f failingChangeSQL) bumpEpoch(context.Context, meta.Tx, string) (int64, error) { return 0, f.err }

func (f failingChangeSQL) insertChange(context.Context, meta.Tx, string, int64, string) error {
	return f.err
}
