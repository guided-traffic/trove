package oci

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

const digestZero = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// The image the fixtures seed: a config, a layer and the manifest over them.
var (
	seedConfig   = []byte("{}")
	seedLayer    = []byte("layer one")
	seedManifest = imageManifestBytes(sha(seedConfig), sha(seedLayer))
	seedDigest   = sha(seedManifest)
)

// bind gives the twin of the data port the Host it reads items and package rows through.
func bind(h *formattest.Host) *twin {
	tw := h.FormatData(ID).(*twin)
	tw.host = h
	return tw
}

// seed stores the blobs of the image under "app" and pushes its manifest as the tag "1" the way a client does.
func seed(h *formattest.Host) error {
	bind(h)
	h.PutItem(BlobPath("app", sha(seedConfig)), "application/octet-stream", seedConfig)
	h.PutItem(BlobPath("app", sha(seedLayer)), "application/octet-stream", seedLayer)
	return pushManifest(h, "app/manifests/1", seedManifest)
}

func pushManifest(h *formattest.Host, path string, body []byte) error {
	resp, err := h.Serve(context.Background(), http.MethodPut, path, bytes.NewReader(body), false, func(r *http.Request) {
		r.Header.Set("Content-Type", ctImage)
		r.ContentLength = int64(len(body))
	})
	if err != nil {
		return err
	}
	if resp.Code != http.StatusCreated {
		return fmt.Errorf("PUT %s: %d %s", path, resp.Code, resp.Body)
	}
	return nil
}

// conformance is the fixture of the kit: the checks that need a built protocol (the remote and virtual adapters, the setup snippets)
// are skipped or listed as not built, each with its reason, and the work package that builds the operation removes the entry (the kit
// refuses a skip the format contradicts).
func conformance() formattest.Fixture {
	const uploadNoRow = "an upload session and the blob it commits are no package row and no row of the format's tables: no document the revision keys changes"
	return formattest.Fixture{
		Rejects: []string{"app/manifests/%2e%2e", "../app/manifests/1", "app/manifests/1%00"},
		Paths: []formattest.PathCase{
			{Path: "app/_manifests/" + digestZero, Want: formats.PathInfo{Kind: formats.Content, Package: "app"}},
			{Path: "team/app/_blobs/" + digestZero, Want: formats.PathInfo{Kind: formats.Content, Package: "team/app"}},
			{Path: "team/app/_tags/1.0", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "team/app", Version: "1.0"}},
			{Path: "team/app/_taglist", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "team/app"}},
		},
		Host: func() formattest.HostOptions {
			return formattest.HostOptions{Data: newTwin(), Resolver: proceedResolver{}}
		},
		Canonical: canonical,
		Protocol:  protocol,
		Inspect:   formattest.InspectJSON("team/app/_manifests/" + digestZero),
		Names:     []formattest.NameCase{{Spellings: []string{"library/nginx"}}, {Spellings: []string{"a__b/c-d"}}},
		Content: []formattest.ContentCase{
			{Name: "manifest by tag", Path: "app/manifests/1", Seed: seed},
			{Name: "manifest by tag, HEAD", Method: http.MethodHead, Path: "app/manifests/1", Seed: seed},
			{Name: "manifest by digest", Path: "app/manifests/" + seedDigest, Seed: seed},
			{Name: "blob", Path: "app/blobs/" + sha(seedLayer), Seed: seed},
			{Name: "tags list", Path: "app/tags/list", Seed: func(h *formattest.Host) error {
				bind(h).putTag("app", "1", digestN(1))
				return nil
			}},
			{Name: "tags list of an unknown name", Path: "nothing/tags/list", Status: http.StatusNotFound, Seed: func(h *formattest.Host) error { bind(h); return nil }},
			{Name: "referrers", Path: "app/referrers/" + digestZero, Seed: func(h *formattest.Host) error { bind(h); return nil }},
		},
		OptionNames: optionNames(),
		Changes: append(changes(), []formattest.ChangeCase{
			{
				Name: "push a manifest by tag", Package: "app", Seed: func(h *formattest.Host) error {
					bind(h)
					h.PutItem(BlobPath("app", sha(seedConfig)), "application/octet-stream", seedConfig)
					h.PutItem(BlobPath("app", sha(seedLayer)), "application/octet-stream", seedLayer)
					return nil
				},
				Do: func(_ context.Context, h *formattest.Host) error {
					return pushManifest(h, "app/manifests/1", seedManifest)
				},
			},
			{
				Name: "push a manifest by digest", Package: "app", Seed: func(h *formattest.Host) error {
					bind(h)
					h.PutItem(BlobPath("app", sha(seedConfig)), "application/octet-stream", seedConfig)
					h.PutItem(BlobPath("app", sha(seedLayer)), "application/octet-stream", seedLayer)
					return nil
				},
				Do: func(_ context.Context, h *formattest.Host) error {
					return pushManifest(h, "app/manifests/"+seedDigest, seedManifest)
				},
			},
		}...),
		NoRevision: map[string]string{
			// A blob is no row of the format and no package version: removing one changes what no document renders.
			routeBlobDelete:  "a blob item is no package row and no row of the format's tables: removing one changes no document the revision keys",
			routeUploadStart: uploadNoRow, routeUploadStatus: uploadNoRow, routeUploadPatch: uploadNoRow,
			routeUploadPut: uploadNoRow, routeUploadCancel: uploadNoRow,
		},
		View: &formattest.ViewFixture{
			Versions: []string{"1.0", "2.0", "3.0"}, Package: "app",
			Seed: func(h *formattest.Host) error {
				bind(h)
				h.PutItem(BlobPath("app", sha(seedConfig)), "application/octet-stream", seedConfig)
				h.PutItem(BlobPath("app", sha(seedLayer)), "application/octet-stream", seedLayer)
				for _, tag := range []string{"1.0", "2.0", "3.0"} {
					if err := pushManifest(h, "app/manifests/"+tag, seedManifest); err != nil {
						return err
					}
				}
				return nil
			},
		},
		Skip: map[formattest.Check]string{
			formattest.CheckRewrite: "local class only: the remote adapter joins with its work package (R-OCI-021)",
			formattest.CheckMerge:   "local class only: the virtual adapter joins with its work package (R-OCI-027)",
		},
	}
}

// unbuiltStore is the data port of a test that reaches no handler which reads it: a call would panic on the nil interface.
type unbuiltStore struct{ Store }

// canonical maps the example of a route to the canonical item path Classify is asked about; a route that addresses no item (an
// upload, the referrers) is held to its kind alone.
func canonical(example string) (string, bool) {
	segs := strings.Split(example, "/")
	n := len(segs)
	switch {
	case n >= 3 && segs[n-2] == wordManifests && ValidDigest(segs[n-1]):
		return ManifestPath(strings.Join(segs[:n-2], "/"), segs[n-1]), true
	case n >= 3 && segs[n-2] == wordManifests:
		return TagPath(strings.Join(segs[:n-2], "/"), segs[n-1]), true
	case n >= 3 && segs[n-2] == wordBlobs && segs[n-1] != wordUploads:
		return BlobPath(strings.Join(segs[:n-2], "/"), segs[n-1]), true
	case n >= 3 && segs[n-2] == wordTags && segs[n-1] == wordList:
		return TaglistPath(strings.Join(segs[:n-2], "/")), true
	}
	return "", false
}

// protocol is the inverse of canonical for the package document: "<name>/_taglist" is requested at "<name>/tags/list".
func protocol(canonical string) string {
	if name, ok := strings.CutSuffix(canonical, "/"+fileTaglist); ok {
		return name + "/tags/list"
	}
	return canonical
}

// TestR_ARCH_010_Conformance runs the conformance kit over the skeleton.
func TestR_ARCH_010_Conformance(t *testing.T) {
	formattest.Run(t, New(), conformance())
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never panics.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, New()) }

// FuzzR_ARCH_028_Inspect: Inspect of a manifest or a tag item never panics, finishes within its deadline and fails only with a client
// error; the seeds are manifests of the four types and a near-valid document.
func FuzzR_ARCH_028_Inspect(f *testing.F) {
	formattest.FuzzInspect(f, New(), ManifestPath("app", digestZero), seedManifest, indexOf(digestZero+" linux/amd64"),
		[]byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json"}`))
}

// changes are the writes of kit check 9 that this build serves: the deletion of a tag, of a manifest by digest, and the removal of a
// manifest through the items (the hook OnItemsRemoved), each of which raises the revision of the image name.
func changes() []formattest.ChangeCase {
	do := func(method, path string, want int) func(context.Context, *formattest.Host) error {
		return func(ctx context.Context, h *formattest.Host) error {
			r, err := h.Serve(ctx, method, path, nil, false)
			if err == nil && r.Code != want {
				err = fmt.Errorf("%s %s: status %d: %s", method, path, r.Code, r.Body)
			}
			return err
		}
	}
	// seedImage stores the item of a tagged manifest and the version the tag leads, as a push does.
	seedImage := func(tagged bool) func(h *formattest.Host) error {
		return func(h *formattest.Host) error {
			ctx := context.Background()
			bind(h).putManifest(rows.Manifest{Name: "app", Digest: digestN(1)})
			h.PutItem(ManifestPath("app", digestText(digestN(1))), manifestType, []byte("m"))
			if !tagged {
				return nil
			}
			tag := h.PutItem(TagPath("app", "1"), manifestType, []byte("m"))
			return h.Seed(ctx, func(w formats.PackageWriter) error {
				pkg, _, err := w.EnsureLocked(ctx, h.Ref(), "app", "app")
				if err != nil {
					return err
				}
				_, err = w.InsertVersion(ctx, pkg, formats.NewVersion{Version: "1", LeadItem: &tag})
				return err
			})
		}
	}
	d1 := digestText(digestN(1))
	return []formattest.ChangeCase{
		{Name: "the deletion of a tag (DELETE, OnItemsRemoved)", Package: "app", Seed: seedImage(true), Do: do(http.MethodDelete, "app/manifests/1", http.StatusAccepted)},
		{Name: "the deletion of a manifest by digest (DELETE, OnItemsRemoved)", Package: "app", Seed: seedImage(false), Do: do(http.MethodDelete, "app/manifests/"+d1, http.StatusAccepted)},
		{
			Name: "the removal of a manifest through the items (OnItemsRemoved)", Package: "app", Seed: seedImage(false),
			Do: func(ctx context.Context, h *formattest.Host) error {
				return h.Remove(ctx, nil, "delete", ManifestPath("app", d1))
			},
		},
	}
}
