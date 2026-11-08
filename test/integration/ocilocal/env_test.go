//go:build integration

// Package ocilocal tests the local OCI repository's lists, deletion cascade, sweep of unreferenced blobs, journal replay and
// reinstate against the integration stack (R-OCI-015 to R-OCI-019, R-OCI-033). The registry routes under /v2/ are mounted by another
// work package, so the state these tests start from is what a push leaves: items committed through the item store of a running
// replica, and the rows of the format's tables written by SQL. What they test is the production code on top of that state: the
// data port, the storage hook behind a raw delete, the sweep job and the replay.
package ocilocal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/config"
	_ "github.com/guided-traffic/trove/internal/formatdata/oci"
	ocidata "github.com/guided-traffic/trove/internal/formatdata/oci"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

const (
	project   = "it"
	repoLocal = "docker-local"
	repoOther = "docker-other"
	repoCache = "docker-hub"
	manifest  = "application/vnd.oci.image.manifest.v1+json"
	index     = "application/vnd.oci.image.index.v1+json"
)

// env is one installation with a local OCI repository, a second one, a remote one, an administrator and a replica.
type env struct {
	t        *testing.T
	st       *testenv.Stack
	r        *testenv.Replica
	db       *postgres.DB
	tokAdmin string
	store    *ocidata.Store
	events   *captured
}

// captured records the events the data port writes outside a request (the store of a test has no audit writer of its own).
type captured struct{ got []audit.Event }

func (c *captured) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	c.got = append(c.got, ev)
	return nil
}
func (c *captured) Record(context.Context, audit.Event) {}

func newEnv(t *testing.T, extra ...string) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "ocilocal", Env: append([]string{"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off"}, extra...)})
	e := &env{t: t, st: st, events: &captured{}}
	admin := st.User("ops")
	st.Bind("admin", admin)
	e.tokAdmin = st.Token(admin, scope.Scope{Inherit: true, Admin: true})
	st.Repository(repoLocal, "local", "oci", "{}")
	st.Repository(repoOther, "local", "oci", "{}")
	st.Repository(repoCache, "remote", "oci", "{}")
	e.r = st.Start(t, testenv.ReplicaOptions{Name: "web-1"})
	loaded, err := config.Load(config.Options{Environ: st.Environ(), Command: config.CommandAdmin})
	if err != nil {
		t.Fatal(err)
	}
	e.db, err = postgres.Open(context.Background(), loaded.Config.Database, "ocilocal", meta.RoleWeb, testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.db.Close)
	e.store, err = ocidata.New(ocidata.Options{DB: e.db, Recorder: e.events})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) repoID(key string) int64 {
	e.t.Helper()
	return e.st.Number(`SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $1 AND r.key = $2`, project, key)
}

// put commits a file as the item at path of the repository, the way a push stores a manifest, a tag or a blob (with the payload oci.SeededItem, the one
// the format's commit hook admits besides its own push path: the rows are written through the data port below), and returns the SHA-256
// of its content. The storage hook of the format runs with it.
func (e *env) put(repo, path, body, mediaType string) [32]byte {
	e.t.Helper()
	ctx := context.Background()
	rp, err := e.r.App.Repositories().ByName(ctx, qname.Repo{Project: project, Key: repo})
	if err != nil {
		e.t.Fatal(err)
	}
	sp, err := e.r.App.Items().Spool(ctx, strings.NewReader(body), items.IngestOptions{ContentLength: int64(len(body))})
	if err != nil {
		e.t.Fatal(err)
	}
	defer sp.Close()
	if _, err := e.r.App.Items().Commit(ctx, items.CommitSpec{
		Repo: rp, Path: repository.Cleaned{Path: path}, Spooled: sp, MediaType: mediaType, Origin: items.OriginFill, UpstreamURL: "https://seed.invalid/", MayOverwrite: true, Payload: oci.SeededItem{},
		Actor: audit.Actor{Kind: "system", Name: "push"},
	}); err != nil {
		e.t.Fatalf("commit %s: %v", path, err)
	}
	return sha256.Sum256([]byte(body))
}

func dg(d [32]byte) string { return "sha256:" + hex.EncodeToString(d[:]) }

// image is a manifest of an image name as a push leaves it: the item `<name>/_manifests/<digest>` and its row.
type image struct {
	name   string
	digest [32]byte
}

func (i image) path() string { return i.name + "/_manifests/" + dg(i.digest) }

// refs are the references of a manifest row.
type ref struct {
	target [32]byte
	kind   string
}

// manifestOf stores a manifest item and its row; subject and artifactType may be empty.
func (e *env) manifestOf(repo, name, body, mediaType string, subject *[32]byte, artifactType string, refs ...ref) image {
	e.t.Helper()
	d := e.put(repo, name+"/_manifests/"+dg(sha256.Sum256([]byte(body))), body, mediaType)
	im := image{name: name, digest: d}
	e.row(repo, im, mediaType, subject, artifactType, int64(len(body)), refs...)
	return im
}

// row writes the rows of a manifest through the data port, in a transaction, as the commit hook of a push does (PutManifest dedupes the
// references and bounds the annotations).
func (e *env) row(repo string, im image, mediaType string, subject *[32]byte, artifactType string, size int64, refs ...ref) {
	e.t.Helper()
	in := rows.ManifestInput{Manifest: rows.Manifest{
		RepoID: e.repoID(repo), Name: im.name, Digest: im.digest, MediaType: mediaType, ArtifactType: artifactType, Subject: subject, Size: size,
	}}
	for _, r := range refs {
		in.References = append(in.References, rows.Reference{Target: r.target, Kind: r.kind})
	}
	if err := e.db.InTx(context.Background(), func(tx meta.Tx) error { return e.store.PutManifest(context.Background(), tx, in) }); err != nil {
		e.t.Fatalf("PutManifest: %v", err)
	}
}

// tag stores the tag item of a manifest (the same bytes) and the version row it leads.
func (e *env) tag(repo, name, tag, body string) {
	e.t.Helper()
	e.put(repo, name+"/_tags/"+tag, body, manifest)
	id := e.repoID(repo)
	e.st.Exec(`INSERT INTO packages (repo_id, name, display_name) VALUES ($1, $2, $2) ON CONFLICT DO NOTHING`, id, name)
	e.st.Exec(`INSERT INTO package_versions (package_id, version, display_version, lead_item_id)
		SELECT p.id, $3, $3, i.id FROM packages p, items i WHERE p.repo_id = $1 AND p.name = $2 AND i.repo_id = $1 AND i.path = $4`,
		id, name, tag, name+"/_tags/"+tag)
	e.st.Exec(`UPDATE items SET version_id = pv.id FROM package_versions pv, packages p
		WHERE pv.package_id = p.id AND p.repo_id = $1 AND p.name = $2 AND pv.version = $3 AND items.repo_id = $1 AND items.path = $4`,
		id, name, tag, name+"/_tags/"+tag)
}

// itemCount is the number of live items at the path.
func (e *env) itemCount(repo, path string) int64 {
	return e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND path = $2 AND trashed_at IS NULL`, e.repoID(repo), path)
}

func (e *env) revSeq(repo, name string) int64 {
	return e.st.Number(`SELECT COALESCE(max(rev_seq), 0) FROM packages WHERE repo_id = $1 AND name = $2`, e.repoID(repo), name)
}

func (e *env) liveVersions(repo, name string) int64 {
	return e.st.Number(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id
		WHERE p.repo_id = $1 AND p.name = $2 AND pv.trashed_at IS NULL`, e.repoID(repo), name)
}

// age sets the creation time of the items at the paths back, so that a grace period has passed for them.
func (e *env) age(repo string, d time.Duration, paths ...string) {
	e.t.Helper()
	for _, p := range paths {
		e.st.Exec(`UPDATE items SET created_at = now() - make_interval(secs => $3) WHERE repo_id = $1 AND path = $2`, e.repoID(repo), p, d.Seconds())
	}
}

// strings runs a query of one text column and returns its rows.
func (e *env) strings(sql string, args ...any) []string {
	e.t.Helper()
	var out []string
	e.st.Query(sql, func(r pgx.Rows) error {
		var s string
		if err := r.Scan(&s); err != nil {
			return err
		}
		out = append(out, s)
		return nil
	}, args...)
	return out
}

func sha256Of(s string) [32]byte { return sha256.Sum256([]byte(s)) }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
