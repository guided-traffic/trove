package deb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/test/debstub/debgen"
)

// upload stores a generated package in the suite through the upload route of the Host, the way a client does.
func upload(version, suite string) func(h *formattest.Host) error {
	return func(h *formattest.Host) error {
		d, err := debgen.Build(debgen.Hello("hello", version, "amd64"))
		if err != nil {
			return err
		}
		q := url.Values{"distribution": {suite}, "component": {"main"}}
		r, err := h.Serve(context.Background(), http.MethodPut, "upload/hello_"+version+"_amd64.deb", bytes.NewReader(d.Bytes), false,
			func(r *http.Request) { r.URL.RawQuery = q.Encode() })
		if err == nil && r.Code != http.StatusCreated && r.Code != http.StatusOK {
			err = fmt.Errorf("upload %s: status %d: %s", version, r.Code, r.Body.String())
		}
		return err
	}
}

// armoredKey is the block the fixture's port serves as the repository's public key (R-DEB-018).
const armoredKey = "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nAAAA\n-----END PGP PUBLIC KEY BLOCK-----\n"

// PublicCertificates is the one operation of the port a built handler reads here: signing-key.asc (handlers_keys.go).
func (twinPort) PublicCertificates(context.Context, int64) ([]byte, [32]byte, error) {
	return []byte(armoredKey), sha256.Sum256([]byte(armoredKey)), nil
}

// noShadow lets a first upload of a name proceed: the shadowing check has nothing to ask in a repository without remote members.
type noShadow struct{ formattest.Resolver }

func (noShadow) ShadowCheck(context.Context, *formats.Request, string) (formats.ShadowOutcome, error) {
	return formats.ShadowOutcome{Proceed: true}, nil
}

func seed(steps ...func(h *formattest.Host) error) func(h *formattest.Host) error {
	return func(h *formattest.Host) error {
		for _, s := range steps {
			if err := s(h); err != nil {
				return err
			}
		}
		return nil
	}
}

func poolPath(version string) string { return "pool/h/hello/hello_" + version + "_amd64.deb" }

// conformanceChanges are the writes of check 9: an upload, the same bytes in a second suite, the deletion through the route, and the
// removal and the restore through the items.
func conformanceChanges() []formattest.ChangeCase {
	const name = "hello"
	do := func(fn func(h *formattest.Host) error) func(context.Context, *formattest.Host) error {
		return func(_ context.Context, h *formattest.Host) error { return fn(h) }
	}
	remove := func(reason, version string) func(context.Context, *formattest.Host) error {
		return func(ctx context.Context, h *formattest.Host) error {
			rq, err := h.NewRequest(ctx, http.MethodDelete, poolPath(version), nil)
			if err != nil {
				return err
			}
			return h.Remove(ctx, rq, reason, poolPath(version))
		}
	}
	return []formattest.ChangeCase{
		{Name: "a first upload (OnCommit)", Package: name, Do: do(upload("1.0-1", "stable"))},
		{Name: "a later version (OnCommit)", Package: name, Seed: upload("1.0-1", "stable"), Do: do(upload("1.1-1", "stable"))},
		{
			Name: "the removal of a package file through the items (OnItemsRemoved)", Package: name,
			Seed: seed(upload("1.0-1", "stable"), upload("1.1-1", "stable")), Do: remove("delete", "1.0-1"),
		},
		{
			Name: "the deletion of a package file through its route (OnItemsRemoved)", Package: name,
			Seed: seed(upload("1.0-1", "stable"), upload("1.1-1", "stable")),
			Do: do(func(h *formattest.Host) error {
				r, err := h.Serve(context.Background(), http.MethodDelete, poolPath("1.0-1"), nil, false)
				if err == nil && r.Code != http.StatusNoContent {
					err = fmt.Errorf("delete: status %d: %s", r.Code, r.Body.String())
				}
				return err
			}),
		},
		{
			Name: "the restore of a package file from the trash (OnItemsRestored)", Package: name,
			Seed: seed(upload("1.0-1", "stable"), upload("1.1-1", "stable"), func(h *formattest.Host) error {
				return h.Remove(context.Background(), nil, "trash", poolPath("1.0-1"))
			}),
			Do: func(ctx context.Context, h *formattest.Host) error { return h.Restore(ctx, nil, poolPath("1.0-1")) },
		},
	}
}

// conformance is the fixture of the kit over the format with the local class built (apt-work-packages.md, WP3): the index and key routes
// answer 501 until the work packages of the index builder and of the keys build them, so those routes are listed as not built, each with
// its reason, and the work package that builds the operation removes the entry (the kit refuses a skip the format contradicts).
func conformance() formattest.Fixture {
	const generated = "the files are generated items that only the publication port writes, which the kit's host does not have; test/integration/deblocal serves them"
	const remoteOnly = "the route serves the remote class, which the kit's local host does not address: the answers are covered against a remote engine and the counting stub in test/integration/debremote"
	versions := []string{"1.0-1", "1.0-2", "1.1-1"}
	return formattest.Fixture{
		Rejects: []string{"pool/%2e%2e/x", "../pool/h/hello/hello_2.10-3_amd64.deb", "dists/stable/InRelease%00"},
		Paths: []formattest.PathCase{
			{Path: "dists/stable/main/binary-amd64/Packages.gz", Want: formats.PathInfo{Kind: formats.PathMetadata}},
			{Path: "dists/stable/InRelease", Want: formats.PathInfo{Kind: formats.PathMetadata}},
		},
		Host: func() formattest.HostOptions {
			return formattest.HostOptions{Data: twinPort{twinStore: newTwin()}, Resolver: noShadow{}}
		},
		NoDocument: "an APT archive has no document per package: PackageDocumentPath reports ok false (R-DEB-001, Q-REPO-270)",
		Names:      []formattest.NameCase{{Spellings: []string{"hello"}}, {Spellings: []string{"libxml2-utils"}}},
		Inspect:    inspectDeb("pool/h/hello/hello_1.0-1_amd64.deb"),
		Content: []formattest.ContentCase{
			{Name: "release document", Path: "dists/stable/InRelease", Status: http.StatusNotFound},
			{Name: "index", Path: "dists/stable/main/binary-amd64/Packages.gz", Status: http.StatusNotFound},
			{Name: "by-hash file", Path: "dists/stable/main/binary-amd64/by-hash/SHA256/" + strings.Repeat("0", 64), Status: http.StatusNotFound},
			{Name: "package file", Path: poolPath("1.0-1"), Seed: upload("1.0-1", "stable")},
			{Name: "package file that is not stored", Path: poolPath("9.9-9"), Status: http.StatusNotFound},
			{Name: "signing key", Path: "signing-key.asc", Status: http.StatusOK},
			{Name: "release document of a flat directory", Path: "binary/InRelease", Status: http.StatusNotFound},
			{Name: "index of a flat directory", Path: "binary/Packages.gz", Status: http.StatusNotFound},
			{Name: "package file of a flat directory", Path: "binary/hello_1.0_all.deb", Status: http.StatusNotFound},
		},
		NoContent: map[string]string{
			routeDists: generated, routeByHash: generated,
			routeFlatRelease: remoteOnly, routeFlatIndex: remoteOnly, routeFlatFile: remoteOnly,
		},
		Remote: &formattest.RemoteFixture{
			Upstream: "https://deb.upstream.test", Identity: "an APT archive's documents hold no address of the upstream: they are passed through byte for byte (R-DEB-019)",
			Docs: []formattest.RemoteDoc{
				{Name: "release", Path: "dists/stable/InRelease", Body: []byte("-----BEGIN PGP SIGNED MESSAGE-----\n\nOrigin: test\n-----BEGIN PGP SIGNATURE-----\n\nAAAA\n-----END PGP SIGNATURE-----\n")},
				{Name: "index", Path: "dists/stable/main/binary-amd64/Packages.gz", Body: []byte("\x1f\x8b\x08\x00opaque")},
			},
		},
		Changes: conformanceChanges(),
		View: &formattest.ViewFixture{
			Versions: versions, Package: "hello",
			Seed: func(h *formattest.Host) error {
				for _, v := range versions {
					if err := upload(v, "stable")(h); err != nil {
						return err
					}
				}
				return nil
			},
		},
		OptionNames: optionNames(),
		Skip: map[formattest.Check]string{
			formattest.CheckMerge: "local class only: the virtual adapter joins with its work package (R-DEB-025)",
		},
	}
}

// TestR_ARCH_010_Conformance runs the conformance kit over the format.
func TestR_ARCH_010_Conformance(t *testing.T) {
	formattest.Run(t, New(), conformance())
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never panics.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, New()) }
