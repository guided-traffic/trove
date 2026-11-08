//go:build integration

package files

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/uploads"

	ocidata "github.com/guided-traffic/trove/internal/formatdata/oci"
)

// The blob upload protocol and the cross-repository mount of the OCI format on the production host (R-OCI-011, R-OCI-012): three
// replicas on one database and one bucket, each with its own blob store, spool and upload session service, so that a request of a
// session is pinned to the replica the test chooses. The registry root (`/v2/`) is another work package; the format is mounted at
// /api/oci/ as in oci_host_test.go.

const (
	partSize   = 5 << 20 // the smallest part S3 takes
	smallLimit = 6 << 20 // the upload limit of oci-small
	apiPrefix  = "/api/oci/it/"
)

type uploadFixture struct {
	e        *env
	replicas []*replica
	hosts    []*formathost.Host
}

func newUploadFixture(t *testing.T) *uploadFixture {
	t.Helper()
	e := newEnv(t)
	e.breakGlass()
	f := &uploadFixture{e: e}
	for i, name := range []string{"a", "b", "c"} {
		f.addReplica(t, name, i)
	}
	r := f.replicas[0]
	for _, key := range []string{"oci-local", "oci-source", "oci-secret", "oci-peer"} {
		r.createRepository(key, map[string]any{"format": "oci"})
	}
	r.createRepository("oci-small", map[string]any{"format": "oci", "storage": map[string]any{"maxUploadSize": smallLimit}})
	for _, u := range []string{"writer", "writer2", "reader", "mover", "nobody", "pusher"} {
		e.user(u)
	}
	e.grant(r, "up-local", []string{"oci-local"}, authz.PatternLists{}, map[string][]string{
		"user:writer": {"read", "deploy"}, "user:writer2": {"read", "deploy"}, "user:reader": {"read"}, "user:mover": {"read", "deploy"},
	})
	e.grant(r, "up-small", []string{"oci-small"}, authz.PatternLists{}, map[string][]string{"user:writer": {"read", "deploy"}})
	e.grant(r, "up-peer", []string{"oci-peer"}, authz.PatternLists{}, map[string][]string{"user:writer": {"read", "deploy"}})
	e.grant(r, "up-source-push", []string{"oci-source"}, authz.PatternLists{}, map[string][]string{"user:pusher": {"read", "deploy"}})
	e.grant(r, "up-source", []string{"oci-source"}, authz.PatternLists{}, map[string][]string{"user:mover": {"read"}})
	// every replica knows the policies before the first request
	for i, rep := range f.replicas {
		waitFor(t, fmt.Sprintf("the policies on replica %d", i), 15*time.Second, func() bool {
			return rep.do("writer", http.MethodPost, apiPrefix+"oci-local/probe/blobs/uploads/", nil).status == http.StatusAccepted &&
				rep.do("mover", http.MethodGet, apiPrefix+"oci-source/probe/tags/list", nil).status != http.StatusForbidden &&
				rep.do("writer", http.MethodGet, apiPrefix+"oci-small/probe/blobs/uploads/"+uuid.NewString(), nil).status != http.StatusForbidden &&
				rep.do("pusher", http.MethodGet, apiPrefix+"oci-source/probe/tags/list", nil).status != http.StatusForbidden
		})
	}
	return f
}

func (f *uploadFixture) addReplica(t *testing.T, name string, i int) {
	t.Helper()
	b := formathost.NewBindings()
	var rep *replica
	var host *formathost.Host
	var own *authz.Ownership
	deps := formathost.Deps{Bindings: b, Ownership: lateOwnership{get: func() *authz.Ownership { return own }}, AutoClaim: noAutoClaim{}, Remover: func() formathost.ItemRemover {
		if rep != nil && rep.items != nil {
			return rep.items
		}
		return nil
	}}
	seam := formathost.NewFilesSeam(func() (formats.Host, error) { return host, nil }, deps)
	rep = f.e.newReplica(name, replicaOpts{
		hooks: formathost.NewHooks(deps), formats: seam,
		storage: func(c *config.Storage) { c.Uploads.PartSize = partSize },
		routes: func(r *replica, router *httpserver.Router) {
			store, err := ocidata.New(ocidata.Options{DB: r.db, Recorder: r.rec})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Bind("oci", store); err != nil {
				t.Fatal(err)
			}
			pkgs, err := packages.NewStore(r.db)
			if err != nil {
				t.Fatal(err)
			}
			var err2 error
			if own, err2 = authz.NewOwnership(authz.OwnershipOptions{Engine: r.engine, DB: r.db, Recorder: r.rec}); err2 != nil {
				t.Fatal(err2)
			}
			cfg := f.e.s3.cfg
			cfg.Uploads.PartSize = partSize
			svc, err := uploads.New(uploads.Options{DB: r.db, Objects: r.obj, Spool: r.blobs.Spool(), Storage: cfg, Log: quiet})
			if err != nil {
				t.Fatal(err)
			}
			if host, err = formathost.New(formathost.Options{
				Runner: r.db, Decider: r.engine, Items: r.items, Packages: pkgs, Repos: r.reader, Deps: deps, Resolver: proceedResolver{}, Uploads: svc,
			}); err != nil {
				t.Fatal(err)
			}
			disp, err := dispatch.New(dispatch.Options{
				Host: host, Decider: r.engine, Repos: r.reader, Formats: func() []formats.Format { return []formats.Format{apiOCI{oci.New()}} },
			})
			if err != nil {
				t.Fatal(err)
			}
			disp.Register(router)
		},
	})
	f.replicas = append(f.replicas, rep)
	f.hosts = append(f.hosts, host)
}

// lateOwnership is the real package-ownership rule of the decision point (R-AUTHZ-018) behind the seam of the package writer, as the composition
// root binds it (internal/app ownershipSeam); the rule exists once the replica's engine does.
type lateOwnership struct{ get func() *authz.Ownership }

func (l lateOwnership) EnforceTx(ctx context.Context, tx meta.Tx, chk formathost.OwnershipCheck) error {
	_, err := l.get().EnforceTx(ctx, tx, chk.Index, chk.Subject, chk.Coordinate, authz.OwnershipWrite(chk.Write))
	return err
}

func (l lateOwnership) Precheck(ctx context.Context, chk formathost.OwnershipCheck) error {
	return l.get().PrecheckWrite(ctx, chk.Index, chk.Subject, chk.Coordinate, authz.OwnershipWrite(chk.Write))
}

func random(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// do sends a request to replica i as the user.
func (f *uploadFixture) do(i int, user, method, path string, body []byte, hdr ...string) response {
	f.e.t.Helper()
	var r *bytes.Reader
	if body != nil {
		r = bytes.NewReader(body)
	} else {
		r = bytes.NewReader(nil)
	}
	return f.replicas[i].do(user, method, apiPrefix+path, r, append([]string{"Content-Type", "application/octet-stream"}, hdr...)...)
}

// start opens a session on replica i and returns its path below the repository and its id.
func (f *uploadFixture) start(i int, user, repo, name string) (path, id string) {
	f.e.t.Helper()
	res := f.do(i, user, http.MethodPost, repo+"/"+name+"/blobs/uploads/", nil)
	wantOCI(f.e.t, res, http.StatusAccepted, "")
	id = res.header.Get("Docker-Upload-UUID")
	if _, err := uuid.Parse(id); err != nil || !strings.HasSuffix(res.header.Get("Location"), "/"+name+"/blobs/uploads/"+id) {
		f.e.t.Fatalf("session headers = %v", res.header)
	}
	if res.header.Get("OCI-Chunk-Min-Length") != fmt.Sprint(partSize) || res.header.Get("Range") != "0-0" {
		f.e.t.Fatalf("session headers = %v", res.header)
	}
	return repo + "/" + name + "/blobs/uploads/" + id, id
}

func (f *uploadFixture) count(sql string, args ...any) int64 {
	f.e.t.Helper()
	return f.e.number(sql, args...)
}

func (f *uploadFixture) blobItems(path string) int64 {
	return f.count(`SELECT count(*) FROM items WHERE path = $1 AND trashed_at IS NULL`, path)
}

// seed stores a blob item of the name in the repository through the item store of replica 0.
func (f *uploadFixture) seed(repoKey, name string, data []byte) string {
	f.e.t.Helper()
	ctx := context.Background()
	r := f.replicas[0]
	repo, err := r.reader.ByName(ctx, qname.Repo{Project: "it", Key: repoKey})
	if err != nil {
		f.e.t.Fatal(err)
	}
	sp, err := r.items.Spool(ctx, bytes.NewReader(data), items.IngestOptions{ContentLength: int64(len(data))})
	if err != nil {
		f.e.t.Fatal(err)
	}
	defer sp.Close()
	d := digestOf(data)
	if _, err := r.items.Commit(ctx, items.CommitSpec{
		Repo: repo, Path: repository.Cleaned{Path: oci.BlobPath(name, d)}, Spooled: sp, MediaType: "application/octet-stream", Origin: items.OriginUpload,
	}); err != nil {
		f.e.t.Fatalf("seed %s: %v", d, err)
	}
	return d
}

func openMultiparts(t *testing.T, f *uploadFixture) int {
	t.Helper()
	ups, err := f.e.s3.raw.ListMultipart(context.Background(), f.e.s3.prefix+"/")
	if err != nil {
		t.Fatal(err)
	}
	return len(ups)
}

// A blob of three chunks whose requests reach three different replicas, and the status and the last bytes on yet others: the blob has
// the file's SHA-256 and every replica serves it (R-OCI-011).
func TestR_OCI_011_AChunkedUploadAcrossThreeReplicasYieldsTheBlob(t *testing.T) {
	f := newUploadFixture(t)
	data := random(2*partSize + partSize/2)
	d := digestOf(data)
	c1, c2, last := data[:partSize+1024], data[partSize+1024:2*partSize+2048], data[2*partSize+2048:]
	path, id := f.start(1, "writer", "oci-local", "team/app")

	r1 := f.do(0, "writer", http.MethodPatch, path, c1, "Content-Range", fmt.Sprintf("0-%d", len(c1)-1))
	wantOCI(t, r1, http.StatusAccepted, "")
	if r1.header.Get("Range") != fmt.Sprintf("0-%d", len(c1)-1) || r1.header.Get("Docker-Upload-UUID") != id {
		t.Fatalf("first chunk: %v", r1.header)
	}
	r2 := f.do(1, "writer", http.MethodPatch, path, c2, "Content-Range", fmt.Sprintf("%d-%d", len(c1), len(c1)+len(c2)-1))
	wantOCI(t, r2, http.StatusAccepted, "")
	wantRange := fmt.Sprintf("0-%d", len(c1)+len(c2)-1)
	if r2.header.Get("Range") != wantRange {
		t.Fatalf("second chunk: %v, want %s", r2.header, wantRange)
	}
	st := f.do(2, "writer", http.MethodGet, path, nil)
	if st.status != http.StatusNoContent || st.header.Get("Range") != wantRange {
		t.Fatalf("status: %d %v", st.status, st.header)
	}
	done := f.do(2, "writer", http.MethodPut, path+"?digest="+d, last)
	wantOCI(t, done, http.StatusCreated, "")
	if done.header.Get("Docker-Content-Digest") != d || !strings.HasSuffix(done.header.Get("Location"), "/team/app/blobs/"+d) {
		t.Fatalf("finish: %v", done.header)
	}
	for i := range f.replicas {
		got := f.do(i, "reader", http.MethodGet, "oci-local/team/app/blobs/"+d, nil)
		if got.status != http.StatusOK || digestOf(got.body) != d {
			t.Fatalf("replica %d serves %d, %d bytes", i, got.status, len(got.body))
		}
	}
	if n := f.blobItems("team/app/_blobs/" + d); n != 1 {
		t.Fatalf("%d blob items", n)
	}
	if n := f.count(`SELECT count(*) FROM items WHERE path = $1 AND trashed_at IS NULL AND media_type = 'application/octet-stream'`, "team/app/_blobs/"+d); n != 1 {
		t.Fatalf("media type not recorded")
	}
	// the finished session is gone for everyone
	wantOCI(t, f.do(2, "writer", http.MethodGet, path, nil), http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN")
	if m := openMultiparts(t, f); m != 0 {
		t.Fatalf("%d multipart uploads left", m)
	}
}

// A chunk at an offset other than the received count is 416 with the range the session holds, on any replica; the session continues.
func TestR_OCI_011_AnOutOfOrderChunkIs416WithTheRangeOnAnyReplica(t *testing.T) {
	f := newUploadFixture(t)
	path, _ := f.start(0, "writer", "oci-local", "app")
	res := f.do(1, "writer", http.MethodPatch, path, []byte("0123456789"), "Content-Range", "10-19")
	wantOCI(t, res, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID")
	if res.header.Get("Range") != "0-0" || res.header.Get("Location") == "" {
		t.Fatalf("headers = %v", res.header)
	}
	wantOCI(t, f.do(2, "writer", http.MethodPatch, path, []byte("0123456789"), "Content-Range", "0-9"), http.StatusAccepted, "")
	res = f.do(0, "writer", http.MethodPatch, path, []byte("0123456789"), "Content-Range", "0-9")
	wantOCI(t, res, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID")
	if res.header.Get("Range") != "0-9" {
		t.Fatalf("Range = %q", res.header.Get("Range"))
	}
	// the last bytes of a finalize are held to the same rule
	d := digestOf([]byte("0123456789ab"))
	res = f.do(1, "writer", http.MethodPut, path+"?digest="+d, []byte("ab"), "Content-Range", "9-10")
	wantOCI(t, res, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID")
	wantOCI(t, f.do(1, "writer", http.MethodPut, path+"?digest="+d, []byte("ab"), "Content-Range", "10-11"), http.StatusCreated, "")
}

// A digest other than the content's is 400 DIGEST_INVALID, stores nothing and keeps the session open; the same for a monolithic POST.
func TestR_OCI_011_AWrongDigestIsRefusedAndTheSessionIsKept(t *testing.T) {
	f := newUploadFixture(t)
	data := []byte("a small blob")
	good, wrong := digestOf(data), digestOf([]byte("another"))
	path, _ := f.start(0, "writer", "oci-local", "app")
	wantOCI(t, f.do(1, "writer", http.MethodPatch, path, data), http.StatusAccepted, "")
	wantOCI(t, f.do(2, "writer", http.MethodPut, path+"?digest="+wrong, nil), http.StatusBadRequest, "DIGEST_INVALID")
	if f.blobItems("app/_blobs/"+wrong) != 0 || f.blobItems("app/_blobs/"+good) != 0 {
		t.Fatal("a refused finalize stored a blob")
	}
	st := f.do(0, "writer", http.MethodGet, path, nil)
	if st.status != http.StatusNoContent || st.header.Get("Range") != fmt.Sprintf("0-%d", len(data)-1) {
		t.Fatalf("the session after the refusal: %d %v", st.status, st.header)
	}
	wantOCI(t, f.do(0, "writer", http.MethodPut, path+"?digest="+good, nil), http.StatusCreated, "")

	// monolithic
	before := f.count(`SELECT count(*) FROM upload_sessions`)
	wantOCI(t, f.do(1, "writer", http.MethodPost, "oci-local/mono/blobs/uploads/?digest="+wrong, data), http.StatusBadRequest, "DIGEST_INVALID")
	if f.blobItems("mono/_blobs/"+wrong) != 0 || f.blobItems("mono/_blobs/"+good) != 0 {
		t.Fatal("a refused monolithic upload stored a blob")
	}
	res := f.do(2, "writer", http.MethodPost, "oci-local/mono/blobs/uploads/?digest="+good, data)
	wantOCI(t, res, http.StatusCreated, "")
	if res.header.Get("Docker-Content-Digest") != good || !strings.HasSuffix(res.header.Get("Location"), "/mono/blobs/"+good) {
		t.Fatalf("headers = %v", res.header)
	}
	if f.blobItems("mono/_blobs/"+good) != 1 {
		t.Fatal("the blob is not stored")
	}
	if after := f.count(`SELECT count(*) FROM upload_sessions`); after != before {
		t.Fatalf("a monolithic upload made %d sessions", after-before)
	}
	got := f.do(1, "reader", http.MethodGet, "oci-local/mono/blobs/"+good, nil)
	if got.status != http.StatusOK || !bytes.Equal(got.body, data) {
		t.Fatalf("pull: %d", got.status)
	}
}

// DELETE aborts the session on any replica and what it staged in the object store goes with it.
func TestR_OCI_011_AnAbortedSessionLeavesNoUpload(t *testing.T) {
	f := newUploadFixture(t)
	path, _ := f.start(0, "writer", "oci-local", "app")
	wantOCI(t, f.do(1, "writer", http.MethodPatch, path, random(partSize+10)), http.StatusAccepted, "")
	if openMultiparts(t, f) == 0 {
		t.Fatal("the session has no multipart upload after a whole part")
	}
	res := f.do(2, "writer", http.MethodDelete, path, nil)
	if res.status != http.StatusNoContent || len(res.body) != 0 {
		t.Fatalf("abort: %d %s", res.status, res.body)
	}
	if m := openMultiparts(t, f); m != 0 {
		t.Fatalf("%d multipart uploads after the abort", m)
	}
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodPut, http.MethodDelete} {
		wantOCI(t, f.do(0, "writer", method, path+"?digest="+digestOf(nil), nil), http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN")
	}
}

// A session is the principal's and the image name's: another principal, another name and another repository get 404
// BLOB_UPLOAD_UNKNOWN on every method, and the owner's session is untouched.
func TestR_OCI_011_ASessionOfAnotherPrincipalOrNameIs404(t *testing.T) {
	f := newUploadFixture(t)
	path, id := f.start(0, "writer", "oci-local", "team/app")
	wantOCI(t, f.do(1, "writer", http.MethodPatch, path, []byte("mine")), http.StatusAccepted, "")
	probe := func(who, p string) {
		t.Helper()
		for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodPut, http.MethodDelete} {
			res := f.do(2, who, method, p+"?digest="+digestOf([]byte("mine")), []byte("x"))
			if res.status != http.StatusNotFound || !strings.Contains(string(res.body), `"BLOB_UPLOAD_UNKNOWN"`) {
				t.Errorf("%s %s as %s: %d %s", method, p, who, res.status, res.body)
			}
		}
	}
	probe("writer2", path)                                    // another principal with the same rights
	probe("writer", "oci-local/team/other/blobs/uploads/"+id) // another image name
	probe("writer", "oci-local/team/blobs/uploads/"+id)
	probe("writer", "oci-local/app/blobs/uploads/"+id)
	probe("writer", "oci-peer/team/app/blobs/uploads/"+id) // another repository the principal may write to
	probe("mover", "oci-local/team/app/blobs/uploads/"+id)
	wantOCI(t, f.do(2, "writer", http.MethodGet, "oci-local/team/app/blobs/uploads/not-a-uuid", nil), http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN")
	st := f.do(1, "writer", http.MethodGet, path, nil)
	if st.status != http.StatusNoContent || st.header.Get("Range") != "0-3" {
		t.Fatalf("the owner's session: %d %v", st.status, st.header)
	}
	wantOCI(t, f.do(0, "writer", http.MethodPut, path+"?digest="+digestOf([]byte("mine")), nil), http.StatusCreated, "")
}

// A caller without deploy starts nothing.
func TestR_OCI_011_ACallerWithoutDeployStartsNoUpload(t *testing.T) {
	f := newUploadFixture(t)
	before := f.count(`SELECT count(*) FROM upload_sessions`)
	for _, who := range []string{"reader", "nobody"} {
		res := f.do(0, who, http.MethodPost, "oci-local/app/blobs/uploads/", nil)
		if res.status != http.StatusForbidden && res.status != http.StatusNotFound {
			t.Errorf("%s: %d %s", who, res.status, res.body)
		}
		d := digestOf([]byte("x"))
		res = f.do(0, who, http.MethodPost, "oci-local/app/blobs/uploads/?digest="+d, []byte("x"))
		if res.status != http.StatusForbidden && res.status != http.StatusNotFound {
			t.Errorf("%s monolithic: %d %s", who, res.status, res.body)
		}
	}
	if after := f.count(`SELECT count(*) FROM upload_sessions`); after != before {
		t.Fatalf("%d sessions started", after-before)
	}
}

// --- R-OCI-012

// A mount from a repository the caller may read links the blob: 201, the blob is served through the target name, and no bytes went to
// the object store.
func TestR_OCI_012_AMountFromAReadableRepositoryLinksTheBlobAndUploadsNothing(t *testing.T) {
	f := newUploadFixture(t)
	data := random(1 << 20)
	d := f.seed("oci-source", "base", data)
	puts := f.replicas[1].obj.puts.Load()
	parts := f.replicas[1].obj.parts.Load()
	sessions := f.count(`SELECT count(*) FROM upload_sessions`)

	res := f.do(1, "mover", http.MethodPost, "oci-local/team/app/blobs/uploads/?mount="+d+"&from=it/oci-source/base", nil)
	wantOCI(t, res, http.StatusCreated, "")
	if res.header.Get("Docker-Content-Digest") != d || !strings.HasSuffix(res.header.Get("Location"), "/team/app/blobs/"+d) {
		t.Fatalf("headers = %v", res.header)
	}
	if n := f.replicas[1].obj.puts.Load() - puts; n != 0 || f.replicas[1].obj.parts.Load() != parts {
		t.Fatalf("a mount put %d objects", n)
	}
	if after := f.count(`SELECT count(*) FROM upload_sessions`); after != sessions {
		t.Fatal("a mount opened a session")
	}
	for i := range f.replicas {
		got := f.do(i, "reader", http.MethodGet, "oci-local/team/app/blobs/"+d, nil)
		if got.status != http.StatusOK || !bytes.Equal(got.body, data) {
			t.Fatalf("replica %d: %d", i, got.status)
		}
	}
	if n := f.count(`SELECT count(DISTINCT blob_sha256) FROM items WHERE path IN ($1, $2)`, "base/_blobs/"+d, "team/app/_blobs/"+d); n != 1 {
		t.Fatalf("the two items hold %d blobs, want one", n)
	}
	// the blob is readable through the target name only
	wantOCI(t, f.do(0, "reader", http.MethodGet, "oci-local/other/blobs/"+d, nil), http.StatusNotFound, "BLOB_UNKNOWN")
}

// shape is what a client sees of a mount that opened a session: everything but the session's own id.
func shape(res response) string {
	h := res.header.Clone()
	for _, k := range []string{"Location", "Docker-Upload-UUID", "Date", "X-Request-Id", "Trove-Request-Id", "X-Trove-Replica"} {
		h.Del(k)
	}
	return fmt.Sprintf("%d %v %s", res.status, h, res.body)
}

// A mount the caller may not read, or that names nothing, falls back to a normal upload: 202, and the answers are the same.
func TestR_OCI_012_AMountFromAnUnreadableRepositoryOpensASessionAndSaysNothingMore(t *testing.T) {
	f := newUploadFixture(t)
	secret := f.seed("oci-secret", "vault", []byte("a layer writer must not learn of"))
	other := f.seed("oci-source", "base", []byte("a layer in a repository only mover reads"))
	puts := f.replicas[2].obj.puts.Load()
	query := func(d, from string) string { return "oci-local/team/app/blobs/uploads/?mount=" + d + "&from=" + from }

	want := f.do(2, "writer", http.MethodPost, query(secret, "it/oci-secret/vault"), nil)
	wantOCI(t, want, http.StatusAccepted, "")
	for name, q := range map[string]string{
		"a repository that does not exist": query(secret, "it/oci-nowhere/vault"),
		"a name the repository lacks":      query(secret, "it/oci-secret/none"),
		"a blob the name lacks":            query(digestOf([]byte("never stored")), "it/oci-secret/vault"),
		"a repository without a grant":     query(other, "it/oci-source/base"),
		"no from":                          "oci-local/team/app/blobs/uploads/?mount=" + secret,
		"a from that is no name":           query(secret, "it/oci-secret/Vault"),
	} {
		got := f.do(0, "writer", http.MethodPost, q, nil)
		wantOCI(t, got, http.StatusAccepted, "")
		if shape(got) != shape(want) {
			t.Errorf("%s:\n%s\nwant\n%s", name, shape(got), shape(want))
		}
	}
	if f.blobItems("team/app/_blobs/"+secret) != 0 || f.blobItems("team/app/_blobs/"+other) != 0 {
		t.Fatal("a refused mount linked a blob")
	}
	// the session it opened is a normal one: the client uploads the bytes it was not given
	id := want.header.Get("Docker-Upload-UUID")
	data := []byte("a layer writer must not learn of")
	path := "oci-local/team/app/blobs/uploads/" + id
	wantOCI(t, f.do(1, "writer", http.MethodPut, path+"?digest="+secret, data), http.StatusCreated, "")
	if n := f.replicas[2].obj.puts.Load() - puts; n != 0 {
		t.Logf("the fallback upload of a small blob put %d objects on replica c (it went through the session)", n)
	}
	// mover may read the source and does not need the bytes
	wantOCI(t, f.do(2, "mover", http.MethodPost, query(other, "it/oci-source/base"), nil), http.StatusCreated, "")
}

// A mount needs deploy on the target: a caller who may read the source and not write the target is refused.
func TestR_OCI_012_AMountNeedsDeployOnTheTarget(t *testing.T) {
	f := newUploadFixture(t)
	d := f.seed("oci-source", "base", []byte("layer"))
	res := f.do(0, "reader", http.MethodPost, "oci-local/team/app/blobs/uploads/?mount="+d+"&from=it/oci-source/base", nil)
	if res.status != http.StatusForbidden && res.status != http.StatusNotFound {
		t.Fatalf("%d %s", res.status, res.body)
	}
	if f.blobItems("team/app/_blobs/"+d) != 0 {
		t.Fatal("a blob was linked")
	}
}

// A mount reads `<name>/_blobs/<digest>`: the digest of a manifest the source holds is no blob of it, so the mount is a session and nothing is
// linked under the target.
func TestR_OCI_012_AManifestDigestIsNoBlobSource(t *testing.T) {
	f := newUploadFixture(t)
	cfg := f.seed("oci-source", "base", []byte("{}"))
	m := ociManifest(cfg)
	d := digestOf(m)
	res := f.replicas[0].do("pusher", http.MethodPut, apiPrefix+"oci-source/base/manifests/"+d, bytes.NewReader(m), "Content-Type", ociCT)
	wantOCI(t, res, http.StatusCreated, "")
	res = f.do(1, "mover", http.MethodPost, "oci-local/team/app/blobs/uploads/?mount="+d+"&from=it/oci-source/base", nil)
	wantOCI(t, res, http.StatusAccepted, "")
	if f.blobItems("team/app/_blobs/"+d) != 0 || f.blobItems("team/app/_manifests/"+d) != 0 {
		t.Fatal("a manifest was linked")
	}
}
