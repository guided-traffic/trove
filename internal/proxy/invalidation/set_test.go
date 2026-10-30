package invalidation

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta"
)

type fakeChanges struct{ listeners []cluster.Listener }

func (f *fakeChanges) AddListener(l cluster.Listener) { f.listeners = append(f.listeners, l) }

// fakeRunner is never used: the unit tests replace the Set's loader.
type fakeRunner struct{ meta.Runner }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// at is the instant of a second since the epoch.
func at(sec int) time.Time { return time.Unix(int64(sec), 0) }

// newTestSet returns a Set whose loader serves the rows of db.
func newTestSet(t *testing.T, db *[]row) (*Set, *atomic.Int64) {
	t.Helper()
	s, err := NewSet(SetOptions{Runner: fakeRunner{}, Changes: &fakeChanges{}, RetryInterval: 10 * time.Millisecond, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	var loads atomic.Int64
	var mu sync.Mutex
	s.load = func(_ context.Context, repo int64) ([]row, error) {
		loads.Add(1)
		mu.Lock()
		defer mu.Unlock()
		var out []row
		for _, r := range *db {
			if repo == 0 || r.repoID == repo {
				out = append(out, r)
			}
		}
		return out, nil
	}
	return s, &loads
}

func us(sec int) int64 { return at(sec).UnixMicro() }

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		prefix, path string
		want         bool
	}{
		{"", "left-pad", true},
		{"", "@acme/util", true},
		{"@acme", "@acme/util", true},
		{"@acme", "@acme", true},
		{"@acme", "@acmecorp/util", false},
		{"@acme", "@acme-x", false},
		{"@acme/util", "@acme/util/-/util-1.0.0.tgz", true},
		{"@acme/util", "@acme/utility", false},
		{"left-pad", "left-pad", true},
		{"left", "left-pad", false},
		{"@acme/util/-", "@acme/util", false},
	} {
		t.Run("R-REPO-045 prefix "+c.prefix+" path "+c.path, func(t *testing.T) {
			if got := Covers(c.prefix, c.path); got != c.want {
				t.Errorf("Covers(%q, %q) = %v", c.prefix, c.path, got)
			}
		})
	}
}

func TestCheckPrefix(t *testing.T) {
	for _, p := range []string{"", "left-pad", "@acme", "@acme/util", "org/apache/maven", "ü/ö", strings.Repeat("a", MaxPrefixBytes)} {
		if err := CheckPrefix(p); err != nil {
			t.Errorf("R-REPO-045 CheckPrefix(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"/", "/a", "a/", "a//b", ".", "a/./b", "a/../b", "a\x00b", "a\nb", "a\x7fb", "\xff", strings.Repeat("a", MaxPrefixBytes+1)} {
		if err := CheckPrefix(p); err == nil {
			t.Errorf("R-REPO-045 CheckPrefix(%q) is accepted", p)
		}
	}
}

func TestMatcher(t *testing.T) {
	rows := []row{
		{id: 1, repoID: 7, prefix: "@acme", scope: ScopeMetadata, createdUs: us(100)},
		{id: 2, repoID: 7, prefix: "left-pad", scope: ScopeNegative, createdUs: us(200)},
		{id: 3, repoID: 7, prefix: "", scope: ScopeAll, createdUs: us(300)},
		{id: 4, repoID: 8, prefix: "", scope: ScopeAll, createdUs: us(900)},
	}
	s, _ := newTestSet(t, &rows)
	if err := s.reload(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	t.Run("R-REPO-045 a metadata entry validated before a covering metadata row is invalid, after it valid", func(t *testing.T) {
		if s.MetadataValid(7, "@acme/util", at(99)) {
			t.Error("validated before the row: valid")
		}
		// Row 3 (all, empty prefix, 300) is the newest covering row of every path of repo 7.
		if s.MetadataValid(7, "@acme/util", at(299)) {
			t.Error("validated before the whole-repository row: valid")
		}
		if !s.MetadataValid(7, "@acme/util", at(300)) || !s.MetadataValid(7, "@acme/util", at(301)) {
			t.Error("validated at or after the newest row: invalid")
		}
	})
	t.Run("R-REPO-045 @acme does not hit @acmecorp", func(t *testing.T) {
		own := []row{{id: 1, repoID: 1, prefix: "@acme", scope: ScopeAll, createdUs: us(100)}}
		s2, _ := newTestSet(t, &own)
		if err := s2.reload(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
		if s2.MetadataValid(1, "@acme/util", at(50)) || s2.NegativeValid(1, "@acme/util", at(50)) {
			t.Error("@acme/util is not covered")
		}
		if !s2.MetadataValid(1, "@acmecorp/util", at(50)) || !s2.NegativeValid(1, "@acmecorp/util", at(50)) {
			t.Error("@acmecorp/util is covered by @acme")
		}
	})
	t.Run("R-REPO-045 the scope decides which kind of entry a row affects", func(t *testing.T) {
		only := []row{
			{id: 1, repoID: 1, prefix: "a", scope: ScopeMetadata, createdUs: us(100)},
			{id: 2, repoID: 1, prefix: "b", scope: ScopeNegative, createdUs: us(100)},
			{id: 3, repoID: 1, prefix: "c", scope: ScopeAll, createdUs: us(100)},
		}
		s2, _ := newTestSet(t, &only)
		if err := s2.reload(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			path     string
			md, neg  bool
			describe string
		}{
			{"a", false, true, "metadata"}, {"b", true, false, "negative"}, {"c", false, false, "all"}, {"d", true, true, "none"},
		} {
			if got := s2.MetadataValid(1, c.path, at(10)); got != c.md {
				t.Errorf("%s: MetadataValid = %v", c.describe, got)
			}
			if got := s2.NegativeValid(1, c.path, at(10)); got != c.neg {
				t.Errorf("%s: NegativeValid = %v", c.describe, got)
			}
		}
	})
	t.Run("R-REPO-045 R-REPO-028 a negative entry counts only if sent at least 5 s after the row", func(t *testing.T) {
		one := []row{{id: 1, repoID: 1, prefix: "", scope: ScopeNegative, createdUs: us(1000)}}
		s2, _ := newTestSet(t, &one)
		if err := s2.reload(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			sent int
			want bool
		}{{990, false}, {1000, false}, {1004, false}, {1005, true}, {1500, true}} {
			if got := s2.NegativeValid(1, "x", at(c.sent)); got != c.want {
				t.Errorf("sent at %d: valid = %v; the row was created at 1000", c.sent, got)
			}
		}
	})
	t.Run("a repository without rows and an unknown one are valid", func(t *testing.T) {
		if !s.MetadataValid(99, "x", at(0)) || !s.NegativeValid(99, "x", at(0)) {
			t.Error("a repository without rows is invalid")
		}
	})
	t.Run("a repository is not affected by the rows of another", func(t *testing.T) {
		if s.MetadataValid(8, "x", at(899)) || !s.MetadataValid(8, "x", at(900)) {
			t.Error("repository 8 follows its own row only")
		}
	})
}

func TestSetApplyChange(t *testing.T) {
	var db []row
	s, loads := newTestSet(t, &db)
	if err := s.reload(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	base := loads.Load()
	add := Payload{Kind: KindAdd, Repo: 5, ID: 11, Prefix: EncodePrefix("@acme"), Scope: ScopeAll, CreatedUs: us(100)}
	apply := func(p Payload) {
		raw, _ := json.Marshal(p)
		s.ApplyChange(cluster.AreaCacheInvalidation, PayloadVersion, raw)
	}
	t.Run("R-OPS-021 a new row is in force at once, from its payload, without a query", func(t *testing.T) {
		apply(add)
		if s.MetadataValid(5, "@acme/util", at(50)) || s.NegativeValid(5, "@acme/util", at(50)) {
			t.Error("the row is not in force")
		}
		if loads.Load() != base {
			t.Error("applying a row read the database")
		}
	})
	t.Run("a row is applied once however often its change arrives", func(t *testing.T) {
		apply(add)
		apply(add)
		if s.Len() != 1 {
			t.Errorf("rows = %d", s.Len())
		}
	})
	t.Run("a change of another area and a virtual change leave the set alone", func(t *testing.T) {
		raw, _ := json.Marshal(Payload{Kind: KindAdd, Repo: 6, ID: 12, Prefix: EncodePrefix(""), Scope: ScopeAll, CreatedUs: us(1)})
		s.ApplyChange("claims", 1, raw)
		apply(Payload{Kind: KindVirtual, Repo: 9, Prefix: EncodePrefix("x"), Scope: ScopeAll})
		if s.Len() != 1 {
			t.Errorf("rows = %d", s.Len())
		}
	})
	t.Run("R-REPO-045 a payload the set cannot read queues a reload instead of being dropped", func(t *testing.T) {
		s.ApplyChange(cluster.AreaCacheInvalidation, 1, json.RawMessage(`{"k":"zzz"}`))
		s.ApplyChange(cluster.AreaCacheInvalidation, 1, json.RawMessage(`not json`))
		all, _ := s.takePending()
		if !all {
			t.Error("no full reload is pending")
		}
	})
}

func TestSetReload(t *testing.T) {
	ctx := context.Background()
	t.Run("R-REPO-045 a deletion reloads the repository and the rows leave memory only then", func(t *testing.T) {
		db := []row{
			{id: 1, repoID: 1, prefix: "a", scope: ScopeMetadata, createdUs: us(100)},
			{id: 2, repoID: 2, prefix: "a", scope: ScopeMetadata, createdUs: us(100)},
		}
		s, _ := newTestSet(t, &db)
		if err := s.reload(ctx, 0); err != nil {
			t.Fatal(err)
		}
		db = db[1:] // the fold task deleted the row of repository 1
		if s.MetadataValid(1, "a", at(50)) {
			t.Fatal("the row left memory before the reload")
		}
		raw, _ := json.Marshal(Payload{Kind: KindDelete, Repo: 1})
		s.ApplyChange(cluster.AreaCacheInvalidation, 1, raw)
		_, repos := s.takePending()
		if len(repos) != 1 || repos[0] != 1 {
			t.Fatalf("pending = %v", repos)
		}
		if err := s.reload(ctx, 1); err != nil {
			t.Fatal(err)
		}
		if !s.MetadataValid(1, "a", at(50)) {
			t.Error("the deleted row is still in force")
		}
		if s.MetadataValid(2, "a", at(50)) {
			t.Error("the reload of repository 1 dropped the row of repository 2")
		}
	})
	t.Run("a row applied while a reload runs is not lost", func(t *testing.T) {
		var db []row
		s, _ := newTestSet(t, &db)
		started, release := make(chan struct{}), make(chan struct{})
		inner := s.load
		s.load = func(ctx context.Context, repo int64) ([]row, error) {
			rows, err := inner(ctx, repo) // the query ran before the row committed
			close(started)
			<-release
			return rows, err
		}
		done := make(chan error, 1)
		go func() { done <- s.reload(ctx, 0) }()
		<-started
		raw, _ := json.Marshal(Payload{Kind: KindAdd, Repo: 3, ID: 30, Prefix: EncodePrefix(""), Scope: ScopeAll, CreatedUs: us(100)})
		s.ApplyChange(cluster.AreaCacheInvalidation, 1, raw)
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if s.MetadataValid(3, "x", at(50)) {
			t.Error("the row applied during the reload was lost")
		}
	})
	t.Run("FlushArea reloads every repository", func(t *testing.T) {
		db := []row{{id: 1, repoID: 1, prefix: "", scope: ScopeAll, createdUs: us(100)}}
		s, _ := newTestSet(t, &db)
		s.FlushArea(cluster.AreaCacheInvalidation, cluster.FlushChangeLogGap)
		if all, _ := s.takePending(); !all {
			t.Error("no full reload is pending")
		}
		s.FlushArea("claims", cluster.FlushChangeLogGap)
		if all, _ := s.takePending(); all {
			t.Error("a flush of another area queued a reload")
		}
	})
}

func TestSetLoop(t *testing.T) {
	t.Run("R-OPS-021 the loop reloads after a deletion and retries a failed reload", func(t *testing.T) {
		db := []row{{id: 1, repoID: 1, prefix: "", scope: ScopeAll, createdUs: us(100)}}
		s, _ := newTestSet(t, &db)
		var fail atomic.Bool
		inner := s.load
		s.load = func(ctx context.Context, repo int64) ([]row, error) {
			if fail.Load() {
				return nil, context.DeadlineExceeded
			}
			return inner(ctx, repo)
		}
		ch := &fakeChanges{}
		s.o.Changes = ch
		if err := s.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Stop(context.Background()) }()
		if len(ch.listeners) != 1 {
			t.Fatal("the set did not register as a listener")
		}
		if s.MetadataValid(1, "x", at(50)) {
			t.Fatal("the initial load did not install the row")
		}
		fail.Store(true)
		db = nil
		raw, _ := json.Marshal(Payload{Kind: KindDelete, Repo: 1})
		s.ApplyChange(cluster.AreaCacheInvalidation, 1, raw)
		time.Sleep(60 * time.Millisecond) // several failed attempts
		if s.MetadataValid(1, "x", at(50)) {
			t.Fatal("a failed reload dropped the row")
		}
		fail.Store(false)
		deadline := time.Now().Add(2 * time.Second)
		for s.Len() != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if s.Len() != 0 {
			t.Error("the retry did not reload")
		}
	})
	t.Run("a failed initial load stops the start", func(t *testing.T) {
		var db []row
		s, _ := newTestSet(t, &db)
		s.load = func(context.Context, int64) ([]row, error) { return nil, context.DeadlineExceeded }
		if err := s.Start(context.Background()); err == nil {
			t.Error("Start succeeded")
		}
	})
}

func TestPayloadFitsAChangeMessage(t *testing.T) {
	t.Run("R-ARCH-018 the longest prefix of the worst characters fits the 1 KiB limit", func(t *testing.T) {
		worst := strings.Repeat("<", MaxPrefixBytes)
		raw, err := json.Marshal(Payload{Kind: KindAdd, Repo: 1 << 62, ID: 1 << 62, Prefix: EncodePrefix(worst), Scope: ScopeMetadata, CreatedUs: 1 << 62})
		if err != nil {
			t.Fatal(err)
		}
		if limit := cluster.MaxValueSize - 96; len(raw) > limit {
			t.Errorf("payload is %d bytes; the limit is %d", len(raw), limit)
		}
		p, err := ParsePayload(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := p.DecodePrefix(); err != nil || got != worst {
			t.Errorf("prefix did not round-trip: %v", err)
		}
	})
}

// BenchmarkMatcher shows that a lookup costs the segments of the path, not the rows.
func BenchmarkMatcher(b *testing.B) {
	rows := make([]row, 0, 50000)
	for i := range 50000 {
		rows = append(rows, row{id: int64(i + 1), repoID: 1, prefix: "pkg" + strings.Repeat("x", i%40) + "/" + string(rune('a'+i%26)), scope: ScopeAll, createdUs: us(100 + i)})
	}
	by := map[int64]row{}
	for _, r := range rows {
		by[r.id] = r
	}
	s, _ := NewSet(SetOptions{Runner: fakeRunner{}, Changes: &fakeChanges{}})
	s.cur.Store(&snapshot{repos: map[int64]*repoData{1: buildRepo(by)}})
	b.ReportAllocs()
	for b.Loop() {
		s.MetadataValid(1, "@acme/util", at(1))
	}
}
