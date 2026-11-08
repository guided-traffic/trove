//go:build integration

package files

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// A mount decides the read of its source as a read does: an exclude pattern that the source repository gained after the blob was pushed
// hides the blob from a mount as it hides it from a pull (R-REPO-005, R-OCI-012). The mount is a session, and nothing is linked.
func TestR_OCI_012_AnExcludePatternAddedAfterThePushHidesTheSourceFromAMount(t *testing.T) {
	f := newUploadFixture(t)
	d := f.seed("oci-source", "vendor/x", []byte("a layer of a name the repository later excludes"))
	pull := "oci-source/vendor/x/blobs/" + d
	for i := range f.replicas {
		if got := f.do(i, "mover", http.MethodGet, pull, nil); got.status != http.StatusOK {
			t.Fatalf("replica %d before the pattern: %d", i, got.status)
		}
	}
	g := f.replicas[0].get("admin", "/api/v1/projects/it/repositories/oci-source")
	res := f.replicas[0].do("admin", http.MethodPatch, "/api/v1/projects/it/repositories/oci-source", strings.NewReader(`{"excludePatterns":["vendor/*"]}`),
		"Content-Type", "application/merge-patch+json", "If-Match", g.header.Get("ETag"))
	if res.status != http.StatusOK {
		t.Fatalf("patch: %d %s", res.status, res.body)
	}
	// every replica hides the blob from a pull first: the mount is held to the same state
	for i := range f.replicas {
		waitFor(t, "the pattern on replica", 15*time.Second, func() bool {
			return f.do(i, "mover", http.MethodGet, pull, nil).status == http.StatusNotFound
		})
	}
	for i := range f.replicas {
		got := f.do(i, "mover", http.MethodPost, "oci-local/team/app/blobs/uploads/?mount="+d+"&from=it/oci-source/vendor/x", nil)
		wantOCI(t, got, http.StatusAccepted, "")
		if got.header.Get("Docker-Upload-UUID") == "" {
			t.Fatalf("replica %d: no session", i)
		}
	}
	if f.blobItems("team/app/_blobs/"+d) != 0 {
		t.Fatal("a source an exclude pattern hides was linked")
	}
}

// The commit hook of a blob holds on the production host, whatever commits the item: bytes that are not the digest in the path, and a
// name that is no digest, are refused by the item store's transaction (Q-OCI-113).
func TestR_OCI_012_TheHookRefusesABlobItemThatIsNotItsDigestsContent(t *testing.T) {
	f := newUploadFixture(t)
	ctx := context.Background()
	r := f.replicas[0]
	repo, err := r.reader.ByName(ctx, qname.Repo{Project: "it", Key: "oci-local"})
	if err != nil {
		t.Fatal(err)
	}
	commit := func(path string, data []byte) error {
		sp, err := r.items.Spool(ctx, bytes.NewReader(data), items.IngestOptions{ContentLength: int64(len(data))})
		if err != nil {
			t.Fatal(err)
		}
		defer sp.Close()
		_, err = r.items.Commit(ctx, items.CommitSpec{
			Repo: repo, Path: repository.Cleaned{Path: path}, Spooled: sp, MediaType: "application/octet-stream", Origin: items.OriginUpload,
		})
		return err
	}
	if err := commit(oci.BlobPath("app", digestOf([]byte("what the digest names"))), []byte("something else")); err == nil {
		t.Fatal("a blob item of other content was committed")
	}
	if err := commit("app/_blobs/not-a-digest", []byte("x")); err == nil {
		t.Fatal("a blob item with a name that is no digest was committed")
	}
	if n := f.count(`SELECT count(*) FROM items WHERE path LIKE 'app/_blobs/%' AND trashed_at IS NULL`); n != 0 {
		t.Fatalf("%d blob items", n)
	}
	if err := commit(oci.BlobPath("app", digestOf([]byte("ok"))), []byte("ok")); err != nil {
		t.Fatalf("the blob of its digest: %v", err)
	}
}

// Beyond the repository's upload limit a chunk is 413 SIZE_INVALID and the session is aborted (R-OCI-011).
func TestR_OCI_011_AChunkBeyondTheUploadLimitIs413AndEndsTheSession(t *testing.T) {
	f := newUploadFixture(t)
	path, _ := f.start(0, "writer", "oci-small", "app")
	wantOCI(t, f.do(1, "writer", http.MethodPatch, path, random(smallLimit/2)), http.StatusAccepted, "")
	res := f.do(2, "writer", http.MethodPatch, path, random(smallLimit))
	wantOCI(t, res, http.StatusRequestEntityTooLarge, "SIZE_INVALID")
	wantOCI(t, f.do(0, "writer", http.MethodGet, path, nil), http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN")
	if m := openMultiparts(t, f); m != 0 {
		t.Fatalf("%d multipart uploads left by the aborted session", m)
	}
	// a monolithic upload beyond the limit stores nothing
	big := random(smallLimit + 1)
	d := digestOf(big)
	wantOCI(t, f.do(1, "writer", http.MethodPost, "oci-small/mono/blobs/uploads/?digest="+d, big), http.StatusRequestEntityTooLarge, "SIZE_INVALID")
	if f.blobItems("mono/_blobs/"+d) != 0 {
		t.Fatal("a blob beyond the limit was stored")
	}
}

// The chunk Docker sends: PATCH with no Content-Length and no Content-Range (a streamed body), then PUT with no body.
func TestR_OCI_011_ADockerStyleStreamedChunkIsAppendedAtTheEnd(t *testing.T) {
	f := newUploadFixture(t)
	data := random(partSize + 4096)
	d := digestOf(data)
	path, _ := f.start(0, "writer", "oci-local", "app")
	// a reader that hides its length makes the client send the body chunked
	res := f.replicas[1].do("writer", http.MethodPatch, apiPrefix+path, io.NopCloser(bytes.NewReader(data)), "Content-Type", "application/octet-stream")
	wantOCI(t, res, http.StatusAccepted, "")
	if res.header.Get("Range") != fmt.Sprintf("0-%d", len(data)-1) {
		t.Fatalf("Range = %q", res.header.Get("Range"))
	}
	wantOCI(t, f.do(2, "writer", http.MethodPut, path+"?digest="+d, nil), http.StatusCreated, "")
	got := f.do(0, "reader", http.MethodGet, "oci-local/app/blobs/"+d, nil)
	if got.status != http.StatusOK || digestOf(got.body) != d {
		t.Fatalf("pull: %d", got.status)
	}
}
