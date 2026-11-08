//go:build integration

package uploads

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// A start through a virtual repository locks its deployment target and needs a key share on the virtual row for its insert; the
// configuration update of the virtual repository locks the virtual row and then its members. Both take the virtual row first, so the two
// never wait for each other in a cycle (Q-STOR-176).
func TestR_STOR_011_AStartThroughAVirtualRepositoryDoesNotDeadlockWithItsConfigurationUpdate(t *testing.T) {
	w := newWorld(t)
	r := newReplica(t, w.e, w.p, func(c *config.Storage) { c.Uploads.MaxOpenSessions = 10000 })
	ctx := context.Background()
	virtual := w.p.scalarInt(t, `INSERT INTO trove.repositories (key, format, class, project_id) SELECT 'docker-virtual', 'generic', 'virtual', id FROM trove.project WHERE key = $1 RETURNING id`, "it")
	sc := w.sc
	sc.Addressed = virtual

	deadlocks := func() int64 {
		return w.p.scalarInt(t, `SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`)
	}
	before := deadlocks()

	const rounds = 60
	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	for range rounds {
		wg.Add(2)
		go func() { // the configuration update: the virtual row for update, then the members for share
			defer wg.Done()
			errs <- w.p.db.InTx(ctx, func(tx meta.Tx) error {
				ptx, _ := postgres.Unwrap(tx)
				if _, err := ptx.Exec(ctx, `SELECT 1 FROM repositories WHERE id = $1 FOR UPDATE`, virtual); err != nil {
					return err
				}
				time.Sleep(3 * time.Millisecond)
				_, err := ptx.Exec(ctx, `SELECT 1 FROM repositories WHERE id = $1 FOR SHARE`, w.p.repo)
				return err
			})
		}()
		go func() { // a start through the virtual repository, writing to its deployment target
			defer wg.Done()
			_, err := r.svc.Start(ctx, w.p.repo, sc)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(3 * time.Second) // the statistics of the database are flushed about once a second
	if got := deadlocks() - before; got != 0 {
		t.Fatalf("%d deadlocks between the starts and the configuration updates", got)
	}
}
