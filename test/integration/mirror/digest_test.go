//go:build integration

package mirror

import (
	"testing"

	"github.com/guided-traffic/trove/test/npmstub"
)

// A mirror pass fills through the same engine request as a client (R-REPO-041): npm compares the digest its document announces
// whatever the remote's checksum policy says, and a tampered tarball fails its package at once instead of keeping the pass busy
// for the retries of a transient failure (R-REPO-053 step 3). The repository is a row of the seed, so the pass-through policy,
// which the API refuses for npm (Q-NPM-79), reaches the engine.
func TestR_REPO_041_AMirrorPassOfAnNpmRemoteStoresNoTarballThatDoesNotMatchItsAnnouncedIntegrityAndEnds(t *testing.T) {
	r := newRig(t, rigOptions{public: true, include: []string{"@acme/*"}, remote: map[string]any{
		"integrity": map[string]any{"checksumPolicy": "pass-through", "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
	}})
	r.publish("@acme/a", "1.0.0")
	r.publish("@acme/b", "1.0.0")
	r.setStub(npmstub.Mode{BadIntegrity: []string{"@acme/a"}})
	run := r.runner("runner")

	at := r.pass(run)
	r.waitPass(at, "full", "partial", passWithin)
	if r.storedItem(tarballPath("@acme/a", "1.0.0")) {
		t.Error("the tarball whose bytes do not match the announced integrity was stored")
	}
	if !r.storedItem(tarballPath("@acme/b", "1.0.0")) {
		t.Error("the correct tarball of the other package was not stored")
	}
	if st := r.summary(); st["packagesFailed"] != float64(1) || st["itemsStored"] != float64(1) {
		t.Errorf("summary %v: want one package failed and one item stored", st)
	}
	if n := r.stub.Counts().Tarballs("@acme/a"); n != 1 {
		t.Errorf("the tampered tarball was fetched %d times in the pass, want 1", n)
	}
}
