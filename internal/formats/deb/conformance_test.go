package deb

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// unbuiltStore is the data port of a test that reaches no handler which reads it: a call would panic on the nil interface. The
// handlers of the skeleton answer 501 before they reach the port.
type unbuiltStore struct{ Store }

// armoredKey is the block the fixture's port serves as the repository's public key (R-DEB-018).
const armoredKey = "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nAAAA\n-----END PGP PUBLIC KEY BLOCK-----\n"

// PublicCertificates is the one operation of the port a built handler reads here: signing-key.asc (handlers_keys.go).
func (unbuiltStore) PublicCertificates(context.Context, int64) ([]byte, [32]byte, error) {
	return []byte(armoredKey), sha256.Sum256([]byte(armoredKey)), nil
}

// conformance is the fixture of the kit over the skeleton (apt-work-packages.md, WP0): the routes answer 501, so the checks that need a
// built protocol are skipped or listed as not built, each with its reason, and the work package that builds the operation removes the
// entry (the kit refuses a skip the format contradicts).
func conformance() formattest.Fixture {
	const notBuilt = "the handler is built by a later work package of T-17 (apt-work-packages.md); the skeleton answers 501 deb_not_implemented"
	return formattest.Fixture{
		Rejects: []string{"pool/%2e%2e/x", "../pool/h/hello/hello_2.10-3_amd64.deb", "dists/stable/InRelease%00"},
		Paths: []formattest.PathCase{
			{Path: "dists/stable/main/binary-amd64/Packages.gz", Want: formats.PathInfo{Kind: formats.PathMetadata}},
			{Path: "dists/stable/InRelease", Want: formats.PathInfo{Kind: formats.PathMetadata}},
		},
		Host: func() formattest.HostOptions {
			return formattest.HostOptions{Data: unbuiltStore{}}
		},
		NoDocument: "an APT archive has no document per package: PackageDocumentPath reports ok false (R-DEB-001, Q-REPO-270)",
		Names:      []formattest.NameCase{{Spellings: []string{"hello"}}, {Spellings: []string{"libxml2-utils"}}},
		Inspect:    formattest.InspectJSON("pool/h/hello/hello_2.10-3_amd64.deb"),
		Content: []formattest.ContentCase{
			{Name: "release document", Path: "dists/stable/InRelease", Status: http.StatusNotImplemented},
			{Name: "index", Path: "dists/stable/main/binary-amd64/Packages.gz", Status: http.StatusNotImplemented},
			{Name: "by-hash file", Path: "dists/stable/main/binary-amd64/by-hash/SHA256/" + strings.Repeat("0", 64), Status: http.StatusNotImplemented},
			// A file name outside the form <package>_<version>_<arch>.<ext>: Classify places a file of that form in a version, and the
			// decision point decides a version on the path of its file (formats.VersionPather), which a Debian version cannot name from
			// its package and version alone (it has one file per architecture below the pool directory of its source package). The local
			// class decides it with its package screens (Q-DEB-46); the skeleton has no version rows to list.
			{Name: "package file", Path: "pool/h/hello/hello.deb", Status: http.StatusNotImplemented},
			{Name: "signing key", Path: "signing-key.asc", Status: http.StatusOK},
		},
		NoContent: map[string]string{
			routeDists: notBuilt, routeByHash: notBuilt, routePoolGet: notBuilt,
		},
		NoRevision: map[string]string{
			routePoolDelete: notBuilt, routeUpload: notBuilt, "OnCommit": notBuilt, "OnItemsRemoved": notBuilt,
		},
		Skip: map[formattest.Check]string{
			formattest.CheckRewrite: "local class only: the remote adapter joins with its work package (R-DEB-019)",
			formattest.CheckMerge:   "local class only: the virtual adapter joins with its work package (R-DEB-025)",
			formattest.CheckSetup:   "the format declares no client yet: the setup templates and the guide come with the local class (R-DEB-029)",
			formattest.CheckView:    "the package screens come with the local class (R-DEB-030)",
		},
	}
}

// TestR_ARCH_010_Conformance runs the conformance kit over the skeleton.
func TestR_ARCH_010_Conformance(t *testing.T) {
	formattest.Run(t, New(), conformance())
}

// FuzzR_ARCH_028_Match: Match never resolves a path that is not clean and never panics.
func FuzzR_ARCH_028_Match(f *testing.F) { formattest.FuzzMatch(f, New()) }
