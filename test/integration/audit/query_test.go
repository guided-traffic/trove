//go:build integration

package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
)

// seed writes 33 events: 12 transactions of one to five events, so that several
// events share a transaction's time, with a spread of actors, repositories and
// outcomes.
func seed(t *testing.T, e *env) (*audit.Store, *audit.Writer) {
	t.Helper()
	db := e.open()
	w, _, _ := e.writer(db, auditCfg(), time.Now)
	ctx := context.Background()
	n := 0
	for tx := range 12 {
		size := 1 + tx%5
		err := db.InTx(ctx, func(m meta.Tx) error {
			for range size {
				n++
				name := []string{"ann", "bob", "cy"}[n%3]
				ev := audit.Event{
					Type: "probe.thing.created", Actor: audit.Actor{Kind: "user", ID: fmt.Sprintf("5f0e1111-2222-4333-8444-5555666677%02d", n%3), Name: name},
					Subject: audit.Subject{Kind: "repository", Name: fmt.Sprintf("repo-%d", n%4)},
					Details: json.RawMessage(fmt.Sprintf(`{"n":%d}`, n)),
				}
				if n%5 == 0 {
					ev.Type, ev.Outcome, ev.Reason = "probe.thing.deleted", audit.Failure, "conflict"
				}
				if err := w.RecordTx(ctx, m, ev); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Millisecond)
	}
	return audit.NewStore(db), w
}

func pageAll(t *testing.T, s *audit.Store, f audit.Filter, limit int, oldest bool) []audit.Entry {
	t.Helper()
	var all []audit.Entry
	var after *audit.Cursor
	for range 100 {
		res, err := s.Query(context.Background(), f, audit.Page{Limit: limit, After: after, Oldest: oldest})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Items) > limit {
			t.Fatalf("a page of %d items for limit %d", len(res.Items), limit)
		}
		all = append(all, res.Items...)
		if res.Next == nil {
			return all
		}
		if len(res.Items) != limit {
			t.Fatalf("a short page (%d) has a next cursor", len(res.Items))
		}
		after = res.Next
	}
	t.Fatal("pagination does not end")
	return nil
}

func ids(es []audit.Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func TestR_OPS_017_QueryOrdersNewestFirstAndPagesByKeyset(t *testing.T) {
	e := newEnv(t)
	s, w := seed(t, e)
	ctx := context.Background()

	want := scalar[[]string](t, e.admin, "SELECT array_agg(id::text ORDER BY occurred_at DESC, id DESC) FROM trove.audit_event")
	if len(want) != 33 {
		t.Fatalf("seeded %d events", len(want))
	}
	t.Run("R-OPS-017 newest first, in pages that join without a repeat or a gap, across events of one time", func(t *testing.T) {
		for _, limit := range []int{1, 4, 7, 33, 100} {
			if got := ids(pageAll(t, s, audit.Filter{}, limit, false)); !slices.Equal(got, want) {
				t.Errorf("limit %d: pages differ from the full order:\n got %v\nwant %v", limit, got, want)
			}
		}
		// A transaction wrote several events at one instant: the page boundary must cross one.
		if n := scalar[int](t, e.admin, "SELECT max(c) FROM (SELECT count(*) c FROM trove.audit_event GROUP BY occurred_at) x"); n < 3 {
			t.Fatalf("no ties in the seed (max %d)", n)
		}
	})
	t.Run("R-OPS-017 oldest first for the export", func(t *testing.T) {
		rev := slices.Clone(want)
		slices.Reverse(rev)
		if got := ids(pageAll(t, s, audit.Filter{}, 8, true)); !slices.Equal(got, rev) {
			t.Errorf("oldest-first pages differ:\n got %v\nwant %v", got, rev)
		}
	})
	t.Run("R-OPS-017 an event that arrives between pages neither shifts nor repeats the pages", func(t *testing.T) {
		first, err := s.Query(ctx, audit.Filter{}, audit.Page{Limit: 10})
		if err != nil || first.Next == nil {
			t.Fatalf("page 1: %v, %v", first, err)
		}
		db := e.open()
		if err := db.InTx(ctx, func(m meta.Tx) error {
			return w.RecordTx(ctx, m, audit.Event{Type: "probe.thing.created", Actor: user("late")})
		}); err != nil {
			t.Fatal(err)
		}
		rest := pageRest(t, s, first.Next, 10)
		if got := append(ids(first.Items), ids(rest)...); !slices.Equal(got, want) {
			t.Errorf("pages across an insert differ from the order before it:\n got %v\nwant %v", got, want)
		}
	})
	t.Run("R-OPS-017 one event by id, and a clean not found", func(t *testing.T) {
		got, err := s.Get(ctx, want[7])
		if err != nil || got.ID != want[7] || got.Action == "" || got.Actor.Name == "" {
			t.Fatalf("Get = %+v, %v", got, err)
		}
		for _, id := range []string{"01933b1e-7c2a-7f4e-9d1b-6a2f0c9e8b11", "not-an-id", "5f0e1111-2222-4333-8444-555566667777"} {
			if _, err := s.Get(ctx, id); errs.KindOf(err) != errs.NotFound {
				t.Errorf("Get(%s) = %v, want not found", id, err)
			}
		}
	})
}

func pageRest(t *testing.T, s *audit.Store, after *audit.Cursor, limit int) []audit.Entry {
	t.Helper()
	var all []audit.Entry
	for after != nil {
		res, err := s.Query(context.Background(), audit.Filter{}, audit.Page{Limit: limit, After: after})
		if err != nil {
			t.Fatal(err)
		}
		all, after = append(all, res.Items...), res.Next
	}
	return all
}

func TestR_OPS_017_QueryFilters(t *testing.T) {
	e := newEnv(t)
	s, _ := seed(t, e)

	expect := func(t *testing.T, f audit.Filter, where string) {
		t.Helper()
		want := scalar[[]string](t, e.admin, "SELECT coalesce(array_agg(id::text ORDER BY occurred_at DESC, id DESC), '{}') FROM trove.audit_event WHERE "+where)
		if len(want) == 0 {
			t.Fatalf("the filter %q matches nothing in the seed", where)
		}
		if got := ids(pageAll(t, s, f, 6, false)); !slices.Equal(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", where, got, want)
		}
	}
	t.Run("R-OPS-017 action exact", func(t *testing.T) {
		expect(t, audit.Filter{Action: "probe.thing.deleted"}, "action = 'probe.thing.deleted'")
	})
	t.Run("R-OPS-017 action prefix ending in .*", func(t *testing.T) {
		expect(t, audit.Filter{Action: "probe.thing.*"}, "action LIKE 'probe.thing.%'")
		if res, _ := s.Query(context.Background(), audit.Filter{Action: "probe.*"}, audit.Page{}); len(res.Items) != 33 {
			t.Errorf("probe.* = %d", len(res.Items))
		}
		if res, _ := s.Query(context.Background(), audit.Filter{Action: "probe.thi.*"}, audit.Page{}); len(res.Items) != 0 {
			t.Errorf("a prefix must end at a segment: %d", len(res.Items))
		}
	})
	t.Run("R-OPS-017 category and outcome", func(t *testing.T) {
		expect(t, audit.Filter{Category: "repository"}, "category = 'repository'")
		expect(t, audit.Filter{Outcome: audit.Failure}, "outcome = 'failure'")
	})
	t.Run("R-OPS-017 actor by name and by id", func(t *testing.T) {
		expect(t, audit.Filter{Actor: "bob"}, "actor_name = 'bob'")
		id := scalar[string](t, e.admin, "SELECT actor_id::text FROM trove.audit_event WHERE actor_name = 'cy' LIMIT 1")
		expect(t, audit.Filter{Actor: id}, "actor_id = '"+id+"'")
	})
	t.Run("R-OPS-017 repository, resource type and id", func(t *testing.T) {
		expect(t, audit.Filter{Repository: "repo-2"}, "repository = 'repo-2'")
		expect(t, audit.Filter{ResourceType: "repository", ResourceID: "repo-1"}, "resource_type = 'repository' AND resource_id = 'repo-1'")
	})
	t.Run("R-OPS-017 the time range is inclusive at the start and exclusive at the end", func(t *testing.T) {
		mid := scalar[time.Time](t, e.admin, "SELECT occurred_at FROM trove.audit_event ORDER BY occurred_at, id OFFSET 15 LIMIT 1")
		expect(t, audit.Filter{From: mid}, "occurred_at >= '"+mid.Format(time.RFC3339Nano)+"'")
		expect(t, audit.Filter{To: mid}, "occurred_at < '"+mid.Format(time.RFC3339Nano)+"'")
		res, err := s.Query(context.Background(), audit.Filter{From: mid.Add(24 * time.Hour)}, audit.Page{})
		if err != nil || len(res.Items) != 0 {
			t.Errorf("a range after every event: %d, %v", len(res.Items), err)
		}
	})
	t.Run("R-OPS-017 a range that ends before it starts is refused with its code", func(t *testing.T) {
		now := time.Now()
		_, err := s.Query(context.Background(), audit.Filter{From: now, To: now.Add(-time.Hour)}, audit.Page{})
		var ee *errs.Error
		if !errors.As(err, &ee) || ee.Code != errs.CodeInvalidAuditRange {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("R-OPS-017 the limit is capped at 1000", func(t *testing.T) {
		res, err := s.Query(context.Background(), audit.Filter{}, audit.Page{Limit: 100000})
		if err != nil || len(res.Items) != 33 || res.Next != nil {
			t.Errorf("%d items, next %v, %v", len(res.Items), res.Next, err)
		}
	})
}

func TestR_OPS_017_QueryReadsEventsOfEarlierMonths(t *testing.T) {
	e := newEnv(t)
	s := audit.NewStore(e.open())
	mustExec(t, e.admin, "SELECT trove.audit_create_partitions('2026-01-01Z', '2026-01-31Z')")
	mustExec(t, e.admin, `INSERT INTO trove.audit_event (id, occurred_at, action, category, outcome, actor_type)
		VALUES ('018c1111-2222-7333-8444-555566667777', '2026-01-15T10:00:00Z', 'probe.thing.created', 'system', 'success', 'user')`)
	res, err := s.Query(context.Background(), audit.Filter{
		From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}, audit.Page{})
	if err != nil || len(res.Items) != 1 || res.Items[0].OccurredAt.Day() != 15 {
		t.Fatalf("items %v, err %v", res.Items, err)
	}
}
