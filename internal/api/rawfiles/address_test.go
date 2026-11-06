package rawfiles

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/qname"
)

// A raw-file address is /files/<project>/<key>: the decision is asked for the qualified name, and
// the same key in another project is another repository (R-REPO-001, R-GENERIC-001).
func TestR_REPO_001_TheRawFileAddressNamesProjectAndKey(t *testing.T) {
	var sum blob.Digest
	sum[0] = 1
	file := items.Item{ID: 3, RepoID: 5, Repository: qr("generic-local"), Path: "a.bin", Kind: items.KindFile, Size: 3, Digest: sum, SHA512: make([]byte, 64)}

	d := &decider{}
	h := handler(t, d, &store{file: file, body: []byte("abc")})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/files/it/generic-local/a.bin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(d.keys) != 1 || d.keys[0][0] != (qname.Repo{Project: "it", Key: "generic-local"}) {
		t.Fatalf("the index began for %v", d.keys)
	}

	// The key of another project is a repository that does not exist there.
	d = &decider{}
	rec = httptest.NewRecorder()
	handler(t, d, &store{file: file, body: []byte("abc")}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/files/ops/generic-local/a.bin", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a key of another project: %d", rec.Code)
	}
	if len(d.keys) != 1 || d.keys[0][0] != (qname.Repo{Project: "ops", Key: "generic-local"}) {
		t.Fatalf("the index began for %v", d.keys)
	}
}

// One segment, an escape in a named segment and the cache suffix on the project are no raw-file
// address; nothing asks the decision point or the service (R-ARCH-005).
func TestR_REPO_001_OnlyAThreeSegmentFilesPathIsAnAddress(t *testing.T) {
	for _, path := range []string{
		"/files/generic-local", "/files/generic-local/", "/files/it", "/files/it/",
		"/files/i%74/generic-local/a.bin", "/files/it/generic-loca%6C/a.bin", "/files/it%2Fgeneric-local/a.bin",
		"/files/it%7Ecache/generic-local/a.bin", "/files/It/generic-local/a.bin",
	} {
		d := &decider{}
		rec := httptest.NewRecorder()
		handler(t, d, &store{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound || d.begins != 0 {
			t.Errorf("%s: %d, begins %d", path, rec.Code, d.begins)
		}
	}
}
