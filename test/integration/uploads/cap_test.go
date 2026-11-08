//go:build integration

package uploads

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/uploads"
)

// The cap on the open sessions of a principal on a repository is held by the insert itself (Q-STOR-176).
func TestR_STOR_011_AStartAboveTheCapIsRefusedByTheDatabase(t *testing.T) {
	w := newWorld(t)
	r := newReplica(t, w.e, w.p, func(c *config.Storage) { c.Uploads.MaxOpenSessions = 2 })
	ctx := context.Background()
	var first uploads.Session
	for i := range 2 {
		s, err := r.svc.Start(ctx, w.p.repo, w.sc)
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if i == 0 {
			first = s
		}
	}
	if _, err := r.svc.Start(ctx, w.p.repo, w.sc); kind(err) != errs.RateLimited {
		t.Fatalf("the third start: %v", err)
	}
	other := w.sc
	other.Principal = uuid.NewString()
	if _, err := r.svc.Start(ctx, w.p.repo, other); err != nil {
		t.Fatalf("another principal: %v", err)
	}
	anon := w.sc
	anon.Principal = ""
	for i := range 2 {
		if _, err := r.svc.Start(ctx, w.p.repo, anon); err != nil {
			t.Fatalf("anonymous start %d: %v", i, err)
		}
	}
	if _, err := r.svc.Start(ctx, w.p.repo, anon); kind(err) != errs.RateLimited {
		t.Fatalf("the third anonymous start: %v", err)
	}
	if err := r.svc.Abort(ctx, first.ID, w.sc); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Start(ctx, w.p.repo, w.sc); err != nil {
		t.Fatalf("after an abort: %v", err)
	}
}

// Starts that race do not exceed the cap: the lock of the repository row makes them one after the other, and the count of the one behind
// sees the session of the one before.
func TestR_STOR_011_ConcurrentStartsNeverExceedTheCapOfOpenSessions(t *testing.T) {
	w := newWorld(t)
	const cap = 5
	r := newReplica(t, w.e, w.p, func(c *config.Storage) { c.Uploads.MaxOpenSessions = cap })
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([]error, 30)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = r.svc.Start(ctx, w.p.repo, w.sc)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case kind(err) != errs.RateLimited:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != cap {
		t.Fatalf("%d starts succeeded, want exactly %d", ok, cap)
	}
	if n := w.p.count(t, "upload_sessions WHERE state = 'open'"); n != cap {
		t.Fatalf("%d open rows", n)
	}
}

// The count of a start and the listing of the cleaning are served by their indexes at the size where it matters.
func TestR_STOR_011_TheCapCountAndTheCleaningListingUseTheirIndexes(t *testing.T) {
	w := newWorld(t)
	w.p.exec(t, `INSERT INTO trove.upload_sessions (id, repo_id, addressed_repo_id, principal_id, hash_state, part_size, s3_upload_id, state, received, last_activity_at)
		SELECT gen_random_uuid(), CASE WHEN g % 2 = 0 THEN $1::bigint ELSE $2::bigint END, $1::bigint,
		       CASE WHEN g % 7 = 0 THEN NULL ELSE ('00000000-0000-4000-8000-' || lpad((g % 400)::text, 12, '0'))::uuid END,
		       '\x00', 16777216, '', (ARRAY['open','completing','assembled','done','aborted'])[1 + g % 5], 1 + g % 3, now() - (g || ' seconds')::interval
		FROM generate_series(1, 60000) g`, w.p.repo, w.p.other)
	w.p.exec(t, "ANALYZE trove.upload_sessions")
	plan := func(sql string, args ...any) string {
		t.Helper()
		rows, err := w.p.admin.Query(context.Background(), "EXPLAIN "+sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			b.WriteString(line + "\n")
		}
		return b.String()
	}
	const zero = `'00000000-0000-0000-0000-000000000000'::uuid`
	for name, principal := range map[string]string{"a principal": `'00000000-0000-4000-8000-000000000007'::uuid`, "none": `NULL::uuid`} {
		p := plan(`SELECT count(*) FROM trove.upload_sessions o WHERE o.repo_id = $1 AND COALESCE(o.principal_id, `+zero+`) = COALESCE(`+principal+`, `+zero+`)
			AND o.state IN ('open', 'completing', 'assembled')`, w.p.repo)
		if !strings.Contains(p, "upload_sessions_open_per_principal") {
			t.Errorf("the count for %s does not use its index:\n%s", name, p)
		}
	}
	for _, state := range []string{"aborted", "done"} {
		p := plan(`SELECT id FROM trove.upload_sessions WHERE state = '` + state + `' AND NOT cleaned AND received > 0 ORDER BY last_activity_at LIMIT 200`)
		if !strings.Contains(p, "upload_sessions_cleanup") || strings.Contains(p, "Sort") {
			t.Errorf("the listing of the %s sessions does not read its index in order:\n%s", state, p)
		}
	}
}
