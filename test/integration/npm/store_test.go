//go:build integration

package npm

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/audit"
	npmdata "github.com/guided-traffic/trove/internal/formatdata/npm"
	"github.com/guided-traffic/trove/internal/formats/npm"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The data port the composition root binds is the interface the format declares.
var _ npm.Store = (*npmdata.Store)(nil)

// TestQ_ARCH_193_TheDataPortWritesTagsAndTombstones drives the writes that the work
// package of dist-tags and unpublish calls (R-NPM-016, R-NPM-017, R-NPM-008) against
// PostgreSQL: the tags map is stored as given, modified_at moves forward, a tombstone
// outlives the version row it came from and refuses its number.
func TestQ_ARCH_193_TheDataPortWritesTagsAndTombstones(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "ported", Version: "1.0.0"})
	e.mustPublish(a, "npm-local", e.tokA, pkg{Name: "ported", Version: "1.1.0", Tag: "next"})

	db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "store-test", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	st, err := npmdata.New(npmdata.Options{DB: db, Recorder: audit.Nop{}})
	if err != nil {
		t.Fatal(err)
	}
	var pkgID, repoID, v110 int64
	e.st.Query(`SELECT p.id, p.repo_id, pv.id FROM packages p JOIN package_versions pv ON pv.package_id = p.id WHERE p.name = 'ported' AND pv.version = '1.1.0'`,
		func(r pgx.Rows) error { return r.Scan(&pkgID, &repoID, &v110) })
	_ = v110
	au := rows.Audit{RepoKey: "npm-local", Actor: rows.Actor{Kind: "user", ID: e.alice.ID, Name: "alice"}}
	modified := func() time.Time {
		var ts time.Time
		e.st.Query(`SELECT modified_at FROM npm_packages WHERE package_id = $1`, func(r pgx.Rows) error { return r.Scan(&ts) }, pkgID)
		return ts
	}
	before := modified()
	in := func(fn func(tx meta.Tx) error) {
		t.Helper()
		if err := db.InTx(context.Background(), fn); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("Tags reads what a publish stored", func(t *testing.T) {
		in(func(tx meta.Tx) error {
			tags, err := st.Tags(context.Background(), tx, pkgID)
			if err != nil || tags["latest"] != "1.0.0" || tags["next"] != "1.1.0" {
				t.Fatalf("%v %v", tags, err)
			}
			return nil
		})
	})
	t.Run("SetTag stores the map and moves modified_at forward", func(t *testing.T) {
		in(func(tx meta.Tx) error {
			return st.SetTag(context.Background(), tx, rows.SetTagInput{
				RepoID: repoID, PackageID: pkgID, Name: "ported", Tag: "stable", Version: "1.0.0",
				Tags: map[string]string{"latest": "1.0.0", "next": "1.1.0", "stable": "1.0.0"}, At: time.Now(), Audit: au,
			})
		})
		if tags := e.tags("npm-local", "ported"); tags["stable"] != "1.0.0" || len(tags) != 3 {
			t.Fatalf("tags %v", tags)
		}
		if !modified().After(before) {
			t.Fatal("modified_at did not move")
		}
		// an At in the past still moves it forward by a millisecond
		mid := modified()
		in(func(tx meta.Tx) error {
			return st.DeleteTag(context.Background(), tx, rows.DeleteTagInput{
				RepoID: repoID, PackageID: pkgID, Name: "ported", Tag: "stable", Previous: "1.0.0",
				Tags: map[string]string{"latest": "1.0.0", "next": "1.1.0"}, At: mid.Add(-time.Hour), Audit: au,
			})
		})
		if !modified().After(mid) {
			t.Fatal("modified_at went back")
		}
	})
	t.Run("Tombstone writes the row with the integrity of the version and refuses the number", func(t *testing.T) {
		want := e.st.Text(`SELECT nv.integrity FROM npm_versions nv WHERE nv.version_id = $1`, v110)
		in(func(tx meta.Tx) error {
			got, err := st.Tombstone(context.Background(), tx, rows.TombstoneInput{
				RepoID: repoID, PackageID: pkgID, VersionID: v110, Name: "ported", Version: "1.1.0", Source: "unpublish", By: e.alice.ID,
				Tags: map[string]string{"latest": "1.0.0"}, At: time.Now(), Audit: au,
			})
			if err != nil || got != want {
				t.Fatalf("integrity %q %v, want %q", got, err, want)
			}
			// a second tombstone of the same number keeps the first and returns the same integrity
			again, err := st.Tombstone(context.Background(), tx, rows.TombstoneInput{
				RepoID: repoID, PackageID: pkgID, VersionID: v110, Name: "ported", Version: "1.1.0", Source: "journal", By: e.alice.ID,
				Tags: map[string]string{"latest": "1.0.0"}, At: time.Now(), Audit: au,
			})
			if err != nil || again != want {
				t.Fatalf("again: %q %v", again, err)
			}
			return nil
		})
		if got := e.st.Text(`SELECT source FROM npm_tombstones WHERE name = 'ported' AND version = '1.1.0'`); got != "unpublish" {
			t.Fatalf("source %q", got)
		}
		facts, err := st.VersionFacts(context.Background(), repoID, "ported", "1.1.0")
		if err != nil || !facts.Tombstoned || !facts.Live {
			t.Fatalf("%+v %v", facts, err)
		}
		// the core writer removes the version row afterwards; the tombstone stays
		e.st.Exec(`DELETE FROM package_versions WHERE id = $1`, v110)
		facts, err = st.VersionFacts(context.Background(), repoID, "ported", "1.1.0")
		if err != nil || !facts.Tombstoned || facts.Live {
			t.Fatalf("after the removal: %+v %v", facts, err)
		}
		if ans := e.publish(a, "npm-local", e.tokA, pkg{Name: "ported", Version: "1.1.0", Tag: "next"}); ans.Status != 409 || code(t, ans) != "npm_version_unpublished" {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("VersionFacts of a version that never existed is the zero value", func(t *testing.T) {
		facts, err := st.VersionFacts(context.Background(), repoID, "ported", "9.9.9")
		if err != nil || facts.Live || facts.Tombstoned {
			t.Fatalf("%+v %v", facts, err)
		}
	})
}
