//go:build integration

package nuget

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"  //nolint:gosec // the blobs table keeps an MD5 beside the SHA-256
	"crypto/sha1" //nolint:gosec // the blobs table keeps a SHA-1 beside the SHA-256
	"crypto/sha256"
	"crypto/sha512"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	nugetdata "github.com/guided-traffic/trove/internal/formatdata/nuget"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// blobBytes serves the content of blobs by digest, the way the blob store does for a ranged read.
type blobBytes map[string][]byte

func (b blobBytes) ReaderAt(_ context.Context, sha []byte, size int64) io.ReaderAt {
	data := b[string(sha)]
	if int64(len(data)) != size {
		return bytes.NewReader(nil)
	}
	return bytes.NewReader(data)
}

// nupkgWith builds a .nupkg that holds a .nuspec and the given files.
func nupkgWith(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := []string{"Acme.Widgets.nuspec"}
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names[1:])
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		body := files[n]
		if n == "Acme.Widgets.nuspec" {
			body = []byte(`<?xml version="1.0"?><package><metadata><id>Acme.Widgets</id><version>1.0.0</version></metadata></package>`)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestQ_NUGET_48_TheDataPortReadsTheViewFromPostgreSQL drives the reads of the package view against the NuGet
// tables: the summaries, the details with the README out of the stored package, the two searches, and the
// versions a repository serves, in the order the contract defines (R-API-029, R-EP-016, Q-NUGET-48).
func TestQ_NUGET_48_TheDataPortReadsTheViewFromPostgreSQL(t *testing.T) {
	e := newEnv(t)
	db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "nuget-view", "web", testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	readme := nupkgWith(t, map[string][]byte{"docs/README.md": []byte("# Widgets\n"), "docs/binary.md": {0xff, 0xfe, 0x00, 0xc3, 0x28}})
	digest := func(b []byte) []byte { s := sha256.Sum256(b); return s[:] }
	blobs := blobBytes{string(digest(readme)): readme}
	st, err := nugetdata.New(nugetdata.Options{DB: db, Recorder: audit.Nop{}, Blobs: blobs, Jobs: jobs.NewQueue(jobs.QueueOptions{})})
	if err != nil {
		t.Fatal(err)
	}

	repoID := e.st.Number(`SELECT id FROM repositories WHERE key = 'nuget-local'`)
	genericID := e.st.Number(`SELECT id FROM repositories WHERE key = 'gen-local'`)
	e.st.Exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size) VALUES ($1, $2, $3, $4, $5)`,
		digest(readme), sum1(readme), sumMD5(readme), sum512(readme), int64(len(readme)))
	itemID := e.st.Number(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, 'acme.widgets/1.0.0/acme.widgets.1.0.0.nupkg', 'acme.widgets/1.0.0', 'file', $2, $3) RETURNING id`,
		repoID, digest(readme), int64(len(readme)))

	// version inserts a version of a package with its NuGet row and returns the version id.
	version := func(repo int64, pkg, v, metadata string, published time.Time, listed, trashed bool, lead int64) int64 {
		t.Helper()
		pkgID := e.st.Number(`INSERT INTO packages (repo_id, name, display_name) VALUES ($1, $2, $2) ON CONFLICT (repo_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, repo, pkg)
		id := e.st.Number(`INSERT INTO package_versions (package_id, version, display_version, trashed_at, lead_item_id) VALUES ($1, $2, $2, CASE WHEN $3::boolean THEN now() END, NULLIF($4::bigint, 0)) RETURNING id`, pkgID, v, trashed, lead)
		e.st.Exec(`INSERT INTO nuget_version (version_id, repository_id, lower_id, declared_id, original_version, sort_key, is_prerelease, is_semver2, listed, published_at, metadata, search_doc, origin)
		           VALUES ($1, $2, $3, 'Acme.Widgets', $4, '\x01', false, false, $5, $6, $7::jsonb, to_tsvector('simple', $3), 'push')`,
			id, repo, pkg, v, listed, published, metadata)
		return id
	}
	at := func(h int) time.Time { return time.Date(2026, 5, 1, h, 0, 0, 0, time.UTC) }
	full := `{"description": "Acme widgets for Contoso", "tags": ["widgets", "contoso"], "readme": "docs/README.md", "license": {"type": "expression", "value": "MIT"}}`
	v1 := version(repoID, "acme.widgets", "1.0.0", full, at(9), true, false, itemID)
	v2 := version(repoID, "acme.widgets", "1.10.0", `{"summary": "Only a summary"}`, at(3), false, false, 0)
	v3 := version(repoID, "acme.widgets", "1.2.0", `{"description": "  ", "summary": "Second"}`, at(5), true, false, 0)
	trashed := version(repoID, "acme.widgets", "9.9.9", full, at(11), true, true, 0)
	other := version(repoID, "acme.other", "1.0.0", `{"description": "Contoso too", "tags": ["contoso"], "readme": "docs/missing.md"}`, at(1), true, false, itemID)
	binary := version(repoID, "acme.binary", "1.0.0", `{"description": "x", "readme": "docs/binary.md"}`, at(1), true, false, itemID)
	ctx := context.Background()

	t.Run("the summaries are the description, the summary as a fallback, and the tags", func(t *testing.T) {
		got, err := st.ViewSummaries(ctx, []int64{v1, v2, v3, trashed, 999999})
		if err != nil {
			t.Fatal(err)
		}
		want := map[int64]string{v1: "Acme widgets for Contoso", v2: "Only a summary", v3: "Second"}
		if len(got) != len(want) {
			t.Fatalf("%+v", got)
		}
		for _, s := range got {
			if s.Description != want[s.VersionID] {
				t.Errorf("version %d: %q, want %q", s.VersionID, s.Description, want[s.VersionID])
			}
			if s.VersionID == v1 && !slices.Equal(s.Tags, []string{"widgets", "contoso"}) {
				t.Errorf("tags %v", s.Tags)
			}
			if s.VersionID == v2 && len(s.Tags) != 0 {
				t.Errorf("tags of a version without: %v", s.Tags)
			}
		}
	})
	t.Run("the details carry the metadata, the listing state and the declared ID", func(t *testing.T) {
		got, err := st.ViewDetails(ctx, []int64{v1, v2}, false)
		if err != nil || len(got) != 2 {
			t.Fatalf("%+v %v", got, err)
		}
		for _, d := range got {
			if d.DeclaredID != "Acme.Widgets" || d.Readme != "" || d.ReadmeFile != "" || len(d.Metadata) == 0 || d.Listed != (d.VersionID == v1) {
				t.Errorf("%+v", d)
			}
		}
	})
	t.Run("the README is read out of the stored package, only when asked for", func(t *testing.T) {
		got, err := st.ViewDetails(ctx, []int64{v1, v2, other, binary}, true)
		if err != nil || len(got) != 4 {
			t.Fatalf("%+v %v", got, err)
		}
		for _, d := range got {
			switch d.VersionID {
			case v1:
				if d.Readme != "# Widgets\n" || d.ReadmeFile != "docs/README.md" {
					t.Errorf("%+v", d)
				}
			case v2:
				if d.Readme != "" || d.ReadmeFile != "" {
					t.Errorf("a version without a README file: %+v", d)
				}
			case other:
				if d.Readme != "" || d.ReadmeFile != "docs/missing.md" {
					t.Errorf("a README the package does not hold is no README: %+v", d)
				}
			case binary:
				if d.Readme != "" {
					t.Errorf("a README that is no UTF-8 text is no README: %q", d.Readme)
				}
			}
		}
		none, err := nugetdata.New(nugetdata.Options{DB: db, Recorder: audit.Nop{}, Jobs: jobs.NewQueue(jobs.QueueOptions{})})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := none.ViewDetails(ctx, []int64{v1}, true); err != nil || len(got) != 1 || got[0].Readme != "" {
			t.Errorf("without a blob reader: %+v %v", got, err)
		}
	})
	t.Run("the searches match the description and the tags, in the repositories asked", func(t *testing.T) {
		ids := func(repos []string, text, keyword string, limit int) []int64 {
			t.Helper()
			got, err := st.ViewFind(ctx, repos, text, keyword, limit)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
		if got := ids([]string{"it/nuget-local"}, "CONTOSO", "", 10); !slices.Equal(got, []int64{v1, other}) {
			t.Errorf("text: %v", got)
		}
		if got := ids([]string{"it/nuget-local"}, "summary", "", 10); !slices.Equal(got, []int64{v2}) {
			t.Errorf("the summary is searched when there is no description: %v", got)
		}
		if got := ids([]string{"it/nuget-local"}, "", "contoso", 10); !slices.Equal(got, []int64{v1, other}) {
			t.Errorf("keyword: %v", got)
		}
		if got := ids([]string{"it/nuget-local"}, "", "cont", 10); len(got) != 0 {
			t.Errorf("a keyword is a whole tag: %v", got)
		}
		if got := ids([]string{"it/gen-local"}, "CONTOSO", "", 10); len(got) != 0 {
			t.Errorf("another repository: %v", got)
		}
		if got := ids([]string{"it/nuget-local"}, "contoso", "", 1); len(got) != 1 {
			t.Errorf("limit: %v", got)
		}
		if _, err := st.ViewFind(ctx, []string{"nuget-local"}, "a", "b", 1); err == nil {
			t.Error("a search for a text and a keyword at once is refused")
		}
	})
	t.Run("a hosted repository lists what it serves newest arrival first, unlisted versions included", func(t *testing.T) {
		got, err := st.ViewStored(ctx, repoID, "acme.widgets", false, 10)
		if err != nil || !slices.Equal(got, []string{"1.2.0", "1.10.0", "1.0.0"}) {
			t.Errorf("%v %v", got, err)
		}
		if got, _ := st.ViewStored(ctx, repoID, "acme.widgets", false, 2); !slices.Equal(got, []string{"1.2.0", "1.10.0"}) {
			t.Errorf("limit: %v", got)
		}
		if got, _ := st.ViewStored(ctx, repoID, "acme.widgets", false, 0); len(got) != 0 {
			t.Errorf("limit 0: %v", got)
		}
		if got, _ := st.ViewStored(ctx, genericID, "acme.widgets", false, 10); len(got) != 0 {
			t.Errorf("another repository: %v", got)
		}
		if got, _ := st.ViewStored(ctx, repoID, "ghost", false, 10); len(got) != 0 {
			t.Errorf("unknown package: %v", got)
		}
	})
	t.Run("a remote repository lists its cache newest published first", func(t *testing.T) {
		got, err := st.ViewStored(ctx, repoID, "acme.widgets", true, 10)
		if err != nil || !slices.Equal(got, []string{"1.0.0", "1.2.0", "1.10.0"}) {
			t.Errorf("%v %v", got, err)
		}
	})
}

func sum1(b []byte) []byte   { s := sha1.Sum(b); return s[:] } //nolint:gosec // the digest the table keeps
func sumMD5(b []byte) []byte { s := md5.Sum(b); return s[:] }  //nolint:gosec // the digest the table keeps
func sum512(b []byte) []byte { s := sha512.Sum512(b); return s[:] }
