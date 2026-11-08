//go:build integration

// Package oci tests the OCI format's data port against the integration stack's PostgreSQL: the statements that record a pushed
// manifest and its references, check what a manifest names under the locks of the commit transaction, and read the rows the package
// view shows (R-OCI-007, R-OCI-013, R-OCI-031, R-OCI-033). The protocol over HTTP joins when the registry root is mounted.
package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	ocidata "github.com/guided-traffic/trove/internal/formatdata/oci"
	"github.com/guided-traffic/trove/internal/formats/oci/manifest"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// env is a database with one local OCI repository and the data port over it.
type env struct {
	t      *testing.T
	st     *testenv.Stack
	db     meta.Runner
	store  *ocidata.Store
	repoID int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "oci"})
	st.Repository("oci-local", "local", "oci", "{}")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, st.DatabaseConfig(), "oci-store", "web", testenv.Quiet())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(db.Close)
	store, err := ocidata.New(ocidata.Options{DB: db, Recorder: nopRecorder{}})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, db: db, store: store, repoID: st.Number("SELECT id FROM repositories WHERE key = 'oci-local'")}
}

func sum(s string) [32]byte { return sha256.Sum256([]byte(s)) }

func dg(s string) string {
	d := sum(s)
	return "sha256:" + hex.EncodeToString(d[:])
}

// item stores a live file item at the path with the bytes of seed.
func (e *env) item(path, seed string) int64 {
	e.t.Helper()
	d := sum(seed)
	e.st.Exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size) VALUES ($1, decode(repeat('00', 20), 'hex'), decode(repeat('00', 16), 'hex'),
		decode(repeat('00', 64), 'hex'), $2) ON CONFLICT DO NOTHING`, d[:], int64(len(seed)))
	parent := ""
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		parent = path[:i]
	}
	return e.st.Number(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size, media_type) VALUES ($1, $2, $3, 'file', $4, $5, 'application/json')
		RETURNING id`, e.repoID, path, parent, d[:], int64(len(seed)))
}

// nopRecorder takes the events of the removal side of the data port, which these tests do not read.
type nopRecorder struct{}

func (nopRecorder) RecordTx(context.Context, meta.Tx, audit.Event) error { return nil }
func (nopRecorder) Record(context.Context, audit.Event)                  {}

func (e *env) put(in rows.ManifestInput) {
	e.t.Helper()
	in.RepoID = e.repoID
	err := e.db.InTx(context.Background(), func(tx meta.Tx) error { return e.store.PutManifest(context.Background(), tx, in) })
	if err != nil {
		e.t.Fatalf("PutManifest: %v", err)
	}
}

func TestR_OCI_007_ARecordedManifestAndItsReferencesReadBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	subject := sum("subject")
	// The children of the index are recorded first, as a push requires; the index's content size is the sum of theirs (Q-OCI-130).
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "team/app", Digest: sum("c1"), MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 10, ContentSize: 300}})
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "team/app", Digest: sum("c2"), MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 10, ContentSize: 354}})
	in := rows.ManifestInput{
		Manifest: rows.Manifest{
			Name: "team/app", Digest: sum("m1"), MediaType: "application/vnd.oci.image.index.v1+json", ArtifactType: "application/vnd.example+json",
			Subject: &subject, Size: 321, ContentSize: 0, Annotations: json.RawMessage(`{"a": "1", "b": "2"}`), AnnotationsCut: true,
		},
		References: []rows.Reference{
			{Target: sum("c1"), Kind: rows.KindManifest, Platform: "linux/amd64"},
			{Target: sum("c2"), Kind: rows.KindManifest, Platform: "linux/arm64/v8"},
			{Target: sum("c1"), Kind: rows.KindManifest, Platform: "linux/ppc64le"}, // named twice: the first occurrence stays
			{Target: sum("b1"), Kind: rows.KindBlob},
		},
	}
	e.put(in)
	e.put(in) // recorded already: nothing changes

	got, ok, err := e.store.Manifest(ctx, e.repoID, "team/app", sum("m1"))
	if err != nil || !ok {
		t.Fatalf("Manifest: %v %v", ok, err)
	}
	if got.MediaType != in.MediaType || got.ArtifactType != in.ArtifactType || got.Size != 321 || got.ContentSize != 654 || !got.AnnotationsCut ||
		got.Subject == nil || *got.Subject != subject || got.CreatedAt.IsZero() {
		t.Fatalf("manifest = %+v", got)
	}
	var ann map[string]string
	if err := json.Unmarshal(got.Annotations, &ann); err != nil || ann["a"] != "1" || ann["b"] != "2" {
		t.Fatalf("annotations = %s (%v)", got.Annotations, err)
	}
	refs, err := e.store.References(ctx, e.repoID, "team/app", sum("m1"))
	if err != nil || len(refs) != 3 {
		t.Fatalf("References = %+v, %v", refs, err)
	}
	platforms := map[[32]byte]string{}
	kinds := map[[32]byte]string{}
	for _, r := range refs {
		platforms[r.Target], kinds[r.Target] = r.Platform, r.Kind
	}
	if platforms[sum("c1")] != "linux/amd64" || platforms[sum("c2")] != "linux/arm64/v8" || platforms[sum("b1")] != "" ||
		kinds[sum("c1")] != rows.KindManifest || kinds[sum("b1")] != rows.KindBlob {
		t.Fatalf("references = %+v", refs)
	}
	if n := e.st.Number("SELECT count(*) FROM oci_manifests WHERE repo_id = $1", e.repoID); n != 3 { // the index and its two children
		t.Fatalf("%d manifest rows", n)
	}
	// Another name is another manifest, and a manifest that is not recorded is absent.
	if _, ok, _ := e.store.Manifest(ctx, e.repoID, "other", sum("m1")); ok {
		t.Fatal("a manifest of another name was found")
	}
	if _, ok, err := e.store.Manifest(ctx, e.repoID, "team/app", sum("nothing")); ok || err != nil {
		t.Fatalf("an unknown manifest: %v %v", ok, err)
	}
}

func TestR_OCI_007_AManifestWithoutAnnotationsOrSubjectStoresNull(t *testing.T) {
	e := newEnv(t)
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "app", Digest: sum("plain"), MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 10}})
	if n := e.st.Number("SELECT count(*) FROM oci_manifests WHERE annotations IS NULL AND subject IS NULL AND artifact_type IS NULL AND NOT annotations_cut"); n != 1 {
		t.Fatalf("the optional columns are not NULL (%d rows)", n)
	}
}

// The bound the parser computes for the annotations of a row is within the bound of the database's CHECK on the text jsonb prints: the
// hardest content (escapes, control characters, the characters Go and PostgreSQL print differently) at the limit is accepted, and
// plain content uses nearly all of it.
func TestR_OCI_007_TheAnnotationBoundOfTheParserIsWithinTheBoundOfTheColumn(t *testing.T) {
	e := newEnv(t)
	for name, value := range map[string]string{
		"hard":  "q\"b\\s\n\t\u0001\u007f\u2028\u2029<>&\u00e9\u20ac\U0001F600",
		"plain": "plain annotation value",
	} {
		ann := map[string]string{}
		for i := range 4000 {
			ann[fmt.Sprintf("key-%04d-%s", i, strings.Repeat("k", i%7))] = strings.Repeat(value, 1+i%3)
		}
		m := &manifest.Manifest{Annotations: ann}
		raw, cut := m.RowAnnotations(manifest.MaxAnnotationBytes)
		if !cut {
			t.Fatalf("%s: the sample does not reach the limit", name)
		}
		e.put(rows.ManifestInput{Manifest: rows.Manifest{
			Name: name, Digest: sum(name), MediaType: "application/vnd.oci.image.index.v1+json", Annotations: raw, AnnotationsCut: cut,
		}})
		n := e.st.Number("SELECT octet_length(annotations::text) FROM oci_manifests WHERE name = $1", name)
		if n > 65536 || (name == "plain" && n < 60000) {
			t.Fatalf("%s: the stored text is %d bytes", name, n)
		}
	}
}

// The text of the annotations of a row at exactly the limit of the column is kept whole and accepted by the CHECK; the parser's bound
// is exact to the byte.
func TestR_OCI_007_AnnotationsAtExactlyTheLimitOfTheColumnAreStored(t *testing.T) {
	e := newEnv(t)
	frame := len(`{"a": "", "b": ""}`)
	x := strings.Repeat("x", 30000)
	m := &manifest.Manifest{Annotations: map[string]string{"a": x, "b": strings.Repeat("y", manifest.MaxAnnotationBytes-frame-len(x))}}
	raw, cut := m.RowAnnotations(manifest.MaxAnnotationBytes)
	if cut || len(raw) != manifest.MaxAnnotationBytes {
		t.Fatalf("%d bytes, cut %v; the sample must fill the limit exactly", len(raw), cut)
	}
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "exact", Digest: sum("exact"), MediaType: "application/vnd.oci.image.index.v1+json", Annotations: raw}})
	if n := e.st.Number("SELECT octet_length(annotations::text) FROM oci_manifests WHERE name = 'exact'"); n != 65536 {
		t.Fatalf("the stored text is %d bytes", n)
	}
	// One byte more: the parser cuts the second entry, and the column accepts what is left.
	m.Annotations["b"] += "y"
	raw, cut = m.RowAnnotations(manifest.MaxAnnotationBytes)
	if !cut || len(raw) > manifest.MaxAnnotationBytes {
		t.Fatalf("%d bytes, cut %v", len(raw), cut)
	}
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "over", Digest: sum("over"), MediaType: "application/vnd.oci.image.index.v1+json", Annotations: raw, AnnotationsCut: cut}})
}

func TestR_OCI_013_ExistingItemsAreTheLiveFilesOfTheRepository(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.item("app/_blobs/"+dg("live"), "live")
	e.item("app/_blobs/"+dg("trashed"), "trashed")
	e.st.Exec("UPDATE items SET trashed_at = now() WHERE path = $1", "app/_blobs/"+dg("trashed"))
	e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind) VALUES ($1, 'app/_blobs/folder', 'app/_blobs', 'folder')`, e.repoID)
	e.st.Repository("oci-other", "local", "oci", "{}")
	otherRepo := e.st.Number("SELECT id FROM repositories WHERE key = 'oci-other'")
	e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size) SELECT $1, 'app/_blobs/elsewhere', 'app/_blobs', 'file', blob_sha256, size FROM items LIMIT 1`, otherRepo)

	paths := []string{"app/_blobs/" + dg("live"), "app/_blobs/" + dg("trashed"), "app/_blobs/folder", "app/_blobs/elsewhere", "app/_blobs/" + dg("never")}
	got, err := e.store.ExistingItems(ctx, nil, e.repoID, paths)
	if err != nil || len(got) != 1 || got[0] != paths[0] {
		t.Fatalf("ExistingItems = %v, %v", got, err)
	}
	if got, err := e.store.ExistingItems(ctx, nil, e.repoID, nil); err != nil || len(got) != 0 {
		t.Fatalf("no paths: %v %v", got, err)
	}
	var inTx []string
	err = e.db.InTx(ctx, func(tx meta.Tx) (err error) {
		inTx, err = e.store.ExistingItems(ctx, tx, e.repoID, paths)
		return err
	})
	if err != nil || len(inTx) != 1 || inTx[0] != paths[0] {
		t.Fatalf("in a transaction: %v, %v", inTx, err)
	}
}

// Inside the commit transaction the rows a manifest names are held against a removal until the manifest's rows are written, so a blob
// cannot go between the check and the row (R-OCI-013 step 8).
func TestR_OCI_013_TheItemsAManifestNamesAreHeldAgainstARemovalInTheTransaction(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	path := "app/_blobs/" + dg("held")
	e.item(path, "held")
	removed := make(chan error, 1)
	err := e.db.InTx(ctx, func(tx meta.Tx) error {
		got, err := e.store.ExistingItems(ctx, tx, e.repoID, []string{path})
		if err != nil || len(got) != 1 {
			return fmt.Errorf("existing: %v %w", got, err)
		}
		go func() {
			e.st.Exec("SET lock_timeout = '300ms'")
			_, err := e.st.TryNumber(`WITH r AS (UPDATE items SET trashed_at = now() WHERE repo_id = $1 AND path = $2 RETURNING 1) SELECT count(*) FROM r`, e.repoID, path)
			e.st.Exec("RESET lock_timeout")
			removed <- err
		}()
		select {
		case err := <-removed:
			if err == nil {
				return fmt.Errorf("the removal went through while the transaction held the row")
			}
			if !strings.Contains(err.Error(), "55P03") && !strings.Contains(err.Error(), "lock timeout") {
				return fmt.Errorf("the removal failed for another reason: %w", err)
			}
		case <-time.After(5 * time.Second):
			return fmt.Errorf("the removal neither failed nor finished")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The transaction has ended: the item can go now.
	e.st.Exec("UPDATE items SET trashed_at = now() WHERE repo_id = $1 AND path = $2", e.repoID, path)
	if got, _ := e.store.ExistingItems(ctx, nil, e.repoID, []string{path}); len(got) != 0 {
		t.Fatalf("a trashed item exists: %v", got)
	}
}

func TestR_OCI_033_WithheldAmongAndTheTombstoneRead(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.st.Exec("INSERT INTO oci_tombstones (repo_id, name, digest) VALUES ($1, 'app', $2)", e.repoID, sumBytes("gone"))
	got, err := e.store.WithheldAmong(ctx, nil, e.repoID, "app", [][32]byte{sum("gone"), sum("kept")})
	if err != nil || len(got) != 1 || got[0] != sum("gone") {
		t.Fatalf("WithheldAmong = %v, %v", got, err)
	}
	if got, _ := e.store.WithheldAmong(ctx, nil, e.repoID, "other", [][32]byte{sum("gone")}); len(got) != 0 {
		t.Fatalf("the mark is per name: %v", got)
	}
	if got, err := e.store.WithheldAmong(ctx, nil, e.repoID, "app", nil); err != nil || len(got) != 0 {
		t.Fatalf("no digests: %v %v", got, err)
	}
	tomb, ok, err := e.store.Tombstone(ctx, e.repoID, "app", sum("gone"))
	if err != nil || !ok || tomb.WithheldAt.IsZero() || tomb.Digest != sum("gone") {
		t.Fatalf("Tombstone = %+v %v %v", tomb, ok, err)
	}
	if _, ok, err := e.store.Tombstone(ctx, e.repoID, "app", sum("kept")); ok || err != nil {
		t.Fatalf("an unmarked digest: %v %v", ok, err)
	}
}

func sumBytes(s string) []byte {
	d := sum(s)
	return d[:]
}

// tag stores a tag item that holds the bytes of seed (the manifest's) and the version row of the tag, as a push does.
func (e *env) tag(name, tag, seed string) int64 {
	e.t.Helper()
	pkg := e.st.Number(`INSERT INTO packages (repo_id, name, display_name) VALUES ($1, $2, $2) ON CONFLICT (repo_id, name) DO UPDATE SET name = excluded.name RETURNING id`, e.repoID, name)
	item := e.item(name+"/_tags/"+tag, seed)
	v := e.st.Number(`INSERT INTO package_versions (package_id, version, display_version, lead_item_id) VALUES ($1, $2, $2, $3) RETURNING id`, pkg, tag, item)
	e.st.Exec("UPDATE items SET version_id = $1 WHERE id = $2", v, item)
	return v
}

func TestR_OCI_031_TheViewReadsTheTagsTheirManifestsAndThePlatformsOfAnIndex(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "multi", Digest: sum("amd"), MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 100, ContentSize: 1000}})
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "multi", Digest: sum("arm"), MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 110, ContentSize: 2000}})
	e.put(rows.ManifestInput{
		Manifest: rows.Manifest{Name: "multi", Digest: sum("idx"), MediaType: "application/vnd.oci.image.index.v1+json", Size: 300},
		References: []rows.Reference{
			{Target: sum("arm"), Kind: rows.KindManifest, Platform: "linux/arm64"},
			{Target: sum("amd"), Kind: rows.KindManifest, Platform: "linux/amd64"},
		},
	})
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "multi", Digest: sum("solo"), MediaType: "application/vnd.oci.image.manifest.v1+json", ArtifactType: "application/vnd.example.config+json", Size: 90, ContentSize: 500}})
	vIdx := e.tag("multi", "1", "idx")
	vSolo := e.tag("multi", "2", "solo")
	vNone := e.tag("multi", "3", "no row at all") // a tag whose manifest was never recorded

	views, err := e.store.TagViews(ctx, []int64{vIdx, vSolo, vNone, vNone + 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("views = %+v", views)
	}
	byTag := map[string]rows.TagView{}
	for _, v := range views {
		byTag[v.Tag] = v
	}
	idx, solo := byTag["1"], byTag["2"]
	if idx.VersionID != vIdx || idx.Digest != sum("idx") || len(idx.Children) != 2 || idx.Children[0].Platform != "linux/amd64" ||
		idx.Children[0].ContentSize != 1000 || idx.Children[1].Platform != "linux/arm64" || idx.Children[1].Digest != sum("arm") {
		t.Fatalf("index view = %+v", idx)
	}
	if solo.VersionID != vSolo || solo.ContentSize != 500 || solo.ArtifactType != "application/vnd.example.config+json" || len(solo.Children) != 0 {
		t.Fatalf("image view = %+v", solo)
	}
	sizes, err := e.store.TagSizes(ctx, []int64{vIdx, vSolo, vNone, vNone + 1000})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]int64{vIdx: 3000, vSolo: 500}
	if len(sizes) != len(want) {
		t.Fatalf("sizes = %v, want %v", sizes, want)
	}
	for id, n := range want {
		if sizes[id] != n {
			t.Fatalf("sizes = %v, want %v", sizes, want)
		}
	}
	if none, err := e.store.TagSizes(ctx, nil); err != nil || len(none) != 0 {
		t.Fatalf("no tags: %v %v", none, err)
	}
	if v, err := e.store.TagViews(ctx, nil); err != nil || len(v) != 0 {
		t.Fatalf("no tags: %v %v", v, err)
	}

	// Stored lists newest push first; a withheld manifest's tag is not served, and Withheld names its version.
	stored, err := e.store.StoredTags(ctx, e.repoID, "multi", 10)
	if err != nil || strings.Join(stored, ",") != "3,2,1" {
		t.Fatalf("StoredTags = %v, %v", stored, err)
	}
	if two, _ := e.store.StoredTags(ctx, e.repoID, "multi", 2); strings.Join(two, ",") != "3,2" {
		t.Fatalf("limit 2: %v", two)
	}
	if zero, _ := e.store.StoredTags(ctx, e.repoID, "multi", 0); len(zero) != 0 {
		t.Fatalf("limit 0: %v", zero)
	}
	e.st.Exec("INSERT INTO oci_tombstones (repo_id, name, digest) VALUES ($1, 'multi', $2)", e.repoID, sumBytes("idx"))
	stored, _ = e.store.StoredTags(ctx, e.repoID, "multi", 10)
	if strings.Join(stored, ",") != "3,2" {
		t.Fatalf("StoredTags with a withheld manifest = %v", stored)
	}
	ids, err := e.store.WithheldVersions(ctx, []string{"it/oci-local", "it/oci-other"})
	if err != nil || len(ids) != 1 || ids[0] != vIdx {
		t.Fatalf("WithheldVersions = %v, %v", ids, err)
	}
	if ids, _ := e.store.WithheldVersions(ctx, []string{"it/nothing"}); len(ids) != 0 {
		t.Fatalf("another repository: %v", ids)
	}
	e.st.Exec("UPDATE package_versions SET trashed_at = now() WHERE id = $1", vIdx)
	if ids, _ := e.store.WithheldVersions(ctx, []string{"it/oci-local"}); len(ids) != 0 {
		t.Fatalf("a trashed version is not listed: %v", ids)
	}
}

// Concurrent pushes of manifests that name the same targets record each manifest once and lose none.
func TestR_OCI_013_ConcurrentRecordsOfManifestsLoseNone(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	const n = 16
	errc := make(chan error, 2*n)
	for i := range n {
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				in := rows.ManifestInput{
					Manifest:   rows.Manifest{Name: "app", Digest: sum(fmt.Sprint("m", i)), MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 1},
					References: []rows.Reference{{Target: sum("shared"), Kind: rows.KindBlob}, {Target: sum(fmt.Sprint("own", i)), Kind: rows.KindBlob}},
				}
				in.RepoID = e.repoID
				errc <- e.db.InTx(context.Background(), func(tx meta.Tx) error { return e.store.PutManifest(context.Background(), tx, in) })
			}()
		}
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	if m, r := e.st.Number("SELECT count(*) FROM oci_manifests"), e.st.Number("SELECT count(*) FROM oci_references"); m != n || r != 2*n {
		t.Fatalf("%d manifests, %d references", m, r)
	}
}

// TestR_OCI_031_TheSizeOfAnIndexOfIndexesIsTheSumAtAnyDepth: the push of an index stores the sum of its children's content sizes, and a
// child that is an index holds its own sum, so the size is right however deep the nesting goes (Q-OCI-130).
func TestR_OCI_031_TheSizeOfAnIndexOfIndexesIsTheSumAtAnyDepth(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	image := "application/vnd.oci.image.manifest.v1+json"
	index := "application/vnd.oci.image.index.v1+json"
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "deep", Digest: sum("a"), MediaType: image, Size: 10, ContentSize: 1000}})
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "deep", Digest: sum("b"), MediaType: image, Size: 10, ContentSize: 2000}})
	e.put(rows.ManifestInput{
		Manifest:   rows.Manifest{Name: "deep", Digest: sum("l1"), MediaType: index, Size: 10},
		References: []rows.Reference{{Target: sum("a"), Kind: rows.KindManifest, Platform: "linux/amd64"}, {Target: sum("b"), Kind: rows.KindManifest, Platform: "linux/arm64"}},
	})
	e.put(rows.ManifestInput{
		Manifest:   rows.Manifest{Name: "deep", Digest: sum("l2"), MediaType: index, Size: 10},
		References: []rows.Reference{{Target: sum("l1"), Kind: rows.KindManifest}},
	})
	e.put(rows.ManifestInput{
		Manifest:   rows.Manifest{Name: "deep", Digest: sum("l3"), MediaType: index, Size: 10},
		References: []rows.Reference{{Target: sum("l2"), Kind: rows.KindManifest}, {Target: sum("a"), Kind: rows.KindManifest}},
	})
	// An index that names no manifest is not summed: its content size stays what it was recorded with.
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "deep", Digest: sum("empty"), MediaType: index, Size: 10}})
	// The sum saturates at the largest bigint instead of failing.
	huge := int64(^uint64(0) >> 1)
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "deep", Digest: sum("h1"), MediaType: image, Size: 10, ContentSize: huge}})
	e.put(rows.ManifestInput{Manifest: rows.Manifest{Name: "deep", Digest: sum("h2"), MediaType: image, Size: 10, ContentSize: huge}})
	e.put(rows.ManifestInput{
		Manifest:   rows.Manifest{Name: "deep", Digest: sum("hh"), MediaType: index, Size: 10},
		References: []rows.Reference{{Target: sum("h1"), Kind: rows.KindManifest}, {Target: sum("h2"), Kind: rows.KindManifest}},
	})

	vs := map[string]int64{}
	for name, digest := range map[string]string{"l1": "l1", "l2": "l2", "l3": "l3", "empty": "empty", "hh": "hh"} {
		vs[name] = e.tag("deep", name, digest)
	}
	sizes, err := e.store.TagSizes(ctx, []int64{vs["l1"], vs["l2"], vs["l3"], vs["empty"], vs["hh"]})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"l1": 3000, "l2": 3000, "l3": 4000, "empty": 0, "hh": huge}
	for name, n := range want {
		if sizes[vs[name]] != n {
			t.Fatalf("size of %s = %d, want %d (all: %v)", name, sizes[vs[name]], n, sizes)
		}
	}
}
