//go:build integration

package npm

import (
	"strings"
	"testing"
)

// TestR_ARCH_009_ProvenancePublishIsOneTransaction: the tarball and the provenance
// bundle of one publish are written by one transaction (R-ARCH-009, R-NPM-006): both
// items carry the same transaction id, and a bundle that cannot be stored leaves no
// version behind, where two transactions would have left the published version and
// answered 500.
func TestR_ARCH_009_ProvenancePublishIsOneTransaction(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	bundle := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle+json;version=0.3"}`)

	t.Run("R-ARCH-009 the tarball and the bundle of a publish share one transaction", func(t *testing.T) {
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/atomic", Version: "1.0.0", Sigstore: bundle})
		if n := e.st.Number(`SELECT count(DISTINCT i.xmin::text) FROM items i JOIN repositories r ON r.id = i.repo_id
			WHERE r.key = 'npm-local' AND i.path IN ('@acme/atomic/-/atomic-1.0.0.tgz', '@acme/atomic/-/atomic-1.0.0.sigstore')`); n != 1 {
			t.Fatalf("the two items were written by %d transactions", n)
		}
		if n := e.st.Number(`SELECT count(DISTINCT created_at) FROM items WHERE path IN ('@acme/atomic/-/atomic-1.0.0.tgz', '@acme/atomic/-/atomic-1.0.0.sigstore')`); n != 1 {
			t.Fatalf("the two items were created at %d different transaction times", n)
		}
		// One transaction raises the revision once (Q-ARCH-212): a bundle adds no second revision.
		e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "@acme/plain", Version: "1.0.0"})
		if with, without := e.revSeq("npm-local", "@acme/atomic"), e.revSeq("npm-local", "@acme/plain"); with != without {
			t.Fatalf("rev_seq of a publish with a bundle is %d, without it %d", with, without)
		}
	})

	t.Run("R-ARCH-009 a bundle that cannot be stored leaves no version, no tarball and no package rows", func(t *testing.T) {
		// Other content at the bundle's path, with no version to own it: the second file of
		// the commit is refused after the tarball's hook has run.
		e.st.Exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size)
			VALUES (decode(lpad('ab', 64, '0'), 'hex'), decode(lpad('ab', 40, '0'), 'hex'), decode(lpad('ab', 32, '0'), 'hex'), decode(lpad('ab', 128, '0'), 'hex'), 9)`)
		e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, explicit_folder)
			SELECT id, '@acme/blocked', '', 'folder', true FROM repositories WHERE key = 'npm-local'`)
		e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, explicit_folder)
			SELECT id, '@acme/blocked/-', '@acme/blocked', 'folder', true FROM repositories WHERE key = 'npm-local'`)
		e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size)
			SELECT id, '@acme/blocked/-/blocked-1.0.0.sigstore', '@acme/blocked/-', 'file', decode(lpad('ab', 64, '0'), 'hex'), 9
			  FROM repositories WHERE key = 'npm-local'`)
		published := e.events("artifact.deployed", "@acme/blocked/-/blocked-1.0.0.tgz", "")

		ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "@acme/blocked", Version: "1.0.0", Sigstore: bundle})
		if ans.Status < 400 {
			t.Fatalf("publish %d %s", ans.Status, ans.Body)
		}
		if ans.Status != 403 || !strings.Contains(string(ans.Body), "blocked-1.0.0.sigstore") {
			t.Errorf("the refusal does not name the path that holds the other content: %d %s", ans.Status, ans.Body)
		}
		if n := e.itemCount("npm-local", "@acme/blocked/-/blocked-1.0.0.tgz"); n != 0 {
			t.Errorf("the tarball of a publish whose bundle failed is stored (%d)", n)
		}
		if n := e.versions("npm-local", "@acme/blocked"); n != 0 {
			t.Errorf("%d versions were published", n)
		}
		if e.revSeq("npm-local", "@acme/blocked") != 0 {
			t.Error("a package row was left")
		}
		if got := e.events("artifact.deployed", "@acme/blocked/-/blocked-1.0.0.tgz", ""); got != published {
			t.Errorf("a deployed event was recorded for the rolled-back tarball")
		}
		if e.tombstones("npm-local", "@acme/blocked") != 0 {
			t.Error("a tombstone was left")
		}
	})
}
