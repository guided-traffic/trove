//go:build integration

// Package itempublish tests the publication port of the item store (items.Publisher, Q-STOR-182, ADR-170) and the 409 a client gets for
// a generated item (R-STOR-018) against the integration stack: blobs first, then one transaction with the items, the removals and the
// caller's own rows; a superseded or failed publication leaves the previous generation and none of the caller's rows. The replica is
// the production composition root, and the store is the one it serves with.
package itempublish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/audit"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const (
	local   = "gen-local"
	virtual = "gen-virtual"
	// plain is a local repository of the generic format: its clients deploy any path, so a client's file and generated items
	// share one repository (a Debian repository accepts package files only through its upload route).
	plain = "gen-plain"
)

// env is an installation with a local and a virtual repository (a repository of any class takes generated items from the port) and the
// item store of its replica.
type env struct {
	t     *testing.T
	st    *testenv.Stack
	store *items.Store
	repos map[string]repository.Repository
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "itempublish", Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off"}})
	st.Repository(local, "local", "deb", "{}")
	st.Repository(virtual, "virtual", "deb", "{}")
	st.Repository(plain, "local", "generic", "{}")
	st.Exec(`INSERT INTO deb_suites (repo_id, suite) SELECT id, 'stable' FROM repositories WHERE key = $1`, local)
	r := st.Start(t, testenv.ReplicaOptions{Name: "web-1"})
	e := &env{t: t, st: st, store: r.App.Items(), repos: map[string]repository.Repository{}}
	for _, key := range []string{local, virtual, plain} {
		rp, err := r.App.Repositories().ByName(context.Background(), qname.Repo{Project: testenv.DefaultProject, Key: key})
		if err != nil {
			t.Fatal(err)
		}
		e.repos[key] = rp
	}
	return e
}

func (e *env) ref(key string) items.RepoRef { return items.RepoOf(e.repos[key]) }

func file(path, body string, at time.Time) items.GeneratedFile {
	return items.GeneratedFile{
		Path: repository.Cleaned{Path: path}, Body: strings.NewReader(body), Size: int64(len(body)), MediaType: "text/plain", ModifiedAt: at,
	}
}

func (e *env) publish(key string, files []items.GeneratedFile, remove []string, commit func(context.Context, meta.Tx) error) error {
	e.t.Helper()
	var rm []repository.Cleaned
	for _, p := range remove {
		rm = append(rm, repository.Cleaned{Path: p})
	}
	return e.store.Publish(context.Background(), e.ref(key), files, rm, commit)
}

func (e *env) mustPublish(key string, files []items.GeneratedFile, remove []string, commit func(context.Context, meta.Tx) error) {
	e.t.Helper()
	if err := e.publish(key, files, remove, commit); err != nil {
		e.t.Fatalf("publish: %v", err)
	}
}

type row struct {
	id        int64
	generated bool
	sha       string
	modified  int64 // seconds since the epoch
	created   int64
	media     string
}

// item reads the live item at a path straight from the database.
func (e *env) item(key, path string) (row, bool) {
	e.t.Helper()
	var r row
	found := false
	e.st.Query(`SELECT i.id, i.generated, encode(i.blob_sha256, 'hex'), extract(epoch FROM i.modified_at)::bigint, extract(epoch FROM i.created_at)::bigint,
	                   COALESCE(i.media_type, '')
	              FROM items i JOIN repositories r ON r.id = i.repo_id JOIN project p ON p.id = r.project_id
	             WHERE p.key = $1 AND r.key = $2 AND i.path = $3 AND i.trashed_at IS NULL AND i.kind = 'file'`,
		func(rs pgx.Rows) error {
			found = true
			return rs.Scan(&r.id, &r.generated, &r.sha, &r.modified, &r.created, &r.media)
		}, testenv.DefaultProject, key, path)
	return r, found
}

func (e *env) folder(key, path string) bool {
	e.t.Helper()
	return e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path = $2 AND i.kind = 'folder' AND i.trashed_at IS NULL`, key, path) == 1
}

func (e *env) repoID(key string) int64 { return e.ref(key).ID }

func sha(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

func queriesOf(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("not a PostgreSQL transaction")
	}
	return q.New(ptx), nil
}

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func TestR_STOR_018_PublishWritesGeneratedItemsInOneTransactionWithTheCallersRows(t *testing.T) {
	e := newEnv(t)
	id := e.repoID(local)
	e.mustPublish(local, []items.GeneratedFile{
		file("dists/stable/InRelease", "release one", t0),
		file("dists/stable/main/binary-amd64/Packages.gz", "packages one", t0),
		file("dists/stable/main/binary-amd64/by-hash/SHA256/abc", "packages one", t0),
	}, nil, func(ctx context.Context, tx meta.Tx) error {
		// The caller's own rows go in the same transaction.
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		_, err = qs.DebRaiseChangeSeq(ctx, id)
		return err
	})
	if seq := e.st.Number(`SELECT change_seq FROM deb_suites WHERE repo_id = $1`, id); seq != 2 {
		t.Errorf("the caller's row was not written in the transaction: change_seq %d, want 2", seq)
	}
	for path, body := range map[string]string{
		"dists/stable/InRelease":                            "release one",
		"dists/stable/main/binary-amd64/Packages.gz":        "packages one",
		"dists/stable/main/binary-amd64/by-hash/SHA256/abc": "packages one",
	} {
		r, ok := e.item(local, path)
		if !ok || !r.generated || r.sha != sha(body) || r.modified != t0.Unix() || r.media != "text/plain" {
			t.Errorf("%s: %+v, found %v", path, r, ok)
		}
	}
	for _, f := range []string{"dists", "dists/stable", "dists/stable/main", "dists/stable/main/binary-amd64", "dists/stable/main/binary-amd64/by-hash/SHA256"} {
		if !e.folder(local, f) {
			t.Errorf("the folder %s was not created", f)
		}
	}
	// The same bytes under two paths are one blob.
	if n := e.st.Number(`SELECT count(*) FROM blobs WHERE sha256 = decode($1, 'hex')`, sha("packages one")); n != 1 {
		t.Errorf("%d blob rows for equal content", n)
	}
	// A generated item carries no package, no version and no audit event of a deploy.
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND kind = 'file' AND (version_id IS NOT NULL)`, id); n != 0 {
		t.Errorf("%d generated items belong to a version", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = $1`, audit.EventArtifactDeployed); n != 0 {
		t.Errorf("%d deploy events for a publication", n)
	}
}

func TestR_STOR_018_AnIdenticalFileChangesOnlyItsModifiedTime(t *testing.T) {
	e := newEnv(t)
	e.mustPublish(local, []items.GeneratedFile{file("dists/s/Packages", "same", t0), file("dists/s/Release", "v1", t0)}, nil, nil)
	before, _ := e.item(local, "dists/s/Packages")
	t1 := t0.Add(time.Minute)
	e.mustPublish(local, []items.GeneratedFile{file("dists/s/Packages", "same", t1), file("dists/s/Release", "v2", t1)}, nil, nil)
	same, _ := e.item(local, "dists/s/Packages")
	if same.id != before.id || same.sha != before.sha || same.modified != t1.Unix() || same.created != before.created {
		t.Errorf("identical: before %+v, after %+v", before, same)
	}
	changed, _ := e.item(local, "dists/s/Release")
	if changed.sha != sha("v2") || changed.modified != t1.Unix() || !changed.generated {
		t.Errorf("changed: %+v", changed)
	}
	// Two generations never share a Last-Modified (R-STOR-012): every file of the second has the second date.
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND kind = 'file' AND extract(epoch FROM modified_at)::bigint <> $2`, e.repoID(local), t1.Unix()); n != 0 {
		t.Errorf("%d files kept the first generation's time", n)
	}
	// A zero ModifiedAt takes the transaction's time.
	e.mustPublish(local, []items.GeneratedFile{{Path: repository.Cleaned{Path: "dists/s/now"}, Body: strings.NewReader("x"), Size: 1}}, nil, nil)
	if r, _ := e.item(local, "dists/s/now"); time.Since(time.Unix(r.modified, 0)) > time.Minute {
		t.Errorf("modified %d", r.modified)
	}
}

func TestR_STOR_018_PublishRemovesOnlyGeneratedItemsAndSweepsEmptyFolders(t *testing.T) {
	e := newEnv(t)
	e.mustPublish(plain, []items.GeneratedFile{
		file("dists/old/Release", "old", t0), file("dists/old/main/Packages", "old packages", t0), file("dists/keep/Release", "keep", t0),
	}, nil, nil)
	// A client's file in the same repository (a package) and one at a path the port is told to remove.
	e.upload(plain, "pool/main/h/hello/hello_1_all.deb", "package bytes")
	e.mustPublish(plain, []items.GeneratedFile{file("dists/keep/Release", "keep two", t0.Add(time.Minute))},
		[]string{"dists/old/Release", "dists/old/main/Packages", "pool/main/h/hello/hello_1_all.deb", "dists/absent"}, nil)
	for _, p := range []string{"dists/old/Release", "dists/old/main/Packages"} {
		if _, ok := e.item(plain, p); ok {
			t.Errorf("%s is still there", p)
		}
	}
	if e.folder(plain, "dists/old") || e.folder(plain, "dists/old/main") {
		t.Error("the folders the removal emptied were not swept")
	}
	if !e.folder(plain, "dists") || !e.folder(plain, "dists/keep") {
		t.Error("a folder with content was removed")
	}
	if r, ok := e.item(plain, "pool/main/h/hello/hello_1_all.deb"); !ok || r.generated {
		t.Errorf("the client's file was touched by a removal: %+v, %v", r, ok)
	}
}

func (e *env) upload(key, path, body string) {
	e.t.Helper()
	sp, err := e.store.Spool(context.Background(), strings.NewReader(body), items.IngestOptions{ContentLength: int64(len(body))})
	if err != nil {
		e.t.Fatal(err)
	}
	defer sp.Close()
	if _, err := e.store.Commit(context.Background(), items.CommitSpec{
		Repo: e.repos[key], Path: repository.Cleaned{Path: path}, Spooled: sp, Origin: items.OriginUpload, MayOverwrite: true,
		Actor: audit.Actor{Kind: "system", Name: "test"},
	}); err != nil {
		e.t.Fatalf("upload %s: %v", path, err)
	}
}

func TestR_STOR_018_ASupersededPublicationLeavesThePreviousGeneration(t *testing.T) {
	e := newEnv(t)
	id := e.repoID(local)
	e.mustPublish(local, []items.GeneratedFile{file("dists/s/Release", "gen one", t0), file("dists/s/Packages", "p one", t0)}, nil, nil)

	reached := false
	err := e.publish(local, []items.GeneratedFile{file("dists/s/Release", "gen two", t0.Add(time.Hour)), file("dists/s/new", "new file", t0.Add(time.Hour))},
		[]string{"dists/s/Packages"}, func(ctx context.Context, tx meta.Tx) error {
			reached = true
			qs, err := queriesOf(tx)
			if err != nil {
				return err
			}
			if _, err := qs.DebRaiseChangeSeq(ctx, id); err != nil { // the caller's row is written, then the commit finds it was superseded
				return err
			}
			return items.ErrSuperseded
		})
	if !reached || !errors.Is(err, items.ErrSuperseded) {
		t.Fatalf("err = %v, commit reached %v", err, reached)
	}
	assertFirstGeneration(t, e)
	if got := e.st.Number(`SELECT change_seq FROM deb_suites WHERE repo_id = $1`, id); got != 1 {
		t.Errorf("the caller's row of a superseded publication survived: change_seq %d", got)
	}
	// The objects were written first; their rows went with the rolled-back transaction, and the failed commit queued the digests for
	// collection (blob.CommitAll). No item references them.
	if n := e.st.Number(`SELECT count(*) FROM items WHERE blob_sha256 = decode($1, 'hex')`, sha("gen two")); n != 0 {
		t.Errorf("%d items reference the superseded blob", n)
	}
}

// assertFirstGeneration checks that the first generation of the tests above is whole.
func assertFirstGeneration(t *testing.T, e *env) {
	t.Helper()
	r, ok := e.item(local, "dists/s/Release")
	p, okp := e.item(local, "dists/s/Packages")
	_, okn := e.item(local, "dists/s/new")
	if !ok || r.sha != sha("gen one") || r.modified != t0.Unix() || !okp || p.sha != sha("p one") || okn {
		t.Errorf("the previous generation is not whole: release %+v %v, packages %+v %v, new file %v", r, ok, p, okp, okn)
	}
}

func TestR_STOR_018_AFailureBetweenTheBlobWritesAndTheCommitRollsEverythingBack(t *testing.T) {
	e := newEnv(t)
	id := e.repoID(local)
	e.mustPublish(local, []items.GeneratedFile{file("dists/s/Release", "gen one", t0), file("dists/s/Packages", "p one", t0)}, nil, nil)
	seq := e.st.Number(`SELECT change_seq FROM deb_suites WHERE repo_id = $1`, id)

	boom := errors.New("injected failure after the items were written")
	err := e.publish(local, []items.GeneratedFile{file("dists/s/Release", "gen two", t0.Add(time.Hour)), file("dists/s/new", "new file", t0.Add(time.Hour))},
		[]string{"dists/s/Packages"}, func(ctx context.Context, tx meta.Tx) error {
			qs, err := queriesOf(tx)
			if err != nil {
				return err
			}
			if _, err := qs.DebRaiseChangeSeq(ctx, id); err != nil {
				return err
			}
			return boom
		})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	assertFirstGeneration(t, e)
	if got := e.st.Number(`SELECT change_seq FROM deb_suites WHERE repo_id = $1`, id); got != seq {
		t.Errorf("the caller's row survived the rollback: change_seq %d, want %d", got, seq)
	}

	// A body that fails while it is read: the first file is spooled, the second breaks, nothing is stored or written.
	broken := items.GeneratedFile{Path: repository.Cleaned{Path: "dists/s/broken"}, Body: io.MultiReader(strings.NewReader("part"), failingReader{}), Size: 100, ModifiedAt: t0}
	if err := e.publish(local, []items.GeneratedFile{file("dists/s/ok", "fine", t0), broken}, nil, nil); err == nil {
		t.Fatal("a body that fails was published")
	}
	if _, ok := e.item(local, "dists/s/ok"); ok {
		t.Error("the file before the broken one was published")
	}
	if n := e.st.Number(`SELECT count(*) FROM blobs WHERE sha256 = decode($1, 'hex')`, sha("fine")); n != 0 {
		t.Errorf("the blob of a file in a failed publication was stored (%d rows)", n)
	}
	// A body shorter than its declared size fails as well.
	short := file("dists/s/short", "abc", t0)
	short.Size = 10
	if err := e.publish(local, []items.GeneratedFile{short}, nil, nil); err == nil {
		t.Error("a body shorter than its size was published")
	}
	assertFirstGeneration(t, e)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the reader broke") }

func TestR_STOR_018_APublicationRefusesWhatIsNoGeneration(t *testing.T) {
	e := newEnv(t)
	cases := map[string]struct {
		files  []items.GeneratedFile
		remove []string
	}{
		"a folder path":         {files: []items.GeneratedFile{{Path: repository.Cleaned{Path: "dists", Folder: true}, Body: strings.NewReader(""), Size: 0}}},
		"the root":              {files: []items.GeneratedFile{{Path: repository.Cleaned{}, Body: strings.NewReader(""), Size: 0}}},
		"no body":               {files: []items.GeneratedFile{{Path: repository.Cleaned{Path: "a"}, Size: 1}}},
		"a path twice":          {files: []items.GeneratedFile{file("a", "1", t0), file("a", "2", t0)}},
		"published and removed": {files: []items.GeneratedFile{file("a", "1", t0)}, remove: []string{"a"}},
		"a file below a file":   {files: []items.GeneratedFile{file("a", "1", t0), file("a/b", "2", t0)}},
		"a file at a folder":    {files: []items.GeneratedFile{file("d/x", "1", t0), file("d", "2", t0)}},
	}
	for name, c := range cases {
		if err := e.publish(local, c.files, c.remove, nil); err == nil {
			t.Errorf("%s was published", name)
		}
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND kind = 'file'`, e.repoID(local)); n != 0 {
		t.Errorf("%d files after only refusals", n)
	}
}

func TestR_STOR_018_AClientNeitherWritesNorDeletesAGeneratedItem(t *testing.T) {
	e := newEnv(t)
	e.mustPublish(plain, []items.GeneratedFile{file("dists/s/Release", "gen", t0), file("dists/s/main/Packages", "pk", t0)}, nil, nil)
	e.upload(plain, "pool/p.deb", "client file")

	code := func(err error) string {
		var ee interface{ Error() string }
		if err == nil {
			return ""
		}
		ee = err
		return ee.Error()
	}
	// A write at a generated path: 409 generated_item, and the item is unchanged.
	sp, err := e.store.Spool(context.Background(), strings.NewReader("client bytes"), items.IngestOptions{ContentLength: 12})
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	_, err = e.store.Commit(context.Background(), items.CommitSpec{
		Repo: e.repos[plain], Path: repository.Cleaned{Path: "dists/s/Release"}, Spooled: sp, Origin: items.OriginUpload, MayOverwrite: true,
		Actor: audit.Actor{Kind: "system", Name: "test"},
	})
	if !isGenerated(err) {
		t.Fatalf("write = %q, want generated_item", code(err))
	}
	if r, _ := e.item(plain, "dists/s/Release"); r.sha != sha("gen") {
		t.Error("the generated item was replaced")
	}
	// Deleting a generated file, a folder that holds one, or the whole tree is refused whole.
	for _, p := range []string{"dists/s/Release", "dists/s/main", "dists/s", "dists"} {
		_, err := e.store.Delete(context.Background(), items.DeleteSpec{Repo: e.repos[plain], Path: p, Actor: audit.Actor{Kind: "system", Name: "test"}})
		if !isGenerated(err) {
			t.Errorf("delete %s = %q, want generated_item", p, code(err))
		}
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND generated AND kind = 'file'`, e.repoID(plain)); n != 2 {
		t.Errorf("%d generated files left, want 2", n)
	}
	// A client's own file is deleted as ever.
	if _, err := e.store.Delete(context.Background(), items.DeleteSpec{Repo: e.repos[plain], Path: "pool/p.deb", Actor: audit.Actor{Kind: "system", Name: "test"}}); err != nil {
		t.Errorf("delete of a client file: %v", err)
	}
	// The port itself replaces them: a publication over a client's file makes it generated.
	e.upload(plain, "dists/s/client", "was a client file")
	e.mustPublish(plain, []items.GeneratedFile{file("dists/s/client", "now generated", t0)}, nil, nil)
	if r, _ := e.item(plain, "dists/s/client"); !r.generated || r.sha != sha("now generated") {
		t.Errorf("the port did not take the path: %+v", r)
	}
}

func isGenerated(err error) bool {
	return err != nil && strings.Contains(err.Error(), "generated by trove")
}

// The port writes into the repository it is given, whatever its class: the rule that a virtual repository takes generated items from
// the port and from nothing else is the virtual class's (WP8), and the seam is this port.
func TestR_STOR_018_APublicationIntoAVirtualRepository(t *testing.T) {
	e := newEnv(t)
	e.mustPublish(virtual, []items.GeneratedFile{file("dists/v/InRelease", "merged", t0)}, nil, nil)
	if r, ok := e.item(virtual, "dists/v/InRelease"); !ok || !r.generated {
		t.Errorf("virtual: %+v %v", r, ok)
	}
	if _, ok := e.item(local, "dists/v/InRelease"); ok {
		t.Error("the file reached another repository")
	}
}

func TestR_STOR_018_ConcurrentPublicationsOfOverlappingFilesBothFinish(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			at := t0.Add(time.Duration(i) * time.Second)
			errs[i] = e.publish(local, []items.GeneratedFile{
				file("dists/s/Release", fmt.Sprintf("release %d", i), at), file("dists/s/shared", "shared", at), file(fmt.Sprintf("dists/s/own%d", i), "own", at),
			}, nil, nil)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("publication %d: %v", i, err)
		}
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND kind = 'file' AND path = 'dists/s/Release'`, e.repoID(local)); n != 1 {
		t.Errorf("%d live Release items", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND kind = 'file'`, e.repoID(local)); n != 2+8 {
		t.Errorf("%d files, want 10", n)
	}
}
