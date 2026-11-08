package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

// indexOf builds an OCI image index over manifests; an entry is "<digest>" or "<digest> <os>/<arch>[/<variant>]".
func indexOf(entries ...string) []byte {
	var b strings.Builder
	b.WriteString(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[`)
	for i, e := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		digest, platform, _ := strings.Cut(e, " ")
		fmt.Fprintf(&b, `{"mediaType":%q,"digest":%q,"size":3`, ctImage, digest)
		if platform != "" {
			parts := strings.Split(platform, "/")
			fmt.Fprintf(&b, `,"platform":{"os":%q,"architecture":%q`, parts[0], parts[1])
			if len(parts) > 2 {
				fmt.Fprintf(&b, `,"variant":%q`, parts[2])
			}
			b.WriteByte('}')
		}
		b.WriteByte('}')
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func hexOf(digest string) [32]byte {
	var d [32]byte
	b, _ := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	copy(d[:], b)
	return d
}

func TestR_OCI_009_PullAManifestByTagAndByDigest(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	m, digest := fx.image("app", "1", "layer")

	for _, ref := range []string{"1", digest} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			resp := fx.request(method, "app/manifests/"+ref, nil, "")
			fx.wantStatus(resp, http.StatusOK, "")
			if got := resp.Header().Get("Docker-Content-Digest"); got != digest {
				t.Errorf("%s %s: Docker-Content-Digest = %q, want %q", method, ref, got, digest)
			}
			if got := resp.Header().Get("Content-Type"); got != ctImage {
				t.Errorf("%s %s: Content-Type = %q", method, ref, got)
			}
			if got := resp.Header().Get("ETag"); got != `"`+digest+`"` {
				t.Errorf("%s %s: ETag = %q, want the digest in quotes", method, ref, got)
			}
			if got := resp.Header().Get("Content-Length"); got != fmt.Sprint(len(m)) {
				t.Errorf("%s %s: Content-Length = %q", method, ref, got)
			}
			if method == http.MethodHead && resp.Body.Len() != 0 {
				t.Errorf("HEAD %s answered a body", ref)
			}
			if method == http.MethodGet {
				sum := sha256.Sum256(resp.Body.Bytes())
				if !bytes.Equal(resp.Body.Bytes(), m) || "sha256:"+hex.EncodeToString(sum[:]) != digest {
					t.Errorf("GET %s: the body is not the pushed bytes", ref)
				}
			}
		}
	}
}

func TestR_OCI_009_TheStoredManifestIsServedWhateverAcceptSays(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	cfg := fx.blob("app", []byte("{}"))
	layer := fx.blob("app", []byte("layer"))
	doc := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json",`+
		`"config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":2,"digest":%q},`+
		`"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","size":5,"digest":%q}]}`, cfg, layer))
	fx.wantStatus(fx.put("app/manifests/1", doc, "application/vnd.docker.distribution.manifest.v2+json"), http.StatusCreated, "")
	resp := fx.get("app/manifests/1", func(r *http.Request) { r.Header.Set("Accept", ctImage) })
	fx.wantStatus(resp, http.StatusOK, "")
	if resp.Header().Get("Content-Type") != "application/vnd.docker.distribution.manifest.v2+json" || !bytes.Equal(resp.Body.Bytes(), doc) {
		t.Fatalf("Content-Type %q, body equal %v", resp.Header().Get("Content-Type"), bytes.Equal(resp.Body.Bytes(), doc))
	}
}

func TestR_OCI_009_PullRefusals(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	fx.image("app", "1", "layer")
	zero := "sha256:" + strings.Repeat("0", 64)
	for _, tc := range []struct {
		name, path string
		status     int
		code       string
	}{
		{"an unknown tag", "app/manifests/2", 404, "MANIFEST_UNKNOWN"},
		{"an unknown digest", "app/manifests/" + zero, 404, "MANIFEST_UNKNOWN"},
		{"an unknown name", "other/manifests/1", 404, "MANIFEST_UNKNOWN"},
		{"a tag outside the grammar is unknown", "app/manifests/-x", 404, "MANIFEST_UNKNOWN"},
		{"an overlong tag is unknown", "app/manifests/" + strings.Repeat("a", 129), 404, "MANIFEST_UNKNOWN"},
		{"another algorithm", "app/manifests/sha512:" + strings.Repeat("a", 128), 400, "DIGEST_INVALID"},
		{"a short digest", "app/manifests/sha256:" + strings.Repeat("a", 63), 400, "DIGEST_INVALID"},
		{"an upper-case digest", "app/manifests/sha256:" + strings.Repeat("A", 64), 400, "DIGEST_INVALID"},
		{"an upper-case name", "App/manifests/1", 400, "NAME_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) { fx.wantStatus(fx.get(tc.path), tc.status, tc.code) })
	}
}

func TestR_OCI_009_AManifestIsServedByDigestWhetherOrNotATagPointsAtIt(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	_, first := fx.image("app", "latest", "one")
	_, second := fx.image("app", "latest", "two") // moves the tag away
	if first == second {
		t.Fatal("the two images are the same")
	}
	fx.wantStatus(fx.get("app/manifests/"+first), http.StatusOK, "")
	if got := fx.get("app/manifests/latest").Header().Get("Docker-Content-Digest"); got != second {
		t.Fatalf("the tag points at %s, want %s", got, second)
	}
}

func TestR_OCI_010_PullABlobOfAName(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	data := []byte(strings.Repeat("layer bytes ", 100))
	d := fx.blob("app", data)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		resp := fx.request(method, "app/blobs/"+d, nil, "")
		fx.wantStatus(resp, http.StatusOK, "")
		if resp.Header().Get("Content-Type") != "application/octet-stream" || resp.Header().Get("Docker-Content-Digest") != d ||
			resp.Header().Get("ETag") != `"`+d+`"` || resp.Header().Get("Content-Length") != fmt.Sprint(len(data)) {
			t.Errorf("%s headers = %v", method, resp.Header())
		}
		if method == http.MethodGet && !bytes.Equal(resp.Body.Bytes(), data) {
			t.Errorf("the blob differs")
		}
	}
	// A layer pushed to one name is not readable through another, although the store holds the bytes.
	fx.wantStatus(fx.get("other/blobs/"+d), http.StatusNotFound, "BLOB_UNKNOWN")
	for _, bad := range []string{"sha512:" + strings.Repeat("a", 128), "sha256:" + strings.Repeat("a", 63), "sha256:" + strings.ToUpper(strings.Repeat("a", 64))} {
		fx.wantStatus(fx.get("app/blobs/"+bad), http.StatusBadRequest, "DIGEST_INVALID")
	}
	fx.wantStatus(fx.get("app/blobs/sha256:"+strings.Repeat("0", 64)), http.StatusNotFound, "BLOB_UNKNOWN")
}

func TestR_OCI_008_ValidatorsAreTheRegistrysForm(t *testing.T) {
	mk := func(inm string) *formats.Request {
		hr, _ := http.NewRequest(http.MethodGet, "https://trove.test/x", nil)
		hr.Header.Set("If-None-Match", inm)
		hr.Header.Set("If-Range", `"sha256:abc"`)
		return &formats.Request{Request: hr}
	}
	rq := mk(`"sha256:aa", W/"sha256:bb", "other"`)
	got := storedValidators(rq)
	if v := got.Header.Get("If-None-Match"); v != `"aa", W/"bb", "other"` {
		t.Errorf("If-None-Match = %q", v)
	}
	if v := got.Header.Get("If-Range"); v != `"abc"` {
		t.Errorf("If-Range = %q", v)
	}
	if rq.Header.Get("If-None-Match") != `"sha256:aa", W/"sha256:bb", "other"` {
		t.Error("the caller's request was changed")
	}
	plain := mk(`"aa"`)
	plain.Header.Del("If-Range")
	if storedValidators(plain) != plain {
		t.Error("a request without registry-form tags is passed on as it is")
	}
}

func TestR_OCI_013_PushByTagCommitsTheTagItemTheManifestItemAndTheRows(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	cfg := fx.blob("team/app", []byte("{}"))
	layer := fx.blob("team/app", []byte("l1"))
	m := imageManifestBytes(cfg, layer)
	d := sha(m)

	resp := fx.put("team/app/manifests/1.0", m, ctImage)
	fx.wantStatus(resp, http.StatusCreated, "")
	if resp.Header().Get("Docker-Content-Digest") != d || !strings.HasSuffix(resp.Header().Get("Location"), "/team/app/manifests/"+d) {
		t.Errorf("headers = %v", resp.Header())
	}
	if resp.Header().Get("OCI-Subject") != "" {
		t.Error("OCI-Subject on a manifest without a subject")
	}
	ctx := context.Background()
	for _, p := range []string{TagPath("team/app", "1.0"), ManifestPath("team/app", d)} {
		it, ok, _ := fx.h.Items().Stat(ctx, fx.h.Ref(), formats.Path{Path: p})
		if !ok || hex.EncodeToString(it.Digests.SHA256) != strings.TrimPrefix(d, "sha256:") {
			t.Errorf("%s: stored %v, digest %x", p, ok, it.Digests.SHA256)
		}
	}
	vs, _, _ := fx.h.Packages().Versions(ctx, fx.h.Ref(), "team/app")
	if len(vs) != 1 || vs[0].Version != "1.0" {
		t.Fatalf("versions = %+v", vs)
	}
	row, ok, _ := fx.twin.Manifest(ctx, fx.h.Ref().ID, "team/app", hexOf(d))
	if !ok || row.MediaType != ctImage || row.Size != int64(len(m)) || row.ContentSize != 5 || row.ArtifactType != "application/vnd.oci.image.config.v1+json" {
		t.Fatalf("row = %+v %v", row, ok)
	}
	refs, _ := fx.twin.References(ctx, fx.h.Ref().ID, "team/app", hexOf(d))
	if len(refs) != 2 || refs[0].Kind != rows.KindBlob || refs[0].Target != hexOf(cfg) || refs[1].Target != hexOf(layer) {
		t.Fatalf("refs = %+v", refs)
	}
	// The same push again changes nothing and answers 201 again.
	rev := fx.h.RevSeq("team/app")
	fx.wantStatus(fx.put("team/app/manifests/1.0", m, ctImage), http.StatusCreated, "")
	vs, _, _ = fx.h.Packages().Versions(ctx, fx.h.Ref(), "team/app")
	if len(vs) != 1 {
		t.Fatalf("versions after a repeat = %+v", vs)
	}
	_ = rev
}

func TestR_OCI_013_PushByDigest(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	cfg := fx.blob("app", []byte("{}"))
	m := imageManifestBytes(cfg)
	d := sha(m)
	fx.wantStatus(fx.put("app/manifests/"+d, m, ctImage), http.StatusCreated, "")
	ctx := context.Background()
	if _, ok, _ := fx.h.Items().Stat(ctx, fx.h.Ref(), formats.Path{Path: ManifestPath("app", d)}); !ok {
		t.Fatal("the manifest item is not stored")
	}
	if vs, _, _ := fx.h.Packages().Versions(ctx, fx.h.Ref(), "app"); len(vs) != 0 {
		t.Fatalf("a push by digest makes no version: %+v", vs)
	}
	if _, ok, _ := fx.twin.Manifest(ctx, fx.h.Ref().ID, "app", hexOf(d)); !ok {
		t.Fatal("the manifest row is not recorded")
	}
	// A digest that is not the manifest's: 400 DIGEST_INVALID, and nothing is stored.
	other := imageManifestBytes(cfg, cfg)
	wrong := sha(m)
	resp := fx.put("app/manifests/"+wrong, other, ctImage)
	fx.wantStatus(resp, http.StatusBadRequest, "DIGEST_INVALID")
	if _, ok, _ := fx.h.Items().Stat(ctx, fx.h.Ref(), formats.Path{Path: ManifestPath("app", sha(other))}); ok {
		t.Fatal("a manifest under a wrong digest was stored")
	}
	// A tag that is stored later links to it: one manifest item, one row.
	fx.wantStatus(fx.put("app/manifests/latest", m, ctImage), http.StatusCreated, "")
	if n := fx.twin.calls["PutManifest"]; n != 1 {
		t.Errorf("PutManifest ran %d times; the tag of a stored manifest is linked and writes no second row", n)
	}
	if got := fx.get("app/manifests/latest").Header().Get("Docker-Content-Digest"); got != d {
		t.Errorf("the tag points at %q", got)
	}
}

func TestR_OCI_013_PushRefusals(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	cfg := fx.blob("app", []byte("{}"))
	layer := fx.blob("app", []byte("layer"))
	good := imageManifestBytes(cfg, layer)
	lonely := sha([]byte("never uploaded"))
	foreign := []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"a/b","digest":%q,"size":2},"layers":[`+
		`{"mediaType":"application/vnd.docker.image.rootfs.foreign.diff.tar.gzip","digest":%q,"size":9,"urls":["https://example.com/l.tar.gz"]}]}`, cfg, lonely))
	pad := func(n int) []byte {
		head, tail := `{"schemaVersion":2,"manifests":[],"annotations":{"k":"`, `"}}`
		return []byte(head + strings.Repeat("a", n-len(head)-len(tail)) + tail)
	}
	for _, tc := range []struct {
		name, path, ct string
		body           []byte
		status         int
		code           string
	}{
		{"schema 1", "app/manifests/1", "application/vnd.docker.distribution.manifest.v1+prettyjws", good, 400, "MANIFEST_INVALID"},
		{"another media type", "app/manifests/1", "application/json", good, 400, "MANIFEST_INVALID"},
		{"no Content-Type", "app/manifests/1", "", good, 400, "MANIFEST_INVALID"},
		{"a mediaType field of another type", "app/manifests/1", ctIndex, good, 400, "MANIFEST_INVALID"},
		{"not JSON", "app/manifests/1", ctImage, []byte("FROM scratch"), 400, "MANIFEST_INVALID"},
		{"a layer that was never uploaded", "app/manifests/1", ctImage, imageManifestBytes(cfg, lonely), 400, "MANIFEST_BLOB_UNKNOWN"},
		{"a config that was never uploaded", "app/manifests/1", ctImage, imageManifestBytes(lonely, layer), 400, "MANIFEST_BLOB_UNKNOWN"},
		{"a foreign layer needs no upload", "app/manifests/foreign", ctImage, foreign, 201, ""},
		{"4 MiB is accepted", "app/manifests/big", ctIndex, pad(4 << 20), 201, ""},
		{"4 MiB and a byte is refused", "app/manifests/big2", ctIndex, pad(4<<20 + 1), 413, "SIZE_INVALID"},
		{"a tag outside the grammar", "app/manifests/-x", ctImage, good, 400, "TAG_INVALID"},
		{"a name outside the grammar", "App/manifests/1", ctImage, good, 400, "NAME_INVALID"},
		{"a digest of another algorithm", "app/manifests/sha512:" + strings.Repeat("a", 128), ctImage, good, 400, "DIGEST_INVALID"},
		{"an extra tag in the query", "app/manifests/1?tag=2", ctImage, good, 400, "UNSUPPORTED"},
		{"another digest algorithm in the query", "app/manifests/1?digest-algorithm=sha512", ctImage, good, 400, "UNSUPPORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			var mods []func(*http.Request)
			if p, q, ok := strings.Cut(path, "?"); ok {
				path = p
				mods = append(mods, func(r *http.Request) { r.URL.RawQuery = q })
			}
			fx.wantStatus(fx.put(path, tc.body, tc.ct, mods...), tc.status, tc.code)
		})
	}
	// What was refused left nothing behind.
	if _, ok, _ := fx.h.Items().Stat(context.Background(), fx.h.Ref(), formats.Path{Path: TagPath("app", "1")}); ok {
		t.Fatal("a refused push left a tag")
	}
}

func TestR_OCI_013_ArtifactsAreStoredByteForByte(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	empty := fx.blob("app", []byte("{}"))
	doc := []byte(fmt.Sprintf("{\n  \"schemaVersion\": 2,\n  \"mediaType\": %q,\n  \"artifactType\": \"application/vnd.example+json\",\n"+
		"  \"config\": {\"mediaType\": \"application/vnd.oci.empty.v1+json\", \"digest\": %q, \"size\": 2, \"data\": \"e30=\"},\n"+
		"  \"layers\": [{\"mediaType\": \"application/vnd.oci.empty.v1+json\", \"digest\": %q, \"size\": 2}],\n"+
		"  \"x-unknown\": {\"nested\": [1, 2]}\n}\n", ctImage, empty, empty))
	fx.wantStatus(fx.put("app/manifests/art", doc, ctImage), http.StatusCreated, "")
	resp := fx.get("app/manifests/art")
	if !bytes.Equal(resp.Body.Bytes(), doc) {
		t.Fatalf("the manifest was not served byte for byte:\n%s", resp.Body)
	}
	row, ok, _ := fx.twin.Manifest(context.Background(), fx.h.Ref().ID, "app", hexOf(sha(doc)))
	if !ok || row.ArtifactType != "application/vnd.example+json" {
		t.Fatalf("row = %+v", row)
	}
	refs, _ := fx.twin.References(context.Background(), fx.h.Ref().ID, "app", hexOf(sha(doc)))
	if len(refs) != 1 {
		t.Fatalf("the empty descriptor as config and as layer is one edge: %+v", refs)
	}
}

func TestR_OCI_013_ASubjectNeedNotExistAndIsAnsweredWithOCISubject(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	empty := fx.blob("app", []byte("{}"))
	subject := sha([]byte("a manifest nobody pushed yet"))
	doc := []byte(fmt.Sprintf(`{"schemaVersion":2,"artifactType":"application/vnd.dev.cosign.artifact.sig.v1+json",`+
		`"config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":%q,"size":2},"layers":[],`+
		`"subject":{"mediaType":%q,"digest":%q,"size":7},"annotations":{"dev.cosignproject.cosign/signature":"MEUC"}}`, empty, ctImage, subject))
	resp := fx.put("app/manifests/"+sha(doc), doc, ctImage)
	fx.wantStatus(resp, http.StatusCreated, "")
	if resp.Header().Get("OCI-Subject") != subject {
		t.Fatalf("OCI-Subject = %q", resp.Header().Get("OCI-Subject"))
	}
	row, _, _ := fx.twin.Manifest(context.Background(), fx.h.Ref().ID, "app", hexOf(sha(doc)))
	if row.Subject == nil || *row.Subject != hexOf(subject) || row.ArtifactType != "application/vnd.dev.cosign.artifact.sig.v1+json" ||
		string(row.Annotations) != `{"dev.cosignproject.cosign/signature": "MEUC"}` {
		t.Fatalf("row = %+v", row)
	}
}

func TestR_OCI_007_TheRowKeepsAtMost64KiBOfAnnotationsAndSaysSoWhenItCutsThem(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	var b strings.Builder
	b.WriteString(`{"schemaVersion":2,"mediaType":"` + ctIndex + `","manifests":[],"annotations":{`)
	for i := range 40 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%02d":"%s"`, i, strings.Repeat("v", 3000))
	}
	b.WriteString(`}}`)
	fx.wantStatus(fx.put("app/manifests/big", []byte(b.String()), ctIndex), http.StatusCreated, "")
	row, ok, _ := fx.twin.Manifest(context.Background(), fx.h.Ref().ID, "app", hexOf(sha([]byte(b.String()))))
	if !ok || !row.AnnotationsCut || len(row.Annotations) > rows.MaxAnnotationBytes || len(row.Annotations) < rows.MaxAnnotationBytes-4000 {
		t.Fatalf("cut %v, %d bytes of annotations", row.AnnotationsCut, len(row.Annotations))
	}
}

func TestR_OCI_014_AnIndexIsPushedAfterItsChildrenAndRecordsTheirPlatforms(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	// The two children are pushed by digest; an index names them with their platforms.
	cfg := fx.blob("multi", []byte("{}"))
	child := func(seed string) (string, []byte) {
		m := imageManifestBytes(cfg, fx.blob("multi", []byte(seed)))
		d := sha(m)
		fx.wantStatus(fx.put("multi/manifests/"+d, m, ctImage), http.StatusCreated, "")
		return d, m
	}
	a, _ := child("amd64")
	b, _ := child("arm64")
	att, _ := child("attestation")
	idx := indexOf(a+" linux/amd64", b+" linux/arm64/v8", att+" unknown/unknown")
	resp := fx.put("multi/manifests/1", idx, ctIndex)
	fx.wantStatus(resp, http.StatusCreated, "")

	refs, _ := fx.twin.References(context.Background(), fx.h.Ref().ID, "multi", hexOf(sha(idx)))
	got := map[[32]byte]string{}
	for _, r := range refs {
		if r.Kind != rows.KindManifest {
			t.Errorf("edge %+v is no manifest edge", r)
		}
		got[r.Target] = r.Platform
	}
	if got[hexOf(a)] != "linux/amd64" || got[hexOf(b)] != "linux/arm64/v8" || got[hexOf(att)] != "unknown/unknown" || len(got) != 3 {
		t.Fatalf("platforms = %v", got)
	}
	out := fx.get("multi/manifests/1")
	if out.Header().Get("Content-Type") != ctIndex || !bytes.Equal(out.Body.Bytes(), idx) {
		t.Fatalf("the index was not served as pushed: %s", out.Header().Get("Content-Type"))
	}

	// An index that names a child the name does not hold is refused, the same for one that exists under another name.
	fx.wantStatus(fx.put("multi/manifests/2", indexOf(sha([]byte("nothing"))+" linux/amd64"), ctIndex), http.StatusBadRequest, "MANIFEST_BLOB_UNKNOWN")
	// Overwriting the tag with a single-platform manifest moves only the tag: the index stays pullable by digest.
	single, single256 := fx.image("multi", "1", "single")
	_ = single
	if fx.get("multi/manifests/1").Header().Get("Docker-Content-Digest") != single256 {
		t.Fatal("the tag did not move")
	}
	fx.wantStatus(fx.get("multi/manifests/"+sha(idx)), http.StatusOK, "")
}

func TestR_OCI_014_ADockerManifestListIsAcceptedAsItIs(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	cfg := fx.blob("app", []byte("{}"))
	child := imageManifestBytes(cfg)
	fx.wantStatus(fx.put("app/manifests/"+sha(child), child, ctImage), http.StatusCreated, "")
	list := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json","manifests":[`+
		`{"mediaType":%q,"digest":%q,"size":3,"platform":{"architecture":"amd64","os":"linux"}}]}`, ctImage, sha(child)))
	fx.wantStatus(fx.put("app/manifests/1", list, "application/vnd.docker.distribution.manifest.list.v2+json"), http.StatusCreated, "")
	resp := fx.get("app/manifests/1")
	if resp.Header().Get("Content-Type") != "application/vnd.docker.distribution.manifest.list.v2+json" || !bytes.Equal(resp.Body.Bytes(), list) {
		t.Fatal("the manifest list was not served as pushed")
	}
}

func TestR_OCI_006_AMovedTagNeedsOverwriteAndASameDigestTagDoesNot(t *testing.T) {
	var mu sync.Mutex
	overwrite := false
	fx := newFixture(t, formattest.HostOptions{Allow: func(tg formats.Target) error {
		mu.Lock()
		defer mu.Unlock()
		for _, a := range tg.Actions {
			if a == formats.ActionOverwrite && !overwrite {
				return errs.E(errs.Forbidden, errs.CodeForbidden, "no overwrite")
			}
		}
		return nil
	}})
	m1, d1 := fx.image("app", "1", "one")
	// The same image again, and the same digest under another tag, need deploy only.
	fx.wantStatus(fx.put("app/manifests/1", m1, ctImage), http.StatusCreated, "")
	fx.wantStatus(fx.put("app/manifests/2", m1, ctImage), http.StatusCreated, "")
	// A different image as app:1 fails with denied, and nothing moved.
	cfg := fx.blob("app", []byte("{}"))
	m2 := imageManifestBytes(cfg, fx.blob("app", []byte("two")))
	fx.wantStatus(fx.put("app/manifests/1", m2, ctImage), http.StatusForbidden, "DENIED")
	if got := fx.get("app/manifests/1").Header().Get("Docker-Content-Digest"); got != d1 {
		t.Fatalf("the tag moved to %s without overwrite", got)
	}
	// A new tag for the different image is a deploy.
	fx.wantStatus(fx.put("app/manifests/3", m2, ctImage), http.StatusCreated, "")
	mu.Lock()
	overwrite = true
	mu.Unlock()
	fx.wantStatus(fx.put("app/manifests/1", m2, ctImage), http.StatusCreated, "")
	if got := fx.get("app/manifests/1").Header().Get("Docker-Content-Digest"); got != sha(m2) {
		t.Fatalf("the tag points at %s", got)
	}
	// One version row per tag, the moved one still one.
	vs, _, _ := fx.h.Packages().Versions(context.Background(), fx.h.Ref(), "app")
	if len(vs) != 3 {
		t.Fatalf("versions = %+v", vs)
	}
	// The old manifest is still pullable by digest.
	fx.wantStatus(fx.get("app/manifests/"+d1), http.StatusOK, "")
}

func TestR_OCI_006_AuthorizationIsDecidedPerNameAndTagBeforeTheBodyIsRead(t *testing.T) {
	var denied bool
	fx := newFixture(t, formattest.HostOptions{Allow: func(tg formats.Target) error {
		if denied && tg.Package == "secret/app" {
			return errs.E(errs.Forbidden, errs.CodeForbidden, "no")
		}
		return nil
	}})
	cfg := fx.blob("app", []byte("{}"))
	fx.blob("secret/app", []byte("{}"))
	m := imageManifestBytes(cfg)
	fx.wantStatus(fx.put("app/manifests/1", m, ctImage), http.StatusCreated, "")
	calls := fx.h.Authorizations()
	found := false
	for _, c := range calls {
		if c.Target.Package == "app" && c.Target.Version == "1" && len(c.Target.Actions) == 1 && c.Target.Actions[0] == formats.ActionDeploy {
			found = true
		}
	}
	if !found {
		t.Fatalf("no deploy decision for app:1 in %+v", calls)
	}
	checks := fx.h.OwnershipChecks()
	if len(checks) == 0 || !checks[0].Write.Publish || !checks[0].Write.FirstVersion {
		t.Fatalf("ownership checks = %+v; the first push of a name is a first version", checks)
	}
	denied = true
	body := &readCounter{r: bytes.NewReader(m)}
	hr := func(r *http.Request) { r.Body = io.NopCloser(body); r.ContentLength = int64(len(m)) }
	resp, err := fx.h.Serve(context.Background(), http.MethodPut, "secret/app/manifests/1", body, false, func(r *http.Request) { r.Header.Set("Content-Type", ctImage) }, hr)
	if err != nil {
		t.Fatal(err)
	}
	fx.wantStatus(resp, http.StatusForbidden, "DENIED")
	if body.n != 0 {
		t.Fatalf("%d bytes of a refused push were read", body.n)
	}
}

type readCounter struct {
	r io.Reader
	n int
}

func (c *readCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestR_OCI_006_ThePrecheckOfOwnershipRunsBeforeTheBody(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{Precheck: func(formats.Target, formats.OwnershipWrite) error {
		return errs.E(errs.Forbidden, errs.CodePackageOwnedByOtherOwner, "owned")
	}})
	cfg := fx.blob("app", []byte("{}"))
	m := imageManifestBytes(cfg)
	body := &readCounter{r: bytes.NewReader(m)}
	resp, err := fx.h.Serve(context.Background(), http.MethodPut, "app/manifests/1", body, false, func(r *http.Request) {
		r.Header.Set("Content-Type", ctImage)
		r.ContentLength = int64(len(m))
	})
	if err != nil {
		t.Fatal(err)
	}
	fx.wantStatus(resp, http.StatusForbidden, "DENIED")
	if body.n != 0 {
		t.Fatalf("%d bytes were read for a push nobody could commit", body.n)
	}
}

func TestR_OCI_006_TheFirstVersionOfANameIsShadowChecked(t *testing.T) {
	refusal := errs.E(errs.Forbidden, errs.CodeShadowsRemotePackage, "the name is served by a remote repository")
	var asked int
	res := shadowResolver{check: func(string) (formats.ShadowOutcome, error) {
		asked++
		return formats.ShadowOutcome{Refusal: refusal}, nil
	}}
	fx := newFixture(t, formattest.HostOptions{Resolver: res})
	cfg := fx.blob("app", []byte("{}"))
	m := imageManifestBytes(cfg)
	fx.wantStatus(fx.put("app/manifests/1", m, ctImage), http.StatusForbidden, "DENIED")
	if asked != 1 {
		t.Fatalf("the check ran %d times", asked)
	}
	if _, ok, _ := fx.h.Items().Stat(context.Background(), fx.h.Ref(), formats.Path{Path: TagPath("app", "1")}); ok {
		t.Fatal("a refused push stored the tag")
	}
	// Once the name holds a version, no push asks again.
	fx2 := newFixture(t, formattest.HostOptions{})
	fx2.image("app", "1", "one")
	asked = 0
	fx3 := newFixture(t, formattest.HostOptions{Resolver: shadowResolver{check: func(string) (formats.ShadowOutcome, error) {
		asked++
		return formats.ShadowOutcome{Proceed: true}, nil
	}}})
	fx3.image("app", "1", "one")
	fx3.image("app", "2", "two")
	fx3.image("app", "3", "three")
	if asked != 1 {
		t.Fatalf("the check ran %d times for three tags; only the first version of a name is checked", asked)
	}
}

type shadowResolver struct {
	formats.Resolver
	check func(name string) (formats.ShadowOutcome, error)
}

func (s shadowResolver) ShadowCheck(_ context.Context, _ *formats.Request, name string) (formats.ShadowOutcome, error) {
	return s.check(name)
}

func TestR_OCI_033_APushOfAWithheldDigestIsRefusedWith409(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	cfg := fx.blob("app", []byte("{}"))
	m := imageManifestBytes(cfg)
	fx.twin.withheld[mkey{fx.h.Ref().ID, "app", hexOf(sha(m))}] = true
	for _, ref := range []string{"1", sha(m)} {
		fx.wantStatus(fx.put("app/manifests/"+ref, m, ctImage), http.StatusConflict, "DENIED")
	}
	if _, ok, _ := fx.h.Items().Stat(context.Background(), fx.h.Ref(), formats.Path{Path: ManifestPath("app", sha(m))}); ok {
		t.Fatal("a withheld digest was stored again")
	}
	// The mark is per name: the same bytes under another name are another manifest.
	cfg2 := fx.blob("other", []byte("{}"))
	if cfg2 != cfg {
		t.Fatal("the same config bytes")
	}
	fx.wantStatus(fx.put("other/manifests/1", m, ctImage), http.StatusCreated, "")
	// An index that names a withheld child is refused as well.
	child := imageManifestBytes(cfg, cfg)
	fx.wantStatus(fx.put("app/manifests/"+sha(child), child, ctImage), http.StatusCreated, "")
	fx.twin.withheld[mkey{fx.h.Ref().ID, "app", hexOf(sha(child))}] = true
	fx.wantStatus(fx.put("app/manifests/idx", indexOf(sha(child)+" linux/amd64"), ctIndex), http.StatusBadRequest, "MANIFEST_BLOB_UNKNOWN")
}

func TestR_OCI_013_APushThatFailsInTheTransactionLeavesNothing(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	cfg := fx.blob("app", []byte("{}"))
	layer := fx.blob("app", []byte("l"))
	m := imageManifestBytes(cfg, layer)
	// The blob goes away between the check outside the transaction and the check in it.
	fx.interleave(func() {
		rq, _ := fx.h.NewRequest(context.Background(), http.MethodDelete, "app/blobs/"+layer, nil)
		if err := fx.h.Remove(context.Background(), rq, "delete", BlobPath("app", layer)); err != nil {
			t.Error(err)
		}
	})
	resp := fx.put("app/manifests/1", m, ctImage)
	fx.wantStatus(resp, http.StatusBadRequest, "MANIFEST_BLOB_UNKNOWN")
	ctx := context.Background()
	for _, p := range []string{TagPath("app", "1"), ManifestPath("app", sha(m))} {
		if _, ok, _ := fx.h.Items().Stat(ctx, fx.h.Ref(), formats.Path{Path: p}); ok {
			t.Errorf("%s was left by a push that failed", p)
		}
	}
	if vs, _, _ := fx.h.Packages().Versions(ctx, fx.h.Ref(), "app"); len(vs) != 0 {
		t.Errorf("versions = %+v", vs)
	}
	if _, ok, _ := fx.twin.Manifest(ctx, fx.h.Ref().ID, "app", hexOf(sha(m))); ok {
		t.Error("a manifest row was left by a push that failed")
	}
}

func TestR_OCI_013_ConcurrentPushesOfOneTagLeaveOneDigestAndOneVersion(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	cfg := fx.blob("app", []byte("{}"))
	const n = 8
	manifests := make([][]byte, n)
	digests := map[string]bool{}
	for i := range n {
		manifests[i] = imageManifestBytes(cfg, fx.blob("app", []byte(fmt.Sprintf("layer %d", i))))
		digests[sha(manifests[i])] = true
	}
	var wg sync.WaitGroup
	statuses := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = fx.put("app/manifests/1", manifests[i], ctImage).Code
		}()
	}
	wg.Wait()
	// Every pusher holds overwrite, so a push that finds the tag moved meanwhile decides again with overwrite and commits: all are 201.
	for _, s := range statuses {
		if s != http.StatusCreated {
			t.Errorf("statuses %v, want every push to end 201", statuses)
			break
		}
	}
	got := fx.get("app/manifests/1")
	if !digests[got.Header().Get("Docker-Content-Digest")] {
		t.Fatalf("the tag points at %q, which no push sent", got.Header().Get("Docker-Content-Digest"))
	}
	vs, _, _ := fx.h.Packages().Versions(context.Background(), fx.h.Ref(), "app")
	if len(vs) != 1 || vs[0].Version != "1" {
		t.Fatalf("versions = %+v", vs)
	}
}

// A push that read its tag as new and finds it taken when its transaction begins decides again (R-OCI-013 step 8, R-OCI-006): with
// overwrite it commits its own digest, without it is refused. Another push of the tag lands exactly between the push's checks and its
// transaction, so the retry of commitTag runs on every execution, not by chance.
func TestR_OCI_013_APushThatFindsTheTagTakenDecidesAgain(t *testing.T) {
	for _, tc := range []struct {
		name      string
		overwrite bool
		status    int
	}{{"with overwrite", true, http.StatusCreated}, {"without overwrite", false, http.StatusForbidden}} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t, formattest.HostOptions{Allow: func(tg formats.Target) error {
				for _, a := range tg.Actions {
					if a == formats.ActionOverwrite && !tc.overwrite {
						return errs.E(errs.Forbidden, errs.CodeForbidden, "no overwrite")
					}
				}
				return nil
			}})
			cfg := fx.blob("app", []byte("{}"))
			mine := imageManifestBytes(cfg, fx.blob("app", []byte("mine")))
			other := imageManifestBytes(cfg, fx.blob("app", []byte("other")))
			fx.interleave(func() {
				fx.wantStatus(fx.put("app/manifests/1", other, ctImage), http.StatusCreated, "")
			})
			fx.wantStatus(fx.put("app/manifests/1", mine, ctImage), tc.status, "")
			want := sha(other)
			if tc.overwrite {
				want = sha(mine)
			}
			if got := fx.get("app/manifests/1").Header().Get("Docker-Content-Digest"); got != want {
				t.Fatalf("the tag points at %s, want %s", got, want)
			}
			vs, _, _ := fx.h.Packages().Versions(context.Background(), fx.h.Ref(), "app")
			if len(vs) != 1 || vs[0].Version != "1" {
				t.Fatalf("versions = %+v", vs)
			}
		})
	}
}

// The criterion's "within 5 s" is shown by structure, not by a wall clock the runner's load decides (Q-OCI-151): every crafted
// manifest is refused by the parser's bounds, before the handler looks up a single referenced blob. The layers name a digest the
// repository does not hold, so a refusal that came after the reference check would read MANIFEST_BLOB_UNKNOWN instead of the
// parser's code; the parser's own work is linear in a document bounded at 4 MiB (manifest package tests of the bounds).
func TestR_OCI_020_CraftedManifestsAreRefusedQuicklyWithoutStoringAnything(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	cfg := fx.blob("app", []byte("{}"))
	unknown := "sha256:" + strings.Repeat("ab", 32)
	layer := `{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"` + unknown + `","size":2}`
	for _, tc := range []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"nested 100,000 deep", `{"schemaVersion":2,"x":` + strings.Repeat("[", 100_000) + strings.Repeat("]", 100_000) + `}`, 0, ""},
		{"20,000 layers", `{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"` + cfg + `","size":2},"layers":[` + strings.TrimSuffix(strings.Repeat(layer+",", 20_000), ",") + `]}`, http.StatusRequestEntityTooLarge, "SIZE_INVALID"},
		{"a 5 MiB index", `{"schemaVersion":2,"manifests":[],"annotations":{"k":"` + strings.Repeat("a", 5<<20) + `"}}`, http.StatusRequestEntityTooLarge, "SIZE_INVALID"},
		{"a digest that is a path", `{"schemaVersion":2,"config":{"mediaType":"a/b","digest":"sha256:../../x","size":2},"layers":[]}`, 0, ""},
		{"a duplicate key", `{"schemaVersion":2,"schemaVersion":2,"config":{"mediaType":"a/b","digest":"` + cfg + `","size":2},"layers":[]}`, http.StatusBadRequest, "MANIFEST_INVALID"},
		{"a key in the wrong case", `{"schemaVersion":2,"Config":{"mediaType":"a/b","digest":"` + cfg + `","size":2},"config":{"mediaType":"a/b","digest":"` + cfg + `","size":2},"layers":[]}`, http.StatusBadRequest, "MANIFEST_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := fx.put("app/manifests/1", []byte(tc.body), ctImage)
			code := errorCode(resp)
			if code == "MANIFEST_BLOB_UNKNOWN" {
				t.Fatalf("refused by the reference check, not by the parser: %s", resp.Body)
			}
			switch {
			case tc.status != 0 && (resp.Code != tc.status || code != tc.code):
				t.Fatalf("status %d code %q, want %d %q: %s", resp.Code, code, tc.status, tc.code, resp.Body)
			case tc.status == 0 && resp.Code != http.StatusBadRequest && resp.Code != http.StatusRequestEntityTooLarge:
				t.Fatalf("status %d: %s", resp.Code, resp.Body)
			}
		})
	}
	if _, ok, _ := fx.h.Items().Stat(context.Background(), fx.h.Ref(), formats.Path{Path: TagPath("app", "1")}); ok {
		t.Fatal("a crafted manifest left a tag")
	}
}

func TestR_OCI_013_AManifestOrTagReachingTheRepositoryAnyOtherWayIsRefused(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	rq, _ := fx.h.NewRequest(context.Background(), http.MethodPut, "app/manifests/1", nil)
	sp, _ := fx.h.Ingest().Spool(context.Background(), rq, strings.NewReader(`{}`), formats.IngestOptions{})
	_, err := fx.h.Ingest().Commit(context.Background(), rq, sp, formats.Commit{Path: formats.Path{Path: TagPath("app", "1")}, MediaType: "application/json"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.Conflict {
		t.Fatalf("err = %v", err)
	}
	// A blob is a plain item: its commit is the upload's.
	sp, _ = fx.h.Ingest().Spool(context.Background(), rq, strings.NewReader(`blob`), formats.IngestOptions{})
	if _, err := fx.h.Ingest().Commit(context.Background(), rq, sp, formats.Commit{Path: formats.Path{Path: BlobPath("app", sha([]byte("blob")))}}); err != nil {
		t.Fatalf("a blob: %v", err)
	}
}

func TestTheVirtualClassIsNotBuiltYet(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{Class: formats.Virtual})
	fx.wantStatus(fx.get("app/manifests/1"), http.StatusNotImplemented, "UNSUPPORTED")
	fx.wantStatus(fx.get("app/blobs/"+sha([]byte("x"))), http.StatusNotImplemented, "UNSUPPORTED")
}

func TestR_OCI_031_ThePackageViewShowsTheDigestTheSizeAndThePlatformsOfATag(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{})
	cfg := fx.blob("multi", []byte("{}"))
	amd := imageManifestBytes(cfg, fx.blob("multi", []byte("amd64 layer")))
	arm := imageManifestBytes(cfg, fx.blob("multi", []byte("arm64 layer!")))
	for _, m := range [][]byte{amd, arm} {
		fx.wantStatus(fx.put("multi/manifests/"+sha(m), m, ctImage), http.StatusCreated, "")
	}
	idx := indexOf(sha(amd)+" linux/amd64", sha(arm)+" linux/arm64")
	fx.wantStatus(fx.put("multi/manifests/1", idx, ctIndex), http.StatusCreated, "")

	ctx := context.Background()
	vs, _, _ := fx.h.Packages().Versions(ctx, fx.h.Ref(), "multi")
	view := New().View()
	data, err := view.Describe(ctx, fx.h, []formats.ViewVersion{{ID: vs[0].ID, Repo: fx.h.Ref(), Package: "multi", Version: "1"}}, formats.ViewDetails, nil)
	if err != nil {
		t.Fatal(err)
	}
	md := data[vs[0].ID].Metadata
	if md["digest"] != sha(idx) || md["mediaType"] != ctIndex {
		t.Fatalf("metadata = %v", md)
	}
	platforms, _ := md["platforms"].([]map[string]any)
	if len(platforms) != 2 || platforms[0]["platform"] != "linux/amd64" || platforms[0]["digest"] != sha(amd) || platforms[0]["size"] != int64(5) ||
		platforms[1]["platform"] != "linux/arm64" || platforms[1]["size"] != int64(5) {
		t.Fatalf("platforms = %v", md["platforms"])
	}
	if md["size"] != int64(10) {
		t.Fatalf("size = %v, want the summed size of the children", md["size"])
	}
	// Newest first, and a tag whose manifest was withheld is listed by Withheld and not by Stored.
	fx.image("multi", "2", "second")
	stored, _ := view.Stored(ctx, fx.h, formats.StoredQuery{Repo: fx.h.Ref(), Package: "multi", Limit: 10})
	if len(stored.Versions) != 2 || stored.Versions[0] != "2" || stored.Versions[1] != "1" {
		t.Fatalf("stored = %v", stored.Versions)
	}
	fx.twin.withheld[mkey{fx.h.Ref().ID, "multi", hexOf(sha(idx))}] = true
	stored, _ = view.Stored(ctx, fx.h, formats.StoredQuery{Repo: fx.h.Ref(), Package: "multi", Limit: 10})
	if len(stored.Versions) != 1 || stored.Versions[0] != "2" {
		t.Fatalf("stored with a withheld tag = %v", stored.Versions)
	}
	ids, _ := view.Withheld(ctx, fx.h, []string{"it/main"})
	if len(ids) != 1 || ids[0] != vs[0].ID {
		t.Fatalf("withheld = %v", ids)
	}
}

func TestR_OCI_031_TheViewOrdersTagsByTheTagOrder(t *testing.T) {
	v := New().View()
	if v.Compare("1.10", "1.9") <= 0 || v.Compare("latest", "1.0") >= 0 || v.Compare("1.0", "1.0-rc1") <= 0 || v.Compare("a", "a") != 0 {
		t.Fatal("the view does not use the tag order of the format")
	}
}
