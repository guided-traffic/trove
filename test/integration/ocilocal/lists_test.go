//go:build integration

package ocilocal

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestR_OCI_015_TheTagsListPagesAcrossManyTags: 2,500 tag items of an image name come in pages in byte order, a withheld tag is
// not listed, and a name the repository holds nothing of has no page (R-OCI-015, R-OCI-033).
func TestR_OCI_015_TheTagsListPagesAcrossManyTags(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	repo := e.repoID(repoLocal)
	// One tag through the item store creates the folders and the blob; the others are rows of the same shape.
	body := `{"schemaVersion":2}`
	e.put(repoLocal, "big/_tags/t0000", body, manifest)
	e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size, media_type)
		SELECT repo_id, 'big/_tags/t' || lpad(n::text, 4, '0'), parent_path, kind, blob_sha256, size, media_type
		FROM items, generate_series(1, 2499) AS n WHERE repo_id = $1 AND path = 'big/_tags/t0000'`, repo)
	e.put(repoLocal, "small/_tags/b", body, manifest)
	e.put(repoLocal, "small/_tags/a", body, manifest)
	e.put(repoLocal, "small/_tags/c", `{"other":1}`, manifest)

	var all []string
	after := ""
	pages := 0
	for {
		page, err := e.store.Tags(ctx, repo, "big", after, 1000)
		must(t, err)
		pages++
		all = append(all, page.Tags...)
		if page.Next == "" {
			break
		}
		if page.Next != page.Tags[len(page.Tags)-1] {
			t.Fatalf("Next %q is not the last tag of the page", page.Next)
		}
		after = page.Next
	}
	if pages != 3 || len(all) != 2500 || !slices.IsSorted(all) || all[0] != "t0000" || all[2499] != "t2499" {
		t.Fatalf("%d pages, %d tags, sorted %v", pages, len(all), slices.IsSorted(all))
	}

	t.Run("a page of exactly the remaining tags has no next page", func(t *testing.T) {
		page, err := e.store.Tags(ctx, repo, "small", "", 3)
		must(t, err)
		if strings.Join(page.Tags, ",") != "a,b,c" || page.Next != "" {
			t.Fatalf("%+v", page)
		}
		page, err = e.store.Tags(ctx, repo, "small", "a", 1)
		must(t, err)
		if strings.Join(page.Tags, ",") != "b" || page.Next != "b" {
			t.Fatalf("%+v", page)
		}
		page, err = e.store.Tags(ctx, repo, "small", "c", 5)
		must(t, err)
		if len(page.Tags) != 0 || page.Next != "" {
			t.Fatalf("%+v", page)
		}
	})
	t.Run("another repository and another name hold none of these tags", func(t *testing.T) {
		for _, c := range []struct {
			repo int64
			name string
		}{{e.repoID(repoOther), "small"}, {repo, "smal"}, {repo, "small/_tags"}} {
			page, err := e.store.Tags(ctx, c.repo, c.name, "", 10)
			must(t, err)
			if len(page.Tags) != 0 {
				t.Errorf("%v: %v", c, page.Tags)
			}
		}
	})
	t.Run("a tag whose manifest was withheld is not listed", func(t *testing.T) {
		withheld := sha256.Sum256([]byte(`{"other":1}`))
		e.st.Exec(`INSERT INTO oci_tombstones (repo_id, name, digest) VALUES ($1, 'small', $2)`, repo, withheld[:])
		page, err := e.store.Tags(ctx, repo, "small", "", 10)
		must(t, err)
		if strings.Join(page.Tags, ",") != "a,b" {
			t.Fatalf("%+v", page)
		}
	})
	t.Run("the 404 of a name rests on what the repository holds", func(t *testing.T) {
		e.put(repoLocal, "blobsonly/_blobs/"+dg(sha256.Sum256([]byte("layer"))), "layer", "application/octet-stream")
		for name, want := range map[string]bool{"small": true, "big": true, "blobsonly": true, "nothing": false, "smal": false, "small/x": false} {
			held, err := e.store.Holds(ctx, repo, name)
			must(t, err)
			if held != want {
				t.Errorf("Holds(%q) = %v, want %v", name, held, want)
			}
		}
		if held, err := e.store.Holds(ctx, e.repoID(repoOther), "small"); err != nil || held {
			t.Errorf("another repository holds the name: %v %v", held, err)
		}
	})
}

// TestR_OCI_016_ReferrersAreReadFromTheRowsInDigestOrder: the referrers of a subject, filtered by artifact type, paged by the cursor
// digest, without the manifests of another subject, another name or another repository, and without a withheld one (R-OCI-016).
func TestR_OCI_016_ReferrersAreReadFromTheRowsInDigestOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	repo := e.repoID(repoLocal)
	const sig, sbom = "application/vnd.dev.cosign.artifact.sig.v1+json", "application/spdx+json"
	subject := e.manifestOf(repoLocal, "app", `{"subject":"image"}`, manifest, nil, "")
	other := e.manifestOf(repoLocal, "app", `{"subject":"other"}`, manifest, nil, "")
	var made []image
	for i, at := range []string{sig, sig, sbom, sig} {
		made = append(made, e.manifestOf(repoLocal, "app", fmt.Sprintf(`{"referrer":%d}`, i), manifest, &subject.digest, at))
	}
	e.manifestOf(repoLocal, "app", `{"referrer":"of-other"}`, manifest, &other.digest, sig)
	e.manifestOf(repoLocal, "different", `{"referrer":"other-name"}`, manifest, &subject.digest, sig)
	e.manifestOf(repoOther, "app", `{"referrer":"other-repo"}`, manifest, &subject.digest, sig)

	got, err := e.store.Referrers(ctx, repo, "app", subject.digest, "", nil, 100)
	must(t, err)
	if len(got) != 4 {
		t.Fatalf("%d referrers, want 4", len(got))
	}
	for i := 1; i < len(got); i++ {
		if dg(got[i-1].Digest) >= dg(got[i].Digest) {
			t.Fatalf("not in digest order: %v", got)
		}
	}
	for _, m := range got {
		if m.Subject == nil || *m.Subject != subject.digest || m.MediaType != manifest || m.Name != "app" || m.RepoID != repo {
			t.Errorf("row %+v", m)
		}
	}

	filtered, err := e.store.Referrers(ctx, repo, "app", subject.digest, sig, nil, 100)
	must(t, err)
	if len(filtered) != 3 {
		t.Fatalf("%d signatures, want 3", len(filtered))
	}
	for _, m := range filtered {
		if m.ArtifactType != sig {
			t.Errorf("artifact type %q", m.ArtifactType)
		}
	}

	t.Run("the cursor continues after a digest", func(t *testing.T) {
		first, err := e.store.Referrers(ctx, repo, "app", subject.digest, "", nil, 2)
		must(t, err)
		next, err := e.store.Referrers(ctx, repo, "app", subject.digest, "", &first[1].Digest, 2)
		must(t, err)
		if len(first) != 2 || len(next) != 2 || next[0].Digest != got[2].Digest || next[1].Digest != got[3].Digest {
			t.Fatalf("pages %v / %v", first, next)
		}
		rest, err := e.store.Referrers(ctx, repo, "app", subject.digest, "", &got[3].Digest, 2)
		must(t, err)
		if len(rest) != 0 {
			t.Fatalf("after the last: %v", rest)
		}
	})
	t.Run("a subject nobody refers to has none", func(t *testing.T) {
		none, err := e.store.Referrers(ctx, repo, "app", sha256.Sum256([]byte("nothing")), "", nil, 10)
		must(t, err)
		if len(none) != 0 {
			t.Fatalf("%v", none)
		}
	})
	t.Run("a withheld referrer is not listed", func(t *testing.T) {
		e.st.Exec(`INSERT INTO oci_tombstones (repo_id, name, digest) VALUES ($1, 'app', $2)`, repo, made[0].digest[:])
		after, err := e.store.Referrers(ctx, repo, "app", subject.digest, "", nil, 100)
		must(t, err)
		if len(after) != 3 {
			t.Fatalf("%d referrers, want 3", len(after))
		}
		for _, m := range after {
			if m.Digest == made[0].digest {
				t.Error("the withheld referrer is listed")
			}
		}
	})
}

func TestR_OCI_017_TheCatalogPageOfARepositoryListsTheNamesWithAManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, name := range []string{"team/zeta", "app", "team/app", "app"} {
		e.manifestOf(repoLocal, name, `{"name":"`+name+fmt.Sprint(len(name))+`"}`, manifest, nil, "")
	}
	e.manifestOf(repoLocal, "app", `{"second":"app"}`, manifest, nil, "")
	e.manifestOf(repoOther, "elsewhere", `{"n":"x"}`, manifest, nil, "")
	e.put(repoLocal, "tagless/_blobs/"+dg(sha256.Sum256([]byte("l"))), "l", "application/octet-stream")

	all, err := e.store.CatalogNames(ctx, e.repoID(repoLocal), "", 10)
	must(t, err)
	if strings.Join(all, ",") != "app,team/app,team/zeta" {
		t.Fatalf("%v", all)
	}
	page, err := e.store.CatalogNames(ctx, e.repoID(repoLocal), "app", 1)
	must(t, err)
	if strings.Join(page, ",") != "team/app" {
		t.Fatalf("%v", page)
	}
	if none, err := e.store.CatalogNames(ctx, e.repoID(repoLocal), "team/zeta", 5); err != nil || len(none) != 0 {
		t.Fatalf("%v %v", none, err)
	}
}
