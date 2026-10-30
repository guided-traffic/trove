//go:build integration

package files

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_STOR_017_ADeployHoldsEveryAncestorOfItsPath holds the exclusive lock of a
// folder two levels above the new file and removes the chain under it, as a
// folder delete does: the deploy waits for the lock (it holds every ancestor FOR
// SHARE, not only the parent), finds the folders gone, creates them again and
// commits a file with all its folders present (R-STOR-017).
func TestR_STOR_017_ADeployHoldsEveryAncestorOfItsPath(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	r := e.newReplica("a", replicaOpts{})
	r.createRepository("generic-up", nil)
	if res := r.put("admin", "/files/generic-up/a/b/c/old.bin", "old"); res.status != 201 {
		t.Fatalf("%d %s", res.status, res.body)
	}

	ctx := context.Background()
	cfg, err := pgx.ParseConfig(testenv.PgDSN(t, e.db))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT id FROM items WHERE path = 'a' AND kind = 'folder' FOR UPDATE"); err != nil {
		t.Fatal(err)
	}

	done := make(chan response, 1)
	go func() { done <- r.put("admin", "/files/generic-up/a/b/c/new.bin", "new") }()
	waitFor(t, "the deploy waiting for the lock of the ancestor", 10*time.Second, func() bool {
		select {
		case res := <-done:
			t.Fatalf("the deploy finished while the ancestor was locked: %d %s", res.status, res.body)
		default:
		}
		return e.number("SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'") > 0
	})
	if _, err := tx.Exec(ctx, "DELETE FROM items WHERE path IN ('a/b/c/old.bin', 'a/b/c', 'a/b', 'a')"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res.status != 201 {
		t.Fatalf("%d %s", res.status, res.body)
	}
	if n := e.number("SELECT count(*) FROM items WHERE kind = 'folder' AND path IN ('a', 'a/b', 'a/b/c') AND trashed_at IS NULL"); n != 3 {
		t.Fatalf("%d of the 3 ancestors exist: the file sits under missing folders", n)
	}
	if n := e.number("SELECT count(*) FROM items WHERE path = 'a/b/c/new.bin' AND kind = 'file'"); n != 1 {
		t.Fatalf("%d files", n)
	}
}

// TestR_STOR_018_ADeleteJobRemovesOnlyWhatWasAuthorized enqueues the delete of a
// subtree above the synchronous limit while no worker runs, adds a file to the
// subtree, and then lets a worker run the job: the file that was never decided
// stays (R-STOR-018).
func TestR_STOR_018_ADeleteJobRemovesOnlyWhatWasAuthorized(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	small := func(c *config.Storage) { c.Delete.MaxSyncItems, c.Delete.BatchSize = 5, 4 }
	web := e.newReplica("web", replicaOpts{storage: small})
	web.createRepository("bulk", nil)
	for i := range 8 {
		if res := web.put("admin", fmt.Sprintf("/files/bulk/tree/d%d/f%d.bin", i%2, i), fmt.Sprint(i)); res.status != 201 {
			t.Fatalf("%d %s", res.status, res.body)
		}
	}
	if del := web.do("admin", "DELETE", "/files/bulk/tree", nil); del.status != 202 {
		t.Fatalf("%d %s", del.status, del.body)
	}
	if res := web.put("admin", "/files/bulk/tree/d0/late.bin", "late"); res.status != 201 {
		t.Fatalf("%d %s", res.status, res.body)
	}
	_ = e.newReplica("worker", replicaOpts{worker: true, storage: small})
	waitFor(t, "the job to settle", 30*time.Second, func() bool {
		return e.number("SELECT count(*) FROM job WHERE kind = 'items-delete' AND state IN ('succeeded', 'failed', 'cancelled')") > 0
	})
	if n := e.number("SELECT count(*) FROM items WHERE path = 'tree/d0/late.bin'"); n != 1 {
		t.Fatalf("the job removed a file that the permission check never saw (%d left)", n)
	}
	if n := e.number("SELECT count(*) FROM items WHERE path LIKE 'tree/%' AND kind = 'file'"); n != 1 {
		t.Fatalf("%d files left below the folder, want only the late one", n)
	}
	if n := e.number("SELECT count(*) FROM items WHERE path IN ('tree', 'tree/d0') AND kind = 'folder'"); n != 2 {
		t.Fatalf("the folders of the file that stays were removed (%d of 2 left)", n)
	}
}

// TestR_STOR_018_DeletingInAVirtualRepositoryIsRefused answers 405 as a deploy
// does (R-STOR-018, R-STOR-007). The generic format has no virtual class, so the
// service reads the repository as one.
func TestR_STOR_018_DeletingInAVirtualRepositoryIsRefused(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	r := e.newReplica("a", replicaOpts{})
	r.createRepository("generic-v", nil)
	svc := r.filesOn(r.blobs, fixedConfig{Reader: r.reader, class: repository.Virtual})
	caller := r.caller(authz.Subject{Kind: principal.KindBreakGlass, ID: uuidV4(), Name: "admin"}, "generic-v")
	_, err := svc.Delete(context.Background(), caller, files.DeleteRequest{Repo: "generic-v", Path: "x/y.bin"})
	ee, ok := errs.As(err)
	if !ok || ee.Kind != errs.MethodNotAllowed {
		t.Fatalf("err = %v, want 405", err)
	}
}

// fixedConfig serves repositories whose stored configuration is replaced, as a
// document that predates the validation rule would be.
type fixedConfig struct {
	repository.Reader
	cfg   string
	class repository.Class
}

func (f fixedConfig) ByKey(ctx context.Context, key string) (repository.Repository, error) {
	repo, err := f.Reader.ByKey(ctx, key)
	if f.cfg != "" {
		repo.Config = []byte(f.cfg)
	}
	if f.class != "" {
		repo.Class = f.class
	}
	return repo, err
}

// TestR_STOR_004_AnUndecodableUploadLimitRefusesTheUpload: a stored limit that is
// not a non-negative integer must not read as "no limit".
func TestR_STOR_004_AnUndecodableUploadLimitRefusesTheUpload(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	r := e.newReplica("a", replicaOpts{})
	r.createRepository("generic-lim", nil)
	// The management API refuses the value in the first place.
	res := r.postJSON("admin", "/api/v1/repositories", json.RawMessage(
		`{"key":"generic-bad","class":"local","format":"generic","storage":{"maxUploadSize":1e9}}`))
	if res.status != 422 || !strings.Contains(res.text(), "/storage/maxUploadSize") {
		t.Fatalf("create with maxUploadSize 1e9: %d %s", res.status, res.body)
	}
	for _, cfg := range []string{`{"storage":{"maxUploadSize":1e9}}`, `{"storage":{"maxUploadSize":-1}}`} {
		svc := r.filesOn(r.blobs, fixedConfig{Reader: r.reader, cfg: cfg})
		caller := r.caller(authz.Subject{Kind: principal.KindBreakGlass, ID: uuidV4(), Name: "admin"}, "generic-lim")
		_, err := svc.Deploy(context.Background(), caller, files.DeployRequest{
			Repo: "generic-lim", Path: repository.Cleaned{Path: "x.bin"}, Body: strings.NewReader("data"), ContentLength: 4,
		})
		if ee, ok := errs.As(err); !ok || ee.Kind != errs.Internal {
			t.Fatalf("config %s: err = %v, want a refusal", cfg, err)
		}
	}
	if n := e.number("SELECT count(*) FROM items WHERE path = 'x.bin'"); n != 0 {
		t.Fatalf("%d items were written", n)
	}
}

// commitProbe wraps the blob store and the holder of one request's decisions: before
// the commit transaction starts it lets a concurrent upload create the path, and
// it counts the decisions that are asked while the link step runs.
type commitProbe struct {
	*blob.Store
	before   func()
	inCommit atomic.Bool
	inside   atomic.Int64
}

func (p *commitProbe) Commit(ctx context.Context, sp *blob.Spooled, link blob.LinkFunc) (blob.CommitResult, error) {
	if p.before != nil {
		p.before()
	}
	return p.Store.Commit(ctx, sp, func(ctx context.Context, tx meta.Tx) error {
		p.inCommit.Store(true)
		defer p.inCommit.Store(false)
		return link(ctx, tx)
	})
}

// probeAccess counts the decisions of its holder that are asked inside the
// commit transaction. RecordAllowed is not one: it is the write the transaction
// exists for.
type probeAccess struct {
	files.Access
	probe *commitProbe
}

func (a probeAccess) Decide(ctx context.Context, c authz.Coordinate) authz.Decision {
	if a.probe.inCommit.Load() {
		a.probe.inside.Add(1)
	}
	return a.Access.Decide(ctx, c)
}

func (a probeAccess) PathsAllowed(ctx context.Context, base authz.Coordinate, act authz.Action, paths []string) ([]bool, error) {
	if a.probe.inCommit.Load() {
		a.probe.inside.Add(1)
	}
	return a.Access.PathsAllowed(ctx, base, act, paths)
}

// TestR_STOR_007_NoDecisionWaitsInsideTheCommitTransaction: a concurrent upload
// creates the path after the checks of a deploy, so the link step has to replace
// content. Whether `overwrite` is held was settled before the transaction
// opened: the deploy-only caller is refused, the caller with `overwrite` replaces,
// and neither asks the decision point while the transaction holds its locks.
func TestR_STOR_007_NoDecisionWaitsInsideTheCommitTransaction(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	r := e.newReplica("a", replicaOpts{})
	r.createRepository("generic-up", nil)
	pu := e.user("pub")
	po := e.user("pubover")
	e.grant(r, "up", []string{"generic-up"}, authz.PatternLists{}, map[string][]string{
		"user:pub": {"read", "deploy"}, "user:pubover": {"read", "deploy", "overwrite"},
	})
	waitFor(t, "the grants", 10*time.Second, func() bool {
		return r.get("pub", "/files/generic-up/").status == 200 && r.get("pubover", "/files/generic-up/").status == 200
	})

	deploy := func(p person, path string) (*commitProbe, error) {
		probe := &commitProbe{Store: r.blobs}
		probe.before = func() {
			if res := r.put("admin", "/files/generic-up/"+path, "somebody else was first"); res.status != 201 {
				t.Errorf("the concurrent upload: %d %s", res.status, res.body)
			}
		}
		svc := r.filesOn(probe, r.reader)
		caller := r.caller(authz.Subject{Kind: principal.KindUser, ID: p.ID, Name: p.Name}, "generic-up")
		caller.Access = probeAccess{Access: caller.Access, probe: probe}
		recheck := caller.Recheck
		caller.Recheck = func(ctx context.Context, key string) (files.Access, error) {
			a, err := recheck(ctx, key)
			return probeAccess{Access: a, probe: probe}, err
		}
		_, err := svc.Deploy(context.Background(), caller, files.DeployRequest{
			Repo: "generic-up", Path: repository.Cleaned{Path: path}, Body: strings.NewReader("mine"), ContentLength: 4,
		})
		return probe, err
	}

	t.Run("R-STOR-007 a caller without overwrite is refused and nothing is decided inside the transaction", func(t *testing.T) {
		probe, err := deploy(pu, "race/one.bin")
		if ee, ok := errs.As(err); !ok || ee.Kind != errs.Forbidden {
			t.Fatalf("err = %v, want 403", err)
		}
		if n := probe.inside.Load(); n != 0 {
			t.Errorf("%d decisions inside the commit transaction", n)
		}
		if got := e.text("SELECT size::text FROM items WHERE path = 'race/one.bin'"); got != "23" {
			t.Errorf("the content was replaced (size %s)", got)
		}
	})
	t.Run("R-STOR-007 a caller with overwrite replaces and nothing is decided inside the transaction", func(t *testing.T) {
		probe, err := deploy(po, "race/two.bin")
		if err != nil {
			t.Fatal(err)
		}
		if n := probe.inside.Load(); n != 0 {
			t.Errorf("%d decisions inside the commit transaction", n)
		}
		if got := e.text("SELECT size::text FROM items WHERE path = 'race/two.bin'"); got != "4" {
			t.Errorf("the content was not replaced (size %s)", got)
		}
	})
}
