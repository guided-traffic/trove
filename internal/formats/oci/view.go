package oci

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
	"github.com/guided-traffic/trove/internal/formats/oci/tagorder"
)

// The package screens of the format (R-OCI-031, R-API-029): an image name is a package and its tags are its versions, newest first by
// push time in a local repository. Per tag the screen shows the digest, the media type, the summed size and, for an index, its
// platforms with the digest and the size of each child, read from the rows of R-OCI-007. Layer history and labels are not shown
// (Deferred).

// View returns the format's part of the package screens.
func (f *Format) View() formats.ViewAdapter { return viewAdapter{} }

type viewAdapter struct{}

var _ formats.ViewAdapter = viewAdapter{}

func viewPort(ports formats.DataPorts) (Store, error) {
	if ports != nil {
		if st, ok := ports.FormatData(ID).(Store); ok {
			return st, nil
		}
	}
	return nil, errs.E(errs.Internal, errs.CodeInternal, "oci: the data port is not bound")
}

// Compare orders tags by the tag order of the format (Q-OCI-17): versions by their numbers, then the release before its variants
// before its pre-releases, then every other tag by its bytes. A string that is no tag sorts before every tag, as the contract asks.
func (viewAdapter) Compare(a, b string) int {
	switch okA, okB := ValidTag(a), ValidTag(b); {
	case !okA && !okB:
		return strings.Compare(a, b)
	case !okA:
		return -1
	case !okB:
		return 1
	}
	return tagorder.Compare(a, b)
}

// Stored lists the tags a local repository serves of an image name, newest push first. A remote repository lists none: its tag
// documents are metadata entries and own no version row (Q-OCI-34, Q-OCI-121), so the screens show the image names and manifests of its
// cache through the format's tables; a virtual repository lists none yet (its adapter is a work package of its own).
func (viewAdapter) Stored(ctx context.Context, ports formats.DataPorts, q formats.StoredQuery) (formats.Stored, error) {
	if q.Repo.Class != formats.Local || q.Limit <= 0 {
		return formats.Stored{}, nil
	}
	st, err := viewPort(ports)
	if err != nil {
		return formats.Stored{}, err
	}
	tags, err := st.StoredTags(ctx, q.Repo.ID, q.Package, q.Limit)
	return formats.Stored{Versions: tags}, err
}

// Withheld returns the tags of the repositories that point at a manifest the journal replay withheld (R-OCI-033): the screens list
// none of them.
func (viewAdapter) Withheld(ctx context.Context, ports formats.DataPorts, repos []string) ([]formats.VersionID, error) {
	if len(repos) == 0 {
		return nil, nil
	}
	st, err := viewPort(ports)
	if err != nil {
		return nil, err
	}
	ids, err := st.WithheldVersions(ctx, repos)
	out := make([]formats.VersionID, len(ids))
	for i, id := range ids {
		out[i] = formats.VersionID(id)
	}
	return out, err
}

// Find is empty: an image has no description or keywords to search (the config is not read, R-OCI-031).
func (viewAdapter) Find(context.Context, formats.DataPorts, formats.ViewFind, int) ([]formats.VersionID, error) {
	return nil, nil
}

// Describe reads, for tags, the facts of the manifests they point at, in two statements however many packages the page holds. The
// summary the package list asks for holds a description and keywords, which an image has none of, so it costs nothing; the details ask
// for the metadata of a version:
//
//	digest, mediaType, size (the summed size of the config and the layers; of an index, of its children), artifactType (the
//	manifest's own, else its config's media type) and, for an index, platforms: one object per child with platform, digest,
//	mediaType and size.
//
// A tag the repository holds no recorded manifest for is absent from the answer.
func (viewAdapter) Describe(ctx context.Context, ports formats.DataPorts, vs []formats.ViewVersion, want formats.ViewFields, _ func(string) bool) (map[formats.VersionID]formats.ViewData, error) {
	if !want.Has(formats.ViewDetails) || len(vs) == 0 {
		return map[formats.VersionID]formats.ViewData{}, nil
	}
	st, err := viewPort(ports)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(vs))
	for i, v := range vs {
		ids[i] = int64(v.ID)
	}
	views, err := st.TagViews(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[formats.VersionID]formats.ViewData, len(views))
	for _, tv := range views {
		out[formats.VersionID(tv.VersionID)] = formats.ViewData{Metadata: tagMetadata(tv)}
	}
	return out, nil
}

var _ formats.ViewSizer = (*Format)(nil)

// ViewSizes implements [formats.ViewSizer]: the size of a tag, as the package screens show it for the version, is the size of its
// image, not of the tag's file, which is the manifest document (R-OCI-031, Q-OCI-130). The push stores it in the manifest's row (the
// config and layers of an image, the sum over the children of an index at any depth), so one read answers for all the tags asked for,
// and for no other id. A tag with no recorded manifest is absent.
func (*Format) ViewSizes(ctx context.Context, ports formats.DataPorts, vs []formats.ViewVersion) (map[formats.VersionID]int64, error) {
	if len(vs) == 0 {
		return map[formats.VersionID]int64{}, nil
	}
	st, err := viewPort(ports)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(vs))
	asked := make(map[int64]bool, len(vs))
	for i, v := range vs {
		ids[i] = int64(v.ID)
		asked[int64(v.ID)] = true
	}
	sizes, err := st.TagSizes(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[formats.VersionID]int64, len(sizes))
	for id, n := range sizes {
		if asked[id] {
			out[formats.VersionID(id)] = n
		}
	}
	return out, nil
}

func tagMetadata(tv rows.TagView) map[string]any {
	md := map[string]any{
		"digest":    "sha256:" + hex.EncodeToString(tv.Digest[:]),
		"mediaType": tv.MediaType,
		"size":      tv.ContentSize,
	}
	if tv.ArtifactType != "" {
		md["artifactType"] = tv.ArtifactType
	}
	if len(tv.Children) == 0 {
		return md
	}
	children := tv.Children // in platform order, as the statement read them
	platforms := make([]map[string]any, 0, len(children))
	for _, c := range children {
		platforms = append(platforms, map[string]any{
			"platform": c.Platform, "digest": "sha256:" + hex.EncodeToString(c.Digest[:]), "mediaType": c.MediaType, "size": c.ContentSize,
		})
	}
	md["platforms"] = platforms
	return md
}

const maxInt64 = int64(^uint64(0) >> 1)
