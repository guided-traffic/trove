package npm

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/npmtest"
)

// publishDoc is the body of a publish of name@version with a generated tarball.
func publishDoc(t testing.TB, name, version, tag string) []byte {
	t.Helper()
	tg := tgzFor(t, name, version)
	s512, s1 := sha512.Sum512(tg), sha1.Sum(tg) //nolint:gosec // see the import
	file := fmt.Sprintf("%s-%s.tgz", name, version)
	doc := map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{tag: version},
		"versions": map[string]any{version: map[string]any{
			"name": name, "version": version,
			"dist": map[string]any{"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]), "shasum": hex.EncodeToString(s1[:])},
		}},
		"_attachments": map[string]any{file: map[string]any{
			"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tg), "length": len(tg),
		}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func tgzFor(t testing.TB, name, version string) []byte {
	t.Helper()
	pj, _ := json.Marshal(map[string]string{"name": name, "version": version})
	return tgz(t, map[string]string{"package/package.json": string(pj), "package/index.js": "module.exports = 1\n"})
}

// conformance is the fixture of the npm format for the kit (R-ARCH-010). Remote and
// virtual repositories are not served yet, so checks 4 and 5 have nothing to run
// over; the kit refuses a skip once the format lists the class.
func conformance(t *testing.T) formattest.Fixture {
	// The twin reads packages.rev_seq of the Host the kit builds for a case, so the
	// revision of a rendered document is the one the Host holds; every Seed binds it.
	var twin *npmtest.Store
	bind := func(h *formattest.Host) { twin.Revisions = func(name string) int64 { return int64(h.RevSeq(name)) } }
	publish := func(name, version string) func(h *formattest.Host) error {
		return func(h *formattest.Host) error {
			bind(h)
			return serveOK(h, http.MethodPut, name, publishDoc(t, name, version, "latest"), 201)
		}
	}
	publishAll := func(name string, versions ...string) func(h *formattest.Host) error {
		return func(h *formattest.Host) error {
			for _, v := range versions {
				if err := publish(name, v)(h); err != nil {
					return err
				}
			}
			return nil
		}
	}
	// revOf is the _rev a client would send for the package now.
	revOf := func(h *formattest.Host, name string) (string, error) {
		row, ok, err := h.Packages().Package(context.Background(), h.Ref(), name)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("package %s does not exist", name)
		}
		return Rev(h.Ref().ID, row.Ref.ID, row.Ref.RevSeq), nil
	}
	unpublish := func(method, path, body string) func(h *formattest.Host) error {
		return func(h *formattest.Host) error {
			rev, err := revOf(h, "lodash")
			if err != nil {
				return err
			}
			return serveOK(h, method, strings.ReplaceAll(path, "{rev}", rev), []byte(body), 200)
		}
	}
	return formattest.Fixture{
		Host: func() formattest.HostOptions {
			twin = npmtest.New()
			return formattest.HostOptions{
				Data: twin, Resolver: noShadow{},
				Subject: authz.Subject{Kind: "user", ID: "11111111-1111-1111-1111-111111111111", Name: "alice"},
			}
		},
		Rejects: []string{
			"a/../b", "%2e%2e/etc/passwd", "@a%2f%2e%2e", "a%2525b", "lodash/-/..%2f..%2fx.tgz", "a%00b", "a%5cb", "@acme", "@/ui",
		},
		Paths: []formattest.PathCase{
			{Path: "", Want: formats.PathInfo{Kind: formats.PathComputed}},
			{Path: "lodash", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "lodash"}},
			{Path: "@acme%2fui", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "@acme/ui"}},
			{Path: "@acme/ui/1.2.0", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "@acme/ui", Version: "1.2.0"}},
			{Path: "@acme/ui/latest", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "@acme/ui"}},
			{Path: "@acme/ui/-/ui-1.2.0.tgz", Want: formats.PathInfo{Kind: formats.Content, Package: "@acme/ui", Version: "1.2.0"}},
			{Path: "@acme%2fui/-/ui-1.2.0.sigstore", Want: formats.PathInfo{Kind: formats.Content, Package: "@acme/ui", Version: "1.2.0"}},
			{Path: "lodash/-/other-1.0.0.tgz", Want: formats.PathInfo{Kind: formats.Content, Package: "lodash"}},
			{Path: "-/ping", Want: formats.PathInfo{Kind: formats.PathComputed}},
			{Path: "a/b/c", Want: formats.PathInfo{Kind: formats.PathMetadata}},
		},
		Inspect: formattest.InspectTarGz("lodash/-/lodash-1.0.0.tgz", "package/package.json"),
		Content: []formattest.ContentCase{
			{Name: "the registry URL", Path: "", Status: 200},
			{Name: "a package document", Path: "lodash", Status: 200, Seed: publish("lodash", "4.17.21")},
			{Name: "an unknown package", Path: "lodash", Status: 404},
			{Name: "a single version", Path: "lodash/4.17.21", Status: 200, Seed: publish("lodash", "4.17.21")},
			{Name: "a single version by its tag", Path: "lodash/latest", Status: 200, Seed: publish("lodash", "4.17.21")},
			{Name: "a version the package does not hold", Path: "lodash/9.9.9", Status: 404, Seed: publish("lodash", "4.17.21")},
			{Name: "a tarball", Path: "lodash/-/lodash-4.17.21.tgz", Status: 200, Seed: publish("lodash", "4.17.21")},
			{Name: "a tarball that is not stored", Path: "lodash/-/lodash-1.0.0.tgz", Status: 404, Seed: publish("lodash", "4.17.21")},
			{Name: "the tarball of an unpublished version", Path: "lodash/-/lodash-4.17.20.tgz", Status: 404, Seed: func(h *formattest.Host) error {
				if err := publishAll("lodash", "4.17.20", "4.17.21")(h); err != nil {
					return err
				}
				return unpublish(http.MethodPut, "lodash/-rev/{rev}", `{"versions":{"4.17.21":{}}}`)(h)
			}},
			{Name: "the dist-tags", Path: "-/package/lodash/dist-tags", Status: 200, Seed: publish("lodash", "4.17.21")},
			{Name: "ping", Path: "-/ping", Status: 200},
			{Name: "whoami", Path: "-/whoami", Status: 200},
			{Name: "an endpoint npm defines and trove does not implement", Path: "-/v1/search", Status: 404},
			{Name: "the audit of a local repository", Method: http.MethodPost, Path: "-/npm/v1/security/advisories/bulk", Body: []byte(`{"lodash":["4.17.21"]}`), Status: 501},
			{Name: "npm 6's quick audit", Method: http.MethodPost, Path: "-/npm/v1/security/audits/quick", Body: []byte(`{}`), Status: 501},
		},
		Changes: []formattest.ChangeCase{
			{
				Name: "a first publish (OnCommit)", Package: "lodash",
				Do: func(_ context.Context, h *formattest.Host) error { return publish("lodash", "4.17.21")(h) },
			},
			{
				Name: "a later version (OnCommit)", Package: "lodash", Seed: publish("lodash", "4.17.20"),
				Do: func(_ context.Context, h *formattest.Host) error { return publish("lodash", "4.17.21")(h) },
			},
			{
				Name: "a dist-tag set", Package: "lodash", Seed: publishAll("lodash", "4.17.20", "4.17.21"),
				Do: func(_ context.Context, h *formattest.Host) error {
					return serveOK(h, http.MethodPut, "-/package/lodash/dist-tags/next", []byte(`"4.17.20"`), 200)
				},
			},
			{
				Name: "a dist-tag removed", Package: "lodash", Seed: func(h *formattest.Host) error {
					if err := publishAll("lodash", "4.17.20", "4.17.21")(h); err != nil {
						return err
					}
					return serveOK(h, http.MethodPut, "-/package/lodash/dist-tags/next", []byte(`"4.17.20"`), 200)
				},
				Do: func(_ context.Context, h *formattest.Host) error {
					return serveOK(h, http.MethodDelete, "-/package/lodash/dist-tags/next", nil, 200)
				},
			},
			{
				Name: "the unpublish of a version (PUT, OnItemsRemoved)", Package: "lodash", Seed: publishAll("lodash", "4.17.20", "4.17.21"),
				Do: func(_ context.Context, h *formattest.Host) error {
					return unpublish(http.MethodPut, "lodash/-rev/{rev}", `{"versions":{"4.17.20":{}}}`)(h)
				},
			},
			{
				Name: "the unpublish of a package (DELETE)", Package: "lodash", Seed: publishAll("lodash", "4.17.20", "4.17.21"),
				Do: func(_ context.Context, h *formattest.Host) error {
					return unpublish(http.MethodDelete, "lodash/-rev/{rev}", "")(h)
				},
			},
			{
				Name: "the removal of a tarball (OnItemsRemoved)", Package: "lodash", Seed: publishAll("lodash", "4.17.20", "4.17.21"),
				Do: func(ctx context.Context, h *formattest.Host) error {
					rq, err := h.NewRequest(ctx, http.MethodGet, "lodash", nil)
					if err != nil {
						return err
					}
					return h.Remove(ctx, rq, "delete", TarballPath(mustName("lodash"), "4.17.20"))
				},
			},
		},
		NoRevision: map[string]string{
			"npm.unpublish.tarball": "the PUT of the unpublish removed the version with its files; this request changes nothing and answers 200 for a version that is gone",
			"npm.deferred.put":      "an endpoint npm defines and trove does not implement: it answers 404 and changes nothing",
			"npm.deferred.delete":   "an endpoint npm defines and trove does not implement: it answers 404 and changes nothing",
			"npm.deferred.post":     "an endpoint npm defines and trove does not implement: it answers 501 and changes nothing",
		},
		Names: []formattest.NameCase{
			{Spellings: []string{"lodash"}}, {Spellings: []string{"@acme/ui"}}, {Spellings: []string{"JSONStream"}},
		},
		OptionNames: []string{"_authToken", "npmAuthToken", "TROVE_TOKEN"},
		Remote: &formattest.RemoteFixture{
			Upstream: "https://registry.example.org",
			Docs: []formattest.RemoteDoc{
				{Name: "an unscoped package", Path: "lodash", Variant: "full", Body: upstreamDocument("lodash", "https://registry.example.org/lodash/-/", "4.17.20", "4.17.21")},
				{Name: "a scoped package", Path: "@acme%2fui", Variant: "full", Body: upstreamDocument("@acme/ui", "https://registry.example.org/@acme/ui/-/", "1.0.0", "1.1.0-beta.1", "2.0.0")},
			},
			DownloadLinks: tarballLinks,
		},
		Merge: &formattest.MergeFixture{
			Cases: []formattest.MergeCase{
				{
					Name: "a local member and a remote member that share a version", Path: "lodash",
					Parts: []formats.MemberDocument{
						{Member: formats.RepoRef{Key: "local"}, Tier: formats.TierLocal, Position: 0, Doc: markedDocument("lodash", "local", "4.17.21")},
						{Member: formats.RepoRef{Key: "remote"}, Tier: formats.TierRemote, Position: 1, Doc: markedDocument("lodash", "remote", "4.17.20", "4.17.21")},
					},
					Sources: []string{"local", "remote"},
				},
				{
					Name: "a remote member with a cached tarball before one without, local last", Path: "@acme%2fui",
					Parts: []formats.MemberDocument{
						{Member: formats.RepoRef{Key: "first"}, Tier: formats.TierRemote, Position: 0, Doc: markedDocument("@acme/ui", "first", "1.0.0", "2.0.0")},
						{Member: formats.RepoRef{Key: "second"}, Tier: formats.TierRemoteCached, Position: 1, Doc: markedDocument("@acme/ui", "second", "1.0.0")},
						{Member: formats.RepoRef{Key: "third"}, Tier: formats.TierLocal, Position: 2, Doc: markedDocument("@acme/ui", "third", "2.0.0")},
					},
					Sources: []string{"first", "second", "third"},
				},
			},
			Versions:      documentVersions,
			VersionSource: entrySource,
		},
	}
}

// upstreamDocument is a full document as the public registry serves it: the tarball
// addresses name the upstream.
func upstreamDocument(pkg, prefix string, versions ...string) []byte {
	vs := map[string]any{}
	times := map[string]string{"created": "2020-01-01T00:00:00.000Z", "modified": "2024-01-01T00:00:00.000Z"}
	base := pkg
	if i := strings.IndexByte(pkg, '/'); i >= 0 {
		base = pkg[i+1:]
	}
	for _, v := range versions {
		sum := sha512.Sum512([]byte(pkg + v))
		vs[v] = map[string]any{
			"name": pkg, "version": v, "description": "upstream " + v,
			"dist": map[string]any{
				"tarball": prefix + base + "-" + v + ".tgz", "integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum[:]),
				"signatures": []any{map[string]string{"keyid": "SHA256:x", "sig": "MEUC"}},
			},
			"_npmUser": map[string]any{"name": "maintainer", "trustedPublisher": map[string]string{"id": "github"}},
		}
		times[v] = "2023-01-01T00:00:00.000Z"
	}
	b, _ := json.Marshal(map[string]any{
		"_id": pkg, "name": pkg, "dist-tags": map[string]string{"latest": versions[len(versions)-1]}, "versions": vs, "time": times,
		"readme": "a <readme> & more",
	})
	return b
}

// markedDocument is a member document whose version entries carry the member that holds
// them, so the check can read back where an entry of the merge came from.
func markedDocument(pkg, member string, versions ...string) []byte {
	vs := map[string]any{}
	for _, v := range versions {
		vs[v] = map[string]any{"name": pkg, "version": v, "x-member": member, "dist": map[string]any{"tarball": "https://" + member + ".example.org/x.tgz"}}
	}
	b, _ := json.Marshal(map[string]any{"name": pkg, "dist-tags": map[string]string{"latest": versions[len(versions)-1]}, "versions": vs})
	return b
}

func tarballLinks(doc []byte) []string {
	var d struct {
		Versions map[string]struct {
			Dist struct {
				Tarball string `json:"tarball"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if json.Unmarshal(doc, &d) != nil {
		return nil
	}
	var out []string
	for _, v := range d.Versions {
		out = append(out, v.Dist.Tarball)
	}
	return out
}

func documentVersions(doc []byte) []string {
	var d struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if json.Unmarshal(doc, &d) != nil {
		return nil
	}
	var out []string
	for v := range d.Versions {
		out = append(out, v)
	}
	return out
}

func entrySource(doc []byte, version string) string {
	var d struct {
		Versions map[string]struct {
			Member string `json:"x-member"`
		} `json:"versions"`
	}
	if json.Unmarshal(doc, &d) != nil {
		return ""
	}
	return d.Versions[version].Member
}

func mustName(s string) name.Name {
	n, err := name.Parse(s)
	if err != nil {
		panic(err)
	}
	return n
}

// noShadow is a resolver that finds no remote member to shadow.
type noShadow struct{ formats.Resolver }

func (noShadow) ShadowCheck(context.Context, *formats.Request, string) (formats.ShadowOutcome, error) {
	return formats.ShadowOutcome{Proceed: true}, nil
}

// serveOK runs a request through the host and requires the status.
func serveOK(h *formattest.Host, method, path string, body []byte, want int) error {
	resp, err := h.Serve(context.Background(), method, path, bytes.NewReader(body), false)
	if err != nil {
		return err
	}
	if resp.Code != want {
		return fmt.Errorf("%s %s: status %d, want %d: %s", method, path, resp.Code, want, resp.Body.String())
	}
	return nil
}

// TestR_ARCH_010_Conformance runs the conformance kit over the npm format.
func TestR_ARCH_010_Conformance(t *testing.T) {
	formattest.Run(t, New(), conformance(t))
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never panics.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, New()) }

// FuzzR_ARCH_028_Inspect: Inspect never panics, finishes in its deadline and fails only
// with a client error.
func FuzzR_ARCH_028_Inspect(f *testing.F) {
	formattest.FuzzInspect(f, New(), "lodash/-/lodash-1.0.0.tgz", tgzFor(f, "lodash", "1.0.0"))
}
