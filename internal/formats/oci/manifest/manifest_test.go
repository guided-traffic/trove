package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// The limits of the descriptor of the format (R-OCI-020).
func limits() untrusted.Limits {
	return untrusted.DefaultLimits().Tighten(untrusted.Limits{JSONDepth: 32, JSONMembers: 100_000, DocumentBytes: 4 << 20})
}

func dg(seed string) string {
	s := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(s[:])
}

// The documents below are written by hand after the examples of the OCI image specification (the image manifest, the image index
// with platforms) and the Docker image manifest v2 schema 2 documentation; no document of a third-party test suite is copied.
func imageManifest() string {
	return fmt.Sprintf(`{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "config": {"mediaType": "application/vnd.oci.image.config.v1+json", "digest": %q, "size": 7023},
  "layers": [
    {"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": %q, "size": 32654},
    {"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": %q, "size": 16724},
    {"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": %q, "size": 73109}
  ],
  "subject": {"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": %q, "size": 7682},
  "annotations": {"com.example.key1": "value1", "com.example.key2": "value2"}
}`, dg("config"), dg("l1"), dg("l2"), dg("l3"), dg("subject"))
}

func imageIndex() string {
	return fmt.Sprintf(`{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {"mediaType": "application/vnd.oci.image.manifest.v1+json", "size": 7143, "digest": %q,
     "platform": {"architecture": "ppc64le", "os": "linux"}},
    {"mediaType": "application/vnd.oci.image.manifest.v1+json", "size": 7682, "digest": %q,
     "platform": {"architecture": "amd64", "os": "linux", "os.version": "10.0", "variant": "v2", "features": ["sse4"]}},
    {"mediaType": "application/vnd.oci.image.manifest.v1+json", "size": 567, "digest": %q,
     "platform": {"architecture": "unknown", "os": "unknown"},
     "annotations": {"vnd.docker.reference.type": "attestation-manifest"}}
  ],
  "annotations": {"com.example.key1": "value1"}
}`, dg("m1"), dg("m2"), dg("m3"))
}

func dockerManifest() string {
	return fmt.Sprintf(`{
  "schemaVersion": 2,
  "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
  "config": {"mediaType": "application/vnd.docker.container.image.v1+json", "size": 7023, "digest": %q},
  "layers": [
    {"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "size": 32654, "digest": %q},
    {"mediaType": "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip", "size": 1000, "digest": %q,
     "urls": ["https://mcr.example.com/layer"]}
  ]
}`, dg("dconfig"), dg("dlayer"), dg("dforeign"))
}

func dockerList() string {
	return fmt.Sprintf(`{
  "schemaVersion": 2,
  "mediaType": "application/vnd.docker.distribution.manifest.list.v2+json",
  "manifests": [
    {"mediaType": "application/vnd.docker.distribution.manifest.v2+json", "size": 7143, "digest": %q,
     "platform": {"architecture": "ppc64le", "os": "linux"}},
    {"mediaType": "application/vnd.docker.distribution.manifest.v2+json", "size": 7682, "digest": %q,
     "platform": {"architecture": "arm", "os": "linux", "variant": "v7"}}
  ]
}`, dg("dm1"), dg("dm2"))
}

const (
	tImage = MediaTypeImageManifest
	tIndex = MediaTypeImageIndex
	tDm    = MediaTypeDockerManifest
	tDl    = MediaTypeDockerList
)

func parse(t *testing.T, doc, ct string) (*Manifest, error) {
	t.Helper()
	return Parse([]byte(doc), ct, limits())
}

func wantCode(t *testing.T, err error, kind errs.Kind, code string) {
	t.Helper()
	e, ok := errs.As(err)
	if !ok {
		t.Fatalf("error = %v, want an *errs.Error", err)
	}
	if e.Kind != kind || e.Code != code {
		t.Fatalf("error = %s/%s (%s), want %s/%s", e.Kind, e.Code, e.Detail, kind, code)
	}
}

// Mechanism of R-OCI-013, not a proof of its criteria: it checks the parser, one step of the commit transaction or a store query;
// the criteria are the refusals by size and media type, the foreign layer, the artifact round trip and the concurrent tag.
func TestParseValidDocuments(t *testing.T) {
	t.Run("an OCI image manifest", func(t *testing.T) {
		m, err := parse(t, imageManifest(), tImage)
		if err != nil {
			t.Fatal(err)
		}
		if m.Type != TypeImage || m.MediaType != tImage || m.Config.Size != 7023 || len(m.Layers) != 3 {
			t.Fatalf("manifest = %+v", m)
		}
		if m.Subject == nil || m.Subject.Digest != dg("subject") {
			t.Fatalf("subject = %+v", m.Subject)
		}
		if m.ContentSize() != 7023+32654+16724+73109 {
			t.Fatalf("content size = %d", m.ContentSize())
		}
		sum := sha256.Sum256([]byte(imageManifest()))
		if m.Digest != "sha256:"+hex.EncodeToString(sum[:]) || m.Sum != sum || m.Size != int64(len(imageManifest())) {
			t.Fatalf("digest %s size %d is not over the exact bytes", m.Digest, m.Size)
		}
		if m.EffectiveArtifactType() != "application/vnd.oci.image.config.v1+json" {
			t.Fatalf("artifact type = %q", m.EffectiveArtifactType())
		}
		if len(m.Annotations) != 2 {
			t.Fatalf("annotations = %v", m.Annotations)
		}
	})
	t.Run("an OCI index with platforms and an attestation entry", func(t *testing.T) {
		m, err := parse(t, imageIndex(), tIndex)
		if err != nil {
			t.Fatal(err)
		}
		refs := m.References()
		if m.Type != TypeIndex || len(refs) != 3 {
			t.Fatalf("manifest = %+v refs = %+v", m, refs)
		}
		want := []string{"linux/ppc64le", "linux/amd64/v2", "unknown/unknown"}
		for i, r := range refs {
			if r.Kind != KindManifest || r.Platform != want[i] || !r.Required {
				t.Errorf("ref %d = %+v, want platform %s", i, r, want[i])
			}
		}
		if m.EffectiveArtifactType() != "" {
			t.Fatalf("an index has no artifact type of its own: %q", m.EffectiveArtifactType())
		}
	})
	t.Run("a Docker schema 2 manifest with a foreign layer", func(t *testing.T) {
		m, err := parse(t, dockerManifest(), tDm)
		if err != nil {
			t.Fatal(err)
		}
		refs := m.References()
		if len(refs) != 3 || !refs[0].Required || !refs[1].Required || refs[2].Required {
			t.Fatalf("refs = %+v; the foreign layer is not required", refs)
		}
	})
	t.Run("a Docker manifest list", func(t *testing.T) {
		m, err := parse(t, dockerList(), tDl)
		if err != nil {
			t.Fatal(err)
		}
		if refs := m.References(); len(refs) != 2 || refs[1].Platform != "linux/arm/v7" {
			t.Fatalf("refs = %+v", refs)
		}
	})
	t.Run("an OCI manifest without a mediaType field is read by its Content-Type", func(t *testing.T) {
		doc := strings.Replace(imageManifest(), `"mediaType": "application/vnd.oci.image.manifest.v1+json",`, "", 1)
		if _, err := parse(t, doc, tImage); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("parameters of the Content-Type are dropped", func(t *testing.T) {
		m, err := parse(t, imageManifest(), tImage+"; charset=utf-8")
		if err != nil || m.MediaType != tImage {
			t.Fatalf("%v %+v", err, m)
		}
	})
	t.Run("an artifact with the empty config descriptor and an artifactType", func(t *testing.T) {
		doc := fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"artifactType":"application/vnd.example+json",`+
			`"config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":%q,"size":2,"data":"e30="},`+
			`"layers":[{"mediaType":"application/vnd.oci.empty.v1+json","digest":%q,"size":2}]}`, tImage, dg("{}"), dg("{}"))
		m, err := parse(t, doc, tImage)
		if err != nil {
			t.Fatal(err)
		}
		if refs := m.References(); len(refs) != 1 {
			t.Fatalf("the empty descriptor as config and layer is one target: %+v", refs)
		}
		if m.EffectiveArtifactType() != "application/vnd.example+json" {
			t.Fatalf("artifact type = %q", m.EffectiveArtifactType())
		}
	})
	t.Run("unknown fields are accepted and ignored", func(t *testing.T) {
		doc := strings.Replace(imageManifest(), `"layers"`, `"x-future": {"a": [1, 2.5, null, true]}, "layers"`, 1)
		if _, err := parse(t, doc, tImage); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a manifest without layers is accepted", func(t *testing.T) {
		doc := fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":%q,"size":0},"layers":[]}`, dg("c"))
		if _, err := parse(t, doc, tImage); err != nil {
			t.Fatal(err)
		}
	})
}

// Mechanism of R-OCI-013, not a proof of its criteria: it checks the parser, one step of the commit transaction or a store query;
// the criteria are the refusals by size and media type, the foreign layer, the artifact round trip and the concurrent tag.
func TestParseRefusals(t *testing.T) {
	d := dg("x")
	desc := fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":1}`, d)
	image := func(extra string) string {
		return fmt.Sprintf(`{"schemaVersion":2,"config":%s,"layers":[%s]%s}`, desc, desc, extra)
	}
	index := func(body string) string { return `{"schemaVersion":2,"manifests":[` + body + `]}` }
	cases := []struct {
		name, doc, ct string
		kind          errs.Kind
		code          string
	}{
		{"schema 1", image(""), "application/vnd.docker.distribution.manifest.v1+json", errs.Invalid, errs.CodeOCIManifestInvalid},
		{"schema 1 signed", image(""), "application/vnd.docker.distribution.manifest.v1+prettyjws", errs.Invalid, errs.CodeOCIManifestInvalid},
		{"another media type", image(""), "application/json", errs.Invalid, errs.CodeOCIManifestInvalid},
		{"no Content-Type", image(""), "", errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a Content-Type that is no media type", image(""), "x;;;=", errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a mediaType field of another type", strings.Replace(imageManifest(), "image.manifest", "image.index", 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a Docker manifest without mediaType", image(""), tDm, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"schemaVersion 1", strings.Replace(image(""), `"schemaVersion":2`, `"schemaVersion":1`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"schemaVersion 2.0", strings.Replace(image(""), `"schemaVersion":2`, `"schemaVersion":2.0`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"schemaVersion as a string", strings.Replace(image(""), `"schemaVersion":2`, `"schemaVersion":"2"`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"no schemaVersion", strings.Replace(image(""), `"schemaVersion":2,`, ``, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"not an object", `[]`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"not JSON", `{`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"empty", ``, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"data after the value", image("") + `{}`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"invalid UTF-8", image(`,"x":"` + "\xff\xfe" + `"`), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a duplicate key at the top", `{"schemaVersion":2,"schemaVersion":2,"config":` + desc + `,"layers":[]}`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a duplicate key hidden by an escape", `{"schemaVersion":2,"config":` + desc + `,"layers":[],"layers":[]}`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a duplicate key in a descriptor", fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":%q,"digest":%q,"size":1},"layers":[]}`, d, dg("y")), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a duplicate key in annotations", image(`,"annotations":{"a":"1","a":"2"}`), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a key that differs in case", strings.Replace(image(""), `"layers"`, `"Layers"`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a descriptor key that differs in case", strings.Replace(image(""), `"digest"`, `"Digest"`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"no config", `{"schemaVersion":2,"layers":[]}`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"no layers", `{"schemaVersion":2,"config":` + desc + `}`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"layers not an array", `{"schemaVersion":2,"config":` + desc + `,"layers":{}}`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"an image manifest with manifests", image(`,"manifests":[]`), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"an index with layers", `{"schemaVersion":2,"manifests":[],"layers":[]}`, tIndex, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"an index with a config", `{"schemaVersion":2,"manifests":[],"config":` + desc + `}`, tIndex, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"an index without manifests", `{"schemaVersion":2}`, tIndex, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a digest of another algorithm", fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"sha512:%s","size":1},"layers":[]}`, strings.Repeat("a", 128)), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a digest with upper-case hex", strings.Replace(image(""), d, strings.ToUpper(d[:7])+strings.ToUpper(d[7:]), 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a digest that is a path", `{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"sha256:../../x","size":1},"layers":[]}`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a short digest", `{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"sha256:ab","size":1},"layers":[]}`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a digest of 65 digits", fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"%s0","size":1},"layers":[]}`, d), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a negative size", strings.Replace(image(""), `"size":1`, `"size":-1`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a fractional size", strings.Replace(image(""), `"size":1`, `"size":1.5`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a size with an exponent", strings.Replace(image(""), `"size":1`, `"size":1e3`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a size with a leading zero", strings.Replace(image(""), `"size":1`, `"size":01`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a size above int64", strings.Replace(image(""), `"size":1`, `"size":9223372036854775808`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a size as a string", strings.Replace(image(""), `"size":1`, `"size":"1"`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"no size", fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":%q},"layers":[]}`, d), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a descriptor without a media type", fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":%q,"size":1},"layers":[]}`, d), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a media type with a parameter", strings.Replace(image(""), `"a/b"`, `"a/b; x=y"`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a media type without a slash", strings.Replace(image(""), `"a/b"`, `"ab"`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"an artifactType that is no media type", image(`,"artifactType":"x y"`), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a descriptor that is not an object", `{"schemaVersion":2,"config":[],"layers":[]}`, tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a subject without a digest", image(`,"subject":{"mediaType":"a/b","size":1}`), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"annotations that are not strings", image(`,"annotations":{"a":1}`), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"annotations that are an array", image(`,"annotations":[]`), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"urls that are not a list", strings.Replace(image(""), `"size":1}`, `"size":1,"urls":"x"}`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a url that is a file", strings.Replace(image(""), `"size":1}`, `"size":1,"urls":["file:///etc/passwd"]}`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a url without a host", strings.Replace(image(""), `"size":1}`, `"size":1,"urls":["http:///x"]}`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"data that is not a string", strings.Replace(image(""), `"size":1}`, `"size":1,"data":5}`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"data that is not base64", strings.Replace(image(""), `"size":1}`, `"size":1,"data":"!!"}`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"data that is not the content of the digest", strings.Replace(image(""), `"size":1}`, `"size":1,"data":"eQ=="}`, 1), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"data of another size", fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":%q,"size":3,"data":"e30="},"layers":[]}`, dg("{}")), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"data that is base64 without its padding", fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":%q,"size":2,"data":"e30"},"layers":[]}`, dg("{}")), tImage, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a platform that is a string", index(fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":1,"platform":"linux/amd64"}`, d)), tIndex, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a platform without os", index(fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":1,"platform":{"architecture":"amd64"}}`, d)), tIndex, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a platform with a slash in a part", index(fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":1,"platform":{"architecture":"a/b","os":"linux"}}`, d)), tIndex, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a platform part that is too long", index(fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":1,"platform":{"architecture":"%s","os":"linux"}}`, d, strings.Repeat("a", 65))), tIndex, errs.Invalid, errs.CodeOCIManifestInvalid},
		{"a platform key that differs in case", index(fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":1,"platform":{"Architecture":"amd64","os":"linux"}}`, d)), tIndex, errs.Invalid, errs.CodeOCIManifestInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := Parse([]byte(c.doc), c.ct, limits())
			if err == nil {
				t.Fatalf("accepted: %+v", m)
			}
			wantCode(t, err, c.kind, c.code)
			if m != nil {
				t.Fatalf("a manifest with an error: %+v", m)
			}
		})
	}
	// ct defaults to the image manifest for the rows above that leave it out.
	_ = tDl
}

func TestR_OCI_020_CraftedBounds(t *testing.T) {
	desc := fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":1}`, dg("x"))
	t.Run("nested 100,000 deep", func(t *testing.T) {
		doc := `{"schemaVersion":2,"x":` + strings.Repeat("[", 100_000) + strings.Repeat("]", 100_000) + `}`
		start := time.Now()
		_, err := parse(t, doc, tImage)
		wantCode(t, err, errs.TooLarge, errs.CodeOCISizeInvalid)
		if time.Since(start) > 5*time.Second {
			t.Fatalf("took %v", time.Since(start))
		}
	})
	t.Run("nested one level past the limit", func(t *testing.T) {
		doc := `{"schemaVersion":2,"x":` + strings.Repeat("[", 31) + `1` + strings.Repeat("]", 31) + `}`
		if _, err := parse(t, doc, tImage); err == nil || errs.KindOf(err) == errs.TooLarge {
			t.Fatalf("depth 32 is the limit and this is 32 with the object: err = %v (expect the shape error, not TooLarge)", err)
		}
		doc = `{"schemaVersion":2,"x":` + strings.Repeat("[", 32) + `1` + strings.Repeat("]", 32) + `}`
		_, err := parse(t, doc, tImage)
		wantCode(t, err, errs.TooLarge, errs.CodeOCISizeInvalid)
	})
	t.Run("20,000 layers", func(t *testing.T) {
		doc := `{"schemaVersion":2,"config":` + desc + `,"layers":[` + strings.TrimSuffix(strings.Repeat(desc+",", 20_000), ",") + `]}`
		_, err := parse(t, doc, tImage)
		wantCode(t, err, errs.TooLarge, errs.CodeOCISizeInvalid)
	})
	t.Run("10,000 descriptors are the largest accepted number", func(t *testing.T) {
		doc := `{"schemaVersion":2,"config":` + desc + `,"layers":[` + strings.TrimSuffix(strings.Repeat(desc+",", MaxDescriptors-1), ",") + `]}`
		if _, err := parse(t, doc, tImage); err != nil {
			t.Fatal(err)
		}
		doc = `{"schemaVersion":2,"config":` + desc + `,"layers":[` + strings.TrimSuffix(strings.Repeat(desc+",", MaxDescriptors), ",") + `]}`
		_, err := parse(t, doc, tImage)
		wantCode(t, err, errs.TooLarge, errs.CodeOCISizeInvalid)
	})
	t.Run("a 5 MiB index", func(t *testing.T) {
		pad := strings.Repeat("a", 5<<20)
		doc := `{"schemaVersion":2,"manifests":[],"annotations":{"k":"` + pad + `"}}`
		_, err := parse(t, doc, tIndex)
		wantCode(t, err, errs.TooLarge, errs.CodeOCISizeInvalid)
	})
	t.Run("exactly 4 MiB is accepted and one byte more is not", func(t *testing.T) {
		head := `{"schemaVersion":2,"manifests":[],"annotations":{"k":"`
		tail := `"}}`
		fill := strings.Repeat("a", 4<<20-len(head)-len(tail))
		doc := head + fill + tail
		if len(doc) != 4<<20 {
			t.Fatalf("len = %d", len(doc))
		}
		if _, err := parse(t, doc, tIndex); err != nil {
			t.Fatalf("4 MiB: %v", err)
		}
		_, err := parse(t, doc+" ", tIndex)
		wantCode(t, err, errs.TooLarge, errs.CodeOCISizeInvalid)
	})
	t.Run("a descriptor digest that climbs out of the layout", func(t *testing.T) {
		doc := `{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"sha256:../../x","size":1},"layers":[]}`
		_, err := parse(t, doc, tImage)
		wantCode(t, err, errs.Invalid, errs.CodeOCIManifestInvalid)
	})
	t.Run("100,001 empty values", func(t *testing.T) {
		doc := `{"schemaVersion":2,"manifests":[],"x":[` + strings.TrimSuffix(strings.Repeat("0,", 100_001), ",") + `]}`
		_, err := parse(t, doc, tIndex)
		wantCode(t, err, errs.TooLarge, errs.CodeOCISizeInvalid)
	})
}

// Mechanism of R-OCI-013, not a proof of its criteria: it checks the parser, one step of the commit transaction or a store query;
// the criteria are the refusals by size and media type, the foreign layer, the artifact round trip and the concurrent tag.
func TestReferencesAreDistinctAndRequiredByAnyDistributableUse(t *testing.T) {
	foreign := fmt.Sprintf(`{"mediaType":%q,"digest":%q,"size":9,"urls":["https://example.com/l"]}`, MediaTypeDockerForeignLayer, dg("f"))
	plain := fmt.Sprintf(`{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":%q,"size":9}`, dg("f"))
	cfg := fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":2}`, dg("cfg"))
	m, err := parse(t, `{"schemaVersion":2,"config":`+cfg+`,"layers":[`+foreign+`,`+cfg+`]}`, tImage)
	if err != nil {
		t.Fatal(err)
	}
	refs := m.References()
	if len(refs) != 2 || refs[0].Digest != dg("cfg") || refs[1].Digest != dg("f") || refs[1].Required {
		t.Fatalf("refs = %+v", refs)
	}
	m, err = parse(t, `{"schemaVersion":2,"config":`+cfg+`,"layers":[`+foreign+`,`+plain+`]}`, tImage)
	if err != nil {
		t.Fatal(err)
	}
	if refs := m.References(); len(refs) != 2 || !refs[1].Required {
		t.Fatalf("a digest named as a foreign layer and as a distributable one is required: %+v", refs)
	}
	for _, mt := range []string{"application/vnd.oci.image.layer.nondistributable.v1.tar", "application/vnd.oci.image.layer.nondistributable.v1.tar+gzip", "application/vnd.oci.image.layer.nondistributable.v1.tar+zstd"} {
		if !(Descriptor{MediaType: mt}).Foreign() {
			t.Errorf("%s is a foreign layer", mt)
		}
	}
	if (Descriptor{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip"}).Foreign() {
		t.Error("an ordinary layer is not foreign")
	}
}

// Mechanism of R-OCI-007, not a proof of its criteria: it checks a row, a bound or a query of the data port; the criteria are the
// digest of the stored bytes, the one object for tag and manifest and the layer shared by two names.
func TestContentSizeSaturates(t *testing.T) {
	big := fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":9223372036854775807}`, dg("b"))
	m, err := parse(t, `{"schemaVersion":2,"config":`+big+`,"layers":[`+big+`,`+big+`]}`, tImage)
	if err != nil {
		t.Fatal(err)
	}
	if m.ContentSize() != maxInt64 {
		t.Fatalf("content size = %d", m.ContentSize())
	}
}

// Mechanism of R-OCI-007, not a proof of its criteria: it checks a row, a bound or a query of the data port; the criteria are the
// digest of the stored bytes, the one object for tag and manifest and the layer shared by two names.
func TestRowAnnotations(t *testing.T) {
	m := &Manifest{Annotations: map[string]string{"b": "2", "a": "1"}}
	raw, cut := m.RowAnnotations(MaxAnnotationBytes)
	if string(raw) != `{"a": "1", "b": "2"}` || cut {
		t.Fatalf("annotations = %s cut=%v", raw, cut)
	}
	if raw, cut := (&Manifest{}).RowAnnotations(100); raw != nil || cut {
		t.Fatalf("no annotations = %s %v", raw, cut)
	}
	// The entries that do not fit are cut, in key order, and the cut is reported.
	m = &Manifest{Annotations: map[string]string{"a": strings.Repeat("x", 60), "b": strings.Repeat("y", 60)}}
	raw, cut = m.RowAnnotations(100)
	if !cut || len(raw) > 100 || !json.Valid(raw) || !strings.Contains(string(raw), `"a"`) || strings.Contains(string(raw), `"b"`) {
		t.Fatalf("annotations = %s cut=%v", raw, cut)
	}
	// An entry with a NUL cannot be stored in jsonb and is left out.
	m = &Manifest{Annotations: map[string]string{"a": "1", "b": "x\x00y"}}
	raw, cut = m.RowAnnotations(MaxAnnotationBytes)
	if !cut || string(raw) != `{"a": "1"}` {
		t.Fatalf("annotations = %s cut=%v", raw, cut)
	}
	m = &Manifest{Annotations: map[string]string{"a\x00": "1"}}
	if raw, cut := m.RowAnnotations(MaxAnnotationBytes); raw != nil || !cut {
		t.Fatalf("only a NUL entry: %s %v", raw, cut)
	}
	// Control characters and the characters Go and PostgreSQL print differently never make the bound too small.
	m = &Manifest{Annotations: map[string]string{"k": " <>&\x01\x7f\"\\"}}
	raw, _ = m.RowAnnotations(MaxAnnotationBytes)
	var back map[string]string
	if err := json.Unmarshal(raw, &back); err != nil || back["k"] != m.Annotations["k"] {
		t.Fatalf("round trip: %v %q", err, back)
	}
}

var (
	digestRE    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	mediaTypeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}$`)
)

// Mechanism of R-OCI-002, not a proof of its criteria: it checks a grammar function against its expression; the criteria are the
// refusals a request through /v2/ receives and the repository keys.
func TestValidDigestEqualsItsExpression(t *testing.T) {
	cases := []string{
		"", "sha256:", dg("a"), strings.ToUpper(dg("a")), "SHA256:" + dg("a")[7:], "sha256:" + strings.Repeat("g", 64),
		dg("a") + "0", dg("a")[:len(dg("a"))-1], "sha512:" + strings.Repeat("a", 128), " " + dg("a"), dg("a") + "\n",
		"sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("0", 63) + "é",
	}
	for _, s := range cases {
		if got, want := ValidDigest(s), digestRE.MatchString(s); got != want {
			t.Errorf("ValidDigest(%q) = %v, expression says %v", s, got, want)
		}
	}
	b, ok := ParseDigest(dg("a"))
	sum := sha256.Sum256([]byte("a"))
	if !ok || b != sum {
		t.Fatalf("ParseDigest = %x %v", b, ok)
	}
	if _, ok := ParseDigest("sha256:zz"); ok {
		t.Fatal("ParseDigest accepted a bad digest")
	}
}

// Mechanism of R-OCI-013, not a proof of its criteria: it checks the parser, one step of the commit transaction or a store query;
// the criteria are the refusals by size and media type, the foreign layer, the artifact round trip and the concurrent tag.
func TestValidMediaTypeEqualsItsExpression(t *testing.T) {
	cases := []string{
		"", "a/b", "application/vnd.oci.image.manifest.v1+json", "application/vnd.cncf.helm.chart.content.v1.tar+gzip", "a/", "/b", "a/b/c",
		"-a/b", "a/-b", "a b/c", "a/b;x=y", "a/b ", "A/B", "a/" + strings.Repeat("b", 127), "a/" + strings.Repeat("b", 128),
		strings.Repeat("a", 127) + "/b", "a/b+c", "a/b!#$&^_.+-", "ä/b", "a/b\n",
	}
	for _, s := range cases {
		want := mediaTypeRE.MatchString(s) && len(s) <= MaxMediaTypeBytes
		if got := ValidMediaType(s); got != want {
			t.Errorf("ValidMediaType(%q) = %v, expression says %v", s, got, want)
		}
	}
}

// A document whose members the standard decoder would read differently from this reader is refused: the pairs below are the
// shapes a second reader (a client, another registry) is known to disagree about.
//
// Mechanism of R-OCI-020, not a proof of its criteria: it checks digest verification or an ambiguity rule of the parser; the
// criteria are the crafted inputs refused before any blob is looked up and the memory bound.
func TestAmbiguousDocumentsAreRefused(t *testing.T) {
	d := dg("x")
	desc := fmt.Sprintf(`{"mediaType":"a/b","digest":%q,"size":1}`, d)
	for name, doc := range map[string]string{
		"MEDIATYPE":             `{"schemaVersion":2,"MEDIATYPE":"x/y","config":` + desc + `,"layers":[]}`,
		"Config":                `{"schemaVersion":2,"Config":` + desc + `,"config":` + desc + `,"layers":[]}`,
		"SchemaVersion":         `{"SchemaVersion":3,"schemaVersion":2,"config":` + desc + `,"layers":[]}`,
		"Subject":               `{"schemaVersion":2,"Subject":` + desc + `,"config":` + desc + `,"layers":[]}`,
		"ArtifactType":          `{"schemaVersion":2,"ArtifactType":"a/b","config":` + desc + `,"layers":[]}`,
		"Annotations":           `{"schemaVersion":2,"Annotations":{},"config":` + desc + `,"layers":[]}`,
		"escaped lookalike key": `{"schemaVersion":2,"Config":` + desc + `,"config":` + desc + `,"layers":[]}`,
	} {
		if _, err := parse(t, doc, tImage); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func FuzzR_OCI_020_Parse(f *testing.F) {
	for _, s := range []string{imageManifest(), imageIndex(), dockerManifest(), dockerList()} {
		f.Add([]byte(s), 0)
		f.Add([]byte(s), 1)
	}
	f.Add([]byte(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"sha256:../../x","size":1},"layers":[]}`), 0)
	f.Add([]byte(`{"schemaVersion":2,"schemaVersion":2}`), 2)
	f.Add([]byte(strings.Repeat("[", 5000)), 0)
	f.Add([]byte(`{"schemaVersion":2,"manifests":[{"mediaType":"a/b","digest":"sha256:`+strings.Repeat("0", 64)+`","size":1,"platform":{"os":"\ud800","architecture":"x"}}]}`), 1)
	types := []string{tImage, tIndex, tDm, tDl}
	lim := limits()
	f.Fuzz(func(t *testing.T, data []byte, which int) {
		ct := types[((which%len(types))+len(types))%len(types)]
		m, err := Parse(data, ct, lim)
		if err != nil {
			e, ok := errs.As(err)
			if !ok || (e.Kind != errs.Invalid && e.Kind != errs.TooLarge) {
				t.Fatalf("Parse failed with %v, want an invalid or too_large *errs.Error", err)
			}
			if m != nil {
				t.Fatal("a manifest with an error")
			}
			return
		}
		sum := sha256.Sum256(data)
		if m.Sum != sum || m.Digest != "sha256:"+hex.EncodeToString(sum[:]) || m.Size != int64(len(data)) {
			t.Fatal("the digest is not over the exact bytes")
		}
		if m.MediaType != ct {
			t.Fatalf("media type %q for content type %q", m.MediaType, ct)
		}
		check := func(d Descriptor) {
			if !ValidDigest(d.Digest) || !ValidMediaType(d.MediaType) || d.Size < 0 {
				t.Fatalf("descriptor %+v passed", d)
			}
		}
		switch m.Type {
		case TypeImage:
			if m.Config == nil || len(m.Manifests) != 0 {
				t.Fatalf("image shape: %+v", m)
			}
			check(*m.Config)
		case TypeIndex:
			if m.Config != nil || len(m.Layers) != 0 {
				t.Fatalf("index shape: %+v", m)
			}
		default:
			t.Fatalf("type %d", m.Type)
		}
		for _, d := range append(append([]Descriptor{}, m.Layers...), m.Manifests...) {
			check(d)
			if d.Platform != nil && len(d.Platform.String()) > MaxPlatformBytes {
				t.Fatalf("platform %q", d.Platform.String())
			}
		}
		if len(m.Layers)+len(m.Manifests)+1 > MaxDescriptors+1 {
			t.Fatal("more descriptors than the bound")
		}
		seen := map[string]bool{}
		for _, r := range m.References() {
			if !ValidDigest(r.Digest) || seen[r.Digest] {
				t.Fatalf("reference %+v", r)
			}
			seen[r.Digest] = true
			if _, ok := ParseDigest(r.Digest); !ok {
				t.Fatalf("reference %q does not parse", r.Digest)
			}
		}
		if raw, _ := m.RowAnnotations(MaxAnnotationBytes); raw != nil && (!json.Valid(raw) || len(raw) > MaxAnnotationBytes) {
			t.Fatalf("row annotations: %q", raw)
		}
		if m.ContentSize() < 0 {
			t.Fatal("negative content size")
		}
	})
}
