//go:build integration

package deblocal

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/config"
	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// captured records the events the data port writes outside a request.
type captured struct{ got []audit.Event }

func (c *captured) RecordTx(_ context.Context, _ meta.Tx, ev audit.Event) error {
	c.got = append(c.got, ev)
	return nil
}
func (c *captured) Record(context.Context, audit.Event) {}

// port opens the data port of the installation beside the replica, the way the replay of a restore uses it.
func port(t *testing.T, e *env) (*debdata.Store, *postgres.DB) {
	t.Helper()
	loaded, err := config.Load(config.Options{Environ: e.st.Environ(), Command: config.CommandAdmin})
	if err != nil {
		t.Fatal(err)
	}
	db, err := postgres.Open(context.Background(), loaded.Config.Database, "deblocal", meta.RoleWeb, testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	st, err := debdata.New(debdata.Options{DB: db, Recorder: &captured{}})
	if err != nil {
		t.Fatal(err)
	}
	return st, db
}

func (e *env) repoID() int64 {
	e.t.Helper()
	return e.st.Number(`SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = $1 AND r.key = $2`, project, repo)
}

// R-DEB-032: a restore withholds a file the journal says was deleted; the bytes are not served and not accepted again until an
// administrator reinstates them, and the replay never deletes.
func TestR_DEB_032_AWithheldFileIsNotServedOrAcceptedUntilReinstated(t *testing.T) {
	e := newEnv(t)
	st, db := port(t, e)
	ctx := context.Background()
	body := build(t, "hello", "2.10-3", nil)
	e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", body, 201, []string{"stable"})
	sum := sha256.Sum256(body)
	id := e.repoID()

	err := db.InTx(ctx, func(tx meta.Tx) error {
		res, err := st.Withhold(ctx, tx, rows.Tombstone{RepoID: id, Path: poolHello, SHA256: sum, DeletedBy: e.alice.ID})
		if res != rows.Applied {
			t.Errorf("withhold = %q", res)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	url := "/api/deb/" + project + "/" + repo + "/" + poolHello
	if a := e.st.Do(e.r, "GET", url, e.tokA, nil); a.Status != 404 {
		t.Errorf("a withheld file = %d", a.Status)
	}
	if a := e.upload(e.tokA, "hello_2.10-3_amd64.deb", body, []string{"testing"}); a.Status != 409 || codeOf(t, a.Body) != "deb_withheld" {
		t.Errorf("the same bytes again = %d %s", a.Status, a.Body)
	}
	if a := e.setCoordinates(e.tokA, poolHello, [2]string{"stable", "main"}, [2]string{"testing", "main"}); a.Status != 409 {
		t.Errorf("coordinates of a withheld file = %d %s", a.Status, a.Body)
	}

	err = db.InTx(ctx, func(tx meta.Tx) error {
		ok, err := st.ReinstateFile(ctx, tx, id, poolHello, sum)
		if !ok {
			t.Error("no mark was removed")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if a := e.st.Do(e.r, "GET", url, e.tokA, nil); a.Status != 200 {
		t.Errorf("a reinstated file = %d", a.Status)
	}
}

// R-DEB-032: the replay of removed coordinates removes them again; a file left without any is withheld, never deleted.
func TestR_DEB_032_ReplayOfRemovedCoordinatesWithholdsTheLastOne(t *testing.T) {
	e := newEnv(t)
	st, db := port(t, e)
	ctx := context.Background()
	body := build(t, "hello", "2.10-3", nil)
	e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", body, 201, []string{"stable", "testing"})
	sum := sha256.Sum256(body)
	id := e.repoID()
	removed := func(cs ...rows.Coordinate) rows.CoordinatesRemoved {
		return rows.CoordinatesRemoved{RepoID: id, RepoKey: project + "/" + repo, Path: poolHello, SHA256: sum, Removed: cs, Actor: e.alice.ID}
	}

	var res rows.ReplayResult
	err := db.InTx(ctx, func(tx meta.Tx) (err error) {
		res, err = st.RemoveCoordinatesByJournal(ctx, tx, removed(rows.Coordinate{Suite: "testing", Component: "main"}))
		return err
	})
	if err != nil || res != rows.Applied {
		t.Fatalf("first replay = %q %v", res, err)
	}
	if got := e.coordinates(poolHello); got != "stable/main" {
		t.Errorf("coordinates = %q", got)
	}

	err = db.InTx(ctx, func(tx meta.Tx) (err error) {
		res, err = st.RemoveCoordinatesByJournal(ctx, tx, removed(rows.Coordinate{Suite: "stable", Component: "main"}))
		return err
	})
	if err != nil || res != rows.Applied {
		t.Fatalf("second replay = %q %v", res, err)
	}
	if n := e.st.Number(`SELECT count(*) FROM deb_binaries b JOIN repositories r ON r.id = b.repo_id WHERE r.key = $1`, repo); n != 1 {
		t.Errorf("the replay deleted the file: %d rows", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM deb_tombstones t JOIN repositories r ON r.id = t.repo_id WHERE r.key = $1`, repo); n != 1 {
		t.Errorf("a file left without coordinates is not withheld: %d marks", n)
	}

	err = db.InTx(ctx, func(tx meta.Tx) (err error) {
		res, err = st.RemoveCoordinatesByJournal(ctx, tx, rows.CoordinatesRemoved{RepoID: id, RepoKey: "x", Path: "pool/h/none/none_1_all.deb", Removed: []rows.Coordinate{{Suite: "a", Component: "main"}}})
		return err
	})
	if err != nil || !strings.Contains(string(res), "unknown") {
		t.Errorf("a file the repository does not hold = %q %v", res, err)
	}
}
