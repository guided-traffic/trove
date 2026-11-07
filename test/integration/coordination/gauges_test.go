//go:build integration

package coordination

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// TestJobQueueGauges runs the query behind trove_jobs_queued and trove_jobs_oldest_queued_age_seconds against PostgreSQL.
func TestJobQueueGauges(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rows := func() map[string]q.JobQueueGaugesRow {
		got, err := q.New(e.admin).JobQueueGauges(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]q.JobQueueGaugesRow{}
		for _, r := range got {
			out[r.Kind] = r
		}
		return out
	}

	t.Run("R-OPS-007 an empty queue has no row", func(t *testing.T) {
		if got := rows(); len(got) != 0 {
			t.Fatalf("rows = %v", got)
		}
	})

	e.exec(`INSERT INTO job (id, kind, run_at) VALUES
		('00000000-0000-7000-8000-0000000000b1', 'it.old',   now() - interval '20 minutes'),
		('00000000-0000-7000-8000-0000000000b2', 'it.old',   now() - interval '5 minutes'),
		('00000000-0000-7000-8000-0000000000b3', 'it.later', now() + interval '1 hour')`)
	e.exec(`INSERT INTO job (id, kind, state, run_at, started_at, locked_by, locked_until) VALUES
		('00000000-0000-7000-8000-0000000000b4', 'it.running', 'running', now() - interval '3 hours', now(), '00000000-0000-7000-8000-0000000000c1', now() + interval '1 minute')`)

	t.Run("R-OPS-007 the count and the age of the oldest due job per kind", func(t *testing.T) {
		got := rows()
		old, ok := got["it.old"]
		if !ok || old.Queued != 2 || old.OldestAgeSeconds < 1200 || old.OldestAgeSeconds > 1260 {
			t.Fatalf("it.old = %+v (present %v), want 2 queued and an oldest age of about 1200 s", old, ok)
		}
	})
	t.Run("R-OPS-007 a job that is not due yet is queued but has no age", func(t *testing.T) {
		later, ok := rows()["it.later"]
		if !ok || later.Queued != 1 || later.OldestAgeSeconds != 0 {
			t.Fatalf("it.later = %+v (present %v), want 1 queued and age 0", later, ok)
		}
	})
	t.Run("R-OPS-007 a running job is not part of the queue", func(t *testing.T) {
		if r, ok := rows()["it.running"]; ok {
			t.Fatalf("it.running = %+v", r)
		}
	})
}

// TestValkeyPrimaryViews runs the query behind trove_valkey_primary_views against PostgreSQL.
func TestValkeyPrimaryViews(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	views := func() int64 {
		n, err := q.New(e.admin).ValkeyPrimaryViews(ctx, 30)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	add := func(uid, name, valkey string, heartbeatAgo string) {
		e.exec(`INSERT INTO replica (instance_uid, name, version, revision, roles, state, secret_key_ids, active_secret_key_id, valkey, started_at, heartbeat_at)
			VALUES ($1::uuid, $2, '0.1.0', 'r', '{api}', 'ready', '{k1}', 'k1', $3::jsonb, now(), now() - $4::interval)`, uid, name, valkey, heartbeatAgo)
	}

	t.Run("R-OPS-007 no replica names no primary", func(t *testing.T) {
		if n := views(); n != 0 {
			t.Fatalf("views = %d", n)
		}
	})

	add("00000000-0000-7000-8000-0000000000d1", "a", `{"primary":"10.0.0.1:6379"}`, "1 second")
	add("00000000-0000-7000-8000-0000000000d2", "b", `{"primary":"10.0.0.1:6379"}`, "1 second")
	add("00000000-0000-7000-8000-0000000000d3", "c", `{"primary":null}`, "1 second")
	add("00000000-0000-7000-8000-0000000000d4", "d", `{}`, "1 second")

	t.Run("R-OPS-007 replicas that name the same primary are one view, replicas that name none count for nothing", func(t *testing.T) {
		if n := views(); n != 1 {
			t.Fatalf("views = %d, want 1", n)
		}
	})

	add("00000000-0000-7000-8000-0000000000d5", "e", `{"primary":"10.0.0.2:6379"}`, "1 second")

	t.Run("R-OPS-007 a replica bound to another primary makes two views", func(t *testing.T) {
		if n := views(); n != 2 {
			t.Fatalf("views = %d, want 2", n)
		}
	})

	e.exec(`UPDATE replica SET heartbeat_at = now() - interval '5 minutes' WHERE name = 'e'`)

	t.Run("R-OPS-007 a replica that stopped heartbeating no longer counts", func(t *testing.T) {
		if n := views(); n != 1 {
			t.Fatalf("views = %d, want 1", n)
		}
	})
}
