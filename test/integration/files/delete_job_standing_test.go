//go:build integration

package files

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
)

// withdrawingHook takes a person from the test stage when the first batch of a removal reaches the storage hook: that batch's
// transaction commits, and the next batch of the job reads the standing of its subject again (Q-STOR-190).
type withdrawingHook struct {
	e    *env
	who  string
	once sync.Once
}

func (h *withdrawingHook) OnCommit(context.Context, meta.Tx, items.CommitEvent) error { return nil }

func (h *withdrawingHook) OnItemsRemoved(context.Context, meta.Tx, items.RemovedEvent) error {
	h.once.Do(func() {
		h.e.peopleMu.Lock()
		delete(h.e.people, h.who)
		h.e.peopleMu.Unlock()
	})
	return nil
}

// TestR_STOR_018_AWithdrawalBetweenTwoBatchesStopsTheJobAtTheNext: the delete job reads the standing of the subject that asked for
// every batch, so a principal withdrawn while the job runs stops it at the batch after the change; what the earlier batches removed
// stays removed, and the job fails without a further attempt (Q-STOR-190, Q-AUTHZ-260).
func TestR_STOR_018_AWithdrawalBetweenTwoBatchesStopsTheJobAtTheNext(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	small := func(c *config.Storage) { c.Delete.MaxSyncItems, c.Delete.BatchSize = 5, 2 }
	web := e.newReplica("web", replicaOpts{storage: small})
	web.createRepository("bulk", nil)
	e.user("deleter")
	e.grant(web, "bulk-del", []string{"bulk"}, authz.PatternLists{}, map[string][]string{"user:deleter": {"read", "delete"}})
	for i := range 9 {
		if res := web.put("admin", fmt.Sprintf("/files/it/bulk/tree/d%d/f%d.bin", i%3, i), fmt.Sprint(i)); res.status != http.StatusCreated {
			t.Fatalf("%d %s", res.status, res.body)
		}
	}
	waitFor(t, "the deleter's grant", 10*time.Second, func() bool {
		return web.get("deleter", "/files/it/bulk/tree/d0/f0.bin").status == http.StatusOK
	})
	if del := web.do("deleter", http.MethodDelete, "/files/it/bulk/tree", nil); del.status != http.StatusAccepted {
		t.Fatalf("%d %s", del.status, del.body)
	}
	_ = e.newReplica("worker", replicaOpts{worker: true, storage: small, hooks: &withdrawingHook{e: e, who: "deleter"}})
	waitFor(t, "the job to settle", 30*time.Second, func() bool {
		return e.number("SELECT count(*) FROM job WHERE kind = 'items-delete' AND state IN ('succeeded', 'failed', 'cancelled')") > 0
	})
	state := e.text("SELECT state::text || ' ' || attempt || ' ' || COALESCE(error::text, '') FROM job WHERE kind = 'items-delete'")
	if !strings.HasPrefix(state, "failed 1 ") || !strings.Contains(state, "no longer active") {
		t.Errorf("the job must fail at its first attempt naming the withdrawal: %s", state)
	}
	if left := e.number("SELECT count(*) FROM items WHERE path LIKE 'tree/%' AND kind = 'file'"); left != 7 {
		t.Errorf("%d files left, want the 7 the batches after the withdrawal did not reach", left)
	}
}
