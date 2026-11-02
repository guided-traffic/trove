//go:build integration

package nuget

import (
	"context"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	nugetdata "github.com/guided-traffic/trove/internal/formatdata/nuget"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_NUGET_034_TheStoreReadsTheScanAsOneStatement (Q-NUGET-74): the data port hands the scan of
// a caller who reads row by row to the format a batch at a time from one statement in one
// transaction, in the rank order and with the keys the batches of Search with PerVersion paged by
// offset gave: 1,203 packages arrive as 500, 500 and 203 in one attempt, and a visitor that stops
// ends the read.
func TestR_NUGET_034_TheStoreReadsTheScanAsOneStatement(t *testing.T) {
	st := testenv.NewStack(t, testenv.StackOptions{Name: "nugetscan"})
	st.Repository("nuget-scan", "local", "nuget", "{}")
	const n = 1_203
	v := version.MustParse("1.0.0")
	st.Exec(`INSERT INTO packages (repo_id, name, display_name)
		SELECT r.id, 'acme.p' || lpad(i::text, 5, '0'), 'Acme.P' || lpad(i::text, 5, '0')
		  FROM repositories r, generate_series(1, $2) AS i WHERE r.key = $1`, "nuget-scan", n)
	st.Exec(`INSERT INTO package_versions (package_id, version, display_version)
		SELECT p.id, $2, $3 FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1`, "nuget-scan", v.URLForm(), v.FullNormalized())
	st.Exec(`INSERT INTO nuget_version (version_id, repository_id, lower_id, declared_id, original_version, sort_key,
		                                is_prerelease, is_semver2, listed, published_at, package_types, metadata, search_doc, origin)
		SELECT pv.id, r.id, p.name, p.display_name, '1.0.0', $2, false, false, true, now(), ARRAY['dependency'], '{}'::jsonb,
		       to_tsvector('simple', p.name), 'push'
		  FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
		 WHERE r.key = $1`, "nuget-scan", v.SortKey())

	ctx := context.Background()
	db, err := postgres.Open(ctx, st.DatabaseConfig(), "nuget-scan", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	store, err := nugetdata.New(nugetdata.Options{DB: db, Recorder: audit.Nop{}, Blobs: blobBytes{}, Jobs: jobs.NewQueue(jobs.QueueOptions{})})
	if err != nil {
		t.Fatal(err)
	}
	repoID := st.Number(`SELECT id FROM repositories WHERE key = 'nuget-scan'`)
	query := rows.SearchQuery{RepoID: repoID, KeysOnly: true, PerVersion: true}

	// The keys as the batches paged by offset read them.
	var paged []string
	for off := 0; ; off += 500 {
		q := query
		q.Offset, q.Limit = off, 500
		batch, err := store.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range batch {
			paged = append(paged, h.LowerID)
		}
		if len(batch) < 500 {
			break
		}
	}
	var streamed []string
	var sizes []int
	starts := 0
	err = store.SearchScan(ctx, query, 500, func() { starts, streamed, sizes = starts+1, nil, nil }, func(batch []rows.SearchHit) (bool, error) {
		sizes = append(sizes, len(batch))
		for _, h := range batch {
			if len(h.ListedMatch) != 1 || !h.ListedMatch[0] {
				t.Fatalf("%s carries no match flag: %+v", h.LowerID, h)
			}
			streamed = append(streamed, h.LowerID)
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 || !slices.Equal(sizes, []int{500, 500, 203}) || len(streamed) != n || !slices.Equal(streamed, paged) {
		t.Errorf("%d attempts, batches %v, %d keys (paged %d), same order %v", starts, sizes, len(streamed), len(paged), slices.Equal(streamed, paged))
	}
	visits := 0
	if err := store.SearchScan(ctx, query, 500, func() {}, func([]rows.SearchHit) (bool, error) { visits++; return false, nil }); err != nil || visits != 1 {
		t.Errorf("a visitor that stops: %d visits, %v", visits, err)
	}
}
