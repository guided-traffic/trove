package oci

import (
	"context"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/oci/manifest"
	"github.com/guided-traffic/trove/internal/formats/oci/tagorder"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// Annotations of BuildKit's attestation manifests in an index: the entry names the manifest it attests, with the platform
// unknown/unknown, so a platform filter must follow the reference and not the platform (oci.md "Mirror discovery").
const (
	annotationReferenceDigest = "vnd.docker.reference.digest"
)

// MirrorPlan returns what a name needs in the mirror (R-REPO-051, oci.md "Mirror discovery"): the tags its tags list offers that
// oci.mirror selects, newest first, and for each tag the items below it. The tag documents are filled through Upstream.Document (a
// tag already stored costs a HEAD, an unchanged one no manifest GET), the manifests an index names through Upstream.Artifact. A name
// the upstream does not have is an empty plan. On Docker Hub a one-component name is the name below library/ (R-OCI-025).
func (a remoteAdapter) MirrorPlan(ctx context.Context, u discover.Upstream, name string) (discover.Plan, error) {
	plan := discover.Plan{Name: name}
	if !ValidName(name) {
		return plan, nil
	}
	upName := libraryName(u.Base().String(), name)
	tags, found, err := a.readTags(ctx, u, upName)
	if err != nil || !found {
		return plan, err
	}
	ms := mirrorMemberOf(u.Settings())
	maxVersions, prereleases := discover.BoundsOf(u)
	kept := 0
	for _, ct := range selectTags(tags, ms) {
		// The engine keeps the newest maxVersions of the tags that are not prereleases and every pinned tag; a tag it would drop is
		// not read, so that a tag outside the bound costs no manifest request (it stays available by pull-through).
		if !ct.Pinned {
			if (!prereleases && tagorder.Prerelease(ct.Tag)) || (maxVersions > 0 && kept >= maxVersions) {
				continue
			}
			kept++
		}
		items, err := a.tagItems(ctx, u, upName, ct, ms)
		if err != nil {
			return discover.Plan{}, err
		}
		plan.Items = append(plan.Items, items...)
	}
	return plan, nil
}

// tagItems returns the items of one tag: the child manifests of an index that the platforms select, and of every selected image
// manifest its config and its layers, each with the size its descriptor announces. Every item carries the tag as its version. A tag
// the upstream no longer has, or whose manifest the registry does not accept (Docker schema 1, a manifest that fails the reader),
// has no items: one such tag does not fail the name.
func (a remoteAdapter) tagItems(ctx context.Context, u discover.Upstream, name string, ct chosenTag, ms MirrorSettings) ([]discover.Item, error) {
	doc, found, err := u.Document(ctx, TagPath(name, ct.Tag), "")
	if err != nil {
		if invalidManifest(err) {
			return nil, nil
		}
		return nil, err
	}
	if !found {
		return nil, nil
	}
	fm, err := a.f.readFetched(name, doc)
	if err != nil {
		return nil, nil //nolint:nilerr // a manifest the registry does not accept is not mirrored (MANIFEST_INVALID)
	}
	b := &itemBuilder{name: name, version: ct.Tag, prerelease: tagorder.Prerelease(ct.Tag), pinned: ct.Pinned, seen: map[string]bool{}}
	m := fm.m
	if m.Type == manifest.TypeImage {
		b.image(m)
		return b.items, nil
	}
	children, err := selectChildren(doc, m, ms.Platforms)
	if err != nil {
		return nil, err
	}
	for _, d := range children {
		b.manifest(d)
		body, found, err := u.Artifact(ctx, ManifestPath(name, d.Digest), MaxDocumentBytes)
		if err != nil {
			return nil, err
		}
		if !found || body == nil {
			continue // the child is gone, or too large to read: its blobs cannot be named
		}
		child, err := a.f.readFetched(name, body)
		if err != nil {
			continue
		}
		if child.m.Type == manifest.TypeImage {
			b.image(child.m)
		}
	}
	return b.items, nil
}

// invalidManifest reports whether err is the refusal of a fetched manifest that the registry does not accept (readFetched).
func invalidManifest(err error) bool {
	ee, ok := errs.As(err)
	return ok && strings.Contains(ee.Detail, "MANIFEST_INVALID")
}

// itemBuilder collects the items of one tag; a path is listed once per tag.
type itemBuilder struct {
	name       string
	version    string
	prerelease bool
	pinned     bool
	seen       map[string]bool
	items      []discover.Item
}

func (b *itemBuilder) add(path string, size int64) {
	if b.seen[path] {
		return
	}
	b.seen[path] = true
	b.items = append(b.items, discover.Item{Version: b.version, Path: path, Size: max(size, 0), Prerelease: b.prerelease, Pinned: b.pinned})
}

// manifest adds a child manifest of an index.
func (b *itemBuilder) manifest(d manifest.Descriptor) {
	b.add(ManifestPath(b.name, d.Digest), d.Size)
}

// image adds the config and the layers of an image manifest. A layer with urls is foreign: the registry is not asked to hold it.
func (b *itemBuilder) image(m *manifest.Manifest) {
	if m.Config != nil && !m.Config.Foreign() {
		b.add(BlobPath(b.name, m.Config.Digest), m.Config.Size)
	}
	for _, l := range m.Layers {
		if !l.Foreign() {
			b.add(BlobPath(b.name, l.Digest), l.Size)
		}
	}
}

// selectChildren returns the entries of an index whose manifests are mirrored (Q-OCI-181). With no platforms every entry is. With
// platforms, an entry that names a platform is selected when one of them matches it, and an attestation entry (one whose
// vnd.docker.reference.digest annotation names an entry) when the entry it names is selected. An entry with neither is not.
func selectChildren(doc []byte, m *manifest.Manifest, platforms []string) ([]manifest.Descriptor, error) {
	if len(m.Manifests) > maxIndexChildren {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "too_many_children: an index names more than 1000 manifests")
	}
	if len(platforms) == 0 {
		return m.Manifests, nil
	}
	refs, err := attestationRefs(doc)
	if err != nil {
		return nil, err
	}
	picked := map[string]bool{}
	var out []manifest.Descriptor
	for _, d := range m.Manifests {
		if refs[d.Digest] == "" && platformSelected(platforms, d.Platform) {
			picked[d.Digest] = true
			out = append(out, d)
		}
	}
	for _, d := range m.Manifests {
		if ref := refs[d.Digest]; ref != "" && picked[ref] {
			out = append(out, d)
		}
	}
	return out, nil
}

// attestationRefs maps the digest of an index entry to the digest it attests, for the entries that carry the annotation. The parser of
// the manifest keeps no descriptor annotations, so the index is read once more for them.
func attestationRefs(doc []byte) (map[string]string, error) {
	var idx struct {
		Manifests []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := untrusted.UnmarshalJSON(doc, untrusted.DefaultLimits().Tighten(untrusted.Limits{DocumentBytes: MaxDocumentBytes}), &idx); err != nil {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream's index cannot be read for its attestation entries.", errs.WithCause(err))
	}
	out := map[string]string{}
	for _, e := range idx.Manifests {
		if ref := e.Annotations[annotationReferenceDigest]; ref != "" {
			out[e.Digest] = ref
		}
	}
	return out, nil
}

// platformSelected reports whether the platform of an index entry is one of the wanted ones. `os/arch` matches every variant of the
// architecture; `os/arch/variant` matches that variant, and an entry without a variant is read as the architecture's default
// (arm64 is v8, arm is v7), as registries and clients write it (Q-OCI-181).
func platformSelected(want []string, p *manifest.Platform) bool {
	if p == nil {
		return false
	}
	variant := p.Variant
	if variant == "" {
		switch p.Architecture {
		case "arm64":
			variant = "v8"
		case "arm":
			variant = "v7"
		}
	}
	for _, w := range want {
		parts := strings.Split(w, "/")
		if len(parts) < 2 || parts[0] != p.OS || parts[1] != p.Architecture {
			continue
		}
		if len(parts) == 2 || parts[2] == variant || parts[2] == p.Variant {
			return true
		}
	}
	return false
}
