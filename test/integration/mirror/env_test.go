//go:build integration

// Package mirror tests the mirror engine (repositories-and-proxy.md R-REPO-050 to R-REPO-059, docs/developer/mirror.md)
// against the integration stack: PostgreSQL and in-process replicas started through the composition root, so the real job
// framework, the real dispatch task, the real state tables and the real repository snapshot run. The remote engine's side of
// the seam (mirror.Host) is a fake that counts what it was asked to store; the counting stubs and the real formats join in the
// next wave.
package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	pathpkg "path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/app"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const repoKey = "npm-mirror"

// settings are the members of remote.mirror a test sets; the others keep their defaults.
type settings map[string]any

// remoteDoc is the canonical document of a remote repository in mode mirror: every member present, as the repository service
// stores it.
func remoteDoc(mode string, include []string, mirrorSettings settings) string {
	m := map[string]any{"concurrency": 1}
	for k, v := range mirrorSettings {
		m[k] = v
	}
	if include == nil {
		include = []string{}
	}
	doc := map[string]any{"includePatterns": include, "excludePatterns": []string{}, "remote": map[string]any{
		"url": "https://registry.example.org/", "extraHosts": []any{}, "authMode": "none", "username": "", "password": nil,
		"clientCertificate": nil, "headers": []any{}, "forwardAuthOnRedirect": false, "outboundProxy": map[string]any{"mode": "direct"},
		"connectTimeoutMs": 5000, "readTimeoutMs": 15000, "sourceNetwork": nil, "cacheFillAction": "read",
		"cache": map[string]any{
			"storeArtifacts": true, "metadataTtlSeconds": 600, "metadataRefreshTimeoutSeconds": 10, "notFoundTtlSeconds": 60,
			"maxStaleSeconds": 0, "unusedRetentionHours": 0,
		},
		"integrity":    map[string]any{"checksumPolicy": "verify-if-available", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
		"availability": map[string]any{"mode": "online", "failureThreshold": 3, "circuitOpenSeconds": 30},
		"limits":       map[string]any{"maxArtifactBytes": nil, "maxMetadataBytes": nil, "maxConcurrentFetches": 16},
		"mode":         mode, "mirror": m,
	}}
	b, _ := json.Marshal(doc)
	return string(b)
}

// world is the upstream of the fake host: a fixed set of packages with one artifact of 100 bytes each, which counts what the
// engine stored and lets a test hold a fill back. It is shared by every replica of a test.
type world struct {
	names []string
	size  int64

	mu     sync.Mutex
	stored map[string]int
	// gate is called before an artifact is stored; it may block until ctx ends.
	gate func(ctx context.Context, path string) error
	// persist, when set, writes the stored artifact as a file item with its blob into the repository, as the remote engine's
	// commit does, so that the usage triggers and the fold count it (R-STOR-003). A test that sets it does so before it starts a
	// replica.
	persist func(ctx context.Context, path string, size int64) error

	namesCalls atomic.Int64
	planCalls  atomic.Int64
	// discoveryErr is the error MirrorNames returns while set.
	discoveryErr atomic.Pointer[error]
}

func newWorld(n int) *world {
	w := &world{size: 100, stored: map[string]int{}}
	for i := range n {
		w.names = append(w.names, fmt.Sprintf("pkg-%03d", i))
	}
	return w
}

func pathOf(name string) string { return name + "/-/1.0.0.tgz" }

func (w *world) Ready(context.Context) error { return nil }

func (w *world) Lister(string) (discover.Lister, bool) { return w, true }

func (w *world) Admits(context.Context, repository.Repository) (func(string) bool, error) {
	return func(string) bool { return true }, nil
}

func (w *world) Upstream(repository.Repository, mirror.Pass) (discover.Upstream, error) {
	return nil, nil //nolint:nilnil // the fake lister does not use it
}

func (w *world) MirrorMethods(context.Context, discover.Upstream) ([]discover.Method, error) {
	return []discover.Method{discover.MethodFeed}, nil
}

func (w *world) MirrorNames(_ context.Context, _ discover.Upstream, _ discover.PageRequest) (discover.Page, error) {
	w.namesCalls.Add(1)
	if e := w.discoveryErr.Load(); e != nil {
		return discover.Page{}, *e
	}
	return discover.Page{Names: append([]string(nil), w.names...), Done: true, Cursor: json.RawMessage(`{"seq":1}`)}, nil
}

func (w *world) MirrorPlan(_ context.Context, _ discover.Upstream, name string) (discover.Plan, error) {
	w.planCalls.Add(1)
	return discover.Plan{Name: name, Items: []discover.Item{{Version: "1.0.0", Path: pathOf(name), Size: w.size}}}, nil
}

func (w *world) MirrorHosts(*url.URL) []string { return nil }

func (w *world) Fill(ctx context.Context, _ repository.Repository, _ mirror.Pass, item discover.Item) (mirror.FillResult, error) {
	w.mu.Lock()
	have := w.stored[item.Path] > 0
	gate := w.gate
	w.mu.Unlock()
	if have {
		return mirror.FillResult{Present: true}, nil
	}
	if gate != nil {
		if err := gate(ctx, item.Path); err != nil {
			return mirror.FillResult{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return mirror.FillResult{}, err
	}
	if w.persist != nil {
		if err := w.persist(ctx, item.Path, w.size); err != nil {
			return mirror.FillResult{}, err
		}
	}
	w.mu.Lock()
	w.stored[item.Path]++
	w.mu.Unlock()
	return mirror.FillResult{Bytes: w.size}, nil
}

func (w *world) storedCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.stored)
}

func (w *world) times(path string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stored[path]
}

func (w *world) setGate(g func(ctx context.Context, path string) error) {
	w.mu.Lock()
	w.gate = g
	w.mu.Unlock()
}

// env is one installation with its repository and its replicas.
type env struct {
	t     *testing.T
	st    *testenv.Stack
	w     *world
	db    *postgres.DB
	store *mirror.PGStore
	repoN int64
}

// fastJobs makes the job framework quick: short leases, a quick reaper and polling.
var fastJobs = []string{
	"TROVE_JOBS_POLL_INTERVAL=200ms", "TROVE_JOBS_LEASE_DURATION=3s", "TROVE_JOBS_HEARTBEAT_INTERVAL=1s", "TROVE_JOBS_SCHEDULE_TICK=1s",
}

// newEnv creates an installation with one mirrored remote repository.
func newEnv(t *testing.T, w *world, mode string, include []string, s settings) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "mirror"})
	st.Repository(repoKey, "remote", "npm", remoteDoc(mode, include, s))
	db, err := postgres.Open(context.Background(), st.DatabaseConfig(), "mirror-it", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	e := &env{t: t, st: st, w: w, db: db, store: mirror.NewPGStore(db)}
	e.repoN = st.Number(`SELECT id FROM repositories WHERE key = $1`, repoKey)
	return e
}

// persistFiles makes the world write every artifact it stores into the repository as a file item over a live blob.
func (e *env) persistFiles() {
	e.w.persist = func(ctx context.Context, path string, size int64) error {
		sum := sha256.Sum256([]byte(path))
		return e.db.InPgxTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO blobs (sha256, sha1, md5, sha512, size, state) VALUES ($1, $2, $3, $4, $5, 'live') ON CONFLICT DO NOTHING`,
				sum[:], make([]byte, 20), make([]byte, 16), make([]byte, 64), size); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, $2, $3, 'file', $4, $5)`,
				e.repoN, path, pathpkg.Dir(path), sum[:], size)
			return err
		})
	}
}

// start starts a replica of a role whose mirror engine uses the world as its host.
func (e *env) start(name, role string, extra ...string) *testenv.Replica {
	e.t.Helper()
	return e.st.Start(e.t, testenv.ReplicaOptions{
		Name: name, Role: role, Env: append(append([]string{}, fastJobs...), extra...),
		Mirror: &app.MirrorOverride{Host: e.w},
	})
}

// setDocument replaces the repository's document and tells the replicas, as the repository service does.
func (e *env) setDocument(rep *testenv.Replica, doc string) {
	e.t.Helper()
	err := e.db.InTx(context.Background(), func(tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return fmt.Errorf("no pgx transaction")
		}
		if _, err := ptx.Exec(context.Background(), `UPDATE repositories SET config = $1::text::jsonb, updated_at = now() WHERE id = $2`, doc, e.repoN); err != nil {
			return err
		}
		payload := map[string]any{"id": fmt.Sprint(e.repoN), "key": repoKey, "op": "update", "version": 1}
		return rep.App.Changes().Notify(tx, "repo_config", 1, payload)
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// dispatch enqueues the dispatch task's job, as the scheduler does every minute.
func (e *env) dispatch(rep *testenv.Replica) {
	e.t.Helper()
	if _, err := rep.App.Queue().EnqueueNow(context.Background(), jobs.EnqueueSpec{Kind: mirror.KindDispatch}); err != nil {
		e.t.Fatal(err)
	}
}

// makeDue makes the next pass due now.
func (e *env) makeDue() {
	e.t.Helper()
	e.st.Exec(`UPDATE mirror_state SET next_pass_at = now() - interval '1 second', rate_limited_until = NULL WHERE repo_id = $1`, e.repoN)
}

func (e *env) pending() []string {
	var out []string
	e.st.Query(`SELECT name FROM mirror_pending WHERE repo_id = $1 ORDER BY name`, func(r pgx.Rows) error {
		var n string
		if err := r.Scan(&n); err != nil {
			return err
		}
		out = append(out, n)
		return nil
	}, e.repoN)
	sort.Strings(out)
	return out
}

func (e *env) text(sql string) string { return e.st.Text(sql, e.repoN) }

// waitOutcome waits until the state's last pass has the outcome.
func (e *env) waitOutcome(outcome string, within time.Duration) {
	e.t.Helper()
	testenv.WaitFor(e.t, "the pass to end "+outcome, within, func() bool {
		n, err := e.st.TryNumber(`SELECT count(*) FROM mirror_state WHERE repo_id = $1 AND last_pass->>'outcome' = $2`, e.repoN, outcome)
		return err == nil && n == 1
	})
}

// waitFills waits until n distinct artifacts are stored.
func (e *env) waitFills(n int, within time.Duration) {
	e.t.Helper()
	testenv.WaitFor(e.t, fmt.Sprintf("%d artifacts to be stored", n), within, func() bool { return e.w.storedCount() >= n })
}

// activePasses counts the unfinished mirror-pass jobs.
func (e *env) activePasses() int64 {
	return e.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass' AND state IN ('queued', 'running')`)
}

func (e *env) jobResult(state string) map[string]any {
	e.t.Helper()
	raw := e.st.Text(`SELECT COALESCE((SELECT result::text FROM job WHERE kind = 'mirror-pass' AND state = $1::job_state ORDER BY created_at DESC LIMIT 1), '{}')`, state)
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) auditCount(typ string) int64 {
	return e.st.Number(`SELECT count(*) FROM audit_event WHERE action = $1 AND repository = $2`, typ, repoKey)
}

// waitAudit waits until the asynchronous writer has written n events of the type (it writes once a second), then lets one more
// tick pass so that an event that should not exist has had its chance to appear, and returns the count.
func (e *env) waitAudit(typ string, n int64) int64 {
	e.t.Helper()
	testenv.WaitFor(e.t, fmt.Sprintf("%d %s events", n, typ), 20*time.Second, func() bool {
		c, err := e.st.TryNumber(`SELECT count(*) FROM audit_event WHERE action = $1 AND repository = $2`, typ, repoKey)
		return err == nil && c >= n
	})
	time.Sleep(1500 * time.Millisecond)
	return e.auditCount(typ)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// waitMaxBytes waits until the replica's repository snapshot holds the bound.
func (e *env) waitMaxBytes(rep *testenv.Replica, want int64) {
	e.t.Helper()
	e.waitMirror(rep, fmt.Sprintf("maxBytes %d", want), func(s repository.RemoteSettings) bool {
		return s.Mirror.MaxBytes != nil && *s.Mirror.MaxBytes == want
	})
}

// waitMirror waits until the replica's repository snapshot satisfies cond.
func (e *env) waitMirror(rep *testenv.Replica, what string, cond func(s repository.RemoteSettings) bool) {
	e.t.Helper()
	testenv.WaitFor(e.t, "the snapshot to hold "+what, 30*time.Second, func() bool {
		r, err := rep.App.Repositories().ByName(context.Background(), testenvName(repoKey))
		if err != nil {
			return false
		}
		s, ok := r.Remote()
		return ok && cond(s)
	})
}

// testenvName is the name of a repository key in the default project of the stack.
func testenvName(key string) qname.Repo { return qname.Repo{Project: testenv.DefaultProject, Key: key} }
