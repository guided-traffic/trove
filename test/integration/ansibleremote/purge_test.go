//go:build integration

package ansibleremote

import (
	"context"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/fixup"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestR_BAK_008_TheAnsiblePurgerEmptiesARemoteCacheAndForgetsItsRecordedDigests(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	repo := e.fresh()
	id := e.repoID(repo)
	const file = "v3/artifacts/collections/acme-base-1.0.0.tar.gz"
	if a := e.get(r, repo, file); a.Status != http.StatusOK {
		t.Fatalf("archive: %d %s", a.Status, a.Body)
	}
	count := func(table string) int64 {
		return e.st.Number(`SELECT count(*) FROM `+table+` WHERE repo_id = $1`, id)
	}
	if count("ansible_upstream_versions") != 1 || count("remote_cache_entries") == 0 || count("items") == 0 {
		t.Fatalf("the cache is empty before the purge: %d digests, %d entries, %d items", count("ansible_upstream_versions"), count("remote_cache_entries"), count("items"))
	}

	loaded, err := config.Load(config.Options{Environ: e.st.Environ(), Command: config.CommandAdmin})
	if err != nil {
		t.Fatal(err)
	}
	db, err := postgres.Open(context.Background(), loaded.Config.Database, "ansible-purge", meta.RoleWeb, testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	purge := func() {
		t.Helper()
		for _, p := range fixup.Purgers() {
			if p.Name == "ansible-remote" {
				if err := p.Purge(context.Background(), db, fixup.Env{ExcludeRemoteCaches: true, Items: r.App.Items()}); err != nil {
					t.Fatalf("purge: %v", err)
				}
				return
			}
		}
		t.Fatal("no purger ansible-remote")
	}
	purge()
	if n := count("ansible_upstream_versions"); n != 0 {
		t.Errorf("%d recorded digests left", n)
	}
	if n := count("remote_cache_entries"); n != 0 {
		t.Errorf("%d cache entries left", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND kind = 'file'`, id); n != 0 {
		t.Errorf("%d cached items left", n)
	}
	purge() // repeatable

	// The first request afterwards fetches the document and the archive as on an empty remote and records the digest again.
	before := e.requests("/api/v3/collections/acme/base/versions/1.0.0/")
	want, _ := e.digest("acme.base", "1.0.0")
	if a := e.get(r, repo, file); a.Status != http.StatusOK || sum(a.Body) != want {
		t.Fatalf("archive after the purge: %d", a.Status)
	}
	if got := e.requests("/api/v3/collections/acme/base/versions/1.0.0/"); got != before+1 {
		t.Errorf("%d requests for the version document, want %d", got, before+1)
	}
	if n := count("ansible_upstream_versions"); n != 1 {
		t.Errorf("%d recorded digests after the refill, want 1", n)
	}
}
