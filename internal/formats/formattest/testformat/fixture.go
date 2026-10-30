package testformat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// Publish uploads a version through the PUT route of the host and fails unless the
// route answers 201.
func Publish(ctx context.Context, h *formattest.Host, name, version string) error {
	base := name[len(name)-len(lastSegment(name)):]
	return put(ctx, h, name+"/-/"+base+"-"+version+".tgz", Tarball(name, version), http.StatusCreated)
}

func lastSegment(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

func put(ctx context.Context, h *formattest.Host, p string, body []byte, want int) error {
	return do(ctx, h, http.MethodPut, p, body, want)
}

func do(ctx context.Context, h *formattest.Host, method, p string, body []byte, want int) error {
	resp, err := h.Serve(ctx, method, p, bytes.NewReader(body), false)
	if err != nil {
		return err
	}
	if resp.Code != want {
		return fmt.Errorf("%s %s answered %d, want %d: %s", method, p, resp.Code, want, resp.Body.String())
	}
	return nil
}

// Fixture is the fixture of the conforming test format, the one the kit's own
// tests give to every variant.
func Fixture() formattest.Fixture {
	remoteDoc := []byte(`{"name":"left-pad","versions":{"1.0.0":{"dist":{"tarball":"https://registry.upstream.test/left-pad/-/left-pad-1.0.0.tgz"}},` +
		`"2.0.0-legacy":{"dist":{"tarball":"https://registry.upstream.test/left-pad/-/left-pad-2.0.0.tgz"}}}}`)
	return formattest.Fixture{
		Host: func() formattest.HostOptions { return formattest.HostOptions{Data: &MemStore{}} },
		Rejects: []string{
			"left-pad/-/../x", "@scope/%2e%2e/name",
		},
		Paths: []formattest.PathCase{
			{Path: "", Want: formats.PathInfo{Kind: formats.PathComputed}},
			{Path: "left-pad/", Want: formats.PathInfo{Kind: formats.PathComputed}},
			{Path: "left-pad", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "left-pad"}},
			{Path: "Left-Pad", Want: formats.PathInfo{Kind: formats.PathMetadata, Package: "left-pad"}},
			{Path: "@scope/name/-/name-2.1.0.tgz", Want: formats.PathInfo{Kind: formats.Content, Package: "@scope/name", Version: "2.1.0"}},
		},
		Remote: &formattest.RemoteFixture{
			Upstream: "https://registry.upstream.test",
			Docs:     []formattest.RemoteDoc{{Name: "left-pad", Path: "left-pad", Body: remoteDoc}},
			DownloadLinks: func(doc []byte) []string {
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
			},
		},
		Merge: &formattest.MergeFixture{
			Cases: []formattest.MergeCase{{
				Name: "a local and a remote member",
				Path: "left-pad",
				Parts: []formats.MemberDocument{
					{Tier: formats.TierRemote, Position: 0, Doc: []byte(`{"versions":{"1.0.0":{"source":"remote-a"},"3.0.0":{"source":"remote-a"}}}`)},
					{Tier: formats.TierLocal, Position: 1, Doc: []byte(`{"versions":{"1.0.0":{"source":"local"},"2.0.0":{"source":"local"}}}`)},
					{Tier: formats.TierRemote, Position: 2, Doc: []byte(`{"versions":{"3.0.0":{"source":"remote-b"},"4.0.0":{"source":"remote-b"}}}`)},
				},
				Sources: []string{"remote-a", "local", "remote-b"},
			}},
			Versions: func(doc []byte) []string {
				var d struct {
					Versions map[string]json.RawMessage `json:"versions"`
				}
				_ = json.Unmarshal(doc, &d)
				var out []string
				for v := range d.Versions {
					out = append(out, v)
				}
				return out
			},
			VersionSource: func(merged []byte, version string) string {
				var d struct {
					Versions map[string]struct {
						Source string `json:"source"`
					} `json:"versions"`
				}
				_ = json.Unmarshal(merged, &d)
				return d.Versions[version].Source
			},
		},
		Inspect: concat(
			formattest.InspectTarGz("left-pad/-/left-pad-1.0.0.tgz", "package/package.json"),
			formattest.InspectZip("left-pad/-/left-pad-1.0.0.zip", "package.json"),
			formattest.InspectJSON("left-pad/-/left-pad-1.0.0.json"),
			formattest.InspectXML("left-pad/-/left-pad-1.0.0.xml"),
		),
		Content: []formattest.ContentCase{
			{Name: "package document", Path: "left-pad", Seed: publish("left-pad", "1.0.0")},
			{Name: "scoped package document", Path: "@scope%2Fname", Seed: publish("@scope/name", "1.0.0")},
			{Name: "tarball", Path: "left-pad/-/left-pad-1.0.0.tgz", Seed: publish("left-pad", "1.0.0")},
			{Name: "scoped tarball", Path: "@scope/name/-/name-1.0.0.tgz", Seed: publish("@scope/name", "1.0.0")},
			{Name: "tarball, HEAD", Method: http.MethodHead, Path: "left-pad/-/left-pad-1.0.0.tgz", Seed: publish("left-pad", "1.0.0")},
			{Name: "all", Path: "-/all"},
			{Name: "search", Path: "-/search", Seed: publish("left-pad", "1.0.0")},
		},
		Changes: []formattest.ChangeCase{
			{
				Name: "publish through the route (OnCommit)", Package: "left-pad",
				Do: func(ctx context.Context, h *formattest.Host) error { return Publish(ctx, h, "left-pad", "1.0.0") },
			},
			{
				Name: "second version of a package (OnCommit)", Package: "left-pad", Seed: publish("left-pad", "1.0.0"),
				Do: func(ctx context.Context, h *formattest.Host) error { return Publish(ctx, h, "left-pad", "1.1.0") },
			},
			{
				Name: "tag change (Admin route)", Package: "left-pad", Seed: publish("left-pad", "1.0.0"),
				Do: func(ctx context.Context, h *formattest.Host) error {
					return do(ctx, h, http.MethodPut, "left-pad/tags/stable", []byte(`"1.0.0"`), http.StatusOK)
				},
			},
			{
				Name: "delete of a tarball (OnItemsRemoved)", Package: "left-pad", Seed: publish("left-pad", "1.0.0"),
				Do: func(ctx context.Context, h *formattest.Host) error {
					return do(ctx, h, http.MethodDelete, "left-pad/-/left-pad-1.0.0.tgz", nil, http.StatusNoContent)
				},
			},
			{
				Name: "restore of a tarball (OnItemsRestored)", Package: "left-pad",
				Seed: func(h *formattest.Host) error {
					if err := Publish(context.Background(), h, "left-pad", "1.0.0"); err != nil {
						return err
					}
					return h.Remove(context.Background(), nil, "trash", "left-pad/-/left-pad-1.0.0.tgz")
				},
				Do: func(ctx context.Context, h *formattest.Host) error {
					return h.Restore(ctx, nil, "left-pad/-/left-pad-1.0.0.tgz")
				},
			},
		},
		Names: []formattest.NameCase{
			{Spellings: []string{"left-pad", "Left-Pad", "LEFT-PAD"}},
			{Spellings: []string{"@scope/name", "@Scope/Name"}},
		},
	}
}

func publish(name, version string) func(*formattest.Host) error {
	return func(h *formattest.Host) error { return Publish(context.Background(), h, name, version) }
}

func concat[T any](lists ...[]T) []T {
	var out []T
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}
