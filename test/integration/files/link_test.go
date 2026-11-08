//go:build integration

package files

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/servefacts"
)

// TestR_STOR_008_AStoredBlobIsLinkedWithoutABody: the item store commits an item for content the blob store holds, in the
// transaction that holds the blob row's lock, with the item row, the storage hook and the audit row of a commit, and no object
// write (ADR-161); a digest that names no blob, and a blob that is being collected, are errs.NotFound.
func TestR_STOR_008_AStoredBlobIsLinkedWithoutABody(t *testing.T) {
	f := newCommitFixture(t)
	ctx := context.Background()
	src, err := f.commit(t, "pkg/src.txt", "linked content", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.log.take()
	repo, err := f.r.reader.ByName(ctx, qr("generic-local"))
	if err != nil {
		t.Fatal(err)
	}
	digests := func(it items.Item) blob.Digests {
		d := blob.Digests{SHA256: it.Digest, Size: it.Size}
		copy(d.SHA1[:], it.SHA1)
		copy(d.MD5[:], it.MD5)
		copy(d.SHA512[:], it.SHA512)
		return d
	}
	link := func(path string, d blob.Digests, mut func(*items.CommitSpec)) (items.CommitResult, error) {
		spec := items.CommitSpec{
			Repo: repo, Path: repository.Cleaned{Path: path}, MediaType: "text/plain",
			Origin: items.OriginUpload, MayOverwrite: true, Actor: audit.Actor{Kind: "user", Name: "alice"},
		}
		if mut != nil {
			mut(&spec)
		}
		return f.r.items.CommitLinked(ctx, spec, d)
	}

	t.Run("an item is linked to the stored blob with its hook and audit row", func(t *testing.T) {
		res, err := link("pkg/copy.txt", digests(src.Item), func(s *items.CommitSpec) { s.Extra = f.extraRow("l") })
		if err != nil || !res.Created {
			t.Fatalf("%+v %v", res, err)
		}
		if got := f.log.take(); len(got) != 3 || got[0] != "extra" || got[1] != "hook" || got[2] != "audit:artifact.deployed" {
			t.Fatalf("order = %v", got)
		}
		if res.Item.Digest != src.Item.Digest || res.Item.Size != src.Item.Size || res.Item.Path != "pkg/copy.txt" {
			t.Errorf("item = %+v", res.Item)
		}
		if n := f.e.number("SELECT count(DISTINCT blob_sha256) FROM items WHERE path IN ('pkg/src.txt', 'pkg/copy.txt')"); n != 1 {
			t.Errorf("the two items hold %d blobs", n)
		}
		ev := f.hooks.events()
		if last := ev[len(ev)-1]; last.Item.Path != "pkg/copy.txt" || last.Digests.SHA256 != src.Item.Digest || last.Digests.Size != src.Item.Size {
			t.Errorf("event = %+v", last)
		}
	})

	t.Run("the same link again changes nothing", func(t *testing.T) {
		f.log.take()
		res, err := link("pkg/copy.txt", digests(src.Item), nil)
		if err != nil || res.Created || res.Overwritten {
			t.Fatalf("%+v %v", res, err)
		}
		for _, s := range f.log.take() {
			if s == "hook" {
				t.Error("the hook ran for an identical link")
			}
		}
	})

	t.Run("CreateOnly refuses a path that exists", func(t *testing.T) {
		if _, err := link("pkg/copy.txt", digests(src.Item), func(s *items.CommitSpec) { s.CreateOnly = true }); err == nil {
			t.Fatal("linked over an existing path")
		}
	})

	t.Run("a digest that names no blob is not found and leaves no item", func(t *testing.T) {
		var d blob.Digests
		d.SHA256[0], d.Size = 0xfe, 5
		if _, err := link("pkg/none.txt", d, nil); errs.KindOf(err) != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
		if n := f.e.number("SELECT count(*) FROM items WHERE path = 'pkg/none.txt'"); n != 0 {
			t.Errorf("%d items", n)
		}
	})

	t.Run("a blob that is being deleted is not found", func(t *testing.T) {
		other, err := f.commit(t, "pkg/doomed.txt", "doomed content", nil)
		if err != nil {
			t.Fatal(err)
		}
		f.e.exec("UPDATE blobs SET state = 'deleting' WHERE sha256 = $1", other.Item.Digest[:])
		if _, err := link("pkg/doomed-copy.txt", digests(other.Item), nil); errs.KindOf(err) != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("companions are refused", func(t *testing.T) {
		_, err := link("pkg/c.txt", digests(src.Item), func(s *items.CommitSpec) {
			s.Companions = []items.Companion{{Path: repository.Cleaned{Path: "pkg/c2.txt"}}}
		})
		if errs.KindOf(err) != errs.Internal {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestR_STOR_008_TheHostLinksFromASourceItNamesByRepository: through the host of a format, a source named by its repository and path is
// resolved by the host, decided as a read of the caller on that repository, and linked at another path with the same blob; a caller
// without the read, and a repository or path that does not exist, get errs.NotFound and no item (ADR-161).
func TestR_STOR_008_TheHostLinksFromASourceItNamesByRepository(t *testing.T) {
	f := newFormatsFixture(t)
	ctx := context.Background()
	if res := f.publish("writer", "left-pad", "1.0.0"); res.status != 201 && res.status != 200 {
		t.Fatalf("publish: %d %s", res.status, res.body)
	}
	const src = "left-pad/-/left-pad-1.0.0.tgz"
	target, err := f.r.reader.ByName(ctx, qr("probe-local"))
	if err != nil {
		t.Fatal(err)
	}
	link := func(user, repo, path, to string) error {
		p := f.e.people[user]
		rq := &formats.Request{
			Request: httptest.NewRequest(http.MethodPut, "/api/probe/it/probe-local/x", nil), Repo: &target,
			Route:   &formats.Route{Name: "probe.file.put", Kind: formats.Upload},
			Subject: authz.Subject{Kind: p.Kind, ID: p.ID, Name: p.Name, Groups: p.Groups, Endpoint: "default"},
		}
		defer f.host.Begin(rq)()
		_, err := f.host.Ingest().Link(ctx, rq, formats.SourceRef{Repo: qr(repo), Path: path},
			formats.Commit{Path: formats.Path{Path: to}, MediaType: "application/octet-stream", MayOverwrite: true})
		return err
	}
	if err := link("writer", "probe-local", src, "left-pad/-/copy-1.0.0.tgz"); err != nil {
		t.Fatal(err)
	}
	if n := f.e.number("SELECT count(DISTINCT blob_sha256) FROM items WHERE path IN ($1, 'left-pad/-/copy-1.0.0.tgz')", src); n != 1 {
		t.Errorf("the two items hold %d blobs", n)
	}
	for name, c := range map[string][3]string{
		"a caller without the read":  {"nobody", "probe-local", src},
		"a repository that is none":  {"writer", "ghost", src},
		"a path that is none":        {"writer", "probe-local", "left-pad/-/none.tgz"},
		"another repository without": {"writer", "gen-local", src},
	} {
		if err := link(c[0], c[1], c[2], "left-pad/-/refused.tgz"); errs.KindOf(err) != errs.NotFound {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := f.e.number("SELECT count(*) FROM items WHERE path = 'left-pad/-/refused.tgz'"); n != 0 {
		t.Errorf("%d items after a refused link", n)
	}
}

// TestR_ARCH_027_TheItemLookupReadsTheManifestRowAndTheWithheldMarkOfAnOCIItem: the statement of the item lookup returns, for the key
// of kind oci_local, whether the repository holds the row of the manifest the item's blob is and whether a tombstone withholds its
// digest, in the one statement of the download (Q-OCI-33).
func TestR_ARCH_027_TheItemLookupReadsTheManifestRowAndTheWithheldMarkOfAnOCIItem(t *testing.T) {
	f := newCommitFixture(t)
	ctx := context.Background()
	res, err := f.commit(t, "app/_tags/1", `{"schemaVersion":2}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := f.r.reader.ByName(ctx, qr("generic-local"))
	if err != nil {
		t.Fatal(err)
	}
	ref := items.RepoOf(repo)
	ref.Facts = servefacts.Key{Kind: servefacts.OCILocal, Package: "app"}
	read := func() servefacts.Facts {
		it, found, err := f.r.items.Live(ctx, ref, "app/_tags/1")
		if err != nil || !found {
			t.Fatalf("%v %v", found, err)
		}
		if !it.Facts.For(ref.Facts) {
			t.Fatalf("the facts were not read for the key: %+v", it.Facts)
		}
		return it.Facts
	}
	if got := read(); got.OCIManifest || got.Tombstoned {
		t.Fatalf("without rows: %+v", got)
	}
	f.e.exec(`INSERT INTO oci_manifests (repo_id, name, digest, media_type, size) VALUES ($1, 'app', $2, 'application/vnd.oci.image.manifest.v1+json', 19)`, ref.ID, res.Item.Digest[:])
	if got := read(); !got.OCIManifest || got.Tombstoned {
		t.Fatalf("with the manifest row: %+v", got)
	}
	f.e.exec(`INSERT INTO oci_tombstones (repo_id, name, digest) VALUES ($1, 'app', $2)`, ref.ID, res.Item.Digest[:])
	if got := read(); !got.OCIManifest || !got.Tombstoned {
		t.Fatalf("withheld: %+v", got)
	}
	// Another image name reads neither row.
	ref.Facts.Package = "other"
	if got := read(); got.OCIManifest || got.Tombstoned {
		t.Fatalf("another name: %+v", got)
	}
}
