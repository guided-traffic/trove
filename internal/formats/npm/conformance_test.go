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
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
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
	publish := func(name, version string) func(h *formattest.Host) error {
		return func(h *formattest.Host) error {
			return serveOK(h, http.MethodPut, name, publishDoc(t, name, version, "latest"), 201)
		}
	}
	return formattest.Fixture{
		Host: func() formattest.HostOptions {
			return formattest.HostOptions{Data: npmtest.New(), Resolver: noShadow{}}
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
			{Name: "a package document", Path: "lodash", Status: 501},
			{Name: "a single version", Path: "lodash/1.0.0", Status: 501},
			{Name: "a tarball", Path: "lodash/-/lodash-1.0.0.tgz", Status: 501},
			{Name: "the dist-tags", Path: "-/package/lodash/dist-tags", Status: 501},
			{Name: "ping", Path: "-/ping", Status: 501},
			{Name: "whoami", Path: "-/whoami", Status: 501},
			{Name: "an endpoint npm defines and trove does not implement", Path: "-/v1/search", Status: 404},
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
		},
		NoRevision: map[string]string{
			"npm.unpublish.put":     "the handler is built with the unpublish work package and answers 501 until then; it gets its change case there",
			"npm.unpublish.delete":  "same: the handler answers 501 until the unpublish work package builds it",
			"npm.unpublish.tarball": "same: the handler answers 501 until the unpublish work package builds it",
			"npm.disttag.put":       "the handler is built with the dist-tag work package and answers 501 until then; it gets its change case there",
			"npm.disttag.delete":    "same: the handler answers 501 until the dist-tag work package builds it",
			"npm.deferred.put":      "an endpoint npm defines and trove does not implement: it answers 404 and changes nothing",
			"npm.deferred.delete":   "an endpoint npm defines and trove does not implement: it answers 404 and changes nothing",
			"npm.deferred.post":     "an endpoint npm defines and trove does not implement: it answers 501 and changes nothing",
		},
		Names: []formattest.NameCase{
			{Spellings: []string{"lodash"}}, {Spellings: []string{"@acme/ui"}}, {Spellings: []string{"JSONStream"}},
		},
		OptionNames: []string{"_authToken", "npmAuthToken", "TROVE_TOKEN"},
		Skip: map[formattest.Check]string{
			formattest.CheckRewrite: "the remote class is not served yet: the format lists local only and has no remote adapter",
			formattest.CheckMerge:   "the virtual class is not served yet: the format lists local only and has no virtual adapter",
		},
	}
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
