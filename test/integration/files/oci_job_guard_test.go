//go:build integration

package files

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	ocidata "github.com/guided-traffic/trove/internal/formatdata/oci"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/packages"
)

// newOCIJobFixture is newOCIFixture with a worker and storage.delete.max_sync_items 1, so every subtree delete is a job.
func newOCIJobFixture(t *testing.T) *ociFixture {
	t.Helper()
	e := newEnv(t)
	e.breakGlass()
	b := formathost.NewBindings()
	f := &ociFixture{e: e}
	deps := formathost.Deps{Bindings: b, Ownership: allowOwnership{}, AutoClaim: noAutoClaim{}, Remover: func() formathost.ItemRemover {
		if f.r != nil && f.r.items != nil {
			return f.r.items
		}
		return nil
	}}
	seam := formathost.NewFilesSeam(func() (formats.Host, error) { return f.host, nil }, deps)
	f.r = e.newReplica("a", replicaOpts{
		worker:  true,
		storage: func(c *config.Storage) { c.Delete.MaxSyncItems, c.Delete.BatchSize = 1, 1 },
		hooks:   formathost.NewHooks(deps), formats: seam,
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
			if f.host, err = formathost.New(formathost.Options{
				Runner: r.db, Decider: r.engine, Items: r.items, Packages: pkgs, Repos: r.reader, Deps: deps, Resolver: proceedResolver{},
			}); err != nil {
				t.Fatal(err)
			}
			disp, err := dispatch.New(dispatch.Options{
				Host: f.host, Decider: r.engine, Repos: r.reader, Formats: func() []formats.Format { return []formats.Format{apiOCI{oci.New()}} },
			})
			if err != nil {
				t.Fatal(err)
			}
			disp.Register(router)
		},
	})
	f.r.createRepository("oci-local", map[string]any{"format": "oci"})
	e.user("writer")
	e.user("deleter")
	e.user("cleaner")
	e.grant(f.r, "oci-write", []string{"oci-local"}, authz.PatternLists{}, map[string][]string{"user:writer": {"read", "deploy"}})
	e.grant(f.r, "oci-del", []string{"oci-local"}, authz.PatternLists{Exclude: []string{"**/_tags/release-*"}}, map[string][]string{"user:deleter": {"read", "delete"}})
	e.grant(f.r, "oci-clean", []string{"oci-local"}, authz.PatternLists{}, map[string][]string{"user:cleaner": {"read", "delete"}})
	waitFor(t, "the policies", 10*time.Second, func() bool {
		return f.push("writer", "probe", "x", nil, "text/plain").status == http.StatusBadRequest
	})
	return f
}

// jobState waits for the newest items-delete job to end and returns its state and error.
func jobState(t *testing.T, f *ociFixture) string {
	t.Helper()
	waitFor(t, "the delete job ends", 60*time.Second, func() bool {
		return f.count(`SELECT count(*) FROM job WHERE kind = 'items-delete' AND state IN ('succeeded', 'failed', 'cancelled')`) > 0
	})
	return f.e.text(`SELECT state::text || ' ' || COALESCE(left(error::text, 200), '') FROM job WHERE kind = 'items-delete' ORDER BY created_at DESC LIMIT 1`)
}

// TestR_OCI_018_ADeleteJobHoldsTheCascadeToTheGrantsOfTheCaller: a delete of `<name>/_manifests` large enough for the job cascades to the
// tags of the name, which lie outside the subtree the caller was checked on; the job decides them for the caller (Q-OCI-76, Q-STOR-190),
// so a deleter whose grant excludes a tag cannot remove it through the job, while a deleter without the exclusion can. The job's error
// names the path the request named, not the excluded tag.
func TestR_OCI_018_ADeleteJobHoldsTheCascadeToTheGrantsOfTheCaller(t *testing.T) {
	f := newOCIJobFixture(t)
	cfg, layer := f.blob("app", []byte("{}")), f.blob("app", []byte("layer"))
	m := ociManifest(cfg, layer)
	wantOCI(t, f.push("writer", "app", "release-1", m, ociCT), 201, "")
	time.Sleep(2 * time.Second)
	if direct := f.r.do("deleter", http.MethodDelete, "/files/it/oci-local/app/_tags/release-1", nil); direct.status != http.StatusForbidden {
		t.Fatalf("the direct delete of the excluded tag: %d", direct.status)
	}
	res := f.r.do("deleter", http.MethodDelete, "/files/it/oci-local/app/_manifests", nil)
	if res.status != http.StatusAccepted {
		t.Fatalf("DELETE app/_manifests: %d %s", res.status, res.body)
	}
	job := jobState(t, f)
	if !strings.HasPrefix(job, "failed") || !strings.Contains(job, "it/oci-local/app/_manifests.") || strings.Contains(job, "release-1") {
		t.Fatalf("the job must fail naming the path the request named, and not the tag it may not remove: %s", job)
	}
	if left := f.count(`SELECT count(*) FROM items WHERE path = 'app/_tags/release-1'`); left != 1 {
		t.Errorf("the excluded tag was removed by the job (%d left)", left)
	}
	if n := f.count(`SELECT count(*) FROM items WHERE path LIKE 'app/_manifests/%'`); n == 0 {
		t.Error("the manifests went although the job failed on the cascade of the first")
	}
	// A deleter whose grant has no exclusion finishes the same delete, tag included.
	if res := f.r.do("cleaner", http.MethodDelete, "/files/it/oci-local/app/_manifests", nil); res.status != http.StatusAccepted {
		t.Fatalf("the cleaner: %d %s", res.status, res.body)
	}
	waitFor(t, "the tag goes with its manifest", 60*time.Second, func() bool {
		return f.count(`SELECT count(*) FROM items WHERE path = 'app/_tags/release-1'`) == 0
	})
}
