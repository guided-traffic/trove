//go:build integration

package nuget

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	nugetdata "github.com/guided-traffic/trove/internal/formatdata/nuget"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_NUGET_030_TheVersionListReadsItsRevisionWithItsRows: the data port reads the version list of
// the flat container together with packages.rev_seq in one statement (F-23), so the local version
// list is validated and cached under the revision of the rows it shows, as the registrations are.
// Every stored version is listed, listed or not, ascending; an unlist raises the revision the next
// read returns; an ID without versions reads as revision 0 with none.
func TestR_NUGET_030_TheVersionListReadsItsRevisionWithItsRows(t *testing.T) {
	e := newWriteEnv(t)
	r := e.replica("web-a")
	for _, v := range []string{"1.0.0", "1.10.0", "1.2.0"} {
		e.mustPush(r, "nuget-local", e.tokDev, nupkg(t, pkgSpec{id: "Acme.Flat", version: v}))
	}
	ctx := context.Background()
	db, err := postgres.Open(ctx, e.st.DatabaseConfig(), "nuget-flat", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	st, err := nugetdata.New(nugetdata.Options{DB: db, Recorder: audit.Nop{}, Blobs: blobBytes{}, Jobs: jobs.NewQueue(jobs.QueueOptions{})})
	if err != nil {
		t.Fatal(err)
	}
	repoID := e.st.Number(`SELECT id FROM repositories WHERE key = 'nuget-local'`)
	check := func(when string) {
		t.Helper()
		idx, err := st.FlatIndex(ctx, repoID, "acme.flat")
		if err != nil {
			t.Fatal(err)
		}
		if want := e.rev("nuget-local", "acme.flat"); idx.Rev != want || want == 0 {
			t.Errorf("%s: revision %d, packages.rev_seq %d", when, idx.Rev, want)
		}
		var got []string
		for _, v := range idx.Versions {
			got = append(got, v.LowerVersion)
		}
		if len(got) != 3 || got[0] != "1.0.0" || got[1] != "1.2.0" || got[2] != "1.10.0" {
			t.Errorf("%s: versions %v, want 1.0.0 1.2.0 1.10.0", when, got)
		}
	}
	check("after the pushes")
	if ans := e.send(r, "DELETE", "nuget-local", "acme.flat/1.2.0", e.tokDev); ans.Status != 204 {
		t.Fatalf("unlist: %d %s", ans.Status, ans.Body)
	}
	check("after an unlist")
	if idx, err := st.FlatIndex(ctx, repoID, "acme.none"); err != nil || idx.Rev != 0 || len(idx.Versions) != 0 {
		t.Errorf("an ID without versions: %+v, %v", idx, err)
	}
}
