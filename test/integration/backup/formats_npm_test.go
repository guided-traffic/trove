//go:build integration

package backup

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formatdata/npm"
	"github.com/guided-traffic/trove/internal/journal"
)

const acmeUI = "@acme/ui"

func TestR_NPM_047_ReplayWithholdsALiveVersionAndNoRouteServesIt(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	e.publish(r, repoNPMLocal, npmPkg{Name: acmeUI, Version: "1.3.0"})
	integrity := e.publish(r, repoNPMLocal, npmPkg{Name: acmeUI, Version: "1.4.0"})
	tarball := acmeUI + "/-/ui-1.4.0.tgz"

	// Before: both versions are served on every route.
	if vs, tags := e.npmDoc(r, repoNPMLocal, acmeUI); len(vs) != 2 || tags["latest"] != "1.4.0" {
		t.Fatalf("before: versions %v, tags %v", vs, tags)
	}
	for _, c := range []struct{ repo, path string }{{repoNPMLocal, tarball}, {repoNPMAll, tarball}} {
		if a := e.npmGet(r, c.repo, c.path); a.Status != 200 {
			t.Fatalf("before: GET %s/%s = %d", c.repo, c.path, a.Status)
		}
	}
	filesPath := "/files/" + project + "/" + repoNPMLocal + "/" + tarball
	if a := e.st.Do(r, "GET", filesPath, e.tokReader, nil); a.Status != 200 {
		t.Fatalf("before: GET %s = %d", filesPath, a.Status)
	}
	revBefore := e.revSeq(repoNPMLocal, acmeUI)
	usageBefore := e.st.Number(`SELECT COALESCE(sum(logical_bytes), 0) FROM repository_usage`)

	at := time.Now()
	res := e.replay(audit.EventNPMUnpublish, e.unpublishEntry(repoNPMLocal, acmeUI, "1.4.0", integrity), at)
	if res.Outcome != journal.Applied {
		t.Fatalf("outcome %q, want applied: %+v", res.Outcome, res)
	}
	if !strings.Contains(string(res.Detail), "1.4.0") || !strings.Contains(res.Line(), "applied") {
		t.Errorf("the replay output does not name the withheld version: %s / %s", res.Detail, res.Line())
	}

	t.Run("R-NPM-047 the tombstone has the source journal and names the entry", func(t *testing.T) {
		source, got, entry := e.tombstone(repoNPMLocal, acmeUI, "1.4.0")
		if source != "journal" || got != integrity || entry != res.ID {
			t.Errorf("tombstone = %q %q %q, want journal %q %q", source, got, entry, integrity, res.ID)
		}
	})
	t.Run("R-NPM-047 nothing is deleted: the version row and its items stay stored", func(t *testing.T) {
		if n := e.storedVersions(repoNPMLocal, acmeUI); n != 2 {
			t.Errorf("%d version rows, want 2", n)
		}
		if n := e.items(repoNPMLocal, tarball); n != 1 {
			t.Errorf("%d tarball items of 1.4.0, want 1", n)
		}
		if n := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'npm.unpublish'`); n != 0 {
			t.Errorf("the replay wrote %d npm.unpublish events", n)
		}
		if usage := e.st.Number(`SELECT COALESCE(sum(logical_bytes), 0) FROM repository_usage`); usage != usageBefore {
			t.Errorf("storage usage %d -> %d: a withheld version counts toward the repository like a live one", usageBefore, usage)
		}
	})
	t.Run("R-NPM-047 the document omits the version, latest moved and rev_seq grew", func(t *testing.T) {
		vs, tags := e.npmDoc(r, repoNPMLocal, acmeUI)
		if len(vs) != 1 || vs[0] != "1.3.0" || tags["latest"] != "1.3.0" {
			t.Errorf("versions %v, tags %v", vs, tags)
		}
		if got := e.revSeq(repoNPMLocal, acmeUI); got != revBefore+1 {
			t.Errorf("rev_seq %d -> %d, want +1", revBefore, got)
		}
		if a := e.npmGet(r, repoNPMLocal, acmeUI+"/1.4.0"); a.Status != 404 {
			t.Errorf("the version document answers %d, want 404", a.Status)
		}
	})
	t.Run("R-NPM-047 the tarball answers 404 on the npm route, through the virtual repository and on /files", func(t *testing.T) {
		for _, c := range []struct{ repo, path string }{{repoNPMLocal, tarball}, {repoNPMAll, tarball}} {
			if a := e.npmGet(r, c.repo, c.path); a.Status != 404 {
				t.Errorf("GET %s/%s = %d %.200s, want 404", c.repo, c.path, a.Status, a.Body)
			}
		}
		if a := e.st.Do(r, "GET", filesPath, e.tokReader, nil); a.Status != 404 {
			t.Errorf("GET %s = %d, want 404", filesPath, a.Status)
		}
		// The lower version is not affected.
		if a := e.npmGet(r, repoNPMAll, acmeUI+"/-/ui-1.3.0.tgz"); a.Status != 200 {
			t.Errorf("1.3.0 through npm-all = %d", a.Status)
		}
	})
	t.Run("R-NPM-047 the virtual repository takes no version from the member", func(t *testing.T) {
		vs, tags := e.npmDoc(r, repoNPMAll, acmeUI)
		if len(vs) != 1 || vs[0] != "1.3.0" || tags["latest"] != "1.3.0" {
			t.Errorf("npm-all lists %v, tags %v", vs, tags)
		}
	})
	t.Run("R-NPM-047 R-NPM-008 publishing the number again answers 409 npm_version_unpublished", func(t *testing.T) {
		a, _ := e.tryPublish(r, repoNPMLocal, npmPkg{Name: acmeUI, Version: "1.4.0"})
		var b struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(a.Body, &b)
		if a.Status != http.StatusConflict || b.Code != "npm_version_unpublished" {
			t.Errorf("republish = %d %s", a.Status, a.Body)
		}
	})
	t.Run("R-NPM-047 a second replay of the entry is already in effect", func(t *testing.T) {
		rev := e.revSeq(repoNPMLocal, acmeUI)
		again := e.replay(audit.EventNPMUnpublish, e.unpublishEntry(repoNPMLocal, acmeUI, "1.4.0", integrity), at)
		if again.Outcome != journal.AlreadyInEffect || e.revSeq(repoNPMLocal, acmeUI) != rev {
			t.Errorf("second replay = %q, rev_seq %d -> %d", again.Outcome, rev, e.revSeq(repoNPMLocal, acmeUI))
		}
	})

	t.Run("R-NPM-047 reinstate brings back the same bytes and writes npm.reinstate", func(t *testing.T) {
		store, err := npm.New(npm.Options{DB: e.db, Recorder: e.recorder()})
		if err != nil {
			t.Fatal(err)
		}
		rev := e.revSeq(repoNPMLocal, acmeUI)
		got, err := store.Reinstate(context.Background(), npm.ReinstateInput{
			Project: project, Repository: repoNPMLocal, Name: acmeUI, Version: "1.4.0", Actor: audit.Actor{Kind: "admin-cli", Name: "root@restore"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Integrity != integrity || got.JournalEntry != res.ID {
			t.Errorf("reinstated %+v, want integrity %s entry %s", got, integrity, res.ID)
		}
		if source, _, _ := e.tombstone(repoNPMLocal, acmeUI, "1.4.0"); source != "" {
			t.Errorf("the tombstone is still there (source %q)", source)
		}
		if n := e.revSeq(repoNPMLocal, acmeUI); n != rev+1 {
			t.Errorf("rev_seq %d -> %d, want +1", rev, n)
		}
		vs, _ := e.npmDoc(r, repoNPMLocal, acmeUI)
		if len(vs) != 2 {
			t.Errorf("versions %v", vs)
		}
		a := e.npmGet(r, repoNPMLocal, tarball)
		if a.Status != 200 || integrityOf(a.Body) != integrity {
			t.Errorf("tarball %d, integrity %s, want 200 and %s", a.Status, integrityOf(a.Body), integrity)
		}
		if a := e.npmGet(r, repoNPMAll, tarball); a.Status != 200 {
			t.Errorf("through npm-all = %d", a.Status)
		}
		details := e.st.Text(`SELECT details::text FROM audit_event WHERE action = 'npm.reinstate'`)
		for _, want := range []string{integrity, res.ID, "1.4.0"} {
			if !strings.Contains(details, want) {
				t.Errorf("npm.reinstate details %s lack %s", details, want)
			}
		}
		// The tombstone is gone: reinstating again finds nothing to reinstate.
		_, err = store.Reinstate(context.Background(), npm.ReinstateInput{Project: project, Repository: repoNPMLocal, Name: acmeUI, Version: "1.4.0"})
		if errs.KindOf(err) != errs.NotFound {
			t.Errorf("a second reinstate = %v, want not found", err)
		}
	})
}
