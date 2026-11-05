//go:build integration

package blob

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
)

// warnCounter counts the records at warn level or above, the level the fold loop reports a failed
// iteration at.
type warnCounter struct {
	n    *atomic.Int64
	msgs *sync.Map
}

func (w warnCounter) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }
func (w warnCounter) Handle(_ context.Context, r slog.Record) error {
	w.n.Add(1)
	w.msgs.Store(r.Message, true)
	r.Attrs(func(a slog.Attr) bool { w.msgs.Store(a.Value.String(), true); return true })
	return nil
}
func (w warnCounter) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w warnCounter) WithGroup(string) slog.Handler      { return w }

func newFolder(tb testing.TB, p *pgEnv, interval time.Duration) (*blob.UsageFolder, *atomic.Int64) {
	f, w, _ := newFolderMsgs(tb, p, interval)
	return f, w
}

func newFolderMsgs(tb testing.TB, p *pgEnv, interval time.Duration) (*blob.UsageFolder, *atomic.Int64, *sync.Map) {
	tb.Helper()
	warns := &atomic.Int64{}
	msgs := &sync.Map{}
	f, err := blob.NewUsageFolder(blob.UsageFolderOptions{
		DB: p.db, Interval: interval, WatchdogTimeout: 30 * time.Second, CallTimeout: 10 * time.Second,
		Log: slog.New(warnCounter{n: warns, msgs: msgs}),
	})
	if err != nil {
		tb.Fatal(err)
	}
	return f, warns, msgs
}

// foldAll folds until the backlog is empty, as the loop does.
func foldAll(tb testing.TB, f *blob.UsageFolder) (rows, iterations int) {
	tb.Helper()
	for {
		n, err := f.Fold(context.Background())
		if err != nil {
			tb.Fatalf("fold: %v", err)
		}
		iterations++
		rows += n
		if n < blob.UsageFoldBatch {
			return rows, iterations
		}
	}
}

type counters struct{ logical, files, folders, physical, blobs int64 }

// exact is the reader of R-STOR-003 that needs exact values: the folded rows and the unfolded deltas in one
// statement, so one snapshot covers both.
func (p *pgEnv) exact(tb testing.TB, repo int64) counters {
	tb.Helper()
	var c counters
	err := p.admin.QueryRow(context.Background(), `
		SELECT (SELECT coalesce(sum(logical_bytes), 0) FROM (SELECT logical_bytes FROM repository_usage WHERE repo_id = $1
		                                                      UNION ALL SELECT logical_bytes FROM storage_usage_deltas WHERE repo_id = $1) x),
		       (SELECT coalesce(sum(files), 0) FROM (SELECT files FROM repository_usage WHERE repo_id = $1
		                                              UNION ALL SELECT files FROM storage_usage_deltas WHERE repo_id = $1) x),
		       (SELECT coalesce(sum(folders), 0) FROM (SELECT folders FROM repository_usage WHERE repo_id = $1
		                                                UNION ALL SELECT folders FROM storage_usage_deltas WHERE repo_id = $1) x),
		       (SELECT physical_bytes + coalesce((SELECT sum(physical_bytes) FROM storage_usage_deltas), 0) FROM storage_usage_totals),
		       (SELECT blobs + coalesce((SELECT sum(blobs) FROM storage_usage_deltas), 0) FROM storage_usage_totals)`, repo).
		Scan(&c.logical, &c.files, &c.folders, &c.physical, &c.blobs)
	if err != nil {
		tb.Fatal(err)
	}
	return c
}

// folded is what the counter rows hold alone.
func (p *pgEnv) folded(tb testing.TB, repo int64) counters {
	tb.Helper()
	var c counters
	err := p.admin.QueryRow(context.Background(), `
		SELECT coalesce((SELECT logical_bytes FROM repository_usage WHERE repo_id = $1), 0),
		       coalesce((SELECT files FROM repository_usage WHERE repo_id = $1), 0),
		       coalesce((SELECT folders FROM repository_usage WHERE repo_id = $1), 0),
		       (SELECT physical_bytes FROM storage_usage_totals), (SELECT blobs FROM storage_usage_totals)`, repo).
		Scan(&c.logical, &c.files, &c.folders, &c.physical, &c.blobs)
	if err != nil {
		tb.Fatal(err)
	}
	return c
}

// truth recounts the repository from items and blobs.
func (p *pgEnv) truth(tb testing.TB, repo int64) counters {
	tb.Helper()
	var c counters
	err := p.admin.QueryRow(context.Background(), `
		SELECT coalesce(sum(size) FILTER (WHERE kind = 'file'), 0), count(*) FILTER (WHERE kind = 'file'), count(*) FILTER (WHERE kind = 'folder'),
		       (SELECT coalesce(sum(size), 0) FROM blobs WHERE state = 'live'), (SELECT count(*) FROM blobs WHERE state = 'live')
		  FROM items WHERE repo_id = $1 AND trashed_at IS NULL`, repo).Scan(&c.logical, &c.files, &c.folders, &c.physical, &c.blobs)
	if err != nil {
		tb.Fatal(err)
	}
	return c
}

func (p *pgEnv) deltas(tb testing.TB) int64 { return p.count(tb, "storage_usage_deltas") }

func (p *pgEnv) deadlocks(tb testing.TB) int64 {
	tb.Helper()
	return p.scalarInt(tb, `SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`)
}

func (p *pgEnv) newRepo(tb testing.TB, key string) int64 {
	tb.Helper()
	return p.scalarInt(tb, `INSERT INTO trove.repositories (key, format, class) VALUES ($1, 'generic', 'local') RETURNING id`, key)
}

// seedDeltas appends n synthetic delta rows spread over the repositories: one in eleven is a physical delta
// (repo_id NULL, a blob of 100 bytes), the others are files of 1 to 13 bytes, every seventh with a folder.
func (p *pgEnv) seedDeltas(tb testing.TB, n int, repos []int64) {
	tb.Helper()
	p.exec(tb, `INSERT INTO storage_usage_deltas (repo_id, logical_bytes, files, folders, physical_bytes, blobs)
		SELECT CASE WHEN g % 11 = 0 THEN NULL ELSE ($2::bigint[])[1 + g % cardinality($2::bigint[])] END,
		       CASE WHEN g % 11 = 0 THEN 0 ELSE 1 + g % 13 END,
		       CASE WHEN g % 11 = 0 THEN 0 ELSE 1 END,
		       CASE WHEN g % 7 = 0 AND g % 11 <> 0 THEN 1 ELSE 0 END,
		       CASE WHEN g % 11 = 0 THEN 100 ELSE 0 END,
		       CASE WHEN g % 11 = 0 THEN 1 ELSE 0 END
		  FROM generate_series(1, $1) g`, n, repos)
}

func TestUsageFold(t *testing.T) {
	ctx := context.Background()

	t.Run("R-STOR-003 the counters follow uploads, overwrites and deletes of a generic local repository", func(t *testing.T) {
		e := newS3(t, "usage-follow")
		p := newPG(t)
		r := newReplica(t, e, p, nil)
		f, _ := newFolder(t, p, time.Hour)

		check := func(step string) {
			t.Helper()
			foldAll(t, f)
			if n := p.deltas(t); n != 0 {
				t.Fatalf("%s: %d deltas left after the fold", step, n)
			}
			if got, want := p.folded(t, p.repo), p.truth(t, p.repo); got != want {
				t.Fatalf("%s: counters %+v, recount of items and blobs %+v", step, got, want)
			}
		}

		a, b := randomBytes(1000), randomBytes(2500)
		if _, _, err := r.upload(ctx, "a.bin", a); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.upload(ctx, "b.bin", b); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.upload(ctx, "copy-of-a.bin", a); err != nil { // same blob, a second reference
			t.Fatal(err)
		}
		check("after three uploads")
		if got := p.folded(t, p.repo); got.logical != 4500 || got.files != 3 || got.physical != 3500 || got.blobs != 2 {
			t.Fatalf("after three uploads: %+v, want logical 4500, 3 files, physical 3500, 2 blobs", got)
		}

		// An overwrite in place: a.bin gets the content of a third upload.
		c := randomBytes(4000)
		_, d, err := r.upload(ctx, "tmp.bin", c)
		if err != nil {
			t.Fatal(err)
		}
		p.exec(t, `UPDATE items SET blob_sha256 = $1, size = $2 WHERE path = 'a.bin'`, d.Bytes(), int64(len(c)))
		p.exec(t, `DELETE FROM items WHERE path = 'tmp.bin'`)
		check("after the overwrite")
		if got := p.folded(t, p.repo); got.logical != 4000+2500+1000 || got.files != 3 {
			t.Fatalf("after the overwrite: %+v", got)
		}

		p.exec(t, `DELETE FROM items WHERE path = 'b.bin'`)
		check("after a delete")
		if got := p.folded(t, p.repo); got.files != 2 || got.logical != 5000 {
			t.Fatalf("after a delete: %+v", got)
		}
		// The exact reader (folded rows plus unfolded deltas) agrees with the folded rows once nothing is unfolded.
		if got, want := p.exact(t, p.repo), p.folded(t, p.repo); got != want {
			t.Fatalf("exact %+v, folded %+v", got, want)
		}
	})

	t.Run("R-STOR-003 two folders on one backlog count every delta exactly once and never deadlock", func(t *testing.T) {
		p := newPG(t)
		repos := []int64{p.repo}
		for i := range 6 {
			repos = append(repos, p.newRepo(t, fmt.Sprintf("generic-%d", i)))
		}
		const n = 50000
		p.seedDeltas(t, n, repos)
		want := map[int64]counters{}
		for _, id := range repos {
			var c counters
			if err := p.admin.QueryRow(ctx, `SELECT coalesce(sum(logical_bytes),0), coalesce(sum(files),0), coalesce(sum(folders),0)
				FROM storage_usage_deltas WHERE repo_id = $1`, id).Scan(&c.logical, &c.files, &c.folders); err != nil {
				t.Fatal(err)
			}
			want[id] = c
		}
		var wantPhysical, wantBlobs int64
		if err := p.admin.QueryRow(ctx, `SELECT coalesce(sum(physical_bytes),0), coalesce(sum(blobs),0) FROM storage_usage_deltas WHERE repo_id IS NULL`).
			Scan(&wantPhysical, &wantBlobs); err != nil {
			t.Fatal(err)
		}
		if p.deltas(t) != n || wantPhysical == 0 {
			t.Fatalf("seeded %d deltas, physical %d", p.deltas(t), wantPhysical)
		}
		deadlocksBefore := p.deadlocks(t)

		f1, warns1 := newFolder(t, p, 10*time.Millisecond)
		f2, warns2 := newFolder(t, p, 10*time.Millisecond)
		start := time.Now()
		for _, f := range []*blob.UsageFolder{f1, f2} {
			if err := f.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer f.Stop(ctx) //nolint:errcheck // the stop of a loop that ends with the test
		}
		deadline := time.Now().Add(2 * time.Minute)
		for p.deltas(t) != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("%d deltas left after two minutes", p.deltas(t))
			}
			time.Sleep(20 * time.Millisecond)
		}
		took := time.Since(start)
		// A last iteration may still be committing its counters: wait for the totals instead of sleeping.
		for time.Now().Before(deadline) && p.folded(t, p.repo).physical != wantPhysical {
			time.Sleep(20 * time.Millisecond)
		}

		for _, id := range repos {
			got := p.folded(t, id)
			if got.logical != want[id].logical || got.files != want[id].files || got.folders != want[id].folders {
				t.Errorf("repository %d: counters %+v, the deltas summed to %+v", id, got, want[id])
			}
		}
		if got := p.folded(t, p.repo); got.physical != wantPhysical || got.blobs != wantBlobs {
			t.Errorf("physical totals %d bytes in %d blobs, the deltas summed to %d in %d", got.physical, got.blobs, wantPhysical, wantBlobs)
		}
		if d := p.deadlocks(t) - deadlocksBefore; d != 0 {
			t.Errorf("PostgreSQL reports %d deadlocks (the retry of a transaction would hide them)", d)
		}
		if warns1.Load()+warns2.Load() != 0 {
			t.Errorf("%d failed iterations", warns1.Load()+warns2.Load())
		}
		t.Logf("%d deltas over %d repositories folded by two loops in %v: no delta lost or counted twice, 0 deadlocks, 0 failed iterations", n, len(repos), took.Round(time.Millisecond))
	})

	t.Run("R-STOR-003 four concurrent folds of one backlog take disjoint rows and match the sums", func(t *testing.T) {
		p := newPG(t)
		repos := []int64{p.repo, p.newRepo(t, "generic-b"), p.newRepo(t, "generic-c")}
		const n = 40000
		p.seedDeltas(t, n, repos)
		var wantLogical, wantFiles int64
		if err := p.admin.QueryRow(ctx, `SELECT coalesce(sum(logical_bytes),0), coalesce(sum(files),0) FROM storage_usage_deltas WHERE repo_id IS NOT NULL`).
			Scan(&wantLogical, &wantFiles); err != nil {
			t.Fatal(err)
		}
		before := p.deadlocks(t)
		f, _ := newFolder(t, p, time.Hour)
		var wg sync.WaitGroup
		var total, iterations atomic.Int64
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rows, its := foldAll(t, f)
				total.Add(int64(rows))
				iterations.Add(int64(its))
			}()
		}
		wg.Wait()
		// A folder that saw no free rows returned at once; the others took every row between them, each exactly once.
		if total.Load() != n || p.deltas(t) != 0 {
			t.Fatalf("the folds took %d rows of %d, %d left", total.Load(), n, p.deltas(t))
		}
		var logical, files int64
		if err := p.admin.QueryRow(ctx, `SELECT sum(logical_bytes), sum(files) FROM repository_usage`).Scan(&logical, &files); err != nil {
			t.Fatal(err)
		}
		if logical != wantLogical || files != wantFiles {
			t.Fatalf("counters %d bytes in %d files, the deltas summed to %d in %d", logical, files, wantLogical, wantFiles)
		}
		if d := p.deadlocks(t) - before; d != 0 {
			t.Fatalf("%d deadlocks", d)
		}
		t.Logf("%d rows folded in %d iterations by four callers", total.Load(), iterations.Load())
	})

	t.Run("R-STOR-003 a repository deleted with unfolded deltas does not break later iterations", func(t *testing.T) {
		p := newPG(t)
		doomed := p.newRepo(t, "generic-doomed")
		p.seedDeltas(t, 2000, []int64{p.repo, doomed})
		p.exec(t, `DELETE FROM repositories WHERE id = $1`, doomed)
		var wantLive, wantPhysical int64
		if err := p.admin.QueryRow(ctx, `SELECT coalesce(sum(logical_bytes) FILTER (WHERE repo_id = $1), 0), coalesce(sum(physical_bytes), 0)
			FROM storage_usage_deltas`, p.repo).Scan(&wantLive, &wantPhysical); err != nil {
			t.Fatal(err)
		}
		f, _ := newFolder(t, p, time.Hour)
		foldAll(t, f)
		if p.deltas(t) != 0 {
			t.Fatal("deltas left")
		}
		if n := p.count(t, "repository_usage WHERE repo_id = $1", doomed); n != 0 {
			t.Fatal("the deleted repository has a counter row")
		}
		got := p.folded(t, p.repo)
		if got.logical != wantLive || got.physical != wantPhysical {
			t.Fatalf("counters %+v, want logical %d physical %d (the live repository and the physical deltas are unaffected)", got, wantLive, wantPhysical)
		}
		// Later iterations keep working.
		p.seedDeltas(t, 100, []int64{p.repo})
		if rows, _ := foldAll(t, f); rows != 100 {
			t.Fatalf("a later iteration folded %d rows, want 100", rows)
		}
	})

	t.Run("R-STOR-003 a backlog above 10,000 rows is folded in consecutive iterations without waiting for the interval", func(t *testing.T) {
		p := newPG(t)
		const n = 25000
		p.seedDeltas(t, n, []int64{p.repo})
		// An interval of an hour: only the rule that a full batch is followed at once by the next iteration can empty
		// the table within the test.
		f, warns, msgs := newFolderMsgs(t, p, time.Hour)
		start := time.Now()
		if err := f.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer f.Stop(ctx) //nolint:errcheck // the stop of a loop that ends with the test
		deadline := time.Now().Add(time.Minute)
		for p.deltas(t) != 0 || p.folded(t, p.repo).physical == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("%d deltas left: the loop waited for its interval; failures %d: %v", p.deltas(t), warns.Load(), keys(msgs))
			}
			time.Sleep(10 * time.Millisecond)
		}
		if warns.Load() != 0 {
			t.Fatalf("%d failed iterations", warns.Load())
		}
		t.Logf("%d deltas folded in %v by one loop with an interval of one hour", n, time.Since(start).Round(time.Millisecond))
	})
}

func keys(m *sync.Map) []string {
	var out []string
	m.Range(func(k, _ any) bool { out = append(out, fmt.Sprint(k)); return true })
	return out
}
