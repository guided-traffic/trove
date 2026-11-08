package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

// twin is the in-memory twin of the data port for the tests of the handlers: the rows of the manifests a push wrote, the references,
// the withheld marks, and the reads of the package view over the in-memory Host's items and package rows. The methods of the list
// and delete side are not built here (they panic on the embedded nil interface, which a test of that side replaces).
type twin struct {
	Store
	host *formattest.Host

	mu        sync.Mutex
	manifests map[mkey]rows.Manifest
	refs      map[mkey][]rows.Reference
	withheld  map[mkey]bool
	// lists is the state of the list and delete side (store_twin_test.go).
	lists *lists
	// calls counts the port's calls by name, for the tests that pin what a request asks.
	calls map[string]int
	// betweenChecks runs once, when the references are checked inside the commit transaction: the moment after the check outside it.
	betweenChecks func()
	// hideInTx names item paths the existence check inside a commit transaction does not find: a removal that wins the race.
	hideInTx map[string]bool
}

type mkey struct {
	repo   int64
	name   string
	digest [32]byte
}

func newTwin() *twin {
	return &twin{manifests: map[mkey]rows.Manifest{}, refs: map[mkey][]rows.Reference{}, withheld: map[mkey]bool{}, calls: map[string]int{}}
}

func (t *twin) note(name string) {
	t.mu.Lock()
	t.calls[name]++
	t.mu.Unlock()
}

// rollbacker is the opaque transaction of the in-memory Host, which runs a function when the transaction ends without a commit.
type rollbacker interface{ OnRollback(func()) }

func (t *twin) PutManifest(_ context.Context, tx formats.Tx, in rows.ManifestInput) error {
	t.note("PutManifest")
	k := mkey{in.RepoID, in.Name, in.Digest}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.manifests[k]; ok {
		return nil
	}
	stored := in.Manifest
	seen := map[[32]byte]bool{}
	var refs []rows.Reference
	var children bool
	var sum int64
	for _, r := range in.References {
		if !seen[r.Target] {
			seen[r.Target] = true
			refs = append(refs, r)
			if r.Kind == rows.KindManifest {
				children = true
				if sum > maxInt64-t.manifests[mkey{in.RepoID, in.Name, r.Target}].ContentSize {
					sum = maxInt64
				} else {
					sum += t.manifests[mkey{in.RepoID, in.Name, r.Target}].ContentSize
				}
			}
		}
	}
	if children {
		stored.ContentSize = sum
	}
	t.manifests[k] = stored
	t.refs[k] = refs
	if rb, ok := tx.(rollbacker); ok {
		rb.OnRollback(func() {
			t.mu.Lock()
			delete(t.manifests, k)
			delete(t.refs, k)
			t.mu.Unlock()
		})
	}
	return nil
}

func (t *twin) Manifest(_ context.Context, repo int64, name string, d [32]byte) (rows.Manifest, bool, error) {
	t.note("Manifest")
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.manifests[mkey{repo, name, d}]
	return m, ok, nil
}

func (t *twin) References(_ context.Context, repo int64, name string, d [32]byte) ([]rows.Reference, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]rows.Reference(nil), t.refs[mkey{repo, name, d}]...), nil
}

func (t *twin) Tombstone(_ context.Context, repo int64, name string, d [32]byte) (rows.Tombstone, bool, error) {
	t.note("Tombstone")
	t.mu.Lock()
	defer t.mu.Unlock()
	return rows.Tombstone{RepoID: repo, Name: name, Digest: d}, t.withheld[mkey{repo, name, d}], nil
}

func (t *twin) ExistingItems(ctx context.Context, tx formats.Tx, repo int64, paths []string) ([]string, error) {
	t.note("ExistingItems")
	if tx != nil && t.betweenChecks != nil {
		f := t.betweenChecks
		t.betweenChecks = nil
		f()
	}
	var out []string
	for _, p := range paths {
		if tx != nil && t.hideInTx[p] {
			continue
		}
		it, ok, err := t.host.Items().Stat(ctx, t.host.Ref(), formats.Path{Path: p})
		if err != nil {
			return nil, err
		}
		if ok && !it.Folder {
			out = append(out, p)
		}
	}
	return out, nil
}

func (t *twin) WithheldAmong(_ context.Context, _ formats.Tx, repo int64, name string, ds [][32]byte) ([][32]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out [][32]byte
	for _, d := range ds {
		if t.withheld[mkey{repo, name, d}] {
			out = append(out, d)
		}
	}
	return out, nil
}

// tagDigest is the digest the tag item of a tag holds.
func (t *twin) tagDigest(ctx context.Context, name, tag string) ([32]byte, bool) {
	it, ok, _ := t.host.Items().Stat(ctx, t.host.Ref(), formats.Path{Path: TagPath(name, tag)})
	var d [32]byte
	if !ok {
		return d, false
	}
	copy(d[:], it.Digests.SHA256)
	return d, true
}

func (t *twin) StoredTags(ctx context.Context, repo int64, name string, limit int) ([]string, error) {
	if t.host == nil {
		return nil, nil
	}
	vs, _, err := t.host.Packages().Versions(ctx, t.host.Ref(), name)
	if err != nil {
		return nil, err
	}
	var out []string
	for i := len(vs) - 1; i >= 0 && len(out) < limit; i-- {
		if d, ok := t.tagDigest(ctx, name, vs[i].Version); ok && !t.isWithheld(repo, name, d) {
			out = append(out, vs[i].Version)
		}
	}
	return out, nil
}

func (t *twin) isWithheld(repo int64, name string, d [32]byte) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.withheld[mkey{repo, name, d}]
}

func (t *twin) WithheldVersions(ctx context.Context, _ []string) ([]int64, error) {
	if t.host == nil {
		return nil, nil
	}
	var out []int64
	for key := range t.withheld {
		vs, _, _ := t.host.Packages().Versions(ctx, t.host.Ref(), key.name)
		for _, v := range vs {
			if d, ok := t.tagDigest(ctx, key.name, v.Version); ok && d == key.digest {
				out = append(out, int64(v.ID))
			}
		}
	}
	return out, nil
}

func (t *twin) TagSizes(ctx context.Context, versionIDs []int64) (map[int64]int64, error) {
	t.note("TagSizes")
	out := map[int64]int64{}
	views, err := t.tagViews(ctx, versionIDs)
	for _, tv := range views {
		out[tv.VersionID] = tv.ContentSize
	}
	return out, err
}

func (t *twin) TagViews(ctx context.Context, versionIDs []int64) ([]rows.TagView, error) {
	t.note("TagViews")
	return t.tagViews(ctx, versionIDs)
}

func (t *twin) tagViews(ctx context.Context, versionIDs []int64) ([]rows.TagView, error) {
	if t.host == nil {
		return nil, nil
	}
	t.mu.Lock()
	names := map[string]int64{}
	for k := range t.manifests {
		names[k.name] = k.repo
	}
	t.mu.Unlock()
	var out []rows.TagView
	for name, repo := range names {
		vs, _, err := t.host.Packages().Versions(ctx, t.host.Ref(), name)
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			if !containsID(versionIDs, int64(v.ID)) {
				continue
			}
			d, ok := t.tagDigest(ctx, name, v.Version)
			t.mu.Lock()
			m, row := t.manifests[mkey{repo, name, d}]
			t.mu.Unlock()
			if !ok || !row {
				continue
			}
			tv := rows.TagView{VersionID: int64(v.ID), Tag: v.Version, Digest: d, MediaType: m.MediaType, ArtifactType: m.ArtifactType, Size: m.Size, ContentSize: m.ContentSize}
			t.mu.Lock()
			for _, r := range t.refs[mkey{repo, name, d}] {
				if r.Kind != rows.KindManifest {
					continue
				}
				c := t.manifests[mkey{repo, name, r.Target}]
				tv.Children = append(tv.Children, rows.Child{Platform: r.Platform, Digest: r.Target, MediaType: c.MediaType, Size: c.Size, ContentSize: c.ContentSize})
			}
			t.mu.Unlock()
			sort.SliceStable(tv.Children, func(i, j int) bool { return tv.Children[i].Platform < tv.Children[j].Platform })
			out = append(out, tv)
		}
	}
	return out, nil
}

func containsID(s []int64, x int64) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// fixture is a Host with the twin bound and helpers that push and pull as a client does.
type fixture struct {
	t    *testing.T
	f    *Format
	h    *formattest.Host
	twin *twin
}

func newFixture(t *testing.T, o formattest.HostOptions) *fixture {
	t.Helper()
	tw := newTwin()
	o.Data = tw
	if o.Resolver == nil {
		o.Resolver = proceedResolver{}
	}
	f := New()
	h := formattest.NewHost(f, o)
	tw.host = h
	return &fixture{t: t, f: f, h: h, twin: tw}
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// blob stores a blob item of an image name and returns its digest.
func (fx *fixture) blob(name string, data []byte) string {
	fx.t.Helper()
	d := sha(data)
	fx.h.PutItem(BlobPath(name, d), "application/octet-stream", data)
	return d
}

// request runs a request through the route table and the dispatcher's guarantees.
func (fx *fixture) request(method, path string, body []byte, ct string, mods ...func(*http.Request)) *formattest.Response {
	fx.t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	all := mods
	if ct != "" {
		all = append([]func(*http.Request){func(r *http.Request) { r.Header.Set("Content-Type", ct) }}, mods...)
	}
	all = append([]func(*http.Request){func(r *http.Request) {
		if body != nil {
			r.ContentLength = int64(len(body))
		}
	}}, all...)
	resp, err := fx.h.Serve(context.Background(), method, path, r, false, all...)
	if err != nil {
		fx.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (fx *fixture) put(path string, body []byte, ct string, mods ...func(*http.Request)) *formattest.Response {
	fx.t.Helper()
	return fx.request(http.MethodPut, path, body, ct, mods...)
}

func (fx *fixture) get(path string, mods ...func(*http.Request)) *formattest.Response {
	fx.t.Helper()
	return fx.request(http.MethodGet, path, nil, "", mods...)
}

// errorCode is the code of the one entry of an error body, "" for a body that is none.
func errorCode(resp *formattest.Response) string {
	body := resp.Body.String()
	const key = `"code":"`
	i := strings.Index(body, key)
	if i < 0 {
		return ""
	}
	rest := body[i+len(key):]
	return rest[:strings.IndexByte(rest, '"')]
}

func (fx *fixture) wantStatus(resp *formattest.Response, status int, code string) {
	fx.t.Helper()
	if resp.Code != status || (code != "" && errorCode(resp) != code) {
		fx.t.Fatalf("status %d code %q, want %d %q: %s", resp.Code, errorCode(resp), status, code, resp.Body)
	}
}

// imageManifest builds an OCI image manifest over a config and layers (digest, size) and returns its bytes.
func imageManifestBytes(cfg string, layers ...string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":2},"layers":[`, cfg)
	for i, l := range layers {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":%q,"size":3}`, l)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

const (
	ctImage = "application/vnd.oci.image.manifest.v1+json"
	ctIndex = "application/vnd.oci.image.index.v1+json"
)

// image pushes a complete image (a config, a layer and a manifest) to the name under the reference and returns the manifest's bytes
// and digest.
func (fx *fixture) image(name, ref, layerSeed string) ([]byte, string) {
	fx.t.Helper()
	cfg := fx.blob(name, []byte("{}"))
	layer := fx.blob(name, []byte(layerSeed))
	m := imageManifestBytes(cfg, layer)
	resp := fx.put(name+"/manifests/"+ref, m, ctImage)
	fx.wantStatus(resp, http.StatusCreated, "")
	return m, sha(m)
}

// proceedResolver is the virtual resolution of a host that has no virtual repository: the shadowing check lets a first push pass.
type proceedResolver struct{ formats.Resolver }

func (proceedResolver) ShadowCheck(context.Context, *formats.Request, string) (formats.ShadowOutcome, error) {
	return formats.ShadowOutcome{Proceed: true}, nil
}
