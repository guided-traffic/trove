package ocistub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Media types the stub serves.
const (
	MediaDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaDockerConfig   = "application/vnd.docker.container.image.v1+json"
	MediaDockerLayer    = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	MediaDockerSchema1  = "application/vnd.docker.distribution.manifest.v1+prettyjws"
	MediaOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaOCIConfig      = "application/vnd.oci.image.config.v1+json"
	MediaOCILayer       = "application/vnd.oci.image.layer.v1.tar+gzip"
	MediaOCIEmpty       = "application/vnd.oci.empty.v1+json"
	MediaInToto         = "application/vnd.in-toto+json"
)

// Annotations of BuildKit's attestation manifests inside an index.
const (
	AnnotationReferenceType   = "vnd.docker.reference.type"
	AnnotationReferenceDigest = "vnd.docker.reference.digest"
	ReferenceTypeAttestation  = "attestation-manifest"
	annotationPredicateType   = "in-toto.io/predicate-type"
)

// Flavors of an image: the media types it is written in.
const (
	FlavorDocker = "docker"
	FlavorOCI    = "oci"
)

// Artifact types of the referrers of acme/signed.
const (
	ArtifactSignature = "application/vnd.example.signature.v1+json"
	ArtifactSBOM      = "application/vnd.example.sbom.v1+json"
)

// created is the creation time of every generated image: fixed, so that two starts produce the same bytes.
const created = "2024-01-01T00:00:00Z"

// The repository with BulkTags tags, which come in four pages with the default page size.
const (
	BulkRepository = "bulk/tags"
	BulkTags       = 2500
)

// Descriptor is an OCI content descriptor.
type Descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Platform     *Platform         `json:"platform,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// Platform is the platform of an index entry.
type Platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

type imageManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Subject       *Descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type indexManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Manifests     []Descriptor      `json:"manifests"`
	Subject       *Descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// object is a stored manifest.
type object struct {
	mediaType string
	data      []byte
	digest    string
}

func (o *object) descriptor() Descriptor {
	return Descriptor{MediaType: o.mediaType, Digest: o.digest, Size: int64(len(o.data))}
}

// recipe says how a top-level manifest of a repository is generated: the same recipe with the same seed and revision gives the
// same bytes.
type recipe struct {
	Flavor    string   // FlavorDocker or FlavorOCI
	Platforms []string // "os/arch[/variant]"; more than one (or Index) makes an index
	Index     bool     // an index even for one platform
	Attest    bool     // BuildKit attestation manifests next to the platform manifests
	Seed      string
	Rev       int
}

// repoDef is the content of one repository.
type repoDef struct {
	tags      map[string]string   // tag to manifest digest
	recipes   map[string]recipe   // top-level manifest digest to its recipe
	members   map[string]bool     // digests of the manifests and blobs the repository holds
	referrers map[string][]string // subject digest to the digests of the manifests that name it
}

func newRepoDef() *repoDef {
	return &repoDef{tags: map[string]string{}, recipes: map[string]recipe{}, members: map[string]bool{}, referrers: map[string][]string{}}
}

func (r *repoDef) clone() *repoDef {
	c := &repoDef{tags: maps.Clone(r.tags), recipes: maps.Clone(r.recipes), members: maps.Clone(r.members), referrers: map[string][]string{}}
	for k, v := range r.referrers {
		c.referrers[k] = slices.Clone(v)
	}
	return c
}

// Corpus is the generated content of the stub: repositories with tags, images, indexes, attestations and referrers. It is built
// in code, with no recorded response and no clock or random source, so two builds are identical byte for byte.
type Corpus struct {
	blobs     map[string][]byte
	manifests map[string]*object
	repos     map[string]*repoDef
}

func newCorpus() *Corpus {
	return &Corpus{blobs: map[string][]byte{}, manifests: map[string]*object{}, repos: map[string]*repoDef{}}
}

func (c *Corpus) clone() *Corpus {
	n := &Corpus{blobs: maps.Clone(c.blobs), manifests: maps.Clone(c.manifests), repos: make(map[string]*repoDef, len(c.repos))}
	for k, v := range c.repos {
		n.repos[k] = v.clone()
	}
	return n
}

// Names lists the repositories, sorted.
func (c *Corpus) Names() []string { return slices.Sorted(maps.Keys(c.repos)) }

// Len is the number of repositories.
func (c *Corpus) Len() int { return len(c.repos) }

// Digest returns the manifest digest a tag has in the corpus as built.
func (c *Corpus) Digest(name, tag string) (string, bool) {
	r := c.repos[name]
	if r == nil {
		return "", false
	}
	d, ok := r.tags[tag]
	return d, ok
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fill returns n deterministic bytes derived from a seed.
func fill(seed string, n int) []byte {
	out := make([]byte, 0, n+sha256.Size)
	for i := 0; len(out) < n; i++ {
		sum := sha256.Sum256([]byte(seed + "#" + strconv.Itoa(i)))
		out = append(out, sum[:]...)
	}
	return out[:n]
}

// layer builds a gzip-compressed tar layer holding one file and returns the blob and its uncompressed digest (the diff ID). The
// gzip stream is stored, not deflated, so that its bytes do not depend on the compressor of the Go release.
func layer(seed string, size int) (blob []byte, diffID string) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	content := fill(seed, size)
	hdr := &tar.Header{Name: "ocistub/" + hex.EncodeToString([]byte(seed))[:16], Mode: 0o644, Size: int64(len(content)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
	if err := tw.WriteHeader(hdr); err != nil {
		panic(err) // writing to a buffer
	}
	if _, err := tw.Write(content); err != nil {
		panic(err)
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	var gz bytes.Buffer
	zw, err := gzip.NewWriterLevel(&gz, gzip.NoCompression)
	if err != nil {
		panic(err)
	}
	if _, err := zw.Write(raw.Bytes()); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return gz.Bytes(), sha(raw.Bytes())
}

func (c *Corpus) addBlob(r *repoDef, data []byte) string {
	d := sha(data)
	c.blobs[d] = data
	r.members[d] = true
	return d
}

func (c *Corpus) addManifest(r *repoDef, mediaType string, v any) *object {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	o := &object{mediaType: mediaType, data: data, digest: sha(data)}
	c.manifests[o.digest] = o
	r.members[o.digest] = true
	return o
}

func mediaTypes(flavor string) (manifest, index, config, layerType string) {
	if flavor == FlavorDocker {
		return MediaDockerManifest, MediaDockerList, MediaDockerConfig, MediaDockerLayer
	}
	return MediaOCIManifest, MediaOCIIndex, MediaOCIConfig, MediaOCILayer
}

// parsePlatform reads "os/arch[/variant]".
func parsePlatform(s string) Platform {
	parts := strings.Split(s, "/")
	p := Platform{OS: parts[0]}
	if len(parts) > 1 {
		p.Architecture = parts[1]
	}
	if len(parts) > 2 {
		p.Variant = parts[2]
	}
	return p
}

type imageConfig struct {
	Architecture string           `json:"architecture"`
	OS           string           `json:"os"`
	Variant      string           `json:"variant,omitempty"`
	Created      string           `json:"created"`
	Config       map[string]any   `json:"config"`
	RootFS       map[string]any   `json:"rootfs"`
	History      []map[string]any `json:"history"`
}

// buildImage generates one platform manifest: a base layer shared by every image of the platform, an application layer of the
// seed and a configuration naming both.
func (c *Corpus) buildImage(r *repoDef, rc recipe, platform string) *object {
	manifestType, _, configType, layerType := mediaTypes(rc.Flavor)
	p := parsePlatform(platform)
	baseBlob, baseDiff := layer("base/"+platform, 8192)
	appBlob, appDiff := layer("app/"+rc.Seed+"#"+strconv.Itoa(rc.Rev)+"/"+platform, 4096)
	cfg := imageConfig{
		Architecture: p.Architecture, OS: p.OS, Variant: p.Variant, Created: created,
		Config: map[string]any{
			"Cmd":    []string{"/bin/sh"},
			"Labels": map[string]string{"org.opencontainers.image.title": rc.Seed, "org.opencontainers.image.revision": strconv.Itoa(rc.Rev)},
		},
		RootFS:  map[string]any{"type": "layers", "diff_ids": []string{baseDiff, appDiff}},
		History: []map[string]any{{"created": created, "created_by": "ocistub base " + platform}, {"created": created, "created_by": "ocistub app " + rc.Seed}},
	}
	cfgData, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	m := imageManifest{
		SchemaVersion: 2, MediaType: manifestType,
		Config: Descriptor{MediaType: configType, Digest: c.addBlob(r, cfgData), Size: int64(len(cfgData))},
		Layers: []Descriptor{
			{MediaType: layerType, Digest: c.addBlob(r, baseBlob), Size: int64(len(baseBlob))},
			{MediaType: layerType, Digest: c.addBlob(r, appBlob), Size: int64(len(appBlob))},
		},
	}
	return c.addManifest(r, manifestType, m)
}

// buildAttestation generates the attestation manifest BuildKit attaches to a platform manifest: an image manifest of unknown
// platform whose one layer is an in-toto statement about the child.
func (c *Corpus) buildAttestation(r *repoDef, child *object, name string) *object {
	statement, err := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v0.1",
		"predicateType": "https://spdx.dev/Document",
		"subject":       []map[string]any{{"name": "pkg:docker/" + name, "digest": map[string]string{"sha256": strings.TrimPrefix(child.digest, "sha256:")}}},
		"predicate":     map[string]any{"name": name, "spdxVersion": "SPDX-2.3"},
	})
	if err != nil {
		panic(err)
	}
	cfg, err := json.Marshal(imageConfig{
		Architecture: "unknown", OS: "unknown", Created: created, Config: map[string]any{},
		RootFS: map[string]any{"type": "layers", "diff_ids": []string{sha(statement)}}, History: []map[string]any{},
	})
	if err != nil {
		panic(err)
	}
	m := imageManifest{
		SchemaVersion: 2, MediaType: MediaOCIManifest,
		Config: Descriptor{MediaType: MediaOCIConfig, Digest: c.addBlob(r, cfg), Size: int64(len(cfg))},
		Layers: []Descriptor{{
			MediaType: MediaInToto, Digest: c.addBlob(r, statement), Size: int64(len(statement)),
			Annotations: map[string]string{annotationPredicateType: "https://spdx.dev/Document"},
		}},
	}
	return c.addManifest(r, MediaOCIManifest, m)
}

// build generates the top-level manifest of a recipe in a repository and returns it.
func (c *Corpus) build(r *repoDef, name string, rc recipe) *object {
	_, indexType, _, _ := mediaTypes(rc.Flavor)
	if len(rc.Platforms) == 1 && !rc.Index {
		o := c.buildImage(r, rc, rc.Platforms[0])
		r.recipes[o.digest] = rc
		return o
	}
	idx := indexManifest{SchemaVersion: 2, MediaType: indexType}
	var attestations []Descriptor
	for _, platform := range rc.Platforms {
		child := c.buildImage(r, rc, platform)
		p := parsePlatform(platform)
		d := child.descriptor()
		d.Platform = &p
		idx.Manifests = append(idx.Manifests, d)
		if rc.Attest {
			att := c.buildAttestation(r, child, name)
			ad := att.descriptor()
			ad.Platform = &Platform{Architecture: "unknown", OS: "unknown"}
			ad.Annotations = map[string]string{AnnotationReferenceType: ReferenceTypeAttestation, AnnotationReferenceDigest: child.digest}
			attestations = append(attestations, ad)
		}
	}
	idx.Manifests = append(idx.Manifests, attestations...)
	o := c.addManifest(r, indexType, idx)
	r.recipes[o.digest] = rc
	return o
}

// attach adds a manifest that names a subject (a signature or an SBOM) to the repository and to the referrers of the subject.
func (c *Corpus) attach(r *repoDef, subject *object, artifactType, seed string) *object {
	payload := fill("artifact/"+seed, 256)
	empty := []byte("{}")
	m := imageManifest{
		SchemaVersion: 2, MediaType: MediaOCIManifest, ArtifactType: artifactType,
		Config:      Descriptor{MediaType: MediaOCIEmpty, Digest: c.addBlob(r, empty), Size: int64(len(empty))},
		Layers:      []Descriptor{{MediaType: artifactType, Digest: c.addBlob(r, payload), Size: int64(len(payload))}},
		Subject:     ptr(subject.descriptor()),
		Annotations: map[string]string{"org.opencontainers.image.created": created},
	}
	o := c.addManifest(r, MediaOCIManifest, m)
	r.referrers[subject.digest] = append(r.referrers[subject.digest], o.digest)
	return o
}

func ptr[T any](v T) *T { return &v }

// referrersIndex is the image index the referrers API and the tag schema fallback answer for a subject, in the order the
// referrers were added, optionally narrowed to an artifact type.
func (c *Corpus) referrersIndex(r *repoDef, subject, artifactType string) *object {
	idx := indexManifest{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{}}
	for _, d := range r.referrers[subject] {
		o := c.manifests[d]
		var m imageManifest
		if err := json.Unmarshal(o.data, &m); err != nil {
			continue
		}
		if artifactType != "" && m.ArtifactType != artifactType {
			continue
		}
		desc := o.descriptor()
		desc.ArtifactType = m.ArtifactType
		desc.Annotations = m.Annotations
		idx.Manifests = append(idx.Manifests, desc)
	}
	data, err := json.Marshal(idx)
	if err != nil {
		panic(err)
	}
	return &object{mediaType: MediaOCIIndex, data: data, digest: sha(data)}
}

// Default builds the corpus of the stub:
//
//	library/busybox   latest, 1.36: Docker manifest list (linux/amd64, linux/arm64/v8); 1.35: Docker manifest, linux/amd64
//	library/alpine    3, 3.20, latest: OCI index (both platforms and an attestation manifest of each); 3.19: OCI manifest
//	library/nginx     1, 1.27, latest: Docker manifest list (both platforms)
//	acme/a            1.0, 1.1, 1.2, 2.0, latest: OCI manifests, one image each
//	acme/b            1.0, latest: OCI indexes (both platforms, two attestation manifests)
//	other/c           1.0: OCI manifest
//	app, library/app  1: OCI manifest, the same image under both names (R-OCI-027, R-OCI-025)
//	acme/tool         1: OCI manifest (R-OCI-027)
//	acme/signed       1.0: OCI manifest with a signature and an SBOM as referrers, and the tag of the referrers tag schema
//	bulk/tags         latest and t0001 to t2499: ten images in turn
func Default() *Corpus {
	c := newCorpus()
	both := []string{"linux/amd64", "linux/arm64/v8"}
	add := func(name string, rc recipe, tags ...string) *object {
		r := c.repos[name]
		if r == nil {
			r = newRepoDef()
			c.repos[name] = r
		}
		o := c.build(r, name, rc)
		for _, t := range tags {
			r.tags[t] = o.digest
		}
		return o
	}
	add("library/busybox", recipe{Flavor: FlavorDocker, Platforms: both, Seed: "busybox/1.36"}, "latest", "1.36")
	add("library/busybox", recipe{Flavor: FlavorDocker, Platforms: both[:1], Seed: "busybox/1.35"}, "1.35")
	add("library/alpine", recipe{Flavor: FlavorOCI, Platforms: both, Attest: true, Seed: "alpine/3.20"}, "3", "3.20", "latest")
	add("library/alpine", recipe{Flavor: FlavorOCI, Platforms: both[:1], Seed: "alpine/3.19"}, "3.19")
	add("library/nginx", recipe{Flavor: FlavorDocker, Platforms: both, Seed: "nginx/1.27"}, "1", "1.27", "latest")
	for _, t := range []string{"1.0", "1.1", "1.2", "2.0", "latest"} {
		add("acme/a", recipe{Flavor: FlavorOCI, Platforms: both[:1], Seed: "acme/a/" + t}, t)
	}
	add("acme/b", recipe{Flavor: FlavorOCI, Platforms: both, Attest: true, Seed: "acme/b/1.0"}, "1.0")
	add("acme/b", recipe{Flavor: FlavorOCI, Platforms: both, Attest: true, Seed: "acme/b/latest"}, "latest")
	// The fixtures of the virtual repository criteria (R-OCI-027): app:1 as a local repository has it and as a Docker Hub mode
	// remote asks for it (library/app, the same image), and acme/tool:1, which only the stub has.
	add("app", recipe{Flavor: FlavorOCI, Platforms: both[:1], Seed: "app/1"}, "1")
	add("library/app", recipe{Flavor: FlavorOCI, Platforms: both[:1], Seed: "app/1"}, "1")
	add("acme/tool", recipe{Flavor: FlavorOCI, Platforms: both[:1], Seed: "acme/tool/1"}, "1")
	add("other/c", recipe{Flavor: FlavorOCI, Platforms: both[:1], Seed: "other/c/1.0"}, "1.0")

	signed := add("acme/signed", recipe{Flavor: FlavorOCI, Platforms: both[:1], Seed: "acme/signed/1.0"}, "1.0")
	sr := c.repos["acme/signed"]
	c.attach(sr, signed, ArtifactSignature, "acme/signed/signature")
	c.attach(sr, signed, ArtifactSBOM, "acme/signed/sbom")
	fallback := c.referrersIndex(sr, signed.digest, "")
	c.manifests[fallback.digest] = fallback
	sr.members[fallback.digest] = true
	sr.tags[FallbackTag(signed.digest)] = fallback.digest

	var variants [10]*object
	br := newRepoDef()
	c.repos[BulkRepository] = br
	for i := range variants {
		variants[i] = c.build(br, BulkRepository, recipe{Flavor: FlavorOCI, Platforms: both[:1], Seed: "bulk/" + strconv.Itoa(i)})
	}
	br.tags["latest"] = variants[0].digest
	for i := 1; i < BulkTags; i++ {
		br.tags[fmt.Sprintf("t%04d", i)] = variants[i%len(variants)].digest
	}
	return c
}

// FallbackTag is the tag of the referrers tag schema for a subject digest: "sha256-<hex>".
func FallbackTag(digest string) string { return strings.Replace(digest, ":", "-", 1) }
