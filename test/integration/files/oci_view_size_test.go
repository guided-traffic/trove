//go:build integration

package files

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci"
	"github.com/guided-traffic/trove/internal/packages/view"
	"github.com/guided-traffic/trove/internal/qname"
)

// readEverything is the read scope of a caller who may read every row.
type readEverything struct{}

func (readEverything) Shape(qname.Repo) authz.ReadShape        { return authz.ReadAll }
func (readEverything) MayRead(qname.Repo, string, string) bool { return true }
func (readEverything) VersionsByPath(qname.Repo) bool          { return false }

// viewOf builds the package view over the same database and the scope of a caller who may read everything.
func (f *ociFixture) viewOf(t *testing.T) (*view.Service, view.Scope) {
	t.Helper()
	ctx := context.Background()
	repos, err := f.r.reader.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	topo := view.NewTopology(repos)
	svc, err := view.New(view.Options{DB: f.r.db, Adapters: formathost.NewViewAdapters([]formats.Format{oci.New()}, f.ports)})
	if err != nil {
		t.Fatal(err)
	}
	sc, err := svc.Scope(ctx, readEverything{}, topo, nil, view.ScopeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return svc, sc
}

// versionOf reads the details of one tag through the package view, as the API does.
func (f *ociFixture) versionOf(t *testing.T, name, tag string) view.VersionDetail {
	t.Helper()
	svc, sc := f.viewOf(t)
	d, found, err := svc.Version(context.Background(), sc, "oci", name, tag)
	if err != nil || !found {
		t.Fatalf("version %s:%s: found %v, %v", name, tag, found, err)
	}
	return d
}

// versionsOf reads the version list of an image name through the package view, as the API does.
func (f *ociFixture) versionsOf(t *testing.T, name string) map[string]view.VersionSummary {
	t.Helper()
	ctx := context.Background()
	svc, sc := f.viewOf(t)
	vs, found, err := svc.Versions(ctx, sc, "oci", name, view.VersionFilter{}, view.VersionOrder{Sort: "publishedAt", Desc: true}, nil, 100)
	if err != nil || !found {
		t.Fatalf("versions of %s: found %v, %v", name, found, err)
	}
	out := map[string]view.VersionSummary{}
	for _, v := range vs {
		out[v.Version] = v
	}
	return out
}

// TestR_OCI_031_AVersionOfATagIsTheSizeOfTheImage: the size of a tag in the version list and in the version details is the image's
// (config and layers; of an index the sum over its children), not the manifest document's, and a layer shared by two tags is
// counted in each.
func TestR_OCI_031_AVersionOfATagIsTheSizeOfTheImage(t *testing.T) {
	f := newOCIFixture(t)
	cfg := f.blob("web/app", []byte("{}"))
	shared := f.blob("web/app", []byte("layer"))
	extra := f.blob("web/app", []byte("another layer"))

	one := ociManifest(cfg, shared)
	two := ociManifest(cfg, shared, extra)
	wantOCI(t, f.push("writer", "web/app", "1", one, ociCT), 201, "")
	wantOCI(t, f.push("writer", "web/app", "2", two, ociCT), 201, "")

	got := f.versionsOf(t, "web/app")
	if len(got) != 2 {
		t.Fatalf("versions = %+v", got)
	}
	// The manifests declare 2 bytes for the config and 5 for each layer.
	if got["1"].SizeBytes != 7 || got["2"].SizeBytes != 12 {
		t.Fatalf("sizes: tag 1 = %d (want 7), tag 2 = %d (want 12); the manifests are %d and %d bytes", got["1"].SizeBytes, got["2"].SizeBytes, len(one), len(two))
	}

	if d := f.versionOf(t, "web/app", "1"); d.SizeBytes != 7 || len(d.Files) != 1 {
		t.Fatalf("the details of tag 1: size %d (want 7), files %+v", d.SizeBytes, d.Files)
	}

	// An index: the sum over its children.
	var kids []string
	for _, seed := range []string{"amd64", "arm64"} {
		m := ociManifest(cfg, f.blob("web/app", []byte(seed)))
		wantOCI(t, f.push("writer", "web/app", shaOf(m), m, ociCT), 201, "")
		kids = append(kids, shaOf(m))
	}
	idx := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"manifests":[`+
		`{"mediaType":%q,"digest":%q,"size":3,"platform":{"os":"linux","architecture":"amd64"}},`+
		`{"mediaType":%q,"digest":%q,"size":3,"platform":{"os":"linux","architecture":"arm64"}},`+
		`{"mediaType":%q,"digest":%q,"size":3,"platform":{"os":"linux","architecture":"arm","variant":"v7"}}]}`,
		ociIndexCT, ociCT, kids[0], ociCT, kids[1], ociCT, kids[0]))
	wantOCI(t, f.push("writer", "web/app", "multi", idx, ociIndexCT), 201, "")
	if got := f.versionsOf(t, "web/app")["multi"].SizeBytes; got != 14 {
		t.Fatalf("the index is %d bytes, want 14 (two children of 7)", got)
	}
	if d := f.versionOf(t, "web/app", "multi"); d.SizeBytes != 14 {
		t.Fatalf("the details of the index: size %d, want 14", d.SizeBytes)
	}

	// An index of indexes: the sum at any depth, however deep the nesting, and an index that names an image besides.
	wrap := func(children ...[]byte) []byte {
		var b strings.Builder
		fmt.Fprintf(&b, `{"schemaVersion":2,"mediaType":%q,"manifests":[`, ociIndexCT)
		for i, c := range children {
			if i > 0 {
				b.WriteByte(',')
			}
			mt := ociIndexCT
			if bytes.Contains(c, []byte(`"config"`)) {
				mt = ociCT
			}
			fmt.Fprintf(&b, `{"mediaType":%q,"digest":%q,"size":%d}`, mt, shaOf(c), len(c))
		}
		b.WriteString("]}")
		return []byte(b.String())
	}
	wantOCI(t, f.push("writer", "web/app", shaOf(idx), idx, ociIndexCT), 201, "")
	outer := wrap(idx)
	wantOCI(t, f.push("writer", "web/app", shaOf(outer), outer, ociIndexCT), 201, "")
	outermost := wrap(outer, one)
	wantOCI(t, f.push("writer", "web/app", "outer", outer, ociIndexCT), 201, "")
	wantOCI(t, f.push("writer", "web/app", "outermost", outermost, ociIndexCT), 201, "")
	got = f.versionsOf(t, "web/app")
	if got["outer"].SizeBytes != 14 || got["outermost"].SizeBytes != 21 {
		t.Fatalf("index of indexes: outer %d (want 14), outermost %d (want 14 + 7 = 21)", got["outer"].SizeBytes, got["outermost"].SizeBytes)
	}
	if d := f.versionOf(t, "web/app", "outermost"); d.SizeBytes != 21 || d.Metadata["size"] != int64(21) {
		t.Fatalf("the details of the outermost index: size %d, metadata size %v", d.SizeBytes, d.Metadata["size"])
	}
}
